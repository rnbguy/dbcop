package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReplaceElementPreservesSurroundingConfig(t *testing.T) {
	data := `<parameters><url>jdbc:mysql://old/voter?a=1&amp;b=2</url><randomSeed>1</randomSeed><nested><url>unchanged</url></nested></parameters>`
	got, err := replaceElement(data, "url", "jdbc:mysql://127.0.0.1:13307/voter?a=1&amp;b=2")
	want := `<parameters><url>jdbc:mysql://127.0.0.1:13307/voter?a=1&amp;b=2</url><randomSeed>1</randomSeed><nested><url>unchanged</url></nested></parameters>`
	if err != nil || got != want {
		t.Fatalf("config = %q, %v; want %q", got, err, want)
	}
	for _, tc := range []struct {
		data string
		want string
	}{
		{`<parameters/>`, "missing config element randomSeed"},
		{`<randomSeed>1`, "unclosed config element randomSeed"},
	} {
		got, err := replaceElement(tc.data, "randomSeed", "42")
		if got != "" || err == nil || err.Error() != tc.want {
			t.Fatalf("replaceElement(%q) = %q, %v; want %q", tc.data, got, err, tc.want)
		}
	}
}

func TestParseHistogramsRejectsTruncatedAndMalformedRows(t *testing.T) {
	text := "Completed Transactions:\ncom.oltpbenchmark.voter.Vote [ 2 ] **\n" +
		"Aborted Transactions:\n<EMPTY>\n" +
		"Rejected Transactions (Server Retry):\n<EMPTY>\n" +
		"Rejected Transactions (Retry Different):\n<EMPTY>\n" +
		"Unexpected SQL Errors:\n<EMPTY>\n" +
		"Unknown Status Transactions:\ncom.oltpbenchmark.voter.Vote [ 1 ] *\n"
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"missing first section", strings.Replace(text, "Completed Transactions:", "", 1), "missing workload histogram Completed Transactions"},
		{"missing row delimiter", strings.Replace(text, "Vote [ 2 ]", "Vote 2", 1), "malformed Completed Transactions row"},
		{"missing count terminator", strings.Replace(text, "[ 2 ]", "[ 2", 1), "invalid Completed Transactions count"},
		{"overflowing count", strings.Replace(text, "[ 2 ]", "[ 99999999999999999999999999999999999999999 ]", 1), "invalid Completed Transactions count"},
		{"truncated last section", strings.Replace(text, "com.oltpbenchmark.voter.Vote [ 1 ] *", "client terminated", 1), "empty or malformed workload histogram Unknown Status Transactions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseHistograms(tc.text)
			if got != nil || err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("histograms = %v, %v; want no counts and %q", got, err, tc.want)
			}
		})
	}
}

func TestCommandRetainsDeadlineAndLogWriteFailures(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("expired deadline", func(t *testing.T) {
		log := filepath.Join(t.TempDir(), "timeout.log")
		data, err := command("", log, 0, nil, binary, "-test.run=^TestAssertionCheckerProcess$")
		if !errors.Is(err, context.DeadlineExceeded) || len(data) != 0 {
			t.Fatalf("command = %q, %v; want deadline with no output", data, err)
		}
		persisted, err := os.ReadFile(log)
		if err != nil || len(persisted) != 0 {
			t.Fatalf("deadline log = %q, %v", persisted, err)
		}
	})
	t.Run("process failure and unwritable log", func(t *testing.T) {
		t.Setenv("DBCOP_TEST_CHECKER_PROCESS", "error")
		log := filepath.Join(t.TempDir(), "missing", "execute.log")
		data, err := command("", log, 10*time.Second, nil, binary, "-test.run=^TestAssertionCheckerProcess$")
		var exitErr *exec.ExitError
		var pathErr *os.PathError
		if data != nil || !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 || !errors.As(err, &pathErr) || pathErr.Path != log {
			t.Fatalf("command = %q, %v; lost process failure or failed log path", data, err)
		}
	})
}

