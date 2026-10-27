// Package backend implements versioned SQL tables over the KV oracle.
package backend

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// SQL identifiers cannot contain NUL, so membership cannot alias a user cell.
const rowExistenceColumn = "\x00dbcop:exists"

// TableDef declares a table shape. PkeyIdx is the legacy single-column
// ordinal; -1 denotes a keyless table. PkeyIdxs preserves composite-key order.
type TableDef struct {
	Name     string
	Columns  []*sql.Column
	PkeyIdx  int
	PkeyIdxs []int
}

type tableState struct {
	mu              sync.RWMutex
	keys            map[string]struct{} // append-only set of committed physical identities
	nextRow         uint64
	autoIncrement   uint64
	autoIncrementMu sync.Mutex
	indexes         map[string]*tableIndex
}

// Keep only the sql.Index contract: memory.Index's optional ordered and
// extended-index interfaces require memory's own row storage.
type tableIndex struct {
	sql.Index
	columns []int
	// A conservative candidate directory, not an oracle-visible value store.
	// Retaining old tuples also permits historical reads after indexed updates.
	entries    map[string][]sql.Row
	equalities []map[string][]string // per-column value -> historical physical identities
}

type Table struct {
	def          TableDef
	storageName  string
	databaseName string
	store        *Store
	iso          Isolation
	state        *tableState
	foreignKeys  *foreignKeyState
	collation    sql.CollationID
	proj         []int
	filters      []sql.Expression
	pushed       []tableFilter
}

var _ sql.Table = (*Table)(nil)
var _ sql.InsertableTable = (*Table)(nil)
var _ sql.UpdatableTable = (*Table)(nil)
var _ sql.DeletableTable = (*Table)(nil)
var _ sql.TruncateableTable = (*Table)(nil)
var _ sql.IndexAddressableTable = (*Table)(nil)
var _ sql.IndexAlterableTable = (*Table)(nil)
var _ sql.ProjectedTable = (*Table)(nil)
var _ sql.PrimaryKeyTable = (*Table)(nil)
var _ sql.FilteredTable = (*Table)(nil)
var _ sql.StatisticsTable = (*Table)(nil)
var _ sql.Databaseable = (*Table)(nil)

type tableFilter struct {
	expr     sql.Expression
	ordinal  int
	index    *tableIndex
	position int
}

func NewTable(def TableDef, store *Store, iso Isolation) *Table {
	return newTable(def, store, iso, "tpcc", def.Name, &foreignKeyState{})
}

func newTable(def TableDef, store *Store, iso Isolation, databaseName, storageName string, foreignKeys *foreignKeyState) *Table {
	t := &Table{def: def, store: store, iso: iso, storageName: storageName, databaseName: databaseName,
		collation: sql.Collation_Default, foreignKeys: foreignKeys,
		state: &tableState{keys: make(map[string]struct{}), indexes: make(map[string]*tableIndex), autoIncrement: 1}}
	if ords := t.primaryOrdinals(); len(ords) > 0 && len(def.Columns) > 0 {
		t.state.indexes["primary"] = t.newIndex("PRIMARY", ords, true, "")
	}
	store.registerTable(storageName, t)
	return t
}

func (t *Table) Name() string               { return t.def.Name }
func (t *Table) String() string             { return t.def.Name }
func (t *Table) Collation() sql.CollationID { return t.collation }
func (t *Table) Database() string           { return t.databaseName }

// Tombstones remain historical candidates, so this is an estimate rather than
// a SQL COUNT shortcut. Supplying zero here makes native join costing degenerate.
func (t *Table) RowCount(ctx *sql.Context) (uint64, bool, error) {
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	t.state.mu.RLock()
	count := uint64(len(t.state.keys))
	t.state.mu.RUnlock()
	return count, false, nil
}

// Use the memory backend's schema-width estimate; no row or oracle reads.
func (t *Table) DataLength(ctx *sql.Context) (uint64, error) {
	var width uint64
	for _, column := range t.def.Columns {
		switch typ := column.Type.(type) {
		case sql.NumberType, sql.DatetimeType, sql.YearType:
			width += 8
		case sql.StringType:
			width += uint64(typ.MaxByteLength())
		case sql.DecimalType:
			width += uint64(typ.MaximumScale())
		case sql.EnumType:
			width += 2
		case types.JsonType:
			width += 20
		case types.TimeType:
			width += 16
		case types.BitType, sql.NullType:
			width++
		}
	}
	count, _, err := t.RowCount(ctx)
	return count * width, err
}

func (t *Table) Schema() sql.Schema {
	if t.proj == nil {
		return t.def.Columns
	}
	schema := make(sql.Schema, len(t.proj))
	for i, ord := range t.proj {
		schema[i] = t.def.Columns[ord]
	}
	return schema
}

func (t *Table) primaryOrdinals() []int {
	if len(t.def.PkeyIdxs) > 0 {
		return t.def.PkeyIdxs
	}
	if t.def.PkeyIdx >= 0 && (len(t.def.Columns) == 0 || t.def.PkeyIdx < len(t.def.Columns)) {
		return []int{t.def.PkeyIdx}
	}
	return nil
}

func (t *Table) PrimaryKeySchema() sql.PrimaryKeySchema {
	return sql.NewPrimaryKeySchema(t.def.Columns, t.primaryOrdinals()...)
}

