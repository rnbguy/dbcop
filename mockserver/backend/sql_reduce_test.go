package backend

import (
	"reflect"
	"testing"
)

func reductionWorkload() SQLWorkload {
	seed, probability := uint64(17), 0.0
	return SQLWorkload{
		SchemaVersion: 1, Isolation: "read-committed", Seed: &seed,
		ReadPolicy: "sparse", DeviationProbability: &probability,
		Setup: []string{
			"CREATE DATABASE replay_reduce",
			"CREATE TABLE replay_reduce.items (id BIGINT PRIMARY KEY, v BIGINT)",
			"INSERT INTO replay_reduce.items VALUES (1, 0)",
		},
		Transactions: []SQLTransaction{
			{ID: "irrelevant", SessionID: 1, Finish: "commit", Statements: []SQLStatement{{ID: "constant", SQL: "SELECT 7"}}},
			{ID: "writer", SessionID: 2, Finish: "commit", Statements: []SQLStatement{
				{ID: "unneeded", SQL: "SELECT 8"},
				{ID: "write", SQL: "UPDATE replay_reduce.items SET v = 1 WHERE id = 1"},
			}},
			{ID: "observer", SessionID: 3, Finish: "commit", Statements: []SQLStatement{{ID: "observe", SQL: "SELECT v FROM replay_reduce.items WHERE id = 1"}}},
		},
		Assertion: SQLAssertion{
			ID: "unchanged", SQL: "SELECT v FROM replay_reduce.items ORDER BY id",
			BaselineRows: []TypedRow{{{Kind: "int", Value: "0"}}},
			WantRows:     []TypedRow{{{Kind: "int", Value: "0"}}},
		},
	}
}

