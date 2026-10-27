package backend

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/shopspring/decimal"
)

const sqlReplaySchema = 1
const sqlValidationPolicy = "latest-admitted-rollback"

var sqlReplayVariables sync.Once

// DecodeSQLWorkload rejects unknown fields, absent required arrays, and trailing JSON.
func DecodeSQLWorkload(reader io.Reader) (SQLWorkload, error) {
	var workload SQLWorkload
	if err := decodeSQLJSON(reader, &workload); err != nil {
		return workload, err
	}
	return normalizeSQLWorkload(workload)
}

// DecodeReplayArtifact applies the same strict JSON boundary as workload input.
func DecodeReplayArtifact(reader io.Reader) (ReplayArtifact, error) {
	var artifact ReplayArtifact
	if err := decodeSQLJSON(reader, &artifact); err != nil {
		return artifact, err
	}
	return artifact, validateSQLArtifact(artifact)
}

func decodeSQLJSON(reader io.Reader, target interface{}) error {
	decoder := json.NewDecoder(reader)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("invalid replay JSON: %w", err)
	}
	if !utf8.Valid(raw) {
		return fmt.Errorf("invalid replay JSON: non-UTF-8 text")
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("invalid replay JSON: trailing value")
		}
		return fmt.Errorf("invalid replay JSON trailer: %w", err)
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid replay JSON: %w", err)
	}
	return requiredSQLJSON(raw, reflect.TypeOf(target).Elem())
}