func (t *Table) pkeyOf(row sql.Row) string {
	ords := t.primaryOrdinals()
	parts := make([]string, len(ords))
	escape := false
	for i, ord := range ords {
		parts[i] = fmt.Sprint(row[ord])
		escape = escape || strings.ContainsAny(parts[i], "#\x00")
	}
	if !escape {
		return strings.Join(parts, "#")
	}
	var key strings.Builder
	key.WriteByte(0)
	for _, part := range parts {
		key.WriteString(strconv.Itoa(len(part)))
		key.WriteByte(':')
		key.WriteString(part)
	}
	return key.String()
}

type singlePartition struct{}

func (*singlePartition) Key() []byte { return []byte{0} }

func (t *Table) Partitions(_ *sql.Context) (sql.PartitionIter, error) {
	return sql.PartitionsToPartitionIter(&singlePartition{}), nil
}

// knownKeys never removes tombstoned identities: their old existence versions
// must remain available to the weak-read oracle.
func (t *Table) knownKeys(sess *Session) []string {
	t.state.mu.RLock()
	keys := maps.Clone(t.state.keys)
	t.state.mu.RUnlock()
	if sess != nil {
		if txn := sess.liveTxn(); txn != nil {
			for _, member := range txn.memberSnapshot() {
				if member.Tab == t.storageName {
					keys[member.Pkey] = struct{}{}
				}
			}
		}
	}
	out := slices.AppendSeq(make([]string, 0, len(keys)), maps.Keys(keys))
	slices.Sort(out)
	return out
}

func (t *Table) readCell(ctx *sql.Context, sess *Session, key, column string) (interface{}, bool, error) {
	if sess != nil {
		value, _, ok := sess.oracleReadContext(explorerContext(ctx), t.storageName, key, column)
		return value, ok, sess.takeReadError()
	}
	versions := t.store.committedVersions(cellKey(t.storageName, key, column))
	if len(versions) == 0 {
		return nil, false, nil
	}
	return versions[len(versions)-1].Value, true, nil
}

func (t *Table) latestCell(sess *Session, key, column string) interface{} {
	if sess != nil {
		if txn := sess.liveTxn(); txn != nil {
			if value, ok := txn.staged(t.storageName, key, column); ok {
				return value
			}
		}
	}
	value, _, _ := t.readCell(nil, nil, key, column)
	return value
}

func (t *Table) latestRow(sess *Session, key string) sql.Row {
	row := make(sql.Row, len(t.def.Columns))
	for i, col := range t.def.Columns {
		row[i] = t.latestCell(sess, key, col.Name)
	}
	return row
}

type lookupPartition struct {
	keys    []string
	columns []int
	filter  sql.Expression
}

func (*lookupPartition) Key() []byte { return []byte{0} }

func (t *Table) PartitionRows(ctx *sql.Context, partition sql.Partition) (sql.RowIter, error) {
	sess := sessionOf(ctx)
	if sess != nil {
		sess.txnMu.Lock()
		sess.readLatest = readLatestVar(ctx)
		sess.txnMu.Unlock()
	}
	var keys []string
	lookup, indexed := partition.(*lookupPartition)
	if indexed {
		keys = lookup.keys
	} else if candidates, bounded := t.pushedKeys(ctx); bounded {
		keys = candidates
	} else {
		keys = t.knownKeys(sess)
	}
	keys, err := t.filterCandidates(ctx, keys)
	if err != nil {
		return nil, err
	}
	rows := make([]sql.Row, 0, len(keys))
	var keylessTxn *Txn
	if sess != nil && len(t.primaryOrdinals()) == 0 {
		keylessTxn = sess.liveTxn()
	}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		present, ok, err := t.readCell(ctx, sess, key, rowExistenceColumn)
		if err != nil {
			return nil, err
		}
		if !ok && sess != nil {
			return nil, fmt.Errorf("no admissible existence version for %s row %q", t.Name(), key)
		}
		if !ok || present != true {
			continue
		}
		full := make(sql.Row, len(t.def.Columns))
		read := make([]bool, len(full))
		readColumn := func(ord int) error {
			if read[ord] {
				return nil
			}
			value, ok, err := t.readCell(ctx, sess, key, t.def.Columns[ord].Name)
			if err != nil {
				return err
			}
			if !ok && sess != nil {
				return fmt.Errorf("no admissible version for %s.%s row %q", t.Name(), t.def.Columns[ord].Name, key)
			}
			full[ord], read[ord] = value, true
			return nil
		}
		if indexed {
			for _, ord := range lookup.columns {
				if err := readColumn(ord); err != nil {
					return nil, err
				}
			}
			match, err := sql.EvaluateCondition(ctx, lookup.filter, full)
			if err != nil {
				return nil, err
			}
			if !sql.IsTrue(match) {
				continue
			}
		}
		matches := true
		for _, filter := range t.pushed {
			if err := readColumn(filter.ordinal); err != nil {
				return nil, err
			}
			match, err := sql.EvaluateCondition(ctx, filter.expr, full)
			if err != nil {
				return nil, err
			}
			if !sql.IsTrue(match) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		if t.proj == nil {
			for i := range t.def.Columns {
				if err := readColumn(i); err != nil {
					return nil, err
				}
			}
			rows = append(rows, full)
		} else {
			row := make(sql.Row, len(t.proj))
			for i, ord := range t.proj {
				if err := readColumn(ord); err != nil {
					return nil, err
				}
				row[i] = full[ord]
			}
			rows = append(rows, row)
		}
		if keylessTxn != nil {
			keylessTxn.recordKeylessRow(t.storageName, key, full)
		}
	}
	return sql.RowsToRowIter(rows...), nil
}

