package backend

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestSQLReplayBoundaryNestedRequiredJSON(t *testing.T) {
	encoded, err := json.Marshal(sqlReplayFixture())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(map[string]interface{})
		want string
	}{
		{"top-level array required", func(w map[string]interface{}) { delete(w, "transactions") }, "required field transactions"},
		{"transaction statements required", func(w map[string]interface{}) {
			delete(w["transactions"].([]interface{})[0].(map[string]interface{}), "statements")
		}, "required field statements"},
		{"statement SQL required", func(w map[string]interface{}) {
			delete(w["transactions"].([]interface{})[0].(map[string]interface{})["statements"].([]interface{})[0].(map[string]interface{}), "sql")
		}, "required field sql"},
		{"assertion SQL required", func(w map[string]interface{}) { delete(w["assertion"].(map[string]interface{}), "sql") }, "required field sql"},
		{"typed value kind required", func(w map[string]interface{}) {
			delete(w["assertion"].(map[string]interface{})["baseline_rows"].([]interface{})[0].([]interface{})[0].(map[string]interface{}), "kind")
		}, "required field kind"},
		{"null nested transaction", func(w map[string]interface{}) { w["transactions"].([]interface{})[0] = nil }, "null backend.SQLTransaction"},
		{"null nested row", func(w map[string]interface{}) {
			w["assertion"].(map[string]interface{})["want_rows"].([]interface{})[0] = nil
		}, "null backend.TypedRow"},
		{"null typed value", func(w map[string]interface{}) {
			w["assertion"].(map[string]interface{})["baseline_rows"].([]interface{})[0].([]interface{})[0] = nil
		}, "null backend.TypedValue"},
		{"case alias is not a second field", func(w map[string]interface{}) { w["SCHEMA_VERSION"] = float64(1) }, "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var document map[string]interface{}
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			test.edit(document)
			input, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSQLWorkload(bytes.NewReader(input)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wanted %q, got %v", test.want, err)
			}
		})
	}
	for _, suffix := range []string{" {", " garbage", " [1,"} {
		if _, err := DecodeSQLWorkload(strings.NewReader(string(encoded) + suffix)); err == nil || !strings.Contains(err.Error(), "JSON trailer") {
			t.Fatalf("malformed trailer %q: %v", suffix, err)
		}
	}
	for _, input := range []string{string(encoded[:len(encoded)-1]), `{"schema_version":`, `[]`, `{"setup":false}`} {
		if _, err := DecodeSQLWorkload(strings.NewReader(input)); err == nil {
			t.Fatalf("malformed document accepted: %s", input)
		}
	}
}

func TestSQLReplayBoundaryProgrammaticInput(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*SQLWorkload)
		want string
	}{
		{"missing assertion identity", func(w *SQLWorkload) { w.Assertion.ID = " \t" }, "ID"},
		{"invalid identity encoding", func(w *SQLWorkload) { w.Transactions[0].Statements[0].ID = string([]byte{0xff}) }, "ID"},
		{"cross-kind identity collision", func(w *SQLWorkload) { w.Assertion.ID = w.Transactions[0].ID }, "ID"},
		{"missing statements", func(w *SQLWorkload) { w.Transactions[0].Statements = nil }, "statements array"},
		{"missing expected rows", func(w *SQLWorkload) { w.Assertion.WantRows = nil }, "arrays are required"},
		{"infinite probability", func(w *SQLWorkload) { p := math.Inf(1); w.DeviationProbability = &p }, "deviation_probability"},
		{"negative probability", func(w *SQLWorkload) { p := -0.1; w.DeviationProbability = &p }, "deviation_probability"},
		{"excess probability", func(w *SQLWorkload) { p := 1.1; w.DeviationProbability = &p }, "deviation_probability"},
		{"null programmatic row", func(w *SQLWorkload) { w.Assertion.WantRows = []TypedRow{nil} }, "typed rows must be arrays"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workload := sqlReplayFixture()
			test.edit(&workload)
			result, err := RunSQLWorkload(workload, []int{})
			if err == nil || !strings.Contains(err.Error(), test.want) || result.Failure != nil {
				t.Fatalf("invalid input became a failure artifact: %+v %v", result, err)
			}
			if _, err := CaptureSQLFailure(workload, result); err == nil {
				t.Fatal("rejected input was capturable")
			}
		})
	}
	for _, value := range []TypedValue{
		{Kind: "null", Value: "NULL"}, {Kind: "bool", Value: "1"},
		{Kind: "int", Value: "9223372036854775808"}, {Kind: "uint", Value: "-1"},
		{Kind: "decimal", Value: "1.00"}, {Kind: "float", Value: "NaN"},
		{Kind: "text", Value: string([]byte{0xff})}, {Kind: "bytes", Value: "AP8"},
		{Kind: "timestamp", Value: "2020-01-01"},
	} {
		workload := sqlReplayFixture()
		workload.Assertion.WantRows = []TypedRow{{value}}
		if result, err := RunSQLWorkload(workload, []int{}); err == nil || !strings.Contains(err.Error(), "canonical typed value") || result.Failure != nil {
			t.Fatalf("noncanonical expectation %+v: %v", value, err)
		}
	}
}