func requiredSQLJSON(raw json.RawMessage, typ reflect.Type) error {
	if typ == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("invalid replay JSON: null %s", typ)
	}
	if typ.Kind() == reflect.Pointer {
		return requiredSQLJSON(raw, typ.Elem())
	}
	switch typ.Kind() {
	case reflect.Struct:
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return err
		}
		known := make(map[string]bool, typ.NumField())
		for index := range typ.NumField() {
			field := typ.Field(index)
			tag := strings.Split(field.Tag.Get("json"), ",")
			known[tag[0]] = true
			value, exists := fields[tag[0]]
			optional := len(tag) > 1 && tag[1] == "omitempty"
			if !exists {
				if !optional {
					return fmt.Errorf("invalid replay JSON: required field %s missing", tag[0])
				}
				continue
			}
			if err := requiredSQLJSON(value, field.Type); err != nil {
				return err
			}
		}
		for name := range fields {
			if !known[name] {
				return fmt.Errorf("invalid replay JSON: unknown field %q", name)
			}
		}
	case reflect.Slice:
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return err
		}
		for _, value := range values {
			if err := requiredSQLJSON(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func sqlIsolation(name string) (Isolation, error) {
	for level, spelling := range []string{"read-committed", "repeatable-read", "atomic-read", "causal", "prefix", "snapshot-isolation", "serializable"} {
		if name == spelling {
			return Isolation(level), nil
		}
	}
	return 0, fmt.Errorf("invalid workload: unknown isolation %q", name)
}

func normalizeSQLWorkload(workload SQLWorkload) (SQLWorkload, error) {
	if workload.SchemaVersion != sqlReplaySchema {
		return workload, fmt.Errorf("invalid workload: unsupported schema_version %d", workload.SchemaVersion)
	}
	if _, err := sqlIsolation(workload.Isolation); err != nil {
		return workload, err
	}
	if workload.Setup == nil || workload.Transactions == nil || workload.Assertion.BaselineRows == nil || workload.Assertion.WantRows == nil {
		return workload, fmt.Errorf("invalid workload: setup, transactions, baseline_rows and want_rows arrays are required")
	}
	if workload.Seed == nil {
		seed := uint64(1)
		workload.Seed = &seed
	}
	if workload.ReadPolicy == "" {
		workload.ReadPolicy = "uniform"
	}
	if workload.DeviationProbability == nil {
		probability := 0.01
		workload.DeviationProbability = &probability
	}
	probability := *workload.DeviationProbability
	if workload.ReadPolicy != "uniform" && workload.ReadPolicy != "sparse" && workload.ReadPolicy != "age-stratified" && workload.ReadPolicy != "bounded-deviation" {
		return workload, fmt.Errorf("invalid workload: unknown read_policy %q", workload.ReadPolicy)
	}
	if math.IsNaN(probability) || math.IsInf(probability, 0) || probability < 0 || probability > 1 {
		return workload, fmt.Errorf("invalid workload: deviation_probability must be finite and in [0,1]")
	}
	if workload.DeviationBudget != nil && *workload.DeviationBudget < 0 {
		return workload, fmt.Errorf("invalid workload: deviation_budget must be nonnegative")
	}
	ids := make(map[string]bool)
	addID := func(id string) error {
		if strings.TrimSpace(id) == "" || !utf8.ValidString(id) || ids[id] {
			return fmt.Errorf("invalid workload: invalid or duplicate ID %q", id)
		}
		ids[id] = true
		return nil
	}
	if err := addID(workload.Assertion.ID); err != nil {
		return workload, err
	}
	for _, rows := range [][]TypedRow{workload.Assertion.BaselineRows, workload.Assertion.WantRows} {
		if err := validateSQLRows(rows); err != nil {
			return workload, fmt.Errorf("invalid workload: %w", err)
		}
	}
	setupDB := ""
	for index, query := range workload.Setup {
		if err := validateSQLStatement(query, "setup", &setupDB); err != nil {
			return workload, fmt.Errorf("invalid setup %d: %w", index, err)
		}
	}
	sessionDB := make(map[int]string)
	for _, transaction := range workload.Transactions {
		if err := addID(transaction.ID); err != nil {
			return workload, err
		}
		if transaction.SessionID <= 0 || transaction.Finish != "commit" && transaction.Finish != "rollback" || transaction.Statements == nil {
			return workload, fmt.Errorf("invalid transaction %q: positive session_id, statements array and commit/rollback finish required", transaction.ID)
		}
		database := sessionDB[transaction.SessionID]
		for _, statement := range transaction.Statements {
			if err := addID(statement.ID); err != nil {
				return workload, err
			}
			if err := validateSQLStatement(statement.SQL, "measured", &database); err != nil {
				return workload, fmt.Errorf("invalid statement %q: %w", statement.ID, err)
			}
		}
		sessionDB[transaction.SessionID] = database
	}
	assertionDB := ""
	if err := validateSQLStatement(workload.Assertion.SQL, "assertion", &assertionDB); err != nil {
		return workload, fmt.Errorf("invalid assertion: %w", err)
	}
	return workload, nil
}

func validateSQLStatement(query, phase string, database *string) error {
	pieces, err := sqlparser.SplitStatementToPieces(query)
	if err != nil || len(pieces) != 1 {
		return fmt.Errorf("exactly one SQL statement required")
	}
	statement, err := sqlparser.Parse(query)
	if err != nil {
		return err
	}
	switch node := statement.(type) {
	case sqlparser.SelectStatement:
	case *sqlparser.Insert:
		if phase == "assertion" || node.Action == sqlparser.ReplaceStr {
			return fmt.Errorf("statement is not permitted in %s", phase)
		}
	case *sqlparser.Update, *sqlparser.Delete:
		if phase == "assertion" {
			return fmt.Errorf("assertion must be SELECT")
		}
	case *sqlparser.Savepoint, *sqlparser.RollbackSavepoint, *sqlparser.ReleaseSavepoint:
		if phase != "measured" {
			return fmt.Errorf("savepoints are permitted only in measured transactions")
		}
	case *sqlparser.Use:
		if phase == "assertion" || node.DBName.IsEmpty() {
			return fmt.Errorf("assertion must be SELECT")
		}
		*database = node.DBName.String()
	case *sqlparser.DDL, *sqlparser.DBDDL:
		if phase != "setup" {
			return fmt.Errorf("DDL is permitted only in setup")
		}
		if node, ok := statement.(*sqlparser.DDL); ok && (node.ViewSpec != nil || node.TriggerSpec != nil || node.ProcedureSpec != nil || node.EventSpec != nil) {
			return fmt.Errorf("setup requires ordinary fixture DDL, not stored SQL")
		}
	default:
		return fmt.Errorf("statement type %T is not permitted in %s", statement, phase)
	}
	ctes := make(map[string]bool)
	_ = walkSQLReplayAST(statement, func(node sqlparser.SQLNode) (bool, error) {
		if cte, ok := node.(*sqlparser.CommonTableExpr); ok {
			ctes[strings.ToLower(cte.As.String())] = true
		}
		return true, nil
	})
	checkTable := func(table sqlparser.TableName) error {
		if phase != "setup" && *database == "" && table.DbQualifier.IsEmpty() && table.SchemaQualifier.IsEmpty() && !ctes[strings.ToLower(table.Name.String())] && !strings.EqualFold(table.Name.String(), "dual") {
			return fmt.Errorf("table %s requires qualification or an explicit USE in its session", table.Name.String())
		}
		return nil
	}
	var visit sqlparser.Visit
	visit = func(node sqlparser.SQLNode) (bool, error) {
		if node == nil || reflect.ValueOf(node).Kind() == reflect.Pointer && reflect.ValueOf(node).IsNil() {
			return false, nil
		}
		switch node := node.(type) {
		case *sqlparser.FuncExpr:
			switch node.Name.Lowered() {
			case "rand", "random", "random_bytes", "uuid", "uuid_short", "now", "sysdate", "curdate", "curtime", "current_date", "current_time", "current_timestamp", "localtime", "localtimestamp", "utc_date", "utc_time", "utc_timestamp", "unix_timestamp", "connection_id", "last_insert_id", "found_rows", "row_count", "sleep", "benchmark", "get_lock", "release_lock", "is_free_lock", "is_used_lock", "load_file", "current_user", "session_user", "system_user", "user", "database", "schema", "version":
				return false, fmt.Errorf("nondeterministic or session-dependent function %s is not permitted", node.Name.String())
			}
		case *sqlparser.SQLVal:
			if node.Type == sqlparser.ValArg {
				return false, fmt.Errorf("query arguments must be supplied as SQL text")
			}
		case *sqlparser.ColName:
			if strings.HasPrefix(node.Name.String(), "@") {
				return false, fmt.Errorf("session variables are not permitted")
			}
		case sqlparser.Nextval:
			return false, fmt.Errorf("stateful expressions are not permitted")
		case *sqlparser.Select:
			if node.Into != nil {
				return false, fmt.Errorf("SELECT INTO is not permitted")
			}
		case *sqlparser.SetOp:
			if node.Into != nil {
				return false, fmt.Errorf("SELECT INTO is not permitted")
			}
		case *sqlparser.Insert:
			if err := checkTable(node.Table); err != nil {
				return false, err
			}
		case *sqlparser.AliasedTableExpr:
			if table, ok := node.Expr.(sqlparser.TableName); ok {
				if err := checkTable(table); err != nil {
					return false, err
				}
			}
		}
		return true, nil
	}
	return walkSQLReplayAST(statement, visit)
}

// The pinned parser's Walk omits DDL defaults, named windows and set-op tails.
// Walk exported AST fields so restrictions also cover those nested expressions.
func walkSQLReplayAST(node sqlparser.SQLNode, visit sqlparser.Visit) error {
	var walk func(reflect.Value, bool) error
	walk = func(value reflect.Value, inspect bool) error {
		if !value.IsValid() {
			return nil
		}
		if value.Kind() == reflect.Interface {
			if value.IsNil() {
				return nil
			}
			return walk(value.Elem(), true)
		}
		if value.Kind() == reflect.Pointer && value.IsNil() {
			return nil
		}
		if inspect && value.CanInterface() {
			if node, ok := value.Interface().(sqlparser.SQLNode); ok {
				descend, err := visit(node)
				if err != nil || !descend {
					return err
				}
			}
		}
		switch value.Kind() {
		case reflect.Pointer:
			return walk(value.Elem(), false)
		case reflect.Struct:
			for index := range value.NumField() {
				if value.Type().Field(index).IsExported() {
					if err := walk(value.Field(index), true); err != nil {
						return err
					}
				}
			}
		case reflect.Slice, reflect.Array:
			if value.Type().Elem().Kind() == reflect.Uint8 {
				return nil
			}
			for index := range value.Len() {
				if err := walk(value.Index(index), true); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(node), true)
}

func validateSQLRows(rows []TypedRow) error {
	for _, row := range rows {
		if row == nil {
			return fmt.Errorf("typed rows must be arrays")
		}
		for _, value := range row {
			valid := false
			switch value.Kind {
			case "null":
				valid = value.Value == ""
			case "bool":
				valid = value.Value == "true" || value.Value == "false"
			case "int":
				n, err := strconv.ParseInt(value.Value, 10, 64)
				valid = err == nil && strconv.FormatInt(n, 10) == value.Value
			case "uint":
				n, err := strconv.ParseUint(value.Value, 10, 64)
				valid = err == nil && strconv.FormatUint(n, 10) == value.Value
			case "decimal":
				n, err := decimal.NewFromString(value.Value)
				valid = err == nil && n.String() == value.Value
			case "float":
				n, err := strconv.ParseFloat(value.Value, 64)
				valid = err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && strconv.FormatFloat(n, 'g', -1, 64) == value.Value
			case "text":
				valid = utf8.ValidString(value.Value)
			case "bytes":
				n, err := base64.StdEncoding.DecodeString(value.Value)
				valid = err == nil && base64.StdEncoding.EncodeToString(n) == value.Value
			}
			if !valid {
				return fmt.Errorf("invalid canonical typed value %+v", value)
			}
		}
	}
	return nil
}

func sqlTypedValue(ctx *sql.Context, typ sql.Type, value interface{}) (TypedValue, error) {
	if typ == types.Null && value == nil {
		return TypedValue{Kind: "null"}, nil
	}
	if typ == nil || types.IsJSON(typ) || types.IsGeometry(typ) {
		return TypedValue{}, fmt.Errorf("unsupported SQL type %v", typ)
	}
	number, numeric := typ.(sql.NumberType)
	kind := ""
	switch {
	case types.IsBoolean(typ):
		kind = "bool"
	case numeric && number.IsFloat():
		kind = "float"
	case numeric && number.IsSigned():
		kind = "int"
	case numeric:
		kind = "uint"
	case types.IsDecimal(typ):
		kind = "decimal"
	case types.IsBinaryType(typ):
		kind = "bytes"
	case types.IsTextOnly(typ):
		kind = "text"
	default:
		return TypedValue{}, fmt.Errorf("unsupported SQL type %s", typ.String())
	}
	if value == nil {
		return TypedValue{Kind: "null"}, nil
	}
	// Aggregate metadata can say DOUBLE while its actual value is exact DECIMAL.
	if exact, ok := value.(decimal.Decimal); ok && (kind == "decimal" || numeric && kind != "bool") {
		return TypedValue{Kind: "decimal", Value: exact.String()}, nil
	}
	converted, _, err := typ.Convert(ctx, value)
	if err != nil {
		return TypedValue{}, err
	}
	result := TypedValue{Kind: kind}
	switch kind {
	case "bool", "int":
		var n int64
		switch value := converted.(type) {
		case bool:
			if value {
				n = 1
			}
		case int:
			n = int64(value)
		case int8:
			n = int64(value)
		case int16:
			n = int64(value)
		case int32:
			n = int64(value)
		case int64:
			n = value
		default:
			return result, fmt.Errorf("unsupported signed representation %T", converted)
		}
		result.Value = strconv.FormatInt(n, 10)
		if kind == "bool" {
			result.Value = strconv.FormatBool(n != 0)
		}
	case "uint":
		var n uint64
		switch value := converted.(type) {
		case uint:
			n = uint64(value)
		case uint8:
			n = uint64(value)
		case uint16:
			n = uint64(value)
		case uint32:
			n = uint64(value)
		case uint64:
			n = value
		default:
			return result, fmt.Errorf("unsupported unsigned representation %T", converted)
		}
		result.Value = strconv.FormatUint(n, 10)
	case "decimal":
		value, ok := converted.(decimal.Decimal)
		if !ok {
			return result, fmt.Errorf("unsupported decimal representation %T", converted)
		}
		result.Value = value.String()
	case "float":
		var n float64
		switch value := converted.(type) {
		case float32:
			n = float64(value)
		case float64:
			n = value
		default:
			return result, fmt.Errorf("unsupported float representation %T", converted)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return result, fmt.Errorf("nonfinite SQL float")
		}
		result.Value = strconv.FormatFloat(n, 'g', -1, 64)
	case "text", "bytes":
		var data []byte
		switch value := converted.(type) {
		case string:
			if kind == "text" {
				if !utf8.ValidString(value) {
					return result, fmt.Errorf("unsupported non-UTF-8 SQL text")
				}
				result.Value = value
				return result, nil
			}
			data = []byte(value)
		case []byte:
			data = value
		default:
			return result, fmt.Errorf("unsupported string representation %T", converted)
		}
		if kind == "bytes" {
			result.Value = base64.StdEncoding.EncodeToString(data)
		} else {
			if !utf8.Valid(data) {
				return result, fmt.Errorf("unsupported non-UTF-8 SQL text")
			}
			result.Value = string(data)
		}
	}
	return result, nil
}

func sqlReplayQuery(engine *sqle.Engine, ctx *sql.Context, query string, typed bool) ([]TypedRow, error) {
	schema, iter, _, queryErr := engine.Query(ctx, query)
	if iter == nil {
		if queryErr != nil {
			return nil, queryErr
		}
		return nil, fmt.Errorf("SQL query returned no iterator")
	}
	if queryErr != nil {
		return nil, errors.Join(queryErr, iter.Close(ctx))
	}
	return consumeSQLReplayRows(ctx, schema, iter, typed)
}

func consumeSQLReplayRows(ctx *sql.Context, schema sql.Schema, iter sql.RowIter, typed bool) ([]TypedRow, error) {
	var queryErr error
	if typed {
		for _, column := range schema {
			if _, err := sqlTypedValue(ctx, column.Type, nil); err != nil {
				return nil, errors.Join(err, iter.Close(ctx))
			}
		}
	}
	rows := make([]TypedRow, 0)
	for {
		row, err := iter.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			queryErr = err
			break
		}
		if !typed {
			continue
		}
		if len(row) != len(schema) {
			queryErr = fmt.Errorf("SQL row/schema width mismatch")
			break
		}
		values := make(TypedRow, len(row))
		for index, value := range row {
			values[index], err = sqlTypedValue(ctx, schema[index].Type, value)
			if err != nil {
				queryErr = err
				break
			}
		}
		if queryErr != nil {
			break
		}
		rows = append(rows, values)
	}
	closeErr := iter.Close(ctx)
	if queryErr != nil || closeErr != nil {
		return nil, errors.Join(queryErr, closeErr)
	}
	return rows, nil
}

func sqlBuildRevision() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				return setting.Value
			}
		}
	}
	return "unknown"
}