func reductionArtifact(t *testing.T) ReplayArtifact {
	t.Helper()
	result, err := RunSQLWorkload(reductionWorkload(), []int{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := CaptureSQLFailure(result.Workload, result)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestSQLReductionRemovesTransactionsAndStatements(t *testing.T) {
	artifact := reductionArtifact(t)
	reduced, err := MinimizeSQLArtifact(artifact, 128)
	if err != nil {
		t.Fatal(err)
	}
	if reduced.Status != "fixed_point" || reduced.Trials == 0 || reduced.Trials > 128 {
		t.Fatalf("unexpected reduction status: %+v", reduced)
	}
	if reduced.Artifact.Failure != artifact.Failure {
		t.Fatalf("changed failure: got %+v want %+v", reduced.Artifact.Failure, artifact.Failure)
	}
	transactions := reduced.Artifact.Workload.Transactions
	if len(transactions) != 1 || transactions[0].ID != "writer" || len(transactions[0].Statements) != 1 || transactions[0].Statements[0].ID != "write" {
		t.Fatalf("irrelevant input survived: %+v", transactions)
	}
	before, after := artifact.Workload, reduced.Artifact.Workload
	before.Transactions, after.Transactions = nil, nil
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("fixed configuration changed: before=%+v after=%+v", before, after)
	}
	for _, choice := range reduced.Artifact.Choices {
		found := false
		for _, old := range artifact.Choices {
			if choice.TransactionID == old.TransactionID && choice.StatementID == old.StatementID && choice.ReadOrdinal == old.ReadOrdinal && choice.Key == old.Key {
				found = true
				if choice.Selected != old.Selected {
					t.Fatalf("retained source changed: before=%+v after=%+v", old, choice)
				}
			}
		}
		if !found {
			t.Fatalf("new read was introduced: %+v", choice)
		}
	}
	if _, err := ReplaySQLArtifact(reduced.Artifact); err != nil {
		t.Fatalf("best did not strictly replay: %v", err)
	}
}

func TestSQLReductionBudgetReturnsStrictlyVerifiedBest(t *testing.T) {
	artifact := reductionArtifact(t)
	for _, budget := range []int{1, 2} {
		reduced, err := MinimizeSQLArtifact(artifact, budget)
		if err != nil {
			t.Fatal(err)
		}
		if reduced.Status != "budget_exhausted" || reduced.Trials != budget || reduced.Artifact.Failure != artifact.Failure {
			t.Fatalf("budget %d returned %+v", budget, reduced)
		}
		if _, err := ReplaySQLArtifact(reduced.Artifact); err != nil {
			t.Fatalf("budget best did not strictly replay: %v", err)
		}
	}
	for _, budget := range []int{0, -1} {
		if _, err := MinimizeSQLArtifact(artifact, budget); err == nil {
			t.Fatalf("accepted nonpositive budget %d", budget)
		}
	}
}

func TestSQLReductionRejectsDivergenceInsteadOfResampling(t *testing.T) {
	artifact := reductionArtifact(t)
	if len(artifact.Choices) == 0 {
		t.Fatal("fixture produced no external reads")
	}
	t.Run("unknown source", func(t *testing.T) {
		choices := append([]ReplayChoice(nil), artifact.Choices...)
		choices[0].Selected = ReplaySource{TransactionID: "absent", StatementID: "absent", WriteOrdinal: 1, Key: choices[0].Key}
		if _, err := runSQLReduction(artifact.Workload, choices, nil); err == nil {
			t.Fatal("unknown source was silently replaced")
		}
	})
	t.Run("new site", func(t *testing.T) {
		workload := artifact.Workload
		workload.Transactions = append([]SQLTransaction(nil), workload.Transactions...)
		transaction := &workload.Transactions[len(workload.Transactions)-1]
		transaction.Statements = append(append([]SQLStatement(nil), transaction.Statements...), SQLStatement{ID: "new_read", SQL: "SELECT v FROM replay_reduce.items WHERE id = 1"})
		if _, err := runSQLReduction(workload, artifact.Choices, nil); err == nil {
			t.Fatal("new read site was silently sampled")
		}
	})
	t.Run("missing site", func(t *testing.T) {
		if _, err := runSQLReduction(withoutSQLTransactions(artifact.Workload, 2, 3), artifact.Choices, nil); err == nil {
			t.Fatal("missing retained read site was ignored")
		}
	})
	t.Run("removed producer", func(t *testing.T) {
		hasSource := false
		for _, choice := range artifact.Choices {
			if choice.TransactionID == "observer" && choice.Selected.TransactionID == "writer" {
				hasSource = true
			}
		}
		if !hasSource {
			t.Fatal("fixture did not retain the producer dependency")
		}
		workload := withoutSQLTransactions(artifact.Workload, 1, 2)
		if _, err := runSQLReduction(workload, retainedSQLChoices(artifact.Choices, workload), nil); err == nil {
			t.Fatal("deleted producer was silently replaced by initial state")
		}
	})
}

func TestSQLReductionChoiceSimplificationChangesOnlyTargetSource(t *testing.T) {
	workload := reductionWorkload()
	latest, err := RunSQLWorkload(workload, []int{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]int, len(latest.Choices))
	target := -1
	for index, choice := range latest.Choices {
		prefix[index] = choice.Choice
		if choice.TransactionID == "observer" && len(choice.Candidates) > 1 {
			target = index
		}
	}
	if target < 0 {
		t.Fatal("fixture has no stale observer choice")
	}
	prefix[target] = 0
	stale, err := RunSQLWorkload(workload, prefix)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := CaptureSQLFailure(stale.Workload, stale)
	if err != nil {
		t.Fatal(err)
	}
	r := sqlReduction{best: artifact, limit: 128}
	if !r.reduceChoices() {
		t.Fatal("non-newest irrelevant observation was not simplified")
	}
	if r.best.Failure != artifact.Failure {
		t.Fatal("choice simplification changed failure class")
	}
	for index, choice := range r.best.Choices {
		if index == target {
			if choice.Selected != choice.Candidates[len(choice.Candidates)-1] {
				t.Fatalf("target did not become newest: %+v", choice)
			}
		} else if choice.Selected != artifact.Choices[index].Selected {
			t.Fatalf("untargeted source changed: before=%+v after=%+v", artifact.Choices[index], choice)
		}
	}
	if _, err := ReplaySQLArtifact(r.best); err != nil {
		t.Fatal(err)
	}
}

func TestSQLReductionRejectsCorruptOriginalBeforeTrials(t *testing.T) {
	artifact := reductionArtifact(t)
	artifact.Failure.AssertionID = "different-assertion"
	if _, err := MinimizeSQLArtifact(artifact, 128); err == nil {
		t.Fatal("corrupt failure fingerprint was accepted")
	}
}

func TestSQLReductionRejectsReadCreatedByStatementDeletion(t *testing.T) {
	workload := reductionWorkload()
	writer := workload.Transactions[1]
	writer.Statements = append(append([]SQLStatement(nil), writer.Statements...), SQLStatement{
		ID: "local_read", SQL: "SELECT v FROM replay_reduce.items WHERE id = 1",
	})
	workload.Transactions = []SQLTransaction{writer}
	result, err := RunSQLWorkload(workload, []int{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := CaptureSQLFailure(result.Workload, result)
	if err != nil {
		t.Fatal(err)
	}
	candidate := artifact.Workload
	candidate.Transactions = append([]SQLTransaction(nil), candidate.Transactions...)
	candidate.Transactions[0].Statements = []SQLStatement{writer.Statements[0], writer.Statements[2]}
	retained := retainedSQLChoices(artifact.Choices, candidate)
	if _, err := runSQLReduction(candidate, retained, nil); err == nil {
		t.Fatal("deleting the local write silently sampled the newly external read")
	}
}

func TestSQLReductionNewestTargetDoesNotResampleLaterRetainedRead(t *testing.T) {
	workload := reductionWorkload()
	observer := &workload.Transactions[2]
	observer.Statements = append(append([]SQLStatement(nil), observer.Statements...), SQLStatement{
		ID: "observe_again", SQL: "SELECT v FROM replay_reduce.items WHERE id = 1",
	})
	latest, err := RunSQLWorkload(workload, []int{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := make([]int, len(latest.Choices))
	target := -1
	for index, choice := range latest.Choices {
		prefix[index] = choice.Choice
		if choice.TransactionID == "observer" && len(choice.Candidates) > 1 {
			prefix[index] = 0
			if target < 0 {
				target = index
			}
		}
	}
	if target < 0 {
		t.Fatal("fixture has no non-newest retained reads")
	}
	stale, err := RunSQLWorkload(workload, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runSQLReduction(stale.Workload, stale.Choices, &stale.Choices[target]); err == nil {
		t.Fatal("newest target silently replaced a subsequent retained initial source")
	}
}
