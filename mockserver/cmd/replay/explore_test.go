package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/backend"
)

func TestReplayCLIExploreAndReplayCompleteCorpus(t *testing.T) {
	directory := t.TempDir()
	workloadPath, artifactPath := filepath.Join(directory, "workload.json"), filepath.Join(directory, "corpus.json")
	writeJSON(t, workloadPath, cliWorkload())
	result, code, diagnostic := invokeCLI(t, "explore", "--workload", workloadPath, "--artifact", artifactPath,
		"--mode", "guided", "--seed", "42", "--max-runs", "3", "--max-decisions", "64", "--max-transactions", "2")
	if code != 1 || result.Status != "assertion_failures" || result.Artifact != artifactPath || result.Exploration == nil || result.Exploration.Seed != 42 || result.Exploration.Failures == 0 || result.ReadPolicy != "replay-prefix-then-newest-admitted" {
		t.Fatalf("guided SQL CLI lost real assertion evidence: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	artifact, err := decodeFile(artifactPath, backend.DecodeSQLExplorationArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Executions) != result.Exploration.Evaluated || artifact.Report.Transactions.Completed != artifact.Report.Evaluated*2 {
		t.Fatalf("incomplete persisted SQL executions: %+v", artifact.Report)
	}
	result, code, diagnostic = invokeCLI(t, "replay-corpus", "--artifact", artifactPath)
	if code != 0 || result.Status != "corpus_reproduced" || result.Exploration == nil || result.Exploration.Failures == 0 {
		t.Fatalf("corpus strict replay: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	original, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	result, code, _ = invokeCLI(t, "explore", "--workload", workloadPath, "--artifact", artifactPath, "--max-runs", "1")
	if code != 2 || result.Artifact != "" {
		t.Fatalf("corpus overwrite claimed success: %+v code=%d", result, code)
	}
	after, err := os.ReadFile(artifactPath)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("corpus overwrite changed existing evidence: %v", err)
	}
}

func TestReplayCLIPassingCorpusIsNotFailureArtifact(t *testing.T) {
	directory := t.TempDir()
	workloadPath, artifactPath := filepath.Join(directory, "workload.json"), filepath.Join(directory, "passing.json")
	workload := cliWorkload()
	workload.Transactions[0].Finish = "rollback"
	writeJSON(t, workloadPath, workload)
	result, code, diagnostic := invokeCLI(t, "explore", "--workload", workloadPath, "--artifact", artifactPath,
		"--mode", "systematic", "--max-runs", "8", "--max-transactions", "2")
	if code != 0 || result.Status != "explored" || result.Exploration == nil || !result.Exploration.Complete || result.Exploration.Failures != 0 || result.Exploration.Transactions.RolledBack != 1 || result.Exploration.Transactions.Committed != 1 {
		t.Fatalf("passing corpus mislabeled failure: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	artifact, err := decodeFile(artifactPath, backend.DecodeSQLExplorationArtifact)
	if err != nil || len(artifact.Executions) != 1 || artifact.Executions[0].Failure != nil {
		t.Fatalf("passing evidence missing or forged: %+v %v", artifact, err)
	}
	if _, err := decodeFile(artifactPath, backend.DecodeReplayArtifact); err == nil {
		t.Fatal("successful coverage corpus accepted as failure-only artifact")
	}
	result, code, diagnostic = invokeCLI(t, "replay-corpus", "--artifact", artifactPath)
	if code != 0 || result.Status != "corpus_reproduced" {
		t.Fatalf("passing corpus failed replay: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
}

func TestReplayCLIExploreSQLExecutionErrorPersistsPartialCorpus(t *testing.T) {
	directory := t.TempDir()
	workloadPath, artifactPath := filepath.Join(directory, "workload.json"), filepath.Join(directory, "error.json")
	workload := cliWorkload()
	workload.Transactions[1].Statements[0].SQL = "SELECT missing FROM replay_cli.items"
	writeJSON(t, workloadPath, workload)
	result, code, diagnostic := invokeCLI(t, "explore", "--workload", workloadPath, "--artifact", artifactPath, "--max-transactions", "2")
	if code != 2 || result.Status != "error" || result.Exploration == nil || result.Exploration.Failures != 0 || result.Exploration.Evaluated != 0 || result.Exploration.Transactions.Attempted != 2 || result.Exploration.Transactions.Completed != 1 || result.Artifact != artifactPath || diagnostic == "" {
		t.Fatalf("SQL error was counted as assertion/finished work: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	artifact, err := decodeFile(artifactPath, backend.DecodeSQLExplorationArtifact)
	if err != nil || artifact.Error == "" || artifact.Interrupted == nil {
		t.Fatalf("SQL error inputs were not persisted: %+v %v", artifact, err)
	}
	result, code, diagnostic = invokeCLI(t, "replay-corpus", "--artifact", artifactPath)
	if code != 2 || result.Status != "error" || !strings.Contains(result.Error, "reproduced execution error") || diagnostic == "" {
		t.Fatalf("SQL error corpus became a clean result: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
}

func TestReplayCLIExploreStrictBoundsAndCorruptCorpus(t *testing.T) {
	for _, extra := range [][]string{
		{"--mode", "random"}, {"--max-runs", "0"}, {"--max-decisions", "-1"}, {"--max-transactions", "1"},
	} {
		directory := t.TempDir()
		workloadPath, artifactPath := filepath.Join(directory, "workload.json"), filepath.Join(directory, "unused.json")
		writeJSON(t, workloadPath, cliWorkload())
		args := append([]string{"explore", "--workload", workloadPath, "--artifact", artifactPath}, extra...)
		result, code, _ := invokeCLI(t, args...)
		if code != 2 || result.Status != "error" || result.Artifact != "" {
			t.Fatalf("invalid exploration bound accepted: %v %+v code=%d", extra, result, code)
		}
		if _, err := os.Stat(artifactPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid options wrote corpus: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"extra":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, code, _ := invokeCLI(t, "replay-corpus", "--artifact", path)
	if code != 2 || result.Status != "error" {
		t.Fatalf("corrupt corpus accepted: code=%d result=%+v", code, result)
	}
}

func TestReplayCLIExampleFixedWorkloadMetrics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failure.json")
	result, code, diagnostic := invokeCLI(t, "run", "--workload", "testdata/fixed-sql.json", "--artifact", path)
	if code != 0 && code != 1 {
		t.Fatalf("usable fixed SQL fixture failed: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	if result.Transactions == nil || result.Transactions.Attempted != 2 || result.Transactions.Committed != 2 || result.Transactions.Completed != 2 || result.Transactions.Rejected != 0 || result.Transactions.RolledBack != 0 || result.WorkloadElapsed <= 0 || result.Seed == nil || *result.Seed != 1 || result.DeviationBudget == nil || *result.DeviationBudget != 1 {
		t.Fatalf("ordinary fixed SQL omitted exact work metrics: %+v", result)
	}
	if code == 1 && (result.Failure == nil || result.Status != "rows_mismatch") || code == 0 && result.Failure != nil {
		t.Fatalf("fixture assertion result mislabeled: %+v code=%d", result, code)
	}
}