// RunSQLWorkload starts a fresh engine. A nonnil prefix consumes once, then uses newest.
func RunSQLWorkload(workload SQLWorkload, prefix []int) (SQLResult, error) {
	controller := &sqlReplayController{prefix: prefix, prefixMode: prefix != nil}
	return runSQLWorkload(workload, controller)
}

func runSQLWorkload(workload SQLWorkload, controller *sqlReplayController) (result SQLResult, err error) {
	defer func() {
		result.Choices = controller.choices
		if err != nil {
			result.Failure = nil
		}
	}()
	workload, err = normalizeSQLWorkload(workload)
	if err != nil {
		return result, err
	}
	result.Workload, result.Revision = workload, sqlBuildRevision()
	sqlReplayVariables.Do(RegisterVariables)
	iso, _ := sqlIsolation(workload.Isolation)
	seed := *workload.Seed
	store := NewStore(seed, seed+1)
	defer store.Close()
	if err := store.SetReadPolicy(workload.ReadPolicy, *workload.DeviationProbability); err != nil {
		return result, err
	}
	budget := 1
	if workload.DeviationBudget != nil {
		budget = *workload.DeviationBudget
	}
	if err := store.SetDeviationBudget(budget); err != nil {
		return result, err
	}
	provider := NewProvider(store, iso, nil)
	engine := NewEngine(provider)
	defer func() {
		// Upstream Shutdown cancels its own context and returns context.Canceled.
		if closeErr := engine.Close(); closeErr != nil && !errors.Is(closeErr, context.Canceled) {
			err = errors.Join(err, fmt.Errorf("execution engine close: %w", closeErr))
		}
	}()
	sessions := make(map[int]*sql.Context)
	defer func() {
		for _, ctx := range sessions {
			ctx.Session.(*Session).SessionEnd()
		}
	}()
	newContext := func(sid int) *sql.Context {
		return sql.NewContext(context.Background(), sql.WithSession(NewSession(store, iso, sid)))
	}
	loader := newContext(1)
	defer loader.Session.(*Session).SessionEnd()
	if err := store.BeginInitialization(); err != nil {
		return result, fmt.Errorf("baseline initialization: %w", err)
	}
	for index, query := range workload.Setup {
		if _, err := sqlReplayQuery(engine, loader, query, false); err != nil {
			return result, fmt.Errorf("baseline setup %d: %w", index, err)
		}
	}
	baseline := newContext(1)
	defer baseline.Session.(*Session).SessionEnd()
	if _, err := sqlReplayQuery(engine, baseline, "BEGIN", false); err != nil {
		return result, fmt.Errorf("baseline begin: %w", err)
	}
	rows, err := sqlReplayQuery(engine, baseline, workload.Assertion.SQL, true)
	rollbackErr := baseline.Session.(*Session).Rollback(baseline, nil)
	if err != nil {
		return result, fmt.Errorf("baseline assertion: %w", err)
	}
	if rollbackErr != nil {
		return result, fmt.Errorf("baseline rollback: %w", rollbackErr)
	}
	if !reflect.DeepEqual(rows, workload.Assertion.BaselineRows) {
		return result, fmt.Errorf("invalid baseline: assertion rows differ from baseline_rows: got %v", rows)
	}
	if err := store.SealInitialState(); err != nil {
		return result, fmt.Errorf("baseline seal: %w", err)
	}
	if err := controller.initialize(store); err != nil {
		return result, err
	}
	store.sqlReplay = controller
	store.candidateKeysForTesting = controller.candidateKeysForTesting
	workloadStarted := time.Now()
	defer func() {
		if !workloadStarted.IsZero() {
			result.Elapsed = time.Since(workloadStarted)
		}
	}()
	for _, transaction := range workload.Transactions {
		result.Transactions.Attempted++
		ctx := sessions[transaction.SessionID]
		if ctx == nil {
			ctx = newContext(transaction.SessionID)
			sessions[transaction.SessionID] = ctx
		}
		session := ctx.Session.(*Session)
		controller.transaction = transaction.ID
		if _, err := sqlReplayQuery(engine, ctx, "BEGIN", false); err != nil {
			return result, fmt.Errorf("execution begin %q: %w", transaction.ID, err)
		}
		provenance := make([]ReplaySource, 0)
		for _, statement := range transaction.Statements {
			controller.statement, controller.ordinal = statement.ID, 0
			_, statementErr := sqlReplayQuery(engine, ctx, statement.SQL, false)
			if controller.err != nil {
				return result, controller.err
			}
			if statementErr != nil {
				return result, fmt.Errorf("execution statement %q: %w", statement.ID, statementErr)
			}
			txn := session.liveTxn()
			if txn == nil {
				return result, fmt.Errorf("execution statement %q ended its transaction", statement.ID)
			}
			txn.mu.Lock()
			if len(provenance) > len(txn.writes) {
				provenance = provenance[:len(txn.writes)]
			}
			ordinal := 0
			for _, write := range txn.writes[len(provenance):] {
				ordinal++
				key, keyErr := controller.cell(write.Tab, write.Pkey, write.Col)
				if keyErr != nil {
					txn.mu.Unlock()
					return result, keyErr
				}
				provenance = append(provenance, ReplaySource{TransactionID: transaction.ID, StatementID: statement.ID, WriteOrdinal: ordinal, Key: key})
			}
			txn.mu.Unlock()
		}
		before := len(store.logSnapshot())
		if _, err := sqlReplayQuery(engine, ctx, strings.ToUpper(transaction.Finish), false); err != nil {
			if errors.Is(err, errCommitRejected) {
				result.Transactions.Rejected++
			}
			if controller.err != nil {
				return result, controller.err
			}
			return result, fmt.Errorf("execution %s %q: %w", transaction.Finish, transaction.ID, err)
		}
		result.Transactions.Completed++
		if transaction.Finish == "commit" {
			result.Transactions.Committed++
		} else {
			result.Transactions.RolledBack++
		}
		if controller.err != nil {
			return result, controller.err
		}
		if transaction.Finish == "commit" {
			logs := store.logSnapshot()
			if len(logs) > before {
				if len(logs) != before+1 || len(logs[before].Writes) != len(provenance) {
					return result, fmt.Errorf("trace provenance: committed write stream changed")
				}
				for index, write := range logs[before].Writes {
					if controller.keys[write.Key] != provenance[index].Key {
						return result, fmt.Errorf("trace provenance: committed write key changed")
					}
					controller.sources[write.ID] = provenance[index]
				}
			} else if len(provenance) != 0 {
				return result, fmt.Errorf("trace provenance: committed writes missing")
			}
		}
	}
	result.Elapsed = time.Since(workloadStarted)
	workloadStarted = time.Time{}
	if err := controller.finish(); err != nil {
		return result, err
	}
	result.Choices = controller.choices
	if result.History, result.Variables, err = controller.history(store.logSnapshot()); err != nil {
		return result, err
	}
	store.sqlReplay = nil
	validationID := 1
	usedSessions := make(map[int]bool, len(workload.Transactions))
	for _, transaction := range workload.Transactions {
		usedSessions[transaction.SessionID] = true
	}
	for usedSessions[validationID] {
		validationID++
	}
	validation := newContext(validationID)
	defer validation.Session.(*Session).SessionEnd()
	if err := validation.Session.SetSessionVariable(validation, "dbcop_read_latest", int8(1)); err != nil {
		return result, fmt.Errorf("validation policy: %w", err)
	}
	if _, err := sqlReplayQuery(engine, validation, "BEGIN", false); err != nil {
		return result, fmt.Errorf("validation begin: %w", err)
	}
	result.ObservedRows, err = sqlReplayQuery(engine, validation, workload.Assertion.SQL, true)
	rollbackErr = validation.Session.(*Session).Rollback(validation, nil)
	if err != nil {
		return result, fmt.Errorf("validation assertion: %w", err)
	}
	if rollbackErr != nil {
		return result, fmt.Errorf("validation rollback: %w", rollbackErr)
	}
	if !reflect.DeepEqual(result.ObservedRows, workload.Assertion.WantRows) {
		result.Failure = &FailureFingerprint{AssertionID: workload.Assertion.ID, Kind: "rows_mismatch"}
	}
	return result, nil
}