func TestSQLReplayBoundaryExpressionScopes(t *testing.T) {
	for _, test := range []struct {
		phase string
		query string
		want  string
	}{
		{"measured", "SELECT FROM", "invalid statement"},
		{"measured", "INSERT INTO items VALUES (2,2)", "requires qualification"},
		{"measured", "REPLACE INTO replaydb.items VALUES (1,2)", "not permitted"},
		{"measured", "WITH c AS (SELECT RAND() AS v) SELECT v FROM c", "nondeterministic"},
		{"measured", "SELECT 1 UNION SELECT 2 ORDER BY RAND()", "nondeterministic"},
		{"measured", "SELECT LAST_INSERT_ID(9)", "session-dependent"},
		{"measured", "SELECT NEXT 1 VALUES FROM replaydb.items", "stateful"},
		{"setup", "SAVEPOINT before_setup", "only in measured"},
		{"setup", "CREATE VIEW replaydb.view_items AS SELECT v FROM replaydb.items", "not stored SQL"},
		{"setup", "CREATE TABLE replaydb.defaults (v DOUBLE DEFAULT (RAND()))", "nondeterministic"},
		{"assertion", "INSERT INTO replaydb.items VALUES (2,2)", "not permitted"},
		{"assertion", "USE replaydb", "must be SELECT"},
		{"assertion", "SELECT 1 UNION SELECT CONNECTION_ID()", "session-dependent"},
	} {
		t.Run(test.phase+"/"+test.query, func(t *testing.T) {
			workload := sqlReplayFixture()
			switch test.phase {
			case "setup":
				workload.Setup = append(workload.Setup, test.query)
			case "assertion":
				workload.Assertion.SQL = test.query
			default:
				workload.Transactions[0].Statements[0].SQL = test.query
			}
			result, err := RunSQLWorkload(workload, []int{})
			if err == nil || !strings.Contains(err.Error(), test.want) || result.Failure != nil {
				t.Fatalf("wanted %q without assertion failure, got %v %+v", test.want, err, result.Failure)
			}
		})
	}
	workload := sqlReplayFixture()
	workload.Setup = append(workload.Setup, "CREATE TABLE replaydb.defaults (id INT PRIMARY KEY, v INT DEFAULT (40+2))")
	workload.Transactions = []SQLTransaction{{ID: "insert-default", SessionID: 3, Statements: []SQLStatement{{ID: "insert-default-row", SQL: "INSERT INTO replaydb.defaults (id) VALUES (1)"}}, Finish: "commit"}}
	workload.Assertion = SQLAssertion{ID: "default-result", SQL: "WITH c AS (SELECT v FROM replaydb.defaults) SELECT v FROM c UNION ALL SELECT 7 ORDER BY v", BaselineRows: []TypedRow{{{Kind: "int", Value: "7"}}}, WantRows: []TypedRow{{{Kind: "int", Value: "7"}}, {{Kind: "int", Value: "42"}}}}
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil || result.Failure != nil || !reflect.DeepEqual(result.ObservedRows, workload.Assertion.WantRows) {
		t.Fatalf("deterministic defaults, CTEs and set operations must remain usable: %v %+v", err, result)
	}
}