func (t *Table) Filters() []sql.Expression { return t.filters }

// Keep residual indexed predicates even when the optimizer chooses another
// index: a partial PRIMARY lookup must not oracle-read every matching prefix.
func (t *Table) HandledFilters(filters []sql.Expression) []sql.Expression {
	var handled []sql.Expression
	for _, filter := range filters {
		if _, ok := t.planFilter(filter); ok {
			handled = append(handled, filter)
		}
	}
	return handled
}

func (t *Table) planFilter(filter sql.Expression) (tableFilter, bool) {
	switch filter.(type) {
	case *expression.Equals, *expression.NullSafeEquals,
		*expression.GreaterThan, *expression.GreaterThanOrEqual,
		*expression.LessThan, *expression.LessThanOrEqual:
	default:
		return tableFilter{}, false
	}
	children := filter.Children()
	field, ok := children[0].(*expression.GetField)
	fieldChild := 0
	if !ok {
		field, ok = children[1].(*expression.GetField)
		fieldChild = 1
	}
	if !ok || (field.Table() != "" && !strings.EqualFold(field.Table(), t.Name())) {
		return tableFilter{}, false
	}
	if _, ok := children[1-fieldChild].(*expression.Literal); !ok {
		return tableFilter{}, false
	}
	ordinal := sql.Schema(t.def.Columns).IndexOfColName(field.Name())
	if ordinal < 0 {
		return tableFilter{}, false
	}
	children[fieldChild] = field.WithIndex(ordinal)
	canonical, err := filter.WithChildren(children...)
	if err != nil {
		return tableFilter{}, false
	}
	t.state.mu.RLock()
	defer t.state.mu.RUnlock()
	for _, index := range t.state.indexes {
		for position, indexedColumn := range index.columns {
			if indexedColumn == ordinal {
				return tableFilter{expr: canonical, ordinal: ordinal, index: index, position: position}, true
			}
		}
	}
	return tableFilter{}, false
}

func (t *Table) WithFilters(_ *sql.Context, filters []sql.Expression) sql.Table {
	copy := *t
	copy.filters = filters
	copy.pushed = make([]tableFilter, len(filters))
	for i, filter := range filters {
		planned, ok := t.planFilter(filter)
		if !ok {
			panic(fmt.Sprintf("unhandled table filter %s", filter))
		}
		copy.pushed[i] = planned
	}
	return &copy
}

func pointFilterValue(filter tableFilter) (interface{}, bool) {
	switch filter.expr.(type) {
	case *expression.Equals, *expression.NullSafeEquals:
		for _, child := range filter.expr.Children() {
			if literal, ok := child.(*expression.Literal); ok && literal.Value() != nil {
				return literal.Value(), true
			}
		}
	}
	return nil, false
}

// Filter pushdown can remove the engine's index node entirely. Use the same
// physical-key / equality directories rather than turning it into a full scan.
func (t *Table) pushedKeys(ctx *sql.Context) ([]string, bool) {
	if len(t.pushed) == 0 {
		return nil, false
	}
	ordinals := t.primaryOrdinals()
	if len(ordinals) > 0 {
		row := make(sql.Row, len(t.def.Columns))
		complete := true
		for _, ordinal := range ordinals {
			found := false
			if types.IsInteger(t.def.Columns[ordinal].Type) {
				for _, filter := range t.pushed {
					value, point := pointFilterValue(filter)
					if filter.ordinal != ordinal || !point {
						continue
					}
					converted, inRange, err := t.def.Columns[ordinal].Type.Convert(ctx, value)
					if err == nil && inRange {
						row[ordinal], found = converted, true
						break
					}
				}
			}
			if !found {
				complete = false
				break
			}
		}
		if complete {
			return []string{t.pkeyOf(row)}, true
		}
	}
	var smallest []string
	bounded := false
	for _, filter := range t.pushed {
		value, point := pointFilterValue(filter)
		if !point {
			continue
		}
		token, supported, err := t.indexEqualityKey(ctx, filter.index, filter.position, value)
		if err != nil || !supported {
			continue
		}
		t.state.mu.RLock()
		posting := filter.index.equalities[filter.position][token]
		t.state.mu.RUnlock()
		if !bounded || len(posting) < len(smallest) {
			smallest, bounded = posting, true
		}
	}
	if !bounded {
		return nil, false
	}
	unique := make(map[string]struct{}, len(smallest))
	for _, key := range smallest {
		unique[key] = struct{}{}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys, true
}

func (t *Table) filterCandidates(ctx *sql.Context, keys []string) ([]string, error) {
	if len(t.pushed) == 0 {
		return keys, nil
	}
	row := make(sql.Row, len(t.def.Columns))
	selected := make([]string, 0, len(keys))
	initialMatches := make([]bool, len(t.pushed))
	for i, filter := range t.pushed {
		value, err := sql.EvaluateCondition(ctx, filter.expr, row)
		if err != nil {
			return nil, err
		}
		initialMatches[i] = sql.IsTrue(value)
	}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		matches := true
		for i, filter := range t.pushed {
			match := initialMatches[i]
			t.state.mu.RLock()
			tuples := filter.index.entries[key]
			t.state.mu.RUnlock()
			if len(tuples) == 0 {
				continue // Unknown point keys still require an oracle existence read.
			}
			for _, tuple := range tuples {
				if match {
					break
				}
				if tuple[filter.position] == nil {
					continue // The cached initial truth also covers stored NULL.
				}
				row[filter.ordinal] = tuple[filter.position]
				value, err := sql.EvaluateCondition(ctx, filter.expr, row)
				if err != nil {
					return nil, err
				}
				match = sql.IsTrue(value)
			}
			if !match {
				matches = false
				break
			}
		}
		if matches {
			selected = append(selected, key)
		}
	}
	return selected, nil
}