func validateSQLArtifact(artifact ReplayArtifact) error {
	return validateSQLArtifactEvidence(artifact, true)
}

func validateSQLArtifactEvidence(artifact ReplayArtifact, requireFailure bool) error {
	if artifact.SchemaVersion != sqlReplaySchema {
		return fmt.Errorf("invalid artifact: unsupported schema_version %d", artifact.SchemaVersion)
	}
	if artifact.Revision == "" || artifact.ValidationPolicy != sqlValidationPolicy || artifact.Choices == nil || artifact.ObservedRows == nil || artifact.Variables == nil || len(artifact.History) == 0 {
		return fmt.Errorf("invalid artifact: missing evidence or unsupported validation policy")
	}
	normalized, err := normalizeSQLWorkload(artifact.Workload)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(normalized, artifact.Workload) {
		return fmt.Errorf("invalid artifact: workload configuration is not normalized")
	}
	if requireFailure || artifact.Failure != (FailureFingerprint{}) {
		if artifact.Failure.AssertionID != artifact.Workload.Assertion.ID || artifact.Failure.Kind != "rows_mismatch" {
			return fmt.Errorf("invalid artifact: failure fingerprint is not this assertion's rows_mismatch")
		}
		if reflect.DeepEqual(artifact.ObservedRows, artifact.Workload.Assertion.WantRows) {
			return fmt.Errorf("invalid artifact: observed rows do not fail the assertion")
		}
	} else if !reflect.DeepEqual(artifact.ObservedRows, artifact.Workload.Assertion.WantRows) {
		return fmt.Errorf("invalid artifact: passing evidence does not satisfy the assertion")
	}
	if err := validateSQLRows(artifact.ObservedRows); err != nil {
		return err
	}
	var history [][]historyTxn
	if err := decodeSQLJSON(bytes.NewReader(artifact.History), &history); err != nil || history == nil {
		return fmt.Errorf("invalid artifact: malformed committed history")
	}
	transactions := make(map[string]SQLTransaction, len(artifact.Workload.Transactions))
	positions := make(map[string]int, len(artifact.Workload.Transactions))
	statements := make(map[string]string)
	for index, transaction := range artifact.Workload.Transactions {
		transactions[transaction.ID], positions[transaction.ID] = transaction, index
		for _, statement := range transaction.Statements {
			statements[statement.ID] = transaction.ID
		}
	}
	ordinals := make(map[string]int)
	for key, variable := range artifact.Variables {
		if key == "" || variable == 0 {
			return fmt.Errorf("invalid artifact: malformed variable identity")
		}
	}
	for index, choice := range artifact.Choices {
		if choice.TransactionID == "" || choice.StatementID == "" || choice.ReadOrdinal <= 0 || choice.Reader <= 0 || choice.Key == "" || choice.Choice < 0 || choice.Choice >= len(choice.Candidates) || choice.Candidates[choice.Choice] != choice.Selected {
			return fmt.Errorf("invalid artifact: malformed choice %d", index)
		}
		transaction, exists := transactions[choice.TransactionID]
		ordinals[choice.StatementID]++
		if !exists || transaction.SessionID != choice.Reader || statements[choice.StatementID] != choice.TransactionID || choice.ReadOrdinal != ordinals[choice.StatementID] || artifact.Variables[choice.Key] == 0 {
			return fmt.Errorf("invalid artifact: unknown or reordered read site %d", index)
		}
		seen := make(map[ReplaySource]bool, len(choice.Candidates))
		for _, source := range choice.Candidates {
			if source.Key != choice.Key || source.Initial && (source.TransactionID != "" || source.StatementID != "" || source.WriteOrdinal != 0) || !source.Initial && (source.TransactionID == "" || source.StatementID == "" || source.WriteOrdinal <= 0) {
				return fmt.Errorf("invalid artifact: malformed source at choice %d", index)
			}
			if seen[source] {
				return fmt.Errorf("invalid artifact: duplicate candidate at choice %d", index)
			}
			seen[source] = true
			if !source.Initial {
				producer, exists := transactions[source.TransactionID]
				if !exists || producer.Finish != "commit" || positions[source.TransactionID] >= positions[choice.TransactionID] || statements[source.StatementID] != source.TransactionID {
					return fmt.Errorf("invalid artifact: unknown or uncommitted source at choice %d", index)
				}
			}
		}
	}
	return nil
}