func TestSQLReplayBoundarySQLIntegerWidths(t *testing.T) {
	rows := []TypedRow{{
		{Kind: "int", Value: "-128"}, {Kind: "int", Value: "-32768"}, {Kind: "int", Value: "-2147483648"}, {Kind: "int", Value: "-9223372036854775808"},
		{Kind: "uint", Value: "255"}, {Kind: "uint", Value: "65535"}, {Kind: "uint", Value: "4294967295"}, {Kind: "uint", Value: "18446744073709551615"},
		{Kind: "bool", Value: "false"}, {Kind: "float", Value: "1.5"}, {Kind: "decimal", Value: "-0.01"}, {Kind: "text", Value: ""}, {Kind: "bytes", Value: "AP8="},
	}}
	workload := SQLWorkload{
		SchemaVersion: 1, Isolation: "serializable",
		Setup: []string{
			"CREATE DATABASE widths",
			"CREATE TABLE widths.numbers (id INT PRIMARY KEY, i8 TINYINT, i16 SMALLINT, i32 INT, i64 BIGINT, u8 TINYINT UNSIGNED, u16 SMALLINT UNSIGNED, u32 INT UNSIGNED, u64 BIGINT UNSIGNED, flag BOOLEAN, f FLOAT, d DECIMAL(4,2), t TEXT, b BLOB)",
			"INSERT INTO widths.numbers VALUES (1,-128,-32768,-2147483648,-9223372036854775808,255,65535,4294967295,18446744073709551615,FALSE,1.5,-0.01,'',X'00FF')",
		},
		Transactions: []SQLTransaction{},
		Assertion:    SQLAssertion{ID: "widths", SQL: "SELECT i8,i16,i32,i64,u8,u16,u32,u64,flag,f,d,t,b FROM widths.numbers", BaselineRows: rows, WantRows: rows},
	}
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil || result.Failure != nil || !reflect.DeepEqual(result.ObservedRows, rows) {
		t.Fatalf("SQL numeric widths or text/binary values lost: %v %v", result.ObservedRows, err)
	}
}