func (t *Table) WithProjections(columns []string) sql.Table {
	copy := *t
	if columns == nil {
		copy.proj = nil
		return &copy
	}
	copy.proj = make([]int, len(columns))
	for i, name := range columns {
		ord := sql.Schema(t.def.Columns).IndexOfColName(name)
		if ord < 0 {
			panic(fmt.Sprintf("unknown projected column %s.%s", t.Name(), name))
		}
		copy.proj[i] = ord
	}
	return &copy
}

func (t *Table) Projections() []string {
	if t.proj == nil {
		return nil
	}
	out := make([]string, len(t.proj))
	for i, ord := range t.proj {
		out[i] = t.def.Columns[ord].Name
	}
	return out
}

func (t *Table) newIndex(name string, ords []int, unique bool, comment string) *tableIndex {
	exprs := make([]sql.Expression, len(ords))
	for i, ord := range ords {
		col := t.def.Columns[ord]
		exprs[i] = expression.NewGetFieldWithTable(ord, 0, col.Type, t.databaseName, t.Name(), col.Name, col.Nullable)
	}
	return &tableIndex{Index: &memory.Index{DB: t.databaseName, TableName: t.Name(), Name: name,
		Exprs: exprs, Unique: unique, CommentStr: comment}, columns: ords, entries: make(map[string][]sql.Row),
		equalities: make([]map[string][]string, len(ords))}
}

func (t *Table) GetIndexes(_ *sql.Context) ([]sql.Index, error) {
	t.state.mu.RLock()
	defer t.state.mu.RUnlock()
	indexes := make([]sql.Index, 0, len(t.state.indexes))
	for _, index := range t.state.indexes {
		indexes = append(indexes, index)
	}
	slices.SortFunc(indexes, func(a, b sql.Index) int { return strings.Compare(a.ID(), b.ID()) })
	return indexes, nil
}

func (t *Table) PreciseMatch() bool { return true }

// No ordering is advertised: the engine retains ORDER BY and LIMIT operators.
func (t *Table) IndexedAccess(_ *sql.Context, lookup sql.IndexLookup) sql.IndexedTable {
	if _, ok := lookup.Index.(*tableIndex); !ok {
		return nil
	}
	if _, ok := lookup.Ranges.(sql.MySQLRangeCollection); !ok {
		return nil
	}
	return &indexedTable{Table: t}
}

type indexedTable struct{ *Table }

func (t *indexedTable) WithProjections(columns []string) sql.Table {
	return &indexedTable{Table: t.Table.WithProjections(columns).(*Table)}
}

func (t *indexedTable) WithFilters(ctx *sql.Context, filters []sql.Expression) sql.Table {
	return &indexedTable{Table: t.Table.WithFilters(ctx, filters).(*Table)}
}

func (t *indexedTable) LookupPartitions(ctx *sql.Context, lookup sql.IndexLookup) (sql.PartitionIter, error) {
	index, ok := lookup.Index.(*tableIndex)
	if !ok {
		return nil, fmt.Errorf("unsupported index %T", lookup.Index)
	}
	ranges, ok := lookup.Ranges.(sql.MySQLRangeCollection)
	if !ok {
		return nil, fmt.Errorf("expected MySQL index ranges, got %T", lookup.Ranges)
	}
	filter, err := expression.NewRangeFilterExpr(index.Index.(*memory.Index).Exprs, ranges)
	if err != nil {
		return nil, err
	}
	var keys []string
	if t.store.candidateKeysForTesting != nil {
		keys = t.store.candidateKeysForTesting(t.Table)
	} else {
		keys, err = t.candidateKeys(ctx, index, ranges)
		if err != nil {
			return nil, err
		}
	}
	var columns []int
	seen := make(map[int]bool)
	if filter != nil {
		sql.Inspect(filter, func(expr sql.Expression) bool {
			if field, ok := expr.(*expression.GetField); ok && !seen[field.Index()] {
				columns = append(columns, field.Index())
				seen[field.Index()] = true
			}
			return true
		})
	}
	return sql.PartitionsToPartitionIter(&lookupPartition{keys: keys, columns: columns, filter: filter}), nil
}