func TestCheckAssertionsRetainsArtifactAndStartFailures(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("both artifacts fail with structured checker failure", func(t *testing.T) {
		t.Setenv("DBCOP_TEST_CHECKER_PROCESS", "error")
		out := t.TempDir()
		for _, name := range []string{"baseline.json", "baseline.stderr.log"} {
			if err := os.Mkdir(filepath.Join(out, name), 0700); err != nil {
				t.Fatal(err)
			}
		}
		checked, err := checkAssertions(out, "baseline", 10*time.Second, "voter", binary, "-test.run=^TestAssertionCheckerProcess$")
		var exitErr *exec.ExitError
		if checked.Error != "SELECT votes: invalid connection" || !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
			t.Fatalf("checked = %+v, %v; lost structured or process failure", checked, err)
		}
		for _, name := range []string{"baseline.json", "baseline.stderr.log"} {
			if !strings.Contains(err.Error(), filepath.Join(out, name)) {
				t.Fatalf("missing artifact failure %s: %v", name, err)
			}
		}
	})
	t.Run("missing executable", func(t *testing.T) {
		out := t.TempDir()
		missing := filepath.Join(out, "missing-checker")
		checked, err := checkAssertions(out, "assertions", time.Second, "voter", missing)
		var pathErr *os.PathError
		var syntaxErr *json.SyntaxError
		if !reflect.DeepEqual(checked, checkResult{}) || !errors.As(err, &pathErr) || pathErr.Path != missing || !errors.As(err, &syntaxErr) {
			t.Fatalf("checked = %+v, %v; want executable and empty JSON failures", checked, err)
		}
		for _, name := range []string{"assertions.json", "assertions.stderr.log"} {
			data, err := os.ReadFile(filepath.Join(out, name))
			if err != nil || len(data) != 0 {
				t.Fatalf("%s = %q, %v", name, data, err)
			}
		}
	})
	t.Run("Wikipedia incomplete set is not a violation", func(t *testing.T) {
		t.Setenv("DBCOP_TEST_CHECKER_PROCESS", "success")
		checked, err := checkAssertions(t.TempDir(), "baseline", 10*time.Second, "wikipedia", binary, "-test.run=^TestAssertionCheckerProcess$")
		if err == nil || !strings.Contains(err.Error(), "incomplete assertion set") || len(checked.Assertions) != 1 || !checked.Assertions[0].Holds {
			t.Fatalf("checked = %+v, %v; want retained incomplete observation", checked, err)
		}
	})
}

func TestExperimentClassifiesConfigAndStartupFailures(t *testing.T) {
	valid := `<parameters><url>jdbc:mysql://localhost/voter?a=1&amp;b=2</url><username>root</username><password>secret</password><randomSeed>1</randomSeed></parameters>`
	for _, tc := range []struct {
		name   string
		config string
		setup  string
		phase  string
		want   string
	}{
		{"missing config", "", "missing config", "config", "no such file"},
		{"malformed XML", "<parameters>", "", "config", "XML syntax error"},
		{"invalid URL", strings.Replace(valid, "jdbc:mysql://localhost/voter?a=1&amp;b=2", "jdbc:mysql://%zz/voter", 1), "", "config", "invalid URL escape"},
		{"database mismatch", strings.Replace(valid, "/voter?", "/smallbank?", 1), "", "config", "config database must match benchmark"},
		{"missing seed", strings.Replace(valid, "<randomSeed>1</randomSeed>", "", 1), "", "config", "missing config element randomSeed"},
		{"config artifact fails", valid, "config directory", "config", "config.xml"},
		{"server log fails", valid, "log directory", "start", "server.log"},
		{"server executable missing", valid, "", "start", "missing-server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "input.xml")
			if tc.setup != "missing config" {
				if err := os.WriteFile(config, []byte(tc.config), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out := filepath.Join(root, "out")
			if err := os.Mkdir(out, 0700); err != nil {
				t.Fatal(err)
			}
			collision := ""
			if tc.setup == "config directory" {
				collision = "config.xml"
			} else if tc.setup == "log directory" {
				collision = "server.log"
			}
			if collision != "" {
				if err := os.Mkdir(filepath.Join(out, collision), 0700); err != nil {
					t.Fatal(err)
				}
			}
			got := experiment(filepath.Join(root, "missing-server"), "", "", "", config, out, "voter", "serializable", "127.0.0.1:13307", 42, time.Second)
			if got.Phase != tc.phase || !strings.Contains(got.Error, tc.want) || got.Benchmark != "voter" || got.Level != "serializable" || got.Seed != 42 {
				t.Fatalf("outcome = %+v; want %s failure containing %q", got, tc.phase, tc.want)
			}
			if got.Baseline.Assertions != nil || got.Final.Assertions != nil || got.Histograms != nil {
				t.Fatalf("incomplete experiment manufactured observations: %+v", got)
			}
			if tc.phase == "start" {
				data, err := os.ReadFile(filepath.Join(out, "config.xml"))
				if err != nil || !strings.Contains(string(data), "127.0.0.1:13307/voter?a=1&amp;b=2") || !strings.Contains(string(data), "<randomSeed>42</randomSeed>") {
					t.Fatalf("rewritten config = %q, %v", data, err)
				}
			}
		})
	}
}

func TestAssertionRunnerMainProcess(t *testing.T) {
	if os.Getenv("DBCOP_TEST_ASSERTION_RUNNER_MAIN") != "1" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
			break
		}
	}
	flag.CommandLine = flag.NewFlagSet("assertion-runner", flag.ExitOnError)
	flag.CommandLine.StringVar(readPolicy, "read-policy", "uniform", "")
	flag.CommandLine.Float64Var(deviationProbability, "deviation-probability", 0.01, "")
	main()
	os.Exit(0)
}