// CaptureSQLFailure publishes only complete rows-mismatch evidence, never SQL errors.
func CaptureSQLFailure(workload SQLWorkload, result SQLResult) (ReplayArtifact, error) {
	normalized, err := normalizeSQLWorkload(workload)
	if err != nil {
		return ReplayArtifact{}, err
	}
	if !reflect.DeepEqual(normalized, result.Workload) || result.Failure == nil {
		return ReplayArtifact{}, fmt.Errorf("capture requires matching normalized workload and assertion failure")
	}
	artifact := ReplayArtifact{SchemaVersion: sqlReplaySchema, Revision: result.Revision, ValidationPolicy: sqlValidationPolicy, Workload: result.Workload, Choices: result.Choices, ObservedRows: result.ObservedRows, History: result.History, Variables: result.Variables, Failure: *result.Failure}
	if err := validateSQLArtifact(artifact); err != nil {
		return ReplayArtifact{}, err
	}
	return artifact, nil
}

// ReplaySQLArtifact requires exact semantic decisions, assertion rows and history.
// Revision drift is informational: the result carries the current build revision.
func ReplaySQLArtifact(artifact ReplayArtifact) (SQLResult, error) {
	if err := validateSQLArtifact(artifact); err != nil {
		return SQLResult{}, err
	}
	result, err := runSQLWorkload(artifact.Workload, &sqlReplayController{strict: true, expected: artifact.Choices})
	if err != nil {
		return result, err
	}
	var expectedHistory, actualHistory [][]historyTxn
	_ = json.Unmarshal(artifact.History, &expectedHistory)
	_ = json.Unmarshal(result.History, &actualHistory)
	if result.Failure == nil || *result.Failure != artifact.Failure || !reflect.DeepEqual(result.ObservedRows, artifact.ObservedRows) || !reflect.DeepEqual(expectedHistory, actualHistory) || !reflect.DeepEqual(result.Variables, artifact.Variables) {
		return result, fmt.Errorf("replay divergence: assertion rows, committed history, variables or failure fingerprint changed")
	}
	return result, nil
}

func runSQLReduction(workload SQLWorkload, retained []ReplayChoice, newest *ReplayChoice) (SQLResult, error) {
	return runSQLWorkload(workload, &sqlReplayController{reduction: true, expected: retained, newest: newest})
}
