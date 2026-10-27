package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/backend"
)

// Each invocation runs the real command entrypoint in a separate process.
func TestReplayCLIProcess(t *testing.T) {
	if os.Getenv("DBCOP_REPLAY_CLI_HELPER") != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Exit(runCLI(os.Args[index+1:], os.Stdout, os.Stderr))
		}
	}
	t.Fatal("missing helper arguments")
}

func invokeCLI(t *testing.T, args ...string) (commandResult, int, string) {
	t.Helper()
	command := exec.Command(os.Args[0], append([]string{"-test.run=^TestReplayCLIProcess$", "--"}, args...)...)
	command.Env = append(os.Environ(), "DBCOP_REPLAY_CLI_HELPER=1")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	code := 0
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	var result commandResult
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("command %v did not emit a result: %v; stdout=%s stderr=%s", args, err, stdout.String(), stderr.String())
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		t.Fatalf("command emitted more than one result: %v", err)
	}
	return result, code, stderr.String()
}

func cliWorkload() backend.SQLWorkload {
	row := backend.TypedRow{{Kind: "int", Value: "0"}, {Kind: "decimal", Value: "1.2"}, {Kind: "text", Value: "fixed"}, {Kind: "null", Value: ""}}
	return backend.SQLWorkload{
		SchemaVersion: 1, Isolation: "read-committed",
		Setup: []string{
			"CREATE DATABASE replay_cli",
			"CREATE TABLE replay_cli.items (id BIGINT PRIMARY KEY, v BIGINT)",
			"INSERT INTO replay_cli.items VALUES (1, 0)",
		},
		Transactions: []backend.SQLTransaction{
			{ID: "writer", SessionID: 1, Finish: "commit", Statements: []backend.SQLStatement{
				{ID: "unneeded", SQL: "SELECT 8"},
				{ID: "write", SQL: "UPDATE replay_cli.items SET v = 1 WHERE id = 1"},
			}},
			{ID: "irrelevant", SessionID: 2, Finish: "commit", Statements: []backend.SQLStatement{{ID: "constant", SQL: "SELECT 7"}}},
		},
		Assertion: backend.SQLAssertion{
			ID: "unchanged", SQL: "SELECT v, CAST(1.20 AS DECIMAL(8,2)), 'fixed', NULL FROM replay_cli.items ORDER BY id",
			BaselineRows: []backend.TypedRow{row}, WantRows: []backend.TypedRow{row},
		},
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func capturedCLIArtifact(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	workload, artifact := filepath.Join(directory, "workload.json"), filepath.Join(directory, "failure.json")
	writeJSON(t, workload, cliWorkload())
	result, code, diagnostic := invokeCLI(t, "run", "--workload", workload, "--artifact", artifact)
	if code != 1 || result.Status != "rows_mismatch" || result.Artifact != artifact || result.Failure == nil || result.Failure.AssertionID != "unchanged" || result.Failure.Kind != "rows_mismatch" {
		t.Fatalf("capture failed: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	if result.Seed == nil || *result.Seed != 1 || result.ReadPolicy != "uniform" {
		t.Fatalf("actual default seed/policy missing: %+v", result)
	}
	if len(result.ObservedRows) != 1 || len(result.ObservedRows[0]) != 4 || result.ObservedRows[0][0].Value != "1" || result.ObservedRows[0][1] != (backend.TypedValue{Kind: "decimal", Value: "1.2"}) {
		t.Fatalf("typed rows were not emitted: %+v", result.ObservedRows)
	}
	return workload, artifact
}

func TestReplayCLIRealProcessCaptureReplayMinimize(t *testing.T) {
	_, artifact := capturedCLIArtifact(t)
	result, code, diagnostic := invokeCLI(t, "replay", "--artifact", artifact)
	if code != 0 || result.Status != "reproduced" || result.Failure == nil || result.Failure.Kind != "rows_mismatch" {
		t.Fatalf("replay: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	output := filepath.Join(t.TempDir(), "minimized.json")
	result, code, diagnostic = invokeCLI(t, "minimize", "--artifact", artifact, "--output", output)
	if code != 0 || result.Status != "minimized" || result.ReductionStatus != "fixed_point" || result.Trials == nil || *result.Trials == 0 {
		t.Fatalf("minimize: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	minimized, err := decodeFile(output, backend.DecodeReplayArtifact)
	if err != nil {
		t.Fatal(err)
	}
	if len(minimized.Workload.Transactions) != 1 || len(minimized.Workload.Transactions[0].Statements) != 1 || minimized.Workload.Transactions[0].Statements[0].ID != "write" {
		t.Fatalf("input did not shrink: %+v", minimized.Workload.Transactions)
	}
	result, code, diagnostic = invokeCLI(t, "replay", "--artifact", output)
	if code != 0 || result.Status != "reproduced" || result.Failure == nil || *result.Failure != minimized.Failure {
		t.Fatalf("minimized replay: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
}

func TestReplayCLIPassingRunDoesNotWriteFailure(t *testing.T) {
	directory := t.TempDir()
	workloadPath, artifact := filepath.Join(directory, "workload.json"), filepath.Join(directory, "absent.json")
	workload := cliWorkload()
	workload.Transactions = []backend.SQLTransaction{}
	writeJSON(t, workloadPath, workload)
	result, code, diagnostic := invokeCLI(t, "run", "--workload", workloadPath, "--artifact", artifact)
	if code != 0 || result.Status != "passed" || result.Failure != nil || result.Seed == nil || result.ReadPolicy == "" {
		t.Fatalf("passing run: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("passing run wrote an artifact: %v", err)
	}
}

func TestReplayCLIRejectsInvalidWorkloads(t *testing.T) {
	base, err := json.Marshal(cliWorkload())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"corrupt":        "{",
		"unknown":        `{"extra":true,` + string(base[1:]),
		"nested unknown": strings.Replace(string(base), `"id":"writer"`, `"id":"writer","extra":true`, 1),
		"trailing":       string(base) + " {}",
		"schema":         strings.Replace(string(base), `"schema_version":1`, `"schema_version":2`, 1),
		"isolation":      strings.Replace(string(base), `"read-committed"`, `"unknown"`, 1),
		"sql error":      strings.Replace(string(base), "UPDATE replay_cli.items SET v = 1 WHERE id = 1", "UPDATE replay_cli.absent SET v = 1", 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			workload, artifact := filepath.Join(directory, "workload.json"), filepath.Join(directory, "failure.json")
			if err := os.WriteFile(workload, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			result, code, diagnostic := invokeCLI(t, "run", "--workload", workload, "--artifact", artifact)
			if code != 2 || result.Status != "error" || result.Error == "" || diagnostic == "" || result.Artifact != "" {
				t.Fatalf("invalid input was accepted: code=%d result=%+v stderr=%s", code, result, diagnostic)
			}
			if _, err := os.Stat(artifact); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid execution published an artifact: %v", err)
			}
		})
	}
}

func TestReplayCLIRejectsCorruptArtifactAndReportsRevisionDrift(t *testing.T) {
	_, path := capturedCLIArtifact(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"corrupt":             "{",
		"unknown":             `{"extra":true,` + string(data[1:]),
		"nested unknown":      strings.Replace(string(data), `"transaction_id":`, `"extra":true,"transaction_id":`, 1),
		"trailing":            string(data) + " null",
		"schema":              strings.Replace(string(data), `"schema_version":1`, `"schema_version":2`, 1),
		"changed observation": strings.Replace(string(data), `"observed_rows":[[{"kind":"int","value":"1"}`, `"observed_rows":[[{"kind":"int","value":"999"}`, 1),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			invalid := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(invalid, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			result, code, diagnostic := invokeCLI(t, "replay", "--artifact", invalid)
			if code != 2 || result.Status != "error" || result.Error == "" || diagnostic == "" {
				t.Fatalf("invalid replay: code=%d result=%+v stderr=%s", code, result, diagnostic)
			}
		})
	}
	artifact, err := decodeFile(path, backend.DecodeReplayArtifact)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Revision = "different-producer-revision"
	drifted := filepath.Join(t.TempDir(), "drifted.json")
	writeJSON(t, drifted, artifact)
	result, code, diagnostic := invokeCLI(t, "replay", "--artifact", drifted)
	if code != 0 || result.Status != "reproduced" || !strings.Contains(diagnostic, "revision drift") {
		t.Fatalf("revision drift: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
}

func TestReplayCLIPersistenceFailureNeverClaimsSuccess(t *testing.T) {
	workload, artifact := capturedCLIArtifact(t)
	original, err := os.ReadFile(artifact)
	if err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{artifact, filepath.Join(t.TempDir(), "missing", "failure.json")} {
		result, code, diagnostic := invokeCLI(t, "run", "--workload", workload, "--artifact", destination)
		if code != 2 || result.Status != "error" || result.Artifact != "" || result.Failure != nil || diagnostic == "" {
			t.Fatalf("persistence failure claimed success: code=%d result=%+v stderr=%s", code, result, diagnostic)
		}
	}
	after, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("preexisting artifact changed: %v", err)
	}
	result, code, diagnostic := invokeCLI(t, "minimize", "--artifact", artifact, "--output", artifact)
	if code != 2 || result.Status != "error" || result.Artifact != "" || diagnostic == "" {
		t.Fatalf("minimize overwrite claimed success: code=%d result=%+v stderr=%s", code, result, diagnostic)
	}
	after, err = os.ReadFile(artifact)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("minimize changed its input: %v", err)
	}
}

func TestPersistArtifactCleansOwnedTemporaryOnEncodeFailure(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "invalid.json")
	if err := persistArtifact(path, backend.ReplayArtifact{History: json.RawMessage("{")}); err == nil {
		t.Fatal("invalid raw JSON was persisted")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed persistence left files: %v error=%v", entries, err)
	}
}

func TestReplayCLIRejectsBadFlagsAndBudgets(t *testing.T) {
	for _, args := range [][]string{
		{}, {"unknown"}, {"run"}, {"replay", "--unknown"}, {"minimize", "--max-trials", "bad"},
		{"run", "--workload", "missing", "--artifact", "unused", "extra"},
	} {
		result, code, diagnostic := invokeCLI(t, args...)
		if code != 2 || result.Status != "error" || result.Error == "" || diagnostic == "" {
			t.Fatalf("bad flags: args=%v code=%d result=%+v stderr=%s", args, code, result, diagnostic)
		}
	}
	_, artifact := capturedCLIArtifact(t)
	for _, budget := range []string{"0", "-1"} {
		output := filepath.Join(t.TempDir(), "unused.json")
		result, code, diagnostic := invokeCLI(t, "minimize", "--artifact", artifact, "--output", output, "--max-trials", budget)
		if code != 2 || result.Status != "error" || result.Artifact != "" || diagnostic == "" {
			t.Fatalf("bad budget: code=%d result=%+v stderr=%s", code, result, diagnostic)
		}
		if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("bad budget published output: %v", err)
		}
	}
}

func TestPersistArtifactConcurrentPublishDoesNotOverwrite(t *testing.T) {
	directory := t.TempDir()
	destination := filepath.Join(directory, "failure.json")
	results := make(chan error, 2)
	for _, revision := range []string{"first", "second"} {
		go func() {
			results <- persistArtifact(destination, backend.ReplayArtifact{
				SchemaVersion: 1, Revision: revision, History: json.RawMessage("[]"),
			})
		}()
	}
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("got %d successful publishers, want exactly one", successes)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	var artifact backend.ReplayArtifact
	if err := json.Unmarshal(data, &artifact); err != nil || (artifact.Revision != "first" && artifact.Revision != "second") {
		t.Fatalf("winning artifact was corrupted: %s error=%v", data, err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != "failure.json" {
		t.Fatalf("publication left temporary files: %v error=%v", entries, err)
	}
}