func TestMainRejectsInvalidRunsAndPersistsIncompleteOutcomes(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		mode string
		want string
	}{
		{"required options", "required", "required: --server"},
		{"invalid policy", "policy", "read-policy must be uniform or sparse"},
		{"invalid probability", "probability", "deviation-probability must be finite"},
		{"existing output refused", "existing", "file exists"},
		{"incomplete experiments are persisted separately", "runs", "read-policy=sparse deviation-probability=0.25"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			out := filepath.Join(root, "out")
			args := []string{"--server", filepath.Join(root, "server"), "--assertions", filepath.Join(root, "checker"), "--client-dir", root, "--config", filepath.Join(root, "missing.xml"), "--output-dir", out, "--benchmark", "voter", "--level", "serializable", "--runs", "2", "--seed", "17", "--read-policy", "sparse", "--deviation-probability", "0.25"}
			switch tc.mode {
			case "required":
				args = nil
			case "policy":
				args = append(args, "--read-policy", "latest")
			case "probability":
				args = append(args, "--deviation-probability", "NaN")
			case "existing":
				if err := os.Mkdir(out, 0700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, append([]string{"-test.run=^TestAssertionRunnerMainProcess$", "--"}, args...)...)
			cmd.Env = append(os.Environ(), "DBCOP_TEST_ASSERTION_RUNNER_MAIN=1")
			var stderr strings.Builder
			cmd.Stderr = &stderr
			data, err := cmd.Output()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("runner stdout = %q, stderr = %q, error = %v", data, stderr.String(), err)
			}
			if tc.mode != "runs" {
				if len(data) != 0 {
					t.Fatalf("invalid invocation emitted outcomes: %q", data)
				}
				return
			}
			decoder := json.NewDecoder(strings.NewReader(string(data)))
			for i, name := range []string{"run-001-seed-17", "run-002-seed-18"} {
				var got outcome
				if err := decoder.Decode(&got); err != nil {
					t.Fatal(err)
				}
				if got.Phase != "config" || !strings.Contains(got.Error, "missing.xml") || got.Seed != uint64(17+i) || got.Benchmark != "voter" || got.Level != "serializable" || got.ReadPolicy != "sparse" || got.DeviationProbability != 0.25 {
					t.Fatalf("incomplete outcome = %+v", got)
				}
				persisted, err := os.ReadFile(filepath.Join(out, name, "outcome.json"))
				if err != nil {
					t.Fatal(err)
				}
				var saved outcome
				if err := json.Unmarshal(persisted, &saved); err != nil || !reflect.DeepEqual(saved, got) {
					t.Fatalf("saved outcome = %s, %v; want %+v", persisted, err, got)
				}
			}
			if err := decoder.Decode(new(outcome)); err != io.EOF {
				t.Fatalf("extra outcome or malformed trailing output: %v", err)
			}
		})
	}
}

func TestExperimentReportsPrematureServerExit(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config := filepath.Join(root, "input.xml")
	if err := os.WriteFile(config, []byte(`<parameters><url>jdbc:mysql://localhost/voter</url><randomSeed>1</randomSeed></parameters>`), 0600); err != nil {
		t.Fatal(err)
	}
	// The test executable rejects server flags rather than pretending to serve SQL.
	got := experiment(binary, "", "", "", config, root, "voter", "serializable", "127.0.0.1:0", 9, time.Second)
	if got.Phase != "start" || !strings.Contains(got.Error, "server exited: exit status 2") {
		t.Fatalf("premature exit outcome = %+v", got)
	}
	log, err := os.ReadFile(filepath.Join(root, "server.log"))
	if err != nil || !strings.Contains(string(log), "flag provided but not defined: -addr") {
		t.Fatalf("server diagnostics = %q, %v", log, err)
	}
	if got.Baseline.Assertions != nil || got.Final.Assertions != nil {
		t.Fatalf("premature server exit produced assertion observations: %+v", got)
	}
}
