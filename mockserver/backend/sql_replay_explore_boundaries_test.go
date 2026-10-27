package backend

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSQLExplorationArtifactRejectsInvalidDomainAndBounds(t *testing.T) {
	options := sqlExploreOptions("guided")
	options.MaxRuns = 1
	artifact, err := ExploreSQLWorkload(sqlGuidedFixture(), options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSQLExplorationArtifact(strings.NewReader("{")); err == nil {
		t.Fatal("malformed JSON accepted")
	}
	for _, test := range []struct {
		name string
		edit func(*SQLExplorationArtifact)
	}{
		{"schema", func(a *SQLExplorationArtifact) { a.SchemaVersion++ }},
		{"unknown isolation", func(a *SQLExplorationArtifact) { a.Workload.Isolation = "unknown" }},
		{"unnormalized seed", func(a *SQLExplorationArtifact) { a.Workload.Seed = nil }},
		{"transaction bound", func(a *SQLExplorationArtifact) { a.Options.MaxTransactions = 1 }},
		{"search seed", func(a *SQLExplorationArtifact) { a.Report.Seed++ }},
		{"execution workload", func(a *SQLExplorationArtifact) { a.Executions[0].Workload.Isolation = "serializable" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := roundTripSQLExploration(t, artifact)
			test.edit(&copy)
			data, err := json.Marshal(copy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSQLExplorationArtifact(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid corpus accepted")
			}
		})
	}
}

func TestSQLExplorationRejectsFalseSystematicExhaustion(t *testing.T) {
	options := sqlExploreOptions("systematic")
	options.MaxRuns = 1
	artifact, err := ExploreSQLWorkload(sqlGuidedFixture(), options)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Report.Complete {
		t.Fatal("one run unexpectedly exhausted source alternatives")
	}
	artifact.Report.Complete = true
	if _, err := ReplaySQLExplorationArtifact(artifact); err == nil || !strings.Contains(err.Error(), "unexplored admitted source branch") {
		t.Fatalf("false completeness was trusted: %v", err)
	}
}

func TestSQLExplorationReplayChecksClaimedOutcomeAgainstSQL(t *testing.T) {
	options := sqlExploreOptions("guided")
	options.MaxRuns = 1
	artifact, err := ExploreSQLWorkload(sqlGuidedFixture(), options)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Executions[0].ObservedRows = []TypedRow{{{Kind: "int", Value: "999"}}}
	artifact.Executions[0].Failure = &FailureFingerprint{AssertionID: artifact.Workload.Assertion.ID, Kind: "rows_mismatch"}
	outcome, err := json.Marshal(artifact.Executions[0].ObservedRows)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Report.Outcomes = map[string]int{string(outcome): 1}
	artifact.Report.Failures, artifact.Report.FirstFailure = 1, 1
	artifact.Report.Corpus[0].Outcome, artifact.Report.Corpus[0].Failed = string(outcome), true
	decoded := roundTripSQLExploration(t, artifact)
	if _, err := ReplaySQLExplorationArtifact(decoded); err == nil || !strings.Contains(err.Error(), "replay divergence") {
		t.Fatalf("self-consistent forged outcome was not checked by executing SQL: %v", err)
	}
}

func TestSQLExplorationInterruptedEvidenceIsNotTrusted(t *testing.T) {
	options := sqlExploreOptions("guided")
	options.MaxDecisions = 0
	artifact, err := ExploreSQLWorkload(sqlGuidedFixture(), options)
	if err == nil || artifact.Interrupted == nil {
		t.Fatal("fixture did not hit its decision bound")
	}
	for _, test := range []struct {
		name string
		edit func(*SQLExplorationArtifact)
	}{
		{"missing prefix", func(a *SQLExplorationArtifact) { a.Interrupted.Prefix = nil }},
		{"missing decisions", func(a *SQLExplorationArtifact) { a.Interrupted.Choices = nil }},
		{"negative choice", func(a *SQLExplorationArtifact) { a.Options.MaxDecisions = 1; a.Interrupted.Prefix = []int{-1} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := roundTripSQLExploration(t, artifact)
			test.edit(&copy)
			data, err := json.Marshal(copy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodeSQLExplorationArtifact(bytes.NewReader(data)); err == nil {
				t.Fatal("invalid interrupted evidence accepted")
			}
		})
	}
	t.Run("claimed error", func(t *testing.T) {
		copy := roundTripSQLExploration(t, artifact)
		copy.Error = "different error"
		if _, err := ReplaySQLExplorationArtifact(copy); err == nil || !strings.Contains(err.Error(), "interrupted execution changed") {
			t.Fatalf("wrong error was trusted: %v", err)
		}
	})
	t.Run("claimed partial work", func(t *testing.T) {
		copy := roundTripSQLExploration(t, artifact)
		copy.Interrupted.Transactions.Attempted++
		copy.Report.Transactions.Attempted++
		if _, err := ReplaySQLExplorationArtifact(copy); err == nil || !strings.Contains(err.Error(), "interrupted transaction or read counts changed") {
			t.Fatalf("wrong partial work was trusted: %v", err)
		}
	})
}
