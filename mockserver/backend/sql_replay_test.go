package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
	"github.com/shopspring/decimal"
)

func sqlReplayFixture() SQLWorkload {
	return SQLWorkload{
		SchemaVersion: 1, Isolation: "read-committed",
		Setup: []string{"CREATE DATABASE replaydb", "CREATE TABLE replaydb.items (id BIGINT PRIMARY KEY, v BIGINT)", "INSERT INTO replaydb.items VALUES (1,0)"},
		Transactions: []SQLTransaction{
			{ID: "writer", SessionID: 1, Statements: []SQLStatement{{ID: "update", SQL: "UPDATE replaydb.items SET v=1 WHERE id=1"}}, Finish: "commit"},
			{ID: "reader", SessionID: 2, Statements: []SQLStatement{{ID: "read", SQL: "SELECT v FROM replaydb.items WHERE id=1"}}, Finish: "commit"},
		},
		Assertion: SQLAssertion{ID: "value", SQL: "SELECT v FROM replaydb.items WHERE id=1", BaselineRows: []TypedRow{{{Kind: "int", Value: "0"}}}, WantRows: []TypedRow{{{Kind: "int", Value: "1"}}}},
	}
}

func sqlReplayFailure(t *testing.T) ReplayArtifact {
	t.Helper()
	workload := sqlReplayFixture()
	workload.Assertion.WantRows = workload.Assertion.BaselineRows
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure == nil || !reflect.DeepEqual(result.ObservedRows, []TypedRow{{{Kind: "int", Value: "1"}}}) {
		t.Fatalf("missing real SQL failure: %+v", result)
	}
	artifact, err := CaptureSQLFailure(workload, result)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func cloneSQLArtifact(t *testing.T, artifact ReplayArtifact) ReplayArtifact {
	t.Helper()
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	copy, err := DecodeReplayArtifact(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return copy
}

func TestSQLReplaySerialAllLevels(t *testing.T) {
	for _, level := range []string{"read-committed", "repeatable-read", "atomic-read", "causal", "prefix", "snapshot-isolation", "serializable"} {
		t.Run(level, func(t *testing.T) {
			workload := sqlReplayFixture()
			workload.Isolation = level
			result, err := RunSQLWorkload(workload, []int{})
			if err != nil {
				t.Fatal(err)
			}
			if result.Failure != nil || !reflect.DeepEqual(result.ObservedRows, workload.Assertion.WantRows) || len(result.Choices) == 0 {
				t.Fatalf("wrong SQL result: %+v", result)
			}
			for _, choice := range result.Choices {
				if choice.Choice != len(choice.Candidates)-1 {
					t.Fatal("empty nonnil prefix did not choose newest")
				}
			}
			iso, _ := sqlIsolation(level)
			var history [][]historyTxn
			if err := json.Unmarshal(result.History, &history); err != nil {
				t.Fatal(err)
			}
			if !checkLevel(history, levelName(iso)) {
				t.Fatal("exported committed history violates its oracle")
			}
			if _, err := CaptureSQLFailure(workload, result); err == nil {
				t.Fatal("passing result captured as failure")
			}
		})
	}
}

func TestSQLReplayCaptureFreshGenerationAndDiagnostics(t *testing.T) {
	artifact := sqlReplayFailure(t)
	if artifact.ValidationPolicy != "latest-admitted-rollback" {
		t.Fatal("validation policy missing")
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeReplayArtifact(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	decoded.Revision = "older-build"
	for index := range decoded.Choices {
		decoded.Choices[index].RuntimeVersion += 1000000
	}
	result, err := ReplaySQLArtifact(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure == nil || *result.Failure != artifact.Failure || !bytes.Equal(result.History, artifact.History) || !reflect.DeepEqual(result.Variables, artifact.Variables) {
		t.Fatal("fresh namespaces changed semantic replay")
	}
	for key := range result.Variables {
		if strings.Contains(key, "\\u0000sql:") {
			t.Fatal("physical table namespace leaked")
		}
	}
	var history [][]historyTxn
	if err := json.Unmarshal(result.History, &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || len(history[0]) != 1 || len(history[1]) != 1 {
		t.Fatalf("setup or final assertion leaked into history: %s", result.History)
	}
}

func TestSQLReplayDroppedTableGenerations(t *testing.T) {
	workload := sqlReplayFixture()
	workload.Setup = append(workload.Setup[:1], "CREATE TABLE replaydb.items (id BIGINT PRIMARY KEY, v BIGINT)", "INSERT INTO replaydb.items VALUES(1,99)", "DROP TABLE replaydb.items", "CREATE TABLE replaydb.items (id BIGINT PRIMARY KEY, v BIGINT)", "INSERT INTO replaydb.items VALUES(1,0)")
	workload.Assertion.WantRows = workload.Assertion.BaselineRows
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := CaptureSQLFailure(workload, result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaySQLArtifact(artifact); err != nil {
		t.Fatal(err)
	}
	for key := range artifact.Variables {
		var components []string
		if err := json.Unmarshal([]byte(key), &components); err != nil || len(components) != 5 || components[2] != "2" {
			t.Fatalf("table recreation generation not retained: %q", key)
		}
	}
}

func TestSQLReplayPrefixAndStrictDivergence(t *testing.T) {
	artifact := sqlReplayFailure(t)
	prefix := make([]int, len(artifact.Choices))
	for index, choice := range artifact.Choices {
		prefix[index] = choice.Choice
	}
	copyPrefix := append([]int(nil), prefix...)
	if _, err := RunSQLWorkload(artifact.Workload, prefix); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(prefix, copyPrefix) {
		t.Fatal("prefix mutated")
	}
	if _, err := RunSQLWorkload(artifact.Workload, prefix[:1]); err != nil {
		t.Fatalf("partial prefix must use newest tail: %v", err)
	}
	for _, invalid := range [][]int{{-1}, {100000}, append(copyPrefix, 0)} {
		if _, err := RunSQLWorkload(artifact.Workload, invalid); err == nil || !strings.Contains(err.Error(), "replay divergence") {
			t.Fatalf("invalid prefix accepted: %v %v", invalid, err)
		}
	}
	cases := map[string]func(*ReplayArtifact){
		"missing":  func(a *ReplayArtifact) { a.Choices = a.Choices[:len(a.Choices)-1] },
		"extra":    func(a *ReplayArtifact) { a.Choices = append(a.Choices, a.Choices[len(a.Choices)-1]) },
		"readsite": func(a *ReplayArtifact) { a.Choices[0].ReadOrdinal++ },
		"source": func(a *ReplayArtifact) {
			for index := range a.Choices {
				for source := range a.Choices[index].Candidates {
					if !a.Choices[index].Candidates[source].Initial {
						a.Choices[index].Candidates[source].StatementID = "different-write"
						a.Choices[index].Selected = a.Choices[index].Candidates[a.Choices[index].Choice]
						return
					}
				}
			}
			t.Fatal("fixture has no committed source")
		},
		"candidates": func(a *ReplayArtifact) {
			for index := range a.Choices {
				if len(a.Choices[index].Candidates) > 1 {
					a.Choices[index].Candidates[0], a.Choices[index].Candidates[1] = a.Choices[index].Candidates[1], a.Choices[index].Candidates[0]
					a.Choices[index].Selected = a.Choices[index].Candidates[a.Choices[index].Choice]
					return
				}
			}
			t.Fatal("fixture has no ambiguous read")
		},
		"rows":    func(a *ReplayArtifact) { a.ObservedRows[0][0].Value = "2" },
		"history": func(a *ReplayArtifact) { a.History = json.RawMessage("[]") },
		"variables": func(a *ReplayArtifact) {
			for key := range a.Variables {
				delete(a.Variables, key)
				break
			}
		},
		"fingerprint": func(a *ReplayArtifact) { a.Failure.AssertionID = "another" },
		"schema":      func(a *ReplayArtifact) { a.SchemaVersion++ },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			changed := cloneSQLArtifact(t, artifact)
			change(&changed)
			if _, err := ReplaySQLArtifact(changed); err == nil {
				t.Fatal("corrupt semantic evidence accepted")
			}
		})
	}
}

func TestSQLReplayTypedValuesAndSum(t *testing.T) {
	rows := []TypedRow{{{Kind: "null"}, {Kind: "int", Value: "-3"}, {Kind: "uint", Value: "18446744073709551615"}, {Kind: "decimal", Value: "12345678901234567890.123456789"}, {Kind: "float", Value: "1.25"}, {Kind: "text", Value: "text"}, {Kind: "bytes", Value: "AP8="}, {Kind: "bool", Value: "true"}}}
	workload := SQLWorkload{SchemaVersion: 1, Isolation: "serializable", Setup: []string{"CREATE DATABASE typeddb", "CREATE TABLE typeddb.values_table (id BIGINT PRIMARY KEY, n BIGINT, u BIGINT UNSIGNED, d DECIMAL(30,9), f DOUBLE, t VARCHAR(20), b VARBINARY(20), flag BOOLEAN, nullable BIGINT)", "INSERT INTO typeddb.values_table VALUES (1,-3,18446744073709551615,12345678901234567890.123456789,1.25,'text',X'00FF',TRUE,NULL)"}, Transactions: []SQLTransaction{}, Assertion: SQLAssertion{ID: "typed", SQL: "SELECT nullable,n,u,d,f,t,b,flag FROM typeddb.values_table ORDER BY id", BaselineRows: rows, WantRows: rows}}
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failure != nil || !reflect.DeepEqual(result.ObservedRows, rows) {
		t.Fatalf("typed result lost SQL values: %v", result.ObservedRows)
	}
	workload.Assertion = SQLAssertion{ID: "sum", SQL: "SELECT SUM(d) FROM typeddb.values_table", BaselineRows: []TypedRow{{{Kind: "decimal", Value: "12345678901234567890.123456789"}}}, WantRows: []TypedRow{{{Kind: "decimal", Value: "12345678901234567890.123456789"}}}}
	if result, err := RunSQLWorkload(workload, []int{}); err != nil || result.Failure != nil {
		t.Fatalf("exact SUM decimal: %v %v", result.ObservedRows, err)
	}
	ctx := sql.NewContext(context.Background())
	for _, test := range []struct {
		typ   sql.Type
		value interface{}
		want  TypedValue
	}{
		{types.Int8, int8(-1), TypedValue{Kind: "int", Value: "-1"}},
		{types.Uint16, uint16(2), TypedValue{Kind: "uint", Value: "2"}},
		{types.Float32, float32(1.5), TypedValue{Kind: "float", Value: "1.5"}},
		{types.MustCreateDecimalType(30, 9), decimal.RequireFromString("12345678901234567890.123456789"), rows[0][3]},
		{types.LongText, []byte("text"), TypedValue{Kind: "text", Value: "text"}},
		{types.LongBlob, "text", TypedValue{Kind: "bytes", Value: "dGV4dA=="}},
	} {
		got, err := sqlTypedValue(ctx, test.typ, test.value)
		if err != nil || got != test.want {
			t.Fatalf("typed %T: %+v %v", test.value, got, err)
		}
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := sqlTypedValue(ctx, types.Float64, value); err == nil {
			t.Fatal("nonfinite float accepted")
		}
	}
	if _, err := sqlTypedValue(ctx, types.JSON, `{"a":1}`); err == nil {
		t.Fatal("unsupported JSON accepted")
	}
}

func TestSQLReplayRejectsLossyTextEncoding(t *testing.T) {
	ctx := sql.NewContext(context.Background())
	invalid := string([]byte{0xff})
	for _, value := range []interface{}{invalid, []byte{0xff}} {
		if _, err := sqlTypedValue(ctx, types.LongText, value); err == nil {
			t.Fatal("SQL text would be replaced during JSON encoding")
		}
	}
	if err := validateSQLRows([]TypedRow{{{Kind: "text", Value: invalid}}}); err == nil {
		t.Fatal("invalid text accepted in an assertion")
	}
	input, err := json.Marshal(sqlReplayFixture())
	if err != nil {
		t.Fatal(err)
	}
	input = bytes.Replace(input, []byte("writer"), []byte{0xff}, 1)
	if _, err := DecodeSQLWorkload(bytes.NewReader(input)); err == nil {
		t.Fatal("invalid JSON text was silently replaced")
	}
	controller := &sqlReplayController{
		tables: map[string][]string{"table": {"db", "items", "1"}},
		keys:   make(map[string]string),
	}
	for _, key := range []string{string([]byte{0xff}), string([]byte{0xfe})} {
		if _, err := controller.cell("table", key, "value"); err == nil {
			t.Fatal("distinct invalid key bytes could alias after JSON replacement")
		}
	}
}

func TestSQLReplayValidationErrors(t *testing.T) {
	for name, change := range map[string]func(*SQLWorkload){
		"schema":        func(w *SQLWorkload) { w.SchemaVersion = 2 },
		"isolation":     func(w *SQLWorkload) { w.Isolation = "invalid" },
		"policy":        func(w *SQLWorkload) { w.ReadPolicy = "replay" },
		"probability":   func(w *SQLWorkload) { n := math.NaN(); w.DeviationProbability = &n },
		"missing setup": func(w *SQLWorkload) { w.Setup = nil },
		"missing rows":  func(w *SQLWorkload) { w.Assertion.BaselineRows = nil },
		"empty ID":      func(w *SQLWorkload) { w.Transactions[0].ID = "" },
		"duplicate ID":  func(w *SQLWorkload) { w.Transactions[1].Statements[0].ID = w.Transactions[0].Statements[0].ID },
		"session":       func(w *SQLWorkload) { w.Transactions[0].SessionID = 0 },
		"finish":        func(w *SQLWorkload) { w.Transactions[0].Finish = "abort" },
		"assertion":     func(w *SQLWorkload) { w.Assertion.SQL = "DELETE FROM replaydb.items" },
		"setup database does not leak": func(w *SQLWorkload) {
			w.Setup = append(w.Setup, "USE replaydb")
			w.Transactions[0].Statements[0].SQL = "UPDATE items SET v=1"
		},
		"baseline":         func(w *SQLWorkload) { w.Assertion.BaselineRows = []TypedRow{{{Kind: "int", Value: "99"}}} },
		"SQL error":        func(w *SQLWorkload) { w.Transactions[0].Statements[0].SQL = "SELECT missing FROM replaydb.items" },
		"setup SQL error":  func(w *SQLWorkload) { w.Setup = append(w.Setup, "INSERT INTO replaydb.missing VALUES(1)") },
		"unsupported type": func(w *SQLWorkload) { w.Assertion.SQL = "SELECT CAST('2020-01-01' AS DATE)" },
	} {
		t.Run(name, func(t *testing.T) {
			workload := sqlReplayFixture()
			change(&workload)
			result, err := RunSQLWorkload(workload, []int{})
			if err == nil || result.Failure != nil {
				t.Fatalf("invalid input became assertion result: %v %+v", err, result.Failure)
			}
		})
	}
	for _, query := range []string{"", "SELECT 1; SELECT 2", "BEGIN", "COMMIT", "ROLLBACK", "SET autocommit=0", "CALL foo()", "CREATE TABLE replaydb.nope(id INT)", "SELECT RAND()", "SELECT (SELECT UUID())", "SELECT CURRENT_TIMESTAMP", "SELECT 1 FROM replaydb.items WHERE v=RAND()", "SELECT SUM(v) OVER (ORDER BY RAND()) FROM replaydb.items", "SELECT SUM(v) OVER w FROM replaydb.items WINDOW w AS (ORDER BY RAND())", "SELECT @v", "SELECT ?", "SELECT 1 INTO OUTFILE '/tmp/replay-test'"} {
		t.Run(query, func(t *testing.T) {
			workload := sqlReplayFixture()
			workload.Transactions[0].Statements[0].SQL = query
			if _, err := RunSQLWorkload(workload, []int{}); err == nil {
				t.Fatal("prohibited SQL accepted")
			}
		})
	}
	workload := sqlReplayFixture()
	workload.Setup = append(workload.Setup, "CREATE TABLE replaydb.clock (t TIMESTAMP DEFAULT CURRENT_TIMESTAMP)")
	if _, err := RunSQLWorkload(workload, []int{}); err == nil {
		t.Fatal("nondeterministic setup default accepted")
	}
	workload = sqlReplayFixture()
	workload.Transactions[0].Statements = []SQLStatement{{ID: "use", SQL: "USE replaydb"}, {ID: "update", SQL: "UPDATE items SET v=1 WHERE id=1"}}
	workload.Transactions[1].SessionID = 1
	workload.Transactions[1].Statements[0].SQL = "SELECT v FROM items WHERE id=1"
	if result, err := RunSQLWorkload(workload, []int{}); err != nil || result.Failure != nil {
		t.Fatalf("persistent session USE lost: %v", err)
	}
}

func TestSQLReplayStrictJSONAndDefaults(t *testing.T) {
	workload := sqlReplayFixture()
	seed := uint64(0)
	workload.Seed = &seed
	encoded, err := json.Marshal(workload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSQLWorkload(bytes.NewReader(encoded))
	if err != nil || decoded.Seed == nil || *decoded.Seed != 0 || decoded.ReadPolicy != "uniform" || decoded.DeviationProbability == nil {
		t.Fatalf("defaults erased explicit seed zero: %+v %v", decoded, err)
	}
	for _, invalid := range []string{string(encoded) + " {}", strings.Replace(string(encoded), `"schema_version":1`, `"schema_version":2`, 1), strings.Replace(string(encoded), `"isolation":`, `"extra":1,"isolation":`, 1), strings.Replace(string(encoded), `"value":"0"`, `"value":"0","extra":1`, 1), strings.Replace(string(encoded), `"value":"0"`, `"missing":"0"`, 1), strings.Replace(string(encoded), `"seed":0`, `"seed":null`, 1), "{", "null"} {
		if _, err := DecodeSQLWorkload(strings.NewReader(invalid)); err == nil {
			t.Fatalf("corrupt JSON accepted: %s", invalid)
		}
	}
	artifact := sqlReplayFailure(t)
	encoded, err = json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSQLWorkload(bytes.NewReader(encoded)); err == nil {
		t.Fatal("artifact decoded as workload")
	}
	workloadJSON, _ := json.Marshal(workload)
	if _, err := DecodeReplayArtifact(bytes.NewReader(workloadJSON)); err == nil {
		t.Fatal("workload decoded as artifact")
	}
	if _, err := DecodeReplayArtifact(strings.NewReader(strings.Replace(string(encoded), `"read_ordinal":1`, `"read_ordinal":1,"unknown":1`, 1))); err == nil {
		t.Fatal("unknown nested trace field accepted")
	}
}

func TestSQLReplayRollbackAndExplorerConsumer(t *testing.T) {
	workload := sqlReplayFixture()
	workload.Transactions[0].Finish = "rollback"
	workload.Assertion.WantRows = workload.Assertion.BaselineRows
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil || result.Failure != nil {
		t.Fatalf("rollback leaked writes: %v %v", result.ObservedRows, err)
	}
	execution, err := SQLWorkloadRunner(workload)(nil)
	if err != nil || execution.Failed || len(execution.Decisions) == 0 || execution.Outcome == "" {
		t.Fatalf("explorer adapter: %+v %v", execution, err)
	}
	for _, choice := range execution.Decisions {
		if choice.Choice != choice.Alternatives-1 {
			t.Fatal("nil explorer prefix sampled instead of choosing newest")
		}
	}
	workload = sqlReplayFixture()
	workload.Assertion.WantRows = workload.Assertion.BaselineRows
	if execution, err := SQLWorkloadRunner(workload)(nil); err != nil || !execution.Failed {
		t.Fatalf("real SQL mismatch lost by adapter: %+v %v", execution, err)
	}
}

// Exercise iterator errors at the consumer boundary, including Close on failure.
type sqlReplayErrorIter struct {
	closed   bool
	nextErr  error
	closeErr error
}

func (iter *sqlReplayErrorIter) Next(*sql.Context) (sql.Row, error) { return nil, iter.nextErr }
func (iter *sqlReplayErrorIter) Close(*sql.Context) error           { iter.closed = true; return iter.closeErr }

func TestSQLReplayIteratorErrorContract(t *testing.T) {
	// The production engine's RowIterToRows contract is not used: sqlReplayQuery
	// consumes iterators itself and must preserve the first error plus Close errors.
	iter := &sqlReplayErrorIter{nextErr: io.ErrUnexpectedEOF, closeErr: context.Canceled}
	ctx := sql.NewContext(context.Background())
	_, err := consumeSQLReplayRows(ctx, nil, iter, false)
	if !iter.closed || !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, context.Canceled) {
		t.Fatalf("iterator cleanup lost error: closed=%t err=%v", iter.closed, err)
	}
}

func TestSQLReplayCommitRejectionIsExecutionError(t *testing.T) {
	workload := SQLWorkload{
		SchemaVersion: 1, Isolation: "serializable",
		Setup: []string{
			"CREATE DATABASE rejectdb",
			"CREATE TABLE rejectdb.x (id BIGINT PRIMARY KEY, v BIGINT)",
			"CREATE TABLE rejectdb.y (id BIGINT PRIMARY KEY, v BIGINT)",
			"INSERT INTO rejectdb.x VALUES(1,0)",
			"INSERT INTO rejectdb.y VALUES(1,0)",
		},
		Transactions: []SQLTransaction{
			{ID: "first", SessionID: 1, Finish: "commit", Statements: []SQLStatement{{ID: "read-y", SQL: "SELECT v FROM rejectdb.y WHERE id=1"}, {ID: "write-x", SQL: "UPDATE rejectdb.x SET v=1 WHERE id=1"}}},
			{ID: "second", SessionID: 2, Finish: "commit", Statements: []SQLStatement{{ID: "read-x", SQL: "SELECT v FROM rejectdb.x WHERE id=1"}, {ID: "write-y", SQL: "UPDATE rejectdb.y SET v=1 WHERE id=1"}}},
		},
		Assertion: SQLAssertion{ID: "x", SQL: "SELECT v FROM rejectdb.x WHERE id=1", BaselineRows: []TypedRow{{{Kind: "int", Value: "0"}}}, WantRows: []TypedRow{{{Kind: "int", Value: "1"}}}},
	}
	// Every read chooses initial. The second write closes the serialization
	// cycle only at commit; surplus indices must not mask that earlier error.
	result, err := RunSQLWorkload(workload, make([]int, 64))
	if err == nil || !strings.Contains(err.Error(), `execution commit "second"`) || result.Failure != nil {
		t.Fatalf("rejected SQL commit was not an execution error: %v %+v", err, result.Failure)
	}
	if _, captureErr := CaptureSQLFailure(workload, result); captureErr == nil {
		t.Fatal("commit rejection captured as assertion failure")
	}
}

func TestSQLReplaySeedZeroAndSparsePolicy(t *testing.T) {
	workload := sqlReplayFixture()
	seed, probability := uint64(0), 0.0
	workload.Seed, workload.DeviationProbability = &seed, &probability
	first, err := RunSQLWorkload(workload, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RunSQLWorkload(workload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *first.Workload.Seed != 0 || !reflect.DeepEqual(first.Choices, second.Choices) {
		t.Fatal("explicit seed zero was replaced or sampling is not reproducible")
	}
	workload.ReadPolicy = "sparse"
	result, err := RunSQLWorkload(workload, nil)
	if err != nil {
		t.Fatal(err)
	}
	if *result.Workload.DeviationProbability != 0 {
		t.Fatal("explicit zero deviation replaced by default")
	}
	for _, choice := range result.Choices {
		if choice.Choice != len(choice.Candidates)-1 {
			t.Fatal("sparse zero-deviation policy did not select newest")
		}
	}
}

func TestSQLReplayNoRowsDifferFromNull(t *testing.T) {
	workload := SQLWorkload{SchemaVersion: 1, Isolation: "read-committed", Setup: []string{}, Transactions: []SQLTransaction{}, Assertion: SQLAssertion{ID: "null", SQL: "SELECT NULL", BaselineRows: []TypedRow{{{Kind: "null"}}}, WantRows: []TypedRow{}}}
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil || result.Failure == nil || len(result.ObservedRows) != 1 || result.ObservedRows[0][0].Kind != "null" {
		t.Fatalf("NULL collapsed into no rows: %v %v", result.ObservedRows, err)
	}
	artifact, err := CaptureSQLFailure(workload, result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaySQLArtifact(artifact); err != nil {
		t.Fatal(err)
	}
}