func TestSQLReplayBoundaryEvidenceTampering(t *testing.T) {
	original := sqlReplayFailure(t)
	producerRead, producerCandidate := -1, -1
	for i, choice := range original.Choices {
		for j, source := range choice.Candidates {
			if !source.Initial {
				producerRead, producerCandidate = i, j
				break
			}
		}
		if producerRead >= 0 {
			break
		}
	}
	if producerRead < 0 {
		t.Fatal("fixture must exercise a committed producer")
	}
	for _, test := range []struct {
		name string
		edit func(*ReplayArtifact)
		want string
	}{
		{"missing revision", func(a *ReplayArtifact) { a.Revision = "" }, "missing evidence"},
		{"different validation policy", func(a *ReplayArtifact) { a.ValidationPolicy = "sampled" }, "validation policy"},
		{"unnormalized configuration", func(a *ReplayArtifact) { a.Workload.ReadPolicy = "" }, "not normalized"},
		{"invalid workload", func(a *ReplayArtifact) { a.Workload.Assertion.ID = "" }, "ID"},
		{"wrong failure class", func(a *ReplayArtifact) { a.Failure.Kind = "sql_error" }, "failure fingerprint"},
		{"noncanonical evidence", func(a *ReplayArtifact) { a.ObservedRows[0][0] = TypedValue{Kind: "int", Value: "01"} }, "canonical typed value"},
		{"passing evidence", func(a *ReplayArtifact) { a.ObservedRows = a.Workload.Assertion.WantRows }, "do not fail"},
		{"history object instead of sessions", func(a *ReplayArtifact) { a.History = json.RawMessage(`{}`) }, "malformed committed history"},
		{"missing nested history events", func(a *ReplayArtifact) { a.History = json.RawMessage(`[[{"committed":true}]]`) }, "malformed committed history"},
		{"unknown nested history field", func(a *ReplayArtifact) { a.History = json.RawMessage(`[[{"events":[],"committed":true,"unknown":0}]]`) }, "malformed committed history"},
		{"empty variable key", func(a *ReplayArtifact) { a.Variables[""] = 1 }, "malformed variable identity"},
		{"zero variable identity", func(a *ReplayArtifact) { a.Variables[a.Choices[0].Key] = 0 }, "malformed variable identity"},
		{"unknown reader transaction", func(a *ReplayArtifact) { a.Choices[0].TransactionID = "absent" }, "unknown or reordered read site"},
		{"statement owned by another transaction", func(a *ReplayArtifact) { a.Choices[0].StatementID = "read" }, "unknown or reordered read site"},
		{"different reader session", func(a *ReplayArtifact) { a.Choices[0].Reader = 99 }, "unknown or reordered read site"},
		{"negative selected index", func(a *ReplayArtifact) { a.Choices[0].Choice = -1 }, "malformed choice"},
		{"selected value outside candidates", func(a *ReplayArtifact) { a.Choices[0].Selected.Key = "other" }, "malformed choice"},
		{"duplicate candidate", func(a *ReplayArtifact) {
			a.Choices[0].Candidates = append(a.Choices[0].Candidates, a.Choices[0].Candidates[0])
		}, "duplicate candidate"},
		{"initial candidate with producer", func(a *ReplayArtifact) {
			a.Choices[0].Candidates[0].TransactionID = "writer"
			a.Choices[0].Selected = a.Choices[0].Candidates[a.Choices[0].Choice]
		}, "malformed source"},
		{"source on wrong key", func(a *ReplayArtifact) {
			a.Choices[producerRead].Candidates[producerCandidate].Key = "other"
			a.Choices[producerRead].Selected = a.Choices[producerRead].Candidates[a.Choices[producerRead].Choice]
		}, "malformed source"},
		{"zero write ordinal", func(a *ReplayArtifact) {
			a.Choices[producerRead].Candidates[producerCandidate].WriteOrdinal = 0
			a.Choices[producerRead].Selected = a.Choices[producerRead].Candidates[a.Choices[producerRead].Choice]
		}, "malformed source"},
		{"missing producer", func(a *ReplayArtifact) {
			a.Choices[producerRead].Candidates[producerCandidate].TransactionID = "absent"
			a.Choices[producerRead].Selected = a.Choices[producerRead].Candidates[a.Choices[producerRead].Choice]
		}, "unknown or uncommitted source"},
		{"rolled-back producer", func(a *ReplayArtifact) { a.Workload.Transactions[0].Finish = "rollback" }, "unknown or uncommitted source"},
		{"nonexistent producer write ordinal", func(a *ReplayArtifact) {
			a.Choices[producerRead].Candidates[producerCandidate].WriteOrdinal += 100
			a.Choices[producerRead].Selected = a.Choices[producerRead].Candidates[a.Choices[producerRead].Choice]
		}, "replay divergence"},
		{"changed committed event stream", func(a *ReplayArtifact) { a.History = json.RawMessage(`[[{"events":[],"committed":true}]]`) }, "replay divergence"},
		{"changed variable numbering", func(a *ReplayArtifact) {
			for key := range a.Variables {
				a.Variables[key] += 100
			}
		}, "replay divergence"},
		{"expected reads removed by SQL", func(a *ReplayArtifact) { a.Workload.Transactions[1].Statements[0].SQL = "SELECT 1" }, "missing reads"},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifact := cloneSQLArtifact(t, original)
			test.edit(&artifact)
			_, err := ReplaySQLArtifact(artifact)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("wanted %q, got %v", test.want, err)
			}
		})
	}
	result, err := ReplaySQLArtifact(original)
	if err != nil || result.Failure == nil || *result.Failure != original.Failure || !bytes.Equal(result.History, original.History) {
		t.Fatalf("rejected artifacts changed later strict replay: %v %+v", err, result)
	}
}

func TestSQLReplayBoundaryCaptureRequiresCompleteEvidence(t *testing.T) {
	artifact := sqlReplayFailure(t)
	for _, test := range []struct {
		name string
		edit func(*SQLResult)
		want string
	}{
		{"missing choices", func(r *SQLResult) { r.Choices = nil }, "missing evidence"},
		{"missing history", func(r *SQLResult) { r.History = nil }, "missing evidence"},
		{"missing variables", func(r *SQLResult) { r.Variables = nil }, "missing evidence"},
		{"passing rows", func(r *SQLResult) { r.ObservedRows = r.Workload.Assertion.WantRows }, "do not fail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := ReplaySQLArtifact(artifact)
			if err != nil {
				t.Fatal(err)
			}
			test.edit(&result)
			if _, err := CaptureSQLFailure(artifact.Workload, result); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("incomplete capture accepted: %v", err)
			}
		})
	}
}

