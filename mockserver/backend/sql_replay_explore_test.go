package backend

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/exploration"
)

func sqlGuidedFixture() SQLWorkload {
	workload := sqlReplayFixture()
	workload.Setup = append(workload.Setup,
		"CREATE TABLE replaydb.sink (id BIGINT PRIMARY KEY, v BIGINT)",
		"INSERT INTO replaydb.sink VALUES (1,0)")
	workload.Transactions[1].Statements[0].SQL = "UPDATE replaydb.sink SET v=(SELECT v FROM replaydb.items WHERE id=1) WHERE id=1"
	workload.Assertion.SQL = "SELECT v FROM replaydb.sink WHERE id=1"
	return workload
}

func sqlExploreOptions(mode string) SQLExplorationOptions {
	return SQLExplorationOptions{Mode: mode, Seed: 42, MaxRuns: 128, MaxDecisions: 64, MaxTransactions: 2}
}

func roundTripSQLExploration(t *testing.T, artifact SQLExplorationArtifact) SQLExplorationArtifact {
	t.Helper()
	encoded, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSQLExplorationArtifact(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestSQLExplorationSystematicRealSQLCorpus(t *testing.T) {
	artifact, err := ExploreSQLWorkload(sqlGuidedFixture(), sqlExploreOptions("systematic"))
	if err != nil {
		t.Fatal(err)
	}
	report := artifact.Report
	if !report.Complete || report.Runs <= 1 || report.Runs != report.Evaluated || report.Failures == 0 || report.Failures == report.Runs || report.FeatureCount == 0 || len(report.Outcomes) < 2 {
		t.Fatalf("did not explore real SQL outcomes: %+v", report)
	}
	if report.Transactions != (exploration.TransactionCounts{Attempted: report.Runs * 2, Committed: report.Runs * 2, Completed: report.Runs * 2}) || report.Elapsed <= 0 {
		t.Fatalf("wrong genuine transaction work: %+v", report)
	}
	passing, failing, singleton := false, false, false
	for _, execution := range artifact.Executions {
		passing = passing || execution.Failure == nil
		failing = failing || execution.Failure != nil
		for _, choice := range execution.Choices {
			singleton = singleton || len(choice.Candidates) == 1
			if choice.Selected != choice.Candidates[choice.Choice] {
				t.Fatal("lost stable selected-source identity")
			}
		}
		if execution.Failure != nil {
			failure, err := CaptureSQLFailure(execution.Workload, execution)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReplaySQLArtifact(failure); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !passing || !failing || !singleton {
		t.Fatal("corpus lost passing, assertion-failing or singleton decisions")
	}
	decoded := roundTripSQLExploration(t, artifact)
	results, err := ReplaySQLExplorationArtifact(decoded)
	if err != nil || len(results) != report.Evaluated {
		t.Fatalf("full fixture/source replay failed: %d %v", len(results), err)
	}
}

func TestSQLExplorationGuidedSeedAndFrozenInput(t *testing.T) {
	workload := sqlGuidedFixture()
	options := sqlExploreOptions("guided")
	options.MaxRuns = 16
	first, err := ExploreSQLWorkload(workload, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ExploreSQLWorkload(workload, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Report.Seed != 42 || first.Report.Runs != options.MaxRuns || first.Report.Complete || first.Report.FeatureCount != second.Report.FeatureCount || !reflect.DeepEqual(first.Report.Outcomes, second.Report.Outcomes) {
		t.Fatalf("seeded SQL search changed: %+v %+v", first.Report, second.Report)
	}
	for index, expected := range first.Executions {
		actual := second.Executions[index]
		if !bytes.Equal(expected.History, actual.History) || !reflect.DeepEqual(expected.ObservedRows, actual.ObservedRows) || len(expected.Choices) != len(actual.Choices) {
			t.Fatalf("seed changed execution %d", index)
		}
		for read, choice := range expected.Choices {
			other := actual.Choices[read]
			if !sameSQLUniverse(choice, other) || choice.Selected != other.Selected {
				t.Fatalf("unstable source identity at %d/%d", index, read)
			}
		}
	}
	run := SQLWorkloadRunner(workload)
	workload.Transactions[0].Statements[0].SQL = "SELECT missing FROM replaydb.absent"
	if execution, err := run(nil); err != nil || execution.Failed {
		t.Fatalf("caller mutation changed frozen SQL: %+v %v", execution, err)
	}
}

func TestSQLExplorationChangedSQLControlFlow(t *testing.T) {
	workload := sqlGuidedFixture()
	workload.Transactions[1].Statements[0].SQL = "SELECT (SELECT v FROM replaydb.sink WHERE id=1) FROM replaydb.items WHERE v=1"
	workload.Assertion.SQL = "SELECT v FROM replaydb.items WHERE id=1"
	artifact, err := ExploreSQLWorkload(workload, sqlExploreOptions("systematic"))
	if err != nil {
		t.Fatal(err)
	}
	minimum, maximum := len(artifact.Executions[0].Choices), 0
	for _, execution := range artifact.Executions {
		minimum = min(minimum, len(execution.Choices))
		maximum = max(maximum, len(execution.Choices))
	}
	if minimum == maximum {
		t.Fatal("fixture did not exercise a result-dependent SQL read suffix")
	}
	if _, err := ReplaySQLExplorationArtifact(roundTripSQLExploration(t, artifact)); err != nil {
		t.Fatal(err)
	}
}

func TestSQLExplorationStableAdmittedUniverse(t *testing.T) {
	runner, err := newSQLWorkloadRunner(sqlGuidedFixture(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.run(nil); err != nil {
		t.Fatal(err)
	}
	// Corrupt recorded expectations, not the oracle or a fabricated execution.
	runner.branch.choice.Candidates[0].Key = "changed-cell"
	if _, err := runner.run(nil); err == nil || !strings.Contains(err.Error(), "admitted universe changed") {
		t.Fatalf("changed admitted identities were silently replayed by index: %v", err)
	}
}

func TestSQLExplorationRollbackAndSavepoint(t *testing.T) {
	workload := sqlGuidedFixture()
	for index := range workload.Transactions {
		workload.Transactions[index].Finish = "rollback"
	}
	workload.Assertion.WantRows = workload.Assertion.BaselineRows
	artifact, err := ExploreSQLWorkload(workload, sqlExploreOptions("guided"))
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Report.Runs != 1 || artifact.Report.Failures != 0 || artifact.Report.Transactions != (exploration.TransactionCounts{Attempted: 2, RolledBack: 2, Completed: 2}) {
		t.Fatalf("rollback counted as commit or leaked sources: %+v", artifact.Report)
	}
	if string(artifact.Executions[0].History) != "[]" {
		t.Fatalf("rolled-back transactions entered history: %s", artifact.Executions[0].History)
	}
	if _, err := ReplaySQLExplorationArtifact(roundTripSQLExploration(t, artifact)); err != nil {
		t.Fatal(err)
	}
	workload = sqlReplayFixture()
	workload.Transactions[0].Statements = []SQLStatement{
		{ID: "keep-write", SQL: "UPDATE replaydb.items SET v=1 WHERE id=1"},
		{ID: "save", SQL: "SAVEPOINT keep"},
		{ID: "discard-write", SQL: "UPDATE replaydb.items SET v=99 WHERE id=1"},
		{ID: "restore", SQL: "ROLLBACK TO SAVEPOINT keep"},
	}
	options := sqlExploreOptions("guided")
	options.MaxRuns = 2
	artifact, err = ExploreSQLWorkload(workload, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, execution := range artifact.Executions {
		for _, choice := range execution.Choices {
			for _, source := range choice.Candidates {
				if source.StatementID == "discard-write" {
					t.Fatal("savepoint rollback leaked an external source")
				}
			}
		}
	}
	if _, err := ReplaySQLExplorationArtifact(roundTripSQLExploration(t, artifact)); err != nil {
		t.Fatal(err)
	}
}

func TestSQLExplorationErrorsAndStrictBounds(t *testing.T) {
	options := sqlExploreOptions("guided")
	for _, edit := range []func(*SQLExplorationOptions){
		func(o *SQLExplorationOptions) { o.Mode = "other" },
		func(o *SQLExplorationOptions) { o.MaxRuns = 0 },
		func(o *SQLExplorationOptions) { o.MaxDecisions = -1 },
		func(o *SQLExplorationOptions) { o.MaxTransactions = -1 },
		func(o *SQLExplorationOptions) { o.MaxTransactions = 1 },
	} {
		invalid := options
		edit(&invalid)
		if artifact, err := ExploreSQLWorkload(sqlGuidedFixture(), invalid); err == nil || artifact.Report.Runs != 0 {
			t.Fatalf("invalid bound executed SQL: %+v %v", artifact, err)
		}
	}
	for _, boundError := range []bool{false, true} {
		workload := sqlGuidedFixture()
		options := options
		if boundError {
			options.MaxDecisions = 0
		} else {
			workload.Transactions[1].Statements[0].SQL = "SELECT missing FROM replaydb.items"
		}
		artifact, err := ExploreSQLWorkload(workload, options)
		if err == nil || artifact.Error == "" || artifact.Interrupted == nil || artifact.Report.Failures != 0 || artifact.Report.Evaluated != 0 || artifact.Report.Runs != 1 || artifact.Report.Complete {
			t.Fatalf("execution error became assertion evidence: %+v %v", artifact, err)
		}
		expected := exploration.TransactionCounts{Attempted: 2, Committed: 1, Completed: 1}
		if boundError {
			expected = exploration.TransactionCounts{Attempted: 1}
		}
		if artifact.Report.Transactions != expected || artifact.Report.Elapsed <= 0 {
			t.Fatalf("interrupted run pretended completion: %+v", artifact.Report)
		}
		decoded := roundTripSQLExploration(t, artifact)
		if _, err := ReplaySQLExplorationArtifact(decoded); err == nil || !strings.Contains(err.Error(), "reproduced execution error") {
			t.Fatalf("error input not replayable: %v", err)
		}
	}
}

func TestSQLExplorationRejectsForgedCoverageAndPassingFailure(t *testing.T) {
	options := sqlExploreOptions("guided")
	options.MaxRuns = 1
	artifact, err := ExploreSQLWorkload(sqlReplayFixture(), options)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*SQLExplorationArtifact){
		func(a *SQLExplorationArtifact) { a.Report.FeatureCount++ },
		func(a *SQLExplorationArtifact) { a.Report.Transactions.Completed++ },
		func(a *SQLExplorationArtifact) { a.Report.Complete = true },
		func(a *SQLExplorationArtifact) {
			a.Executions[0].Failure = &FailureFingerprint{AssertionID: "value", Kind: "rows_mismatch"}
		},
		func(a *SQLExplorationArtifact) { a.Executions[0].Failure = &FailureFingerprint{} },
		func(a *SQLExplorationArtifact) { a.Executions[0].Transactions.Rejected++ },
	} {
		copy := roundTripSQLExploration(t, artifact)
		edit(&copy)
		encoded, err := json.Marshal(copy)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeSQLExplorationArtifact(bytes.NewReader(encoded)); err == nil {
			t.Fatal("forged outcome or coverage accepted")
		}
	}
}

func TestSQLExplorationRejectedCommitIsNotCompleted(t *testing.T) {
	workload := SQLWorkload{
		SchemaVersion: 1, Isolation: "serializable",
		Setup: []string{
			"CREATE DATABASE replay_reject",
			"CREATE TABLE replay_reject.x (id BIGINT PRIMARY KEY, v BIGINT)",
			"CREATE TABLE replay_reject.y (id BIGINT PRIMARY KEY, v BIGINT)",
			"INSERT INTO replay_reject.x VALUES (1,0)",
			"INSERT INTO replay_reject.y VALUES (1,0)",
		},
		Transactions: []SQLTransaction{
			{ID: "writer", SessionID: 1, Finish: "commit", Statements: []SQLStatement{
				{ID: "read-y", SQL: "SELECT v FROM replay_reject.y WHERE id=1"},
				{ID: "write-x", SQL: "UPDATE replay_reject.x SET v=1 WHERE id=1"},
			}},
			{ID: "reader", SessionID: 2, Finish: "commit", Statements: []SQLStatement{
				{ID: "read-x", SQL: "SELECT v FROM replay_reject.x WHERE id=1"},
				{ID: "write-y", SQL: "UPDATE replay_reject.y SET v=1 WHERE id=1"},
			}},
		},
		Assertion: SQLAssertion{ID: "x-written", SQL: "SELECT v FROM replay_reject.x WHERE id=1",
			BaselineRows: []TypedRow{{{Kind: "int", Value: "0"}}},
			WantRows:     []TypedRow{{{Kind: "int", Value: "1"}}}},
	}
	artifact, err := ExploreSQLWorkload(workload, sqlExploreOptions("systematic"))
	if err == nil || artifact.Interrupted == nil || !strings.Contains(artifact.Error, `execution commit "reader"`) {
		t.Fatalf("write-skew commit did not remain an execution rejection: %+v %v", artifact.Report, err)
	}
	progress := artifact.Interrupted.Transactions
	if progress != (exploration.TransactionCounts{Attempted: 2, Committed: 1, Rejected: 1, Completed: 1}) || artifact.Report.Failures != 0 {
		t.Fatalf("rejected commit became completed work or assertion failure: %+v", artifact.Report)
	}
	if _, err := ReplaySQLExplorationArtifact(roundTripSQLExploration(t, artifact)); err == nil || !strings.Contains(err.Error(), "reproduced execution error") {
		t.Fatalf("rejected SQL prefix not reproducible: %v", err)
	}
}

func TestSQLWorkloadNewNativePoliciesAndDeviationBudget(t *testing.T) {
	for _, policy := range []string{"age-stratified", "bounded-deviation"} {
		for _, budget := range []int{0, 1} {
			workload := sqlReplayFixture()
			workload.ReadPolicy = policy
			probability := 1.0
			workload.DeviationProbability, workload.DeviationBudget = &probability, &budget
			workload.Assertion.WantRows = workload.Assertion.BaselineRows
			result, err := RunSQLWorkload(workload, nil)
			if err != nil {
				t.Fatal(err)
			}
			deviations := make(map[string]int)
			for _, choice := range result.Choices {
				if choice.Choice != len(choice.Candidates)-1 {
					deviations[choice.TransactionID]++
				}
			}
			if policy == "bounded-deviation" {
				for transaction, count := range deviations {
					if count > budget {
						t.Fatalf("SQL capture bypassed accepted-read budget: txn=%s count=%d budget=%d", transaction, count, budget)
					}
				}
				if budget == 1 && deviations["reader"] != 1 {
					t.Fatalf("probability-one SQL policy failed to spend its single admitted deviation: %+v", deviations)
				}
			}
			artifact, err := CaptureSQLFailure(workload, result)
			if err != nil {
				t.Fatal(err)
			}
			if artifact.Workload.DeviationBudget == nil || *artifact.Workload.DeviationBudget != budget {
				t.Fatal("native policy budget was not captured")
			}
			if _, err := ReplaySQLArtifact(cloneSQLArtifact(t, artifact)); err != nil {
				t.Fatal(err)
			}
		}
	}
	workload := sqlReplayFixture()
	budget := -1
	workload.DeviationBudget = &budget
	if _, err := RunSQLWorkload(workload, nil); err == nil || !strings.Contains(err.Error(), "deviation_budget") {
		t.Fatalf("negative native deviation budget accepted: %v", err)
	}
}

func TestSQLWorkloadAgeBucketsUseActualInitialProvenance(t *testing.T) {
	for seed := uint64(1); seed <= 8; seed++ {
		workload := sqlReplayFixture()
		workload.Seed, workload.ReadPolicy = &seed, "age-stratified"
		workload.Transactions = []SQLTransaction{
			{ID: "one", SessionID: 1, Finish: "commit", Statements: []SQLStatement{{ID: "write-one", SQL: "UPDATE replaydb.items SET v=1 WHERE id=1"}}},
			{ID: "two", SessionID: 2, Finish: "commit", Statements: []SQLStatement{{ID: "write-two", SQL: "UPDATE replaydb.items SET v=2 WHERE id=1"}}},
			{ID: "three", SessionID: 3, Finish: "commit", Statements: []SQLStatement{{ID: "write-three", SQL: "UPDATE replaydb.items SET v=3 WHERE id=1"}}},
			{ID: "read-all", SessionID: 4, Finish: "commit", Statements: []SQLStatement{{ID: "last-read", SQL: "SELECT v FROM replaydb.items WHERE id=1"}}},
		}
		workload.Assertion.WantRows = []TypedRow{{{Kind: "int", Value: "3"}}}
		result, err := RunSQLWorkload(workload, nil)
		if err != nil {
			t.Fatal(err)
		}
		reference := NewStore(seed, seed+1)
		if err := reference.SetReadPolicy("age-stratified", 0.01); err != nil {
			reference.Close()
			t.Fatal(err)
		}
		distinctBuckets := false
		for _, choice := range result.Choices {
			distinctBuckets = distinctBuckets || len(choice.Candidates) >= 4 && choice.Candidates[0].Initial
			expected, _ := reference.selectReadIndex(len(choice.Candidates), choice.Candidates[0].Initial, nil)
			if expected != choice.Choice {
				reference.Close()
				t.Fatalf("SQL controller lost admitted root provenance at seed %d: expected=%d choice=%+v", seed, expected, choice)
			}
		}
		reference.Close()
		if !distinctBuckets {
			t.Fatal("fixture never reached separate root/oldest/middle/newest buckets")
		}
	}
}

func TestSQLExplorationZeroDecisionAndTransactionBounds(t *testing.T) {
	workload := sqlReplayFixture()
	workload.Transactions = []SQLTransaction{}
	workload.Assertion.WantRows = workload.Assertion.BaselineRows
	options := SQLExplorationOptions{Mode: "systematic", Seed: 42, MaxRuns: 1}
	artifact, err := ExploreSQLWorkload(workload, options)
	if err != nil || !artifact.Report.Complete || artifact.Report.Runs != 1 || artifact.Report.FeatureCount != 0 || artifact.Report.Transactions != (exploration.TransactionCounts{}) || len(artifact.Executions[0].Choices) != 0 {
		t.Fatalf("zero-work bounds changed fixture or invented work: %+v %v", artifact.Report, err)
	}
	if _, err := ReplaySQLExplorationArtifact(roundTripSQLExploration(t, artifact)); err != nil {
		t.Fatal(err)
	}
}