func (t *Table) candidateKeys(ctx *sql.Context, index *tableIndex, ranges sql.MySQLRangeCollection) ([]string, error) {
	// Integer primary-key point lookups do not scan the candidate directory.
	if index.ID() == "PRIMARY" {
		points := make([]string, 0, len(ranges))
		allPoints := true
		for _, bounds := range ranges {
			row := make(sql.Row, len(t.def.Columns))
			if len(bounds) != len(index.columns) {
				allPoints = false
				break
			}
			for i, bound := range bounds {
				ord := index.columns[i]
				if !types.IsInteger(t.def.Columns[ord].Type) || bound.Type() != sql.RangeType_ClosedClosed {
					allPoints = false
					break
				}
				lower, upper := sql.GetMySQLRangeCutKey(bound.LowerBound), sql.GetMySQLRangeCutKey(bound.UpperBound)
				cmp, err := bound.Typ.Compare(ctx, lower, upper)
				if err != nil {
					return nil, err
				}
				if cmp != 0 {
					allPoints = false
					break
				}
				value, inRange, err := t.def.Columns[ord].Type.Convert(ctx, lower)
				if err != nil || !inRange {
					allPoints = false
					break
				}
				row[ord] = value
			}
			if !allPoints {
				break
			}
			points = append(points, t.pkeyOf(row))
		}
		if allPoints {
			slices.Sort(points)
			return slices.Compact(points), nil
		}
	}
	selected := make(map[string]bool)
	// Each indexed cell may independently serve an old value in the KV model.
	// Match columns independently across retained tuples, not just whole rows.
	for _, bounds := range ranges {
		keys, err := t.rangeCandidates(ctx, index, bounds)
		if err != nil {
			return nil, err
		}
		filters := make([]sql.Expression, len(bounds))
		initialMatches := make([]bool, len(bounds))
		for i, bound := range bounds {
			col := t.def.Columns[index.columns[i]]
			expr := expression.NewGetField(0, col.Type, col.Name, col.Nullable)
			var err error
			filters[i], err = expression.NewRangeFilterExpr([]sql.Expression{expr}, []sql.MySQLRange{{bound}})
			if err != nil {
				return nil, err
			}
			value, err := sql.EvaluateCondition(ctx, filters[i], sql.Row{nil})
			if err != nil {
				return nil, err
			}
			initialMatches[i] = sql.IsTrue(value)
		}
		candidateRow := sql.Row{nil}
		for _, key := range keys {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if selected[key] {
				continue
			}
			t.state.mu.RLock()
			tuples := index.entries[key]
			t.state.mu.RUnlock()
			matches := true
			for i, filter := range filters {
				// Initial NULL remains a candidate, but its truth is key-independent.
				match := initialMatches[i]
				for _, tuple := range tuples {
					if match {
						break
					}
					if tuple[i] == nil {
						continue
					}
					candidateRow[0] = tuple[i]
					value, err := sql.EvaluateCondition(ctx, filter, candidateRow)
					if err != nil {
						return nil, err
					}
					match = sql.IsTrue(value)
				}
				if !match {
					matches = false
					break
				}
			}
			selected[key] = matches
		}
	}
	out := make([]string, 0, len(selected))
	for key, matches := range selected {
		if matches {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Equality postings use the engine's conversion and collation. Hash collisions
// only add candidates; SQL range expressions still validate every returned row.
func equalityKey(ctx *sql.Context, typ sql.Type, value interface{}) (string, bool, error) {
	if value == nil {
		return "", false, nil
	}
	if !types.IsInteger(typ) {
		text, ok := typ.(sql.StringType)
		if !ok {
			return "", false, nil
		}
		switch text.CharacterSet() {
		case sql.CharacterSet_utf8mb3, sql.CharacterSet_utf8mb4, sql.CharacterSet_binary:
		default:
			return "", false, nil
		}
	}
	converted, _, err := typ.Convert(ctx, value)
	if err != nil {
		return "", false, err
	}
	if text, ok := typ.(sql.StringType); ok {
		var str string
		switch value := converted.(type) {
		case string:
			str = value
		case []byte:
			str = string(value)
		default:
			return "", false, fmt.Errorf("unexpected string value %T", converted)
		}
		hash, err := text.Collation().HashToUint(str)
		return strconv.FormatUint(hash, 16), true, err
	}
	return fmt.Sprint(converted), true, nil
}

func (t *Table) addIndexPostings(ctx *sql.Context, index *tableIndex, key string, tuple sql.Row) error {
	for i := range index.columns {
		token, supported, err := t.indexEqualityKey(ctx, index, i, tuple[i])
		if err != nil {
			return err
		}
		if !supported {
			continue
		}
		if index.equalities[i] == nil {
			index.equalities[i] = make(map[string][]string)
		}
		posting := index.equalities[i][token]
		if len(posting) == 0 || posting[len(posting)-1] != key {
			index.equalities[i][token] = append(posting, key)
		}
	}
	return nil
}

func (t *Table) uniqueCandidates(ctx *sql.Context, index *tableIndex, row sql.Row) ([]string, error) {
	var candidates []string
	found := false
	for i, ord := range index.columns {
		token, supported, err := t.indexEqualityKey(ctx, index, i, row[ord])
		if err != nil {
			return nil, err
		}
		if !supported {
			continue
		}
		t.state.mu.RLock()
		posting := index.equalities[i][token]
		t.state.mu.RUnlock()
		if !found || len(posting) < len(candidates) {
			candidates, found = posting, true
		}
	}
	if found {
		return candidates, nil
	}
	return t.knownKeys(sessionOf(ctx)), nil
}

func (t *Table) rangeCandidates(ctx *sql.Context, index *tableIndex, bounds sql.MySQLRange) ([]string, error) {
	var candidates []string
	found := false
	for i, bound := range bounds {
		if bound.Type() != sql.RangeType_ClosedClosed {
			continue
		}
		lower, upper := sql.GetMySQLRangeCutKey(bound.LowerBound), sql.GetMySQLRangeCutKey(bound.UpperBound)
		cmp, err := bound.Typ.Compare(ctx, lower, upper)
		if err != nil {
			return nil, err
		}
		if cmp != 0 {
			continue
		}
		token, supported, err := t.indexEqualityKey(ctx, index, i, lower)
		if err != nil {
			continue // The engine's range expression handles non-convertible bounds.
		}
		if !supported {
			continue
		}
		t.state.mu.RLock()
		posting := index.equalities[i][token]
		t.state.mu.RUnlock()
		if !found || len(posting) < len(candidates) {
			candidates, found = posting, true
		}
	}
	if found {
		return candidates, nil
	}
	return t.knownKeys(sessionOf(ctx)), nil
}

// addIndexRow may run before commit. Rolled-back entries are harmless
// supersets: only versioned existence and oracle reads can return a row.
func (t *Table) addIndexRow(ctx *sql.Context, key string, row sql.Row) error {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	for _, index := range t.state.indexes {
		found := false
		for _, old := range index.entries[key] {
			equal := true
			for i, ord := range index.columns {
				cmp, err := t.def.Columns[ord].Type.Compare(ctx, old[i], row[ord])
				if err != nil {
					return err
				}
				if cmp != 0 {
					equal = false
					break
				}
			}
			if equal {
				found = true
				break
			}
		}
		if !found {
			tuple := make(sql.Row, len(index.columns))
			for i, ord := range index.columns {
				tuple[i] = row[ord]
			}
			index.entries[key] = append(index.entries[key], tuple)
			if err := t.addIndexPostings(ctx, index, key, tuple); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *Table) CreateIndex(ctx *sql.Context, def sql.IndexDef) error {
	if def.Constraint != sql.IndexConstraint_None && def.Constraint != sql.IndexConstraint_Unique {
		return fmt.Errorf("unsupported index constraint %v", def.Constraint)
	}
	ords := make([]int, len(def.Columns))
	var prefixes []uint16
	for i, column := range def.Columns {
		ords[i] = sql.Schema(t.def.Columns).IndexOfColName(column.Name)
		if ords[i] < 0 {
			return fmt.Errorf("unknown index column %s", column.Name)
		}
		if column.Length < 0 || column.Length > 65535 {
			return sql.ErrInvalidIndexPrefix.New(column.Name)
		}
		if column.Length > 0 {
			text, ok := t.def.Columns[ords[i]].Type.(sql.StringType)
			if !ok || column.Length > text.Length() {
				return sql.ErrInvalidIndexPrefix.New(column.Name)
			}
			if prefixes == nil {
				prefixes = make([]uint16, len(def.Columns))
			}
			prefixes[i] = uint16(column.Length)
		}
	}
	if len(ords) == 0 {
		return fmt.Errorf("an index needs at least one column")
	}
	if def.Name == "" {
		def.Name = strings.Join(def.ColumnNames(), "_")
	}
	index := t.newIndex(def.Name, ords, def.IsUnique(), def.Comment)
	index.Index.(*memory.Index).PrefixLens = prefixes
	keys := t.knownKeys(sessionOf(ctx))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Separate sparse tuples preserve every old indexed-cell value without
		// constructing a Cartesian product of historical cell versions.
		for i, ord := range ords {
			versions := t.store.committedVersions(cellKey(t.storageName, key, t.def.Columns[ord].Name))
			for _, version := range versions {
				tuple := make(sql.Row, len(ords))
				tuple[i] = version.Value
				index.entries[key] = append(index.entries[key], tuple)
			}
		}
		if sess := sessionOf(ctx); sess != nil && sess.liveTxn() != nil {
			tuple := make(sql.Row, len(ords))
			for i, ord := range ords {
				tuple[i] = t.latestCell(sess, key, t.def.Columns[ord].Name)
			}
			index.entries[key] = append(index.entries[key], tuple)
		}
	}
	for key, tuples := range index.entries {
		for _, tuple := range tuples {
			if err := t.addIndexPostings(ctx, index, key, tuple); err != nil {
				return err
			}
		}
	}
	if index.IsUnique() {
		for _, key := range keys {
			if t.latestCell(sessionOf(ctx), key, rowExistenceColumn) != true {
				continue
			}
			row := t.latestRow(sessionOf(ctx), key)
			candidates, err := t.uniqueCandidates(ctx, index, row)
			if err != nil {
				return err
			}
			for _, other := range candidates {
				if other == key || t.latestCell(sessionOf(ctx), other, rowExistenceColumn) != true {
					continue
				}
				match, err := t.uniqueMatch(ctx, index, row, t.latestRow(sessionOf(ctx), other))
				if err != nil {
					return err
				}
				if match {
					return sql.NewUniqueKeyErr(def.Name, false, row)
				}
			}
		}
	}
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	name := strings.ToLower(def.Name)
	if t.state.indexes[name] != nil {
		return sql.ErrDuplicateKey.New(def.Name)
	}
	t.state.indexes[name] = index
	return nil
}

func (t *Table) DropIndex(_ *sql.Context, name string) error {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	key := strings.ToLower(name)
	if key == "primary" {
		return fmt.Errorf("dropping primary keys is not supported")
	}
	if t.state.indexes[key] == nil {
		return sql.ErrIndexNotFound.New(name)
	}
	delete(t.state.indexes, key)
	return nil
}

func (t *Table) RenameIndex(_ *sql.Context, from, to string) error {
	t.state.mu.Lock()
	defer t.state.mu.Unlock()
	old, next := strings.ToLower(from), strings.ToLower(to)
	index := t.state.indexes[old]
	if index == nil {
		return sql.ErrIndexNotFound.New(from)
	}
	if old == "primary" || next == "primary" {
		return fmt.Errorf("renaming primary keys is not supported")
	}
	if old == next {
		return nil
	}
	if t.state.indexes[next] != nil {
		return sql.ErrDuplicateKey.New(to)
	}
	delete(t.state.indexes, old)
	index.Index.(*memory.Index).Name = to
	t.state.indexes[next] = index
	return nil
}

func (t *Table) Inserter(ctx *sql.Context) sql.RowInserter {
	return &rowWriter{table: t, sess: sessionOf(ctx)}
}
func (t *Table) Updater(ctx *sql.Context) sql.RowUpdater {
	return &rowWriter{table: t, sess: sessionOf(ctx), captureRows: true}
}
func (t *Table) Deleter(ctx *sql.Context) sql.RowDeleter {
	return &rowWriter{table: t, sess: sessionOf(ctx), captureRows: true}
}

func (t *Table) Truncate(ctx *sql.Context) (int, error) {
	sess := sessionOf(ctx)
	if sess == nil {
		return 0, fmt.Errorf("table writes require a backend session")
	}
	references, err := t.GetReferencedForeignKeys(ctx)
	if err != nil {
		return 0, err
	}
	if len(references) > 0 {
		checks, err := ctx.GetSessionVariable(ctx, "foreign_key_checks")
		if err != nil {
			return 0, err
		}
		if checks.(int8) == 1 {
			for _, fk := range references {
				// GMS's truncate analyzer only scans the current database.
				if !strings.EqualFold(fk.Database, t.databaseName) || !strings.EqualFold(fk.Table, t.Name()) {
					return 0, sql.ErrTruncateReferencedFromForeignKey.New(t.Name(), fk.Name, fk.Table)
				}
			}
		}
	}
	txn := sess.beginTxn()
	checkpoint := txn.checkpoint()
	count := 0
	for _, key := range t.knownKeys(sess) {
		if t.latestCell(sess, key, rowExistenceColumn) == true {
			if err := sess.stageMembership(ctx, txn, t.storageName, key, false); err != nil {
				txn.restore(checkpoint)
				sess.takeReadError()
				return 0, err
			}
			count++
		}
	}
	return count, nil
}

type rowWriter struct {
	table        *Table
	sess         *Session
	checkpoint   txnCheckpoint
	statementTxn *Txn
	used         map[string]bool
	captureRows  bool
}

func (w *rowWriter) transaction() (*Txn, error) {
	if w.sess == nil {
		return nil, fmt.Errorf("table writes require a backend session")
	}
	return w.sess.beginTxn(), nil
}

func (t *Table) uniqueMatch(ctx *sql.Context, index *tableIndex, left, right sql.Row) (bool, error) {
	for i, ord := range index.columns {
		if left[ord] == nil || right[ord] == nil {
			return false, nil
		}
		leftValue, err := t.indexValue(ctx, index, i, left[ord])
		if err != nil {
			return false, err
		}
		rightValue, err := t.indexValue(ctx, index, i, right[ord])
		if err != nil {
			return false, err
		}
		cmp, err := t.def.Columns[ord].Type.Compare(ctx, leftValue, rightValue)
		if err != nil || cmp != 0 {
			return false, err
		}
	}
	return true, nil
}

func (w *rowWriter) checkUnique(ctx *sql.Context, key, oldKey string, row sql.Row) error {
	t := w.table
	indexes, _ := t.GetIndexes(ctx)
	for _, raw := range indexes {
		index := raw.(*tableIndex)
		if !index.IsUnique() {
			continue
		}
		keys := []string{key}
		directPrimary := index.ID() == "PRIMARY"
		for _, ord := range index.columns {
			directPrimary = directPrimary && types.IsInteger(t.def.Columns[ord].Type)
		}
		if !directPrimary {
			var err error
			keys, err = t.uniqueCandidates(ctx, index, row)
			if err != nil {
				return err
			}
		}
		for _, other := range keys {
			if other == oldKey || t.latestCell(w.sess, other, rowExistenceColumn) != true {
				continue
			}
			existing := t.latestRow(w.sess, other)
			match, err := t.uniqueMatch(ctx, index, row, existing)
			if err != nil {
				return err
			}
			if match {
				return sql.NewUniqueKeyErr(index.ID(), index.ID() == "PRIMARY", existing)
			}
		}
	}
	return nil
}

func (w *rowWriter) Insert(ctx *sql.Context, row sql.Row) error {
	txn, err := w.transaction()
	if err != nil {
		return err
	}
	if len(row) != len(w.table.def.Columns) {
		return fmt.Errorf("row has %d columns, expected %d", len(row), len(w.table.def.Columns))
	}
	key := w.table.pkeyOf(row)
	if len(w.table.primaryOrdinals()) == 0 {
		w.table.state.mu.Lock()
		w.table.state.nextRow++
		key = "\x00row:" + strconv.FormatUint(w.table.state.nextRow, 10)
		w.table.state.mu.Unlock()
	}
	if err := w.checkUnique(ctx, key, "", row); err != nil {
		return err
	}
	if err := w.table.observeAutoIncrement(ctx, row); err != nil {
		return err
	}
	if err := w.table.addIndexRow(ctx, key, row); err != nil {
		return err
	}
	if err := w.sess.stageMembership(ctx, txn, w.table.storageName, key, true); err != nil {
		return err
	}
	for i, col := range w.table.def.Columns {
		if err := w.sess.stageWrite(ctx, txn, StagedWrite{Tab: w.table.storageName, Pkey: key, Col: col.Name, Value: row[i]}); err != nil {
			return err
		}
	}
	return nil
}

// The engine supplies values rather than physical identity for keyless edits.
// shortcut: identical selected rows have no engine row ID; use an unused matching selection.
func (w *rowWriter) oldKey(ctx *sql.Context, row sql.Row) (string, error) {
	if len(row) != len(w.table.def.Columns) {
		return "", fmt.Errorf("row has %d columns, expected %d", len(row), len(w.table.def.Columns))
	}
	if len(w.table.primaryOrdinals()) > 0 {
		return w.table.pkeyOf(row), nil
	}
	if key, ok := w.selectedKeylessRow(row); ok {
		return key, nil
	}
	for _, key := range w.table.knownKeys(w.sess) {
		if w.used[key] || w.table.latestCell(w.sess, key, rowExistenceColumn) != true {
			continue
		}
		match := true
		for i, col := range w.table.def.Columns {
			if !reflect.DeepEqual(row[i], w.table.latestCell(w.sess, key, col.Name)) {
				match = false
				break
			}
		}
		if match {
			if w.used == nil {
				w.used = make(map[string]bool)
			}
			w.used[key] = true
			return key, nil
		}
	}
	return "", fmt.Errorf("keyless row to edit was not found")
}

// shortcut: value changes approximate SET targets; exact no-op write history needs an engine hook.
func (w *rowWriter) Update(ctx *sql.Context, oldRow, newRow sql.Row) error {
	txn, err := w.transaction()
	if err != nil {
		return err
	}
	oldKey, err := w.oldKey(ctx, oldRow)
	if err != nil {
		return err
	}
	if len(newRow) != len(w.table.def.Columns) {
		return fmt.Errorf("row has %d columns, expected %d", len(newRow), len(w.table.def.Columns))
	}
	key := oldKey
	if len(w.table.primaryOrdinals()) > 0 {
		key = w.table.pkeyOf(newRow)
	}
	if err := w.checkUnique(ctx, key, oldKey, newRow); err != nil {
		return err
	}
	if err := w.table.addIndexRow(ctx, key, newRow); err != nil {
		return err
	}
	if key != oldKey {
		if err := w.sess.stageMembership(ctx, txn, w.table.storageName, oldKey, false); err != nil {
			return err
		}
		if err := w.sess.stageMembership(ctx, txn, w.table.storageName, key, true); err != nil {
			return err
		}
	}
	for i, col := range w.table.def.Columns {
		if key != oldKey || !reflect.DeepEqual(oldRow[i], newRow[i]) {
			if err := w.sess.stageWrite(ctx, txn, StagedWrite{Tab: w.table.storageName, Pkey: key, Col: col.Name, Value: newRow[i]}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *rowWriter) Delete(ctx *sql.Context, row sql.Row) error {
	txn, err := w.transaction()
	if err != nil {
		return err
	}
	key, err := w.oldKey(ctx, row)
	if err != nil {
		return err
	}
	return w.sess.stageMembership(ctx, txn, w.table.storageName, key, false)
}

func (w *rowWriter) StatementBegin(_ *sql.Context) {
	if w.sess != nil {
		w.sess.takeReadError()
		w.statementTxn = w.sess.beginTxn()
		w.checkpoint = w.statementTxn.checkpoint()
		w.beginKeylessRows()
	}
	w.used = nil
}

func (w *rowWriter) DiscardChanges(_ *sql.Context, _ error) error {
	w.endKeylessRows()
	if w.sess != nil {
		w.sess.takeReadError()
	}
	if w.sess != nil && w.statementTxn != nil && w.sess.liveTxn() == w.statementTxn {
		w.statementTxn.restore(w.checkpoint)
	}
	return nil
}
func (w *rowWriter) StatementComplete(_ *sql.Context) error {
	w.endKeylessRows()
	if w.sess != nil {
		w.sess.takeReadError()
	}
	return nil
}
func (w *rowWriter) Close(ctx *sql.Context) error {
	w.endKeylessRows()
	if w.sess != nil {
		w.sess.takeReadError()
	}
	if w.sess != nil && !w.sess.inExplicitTxn() {
		return w.sess.CommitTransaction(ctx, nil)
	}
	return nil
}

func sessionOf(ctx *sql.Context) *Session {
	if ctx != nil && ctx.Session != nil {
		sess, _ := ctx.Session.(*Session)
		return sess
	}
	return nil
}