func TestSQLReplayBoundaryPrefixAcrossReadlessTransactions(t *testing.T) {
	workload := SQLWorkload{
		SchemaVersion: 1, Isolation: "read-committed", Setup: []string{},
		Transactions: []SQLTransaction{
			{ID: "empty", SessionID: 1, Statements: []SQLStatement{}, Finish: "commit"},
			{ID: "constant", SessionID: 2, Statements: []SQLStatement{{ID: "constant-select", SQL: "SELECT 1"}}, Finish: "commit"},
		},
		Assertion: SQLAssertion{ID: "constant-value", SQL: "SELECT 1", BaselineRows: []TypedRow{{{Kind: "int", Value: "1"}}}, WantRows: []TypedRow{{{Kind: "int", Value: "1"}}}},
	}
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil || result.Failure != nil || len(result.Choices) != 0 || !reflect.DeepEqual(result.ObservedRows, workload.Assertion.WantRows) {
		t.Fatalf("readless transactions: %+v %v", result, err)
	}
	if result, err := RunSQLWorkload(workload, []int{0}); err == nil || !strings.Contains(err.Error(), "0 of 1 consumed") || result.Failure != nil {
		t.Fatalf("constant SELECT or validation consumed a prefix: %+v %v", result, err)
	}
	workload = sqlReplayFixture()
	workload.Transactions = append([]SQLTransaction{{ID: "readless", SessionID: 7, Statements: []SQLStatement{{ID: "readless-select", SQL: "SELECT 1"}}, Finish: "commit"}}, workload.Transactions...)
	result, err = RunSQLWorkload(workload, []int{0})
	if err != nil || result.Failure != nil || len(result.Choices) < 2 {
		t.Fatalf("one-use prefix followed by newest tail: %+v %v", result, err)
	}
	ambiguous := false
	for index, choice := range result.Choices {
		if index == 0 {
			if choice.TransactionID != "writer" || choice.Choice != 0 {
				t.Fatalf("readless SQL consumed supplied choice: %+v", choice)
			}
			continue
		}
		if choice.Choice != len(choice.Candidates)-1 {
			t.Fatalf("prefix wrapped rather than selecting newest tail: %+v", choice)
		}
		ambiguous = ambiguous || len(choice.Candidates) > 1
	}
	if !ambiguous {
		t.Fatal("fixture did not distinguish one-use prefix from wrapping")
	}
	if execution, err := SQLWorkloadRunner(workload)([]int{-1}); err == nil || execution.Failed {
		t.Fatalf("exploration adapter turned execution error into assertion failure: %+v %v", execution, err)
	}
}

func TestSQLReplayBoundaryValidationQueryErrorIsNotFailure(t *testing.T) {
	workload := sqlReplayFixture()
	workload.Transactions = []SQLTransaction{{ID: "second-row", SessionID: 4, Statements: []SQLStatement{{ID: "insert-second-row", SQL: "INSERT INTO replaydb.items VALUES (2,2)"}}, Finish: "commit"}}
	workload.Assertion.SQL = "SELECT (SELECT v FROM replaydb.items)"
	workload.Assertion.WantRows = workload.Assertion.BaselineRows
	result, err := RunSQLWorkload(workload, []int{})
	if err == nil || !strings.Contains(err.Error(), "validation assertion") || result.Failure != nil {
		t.Fatalf("final scalar-subquery error became rows_mismatch: %+v %v", result, err)
	}
	if _, err := CaptureSQLFailure(workload, result); err == nil {
		t.Fatal("final query error was captured as a failure")
	}
	if fresh, err := RunSQLWorkload(sqlReplayFixture(), []int{}); err != nil || fresh.Failure != nil {
		t.Fatalf("failed final validation leaked rows into a fresh run: %+v %v", fresh, err)
	}
}
