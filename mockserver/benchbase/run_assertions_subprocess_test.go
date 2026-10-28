package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const checkerDiagnostic = "[mysql] packets.go:58 unexpected EOF\n"
const checkerSuccess = `{"benchmark":"voter","assertions":[{"id":"A14","holds":true}]}`
const checkerFailure = `{"benchmark":"voter","assertions":[],"error":"SELECT votes: invalid connection"}`

func TestAssertionCheckerProcess(t *testing.T) {
	mode := os.Getenv("DBCOP_TEST_CHECKER_PROCESS")
	if mode == "" {
		return
	}
	fmt.Fprint(os.Stderr, checkerDiagnostic)
	switch mode {
	case "success":
		fmt.Fprint(os.Stdout, checkerSuccess)
	case "error", "timeout":
		fmt.Fprint(os.Stdout, checkerFailure)
		if mode == "timeout" {
			time.Sleep(time.Minute)
		}
		os.Exit(7)
	case "malformed":
		fmt.Fprint(os.Stdout, "not JSON")
		os.Exit(7)
	default:
		os.Exit(8)
	}
	os.Exit(0)
}

func TestCheckAssertionsSubprocessProtocol(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		mode      string
		benchmark string
		stdout    string
		wantError string
		exitError bool
		timeout   bool
	}{
		{name: "diagnostics do not corrupt JSON", mode: "success", benchmark: "voter", stdout: checkerSuccess},
		{name: "structured error and process failure survive", mode: "error", benchmark: "voter", stdout: checkerFailure, wantError: "SELECT votes: invalid connection", exitError: true},
		{name: "malformed JSON and process failure survive", mode: "malformed", benchmark: "voter", stdout: "not JSON", wantError: "invalid character", exitError: true},
		{name: "incomplete assertions still fail", mode: "success", benchmark: "tpcc", stdout: checkerSuccess, wantError: "incomplete assertion set"},
		{name: "timeout and structured error survive", mode: "timeout", benchmark: "voter", stdout: checkerFailure, wantError: "SELECT votes: invalid connection", timeout: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DBCOP_TEST_CHECKER_PROCESS", tc.mode)
			out := t.TempDir()
			timeout := 10 * time.Second
			if tc.timeout {
				timeout = 2 * time.Second
			}
			checked, err := checkAssertions(out, "assertions", timeout, tc.benchmark, binary, "-test.run=^TestAssertionCheckerProcess$")
			if tc.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				if len(checked.Assertions) != 1 || !checked.Assertions[0].Holds {
					t.Fatalf("lost checker result: %+v", checked)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if tc.mode == "error" || tc.timeout {
				if checked.Error != "SELECT votes: invalid connection" {
					t.Fatalf("lost structured checker error: %+v", checked)
				}
			}
			if tc.exitError {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
					t.Fatalf("lost process error: %v", err)
				}
			}
			if tc.timeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost timeout: %v", err)
			}
			for name, want := range map[string]string{"assertions.json": tc.stdout, "assertions.stderr.log": checkerDiagnostic} {
				data, readErr := os.ReadFile(filepath.Join(out, name))
				if readErr != nil || string(data) != want {
					t.Fatalf("%s = %q, %v; want %q", name, data, readErr, want)
				}
			}
		})
	}
}

func TestCommandRetainsCombinedOutput(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBCOP_TEST_CHECKER_PROCESS", "success")
	log := filepath.Join(t.TempDir(), "execute.log")
	data, err := command("", log, 10*time.Second, nil, binary, "-test.run=^TestAssertionCheckerProcess$")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), checkerSuccess) || !strings.Contains(string(data), checkerDiagnostic) {
		t.Fatalf("combined output lost a stream: %q", data)
	}
	persisted, err := os.ReadFile(log)
	if err != nil || string(persisted) != string(data) {
		t.Fatalf("combined log = %q, %v; want %q", persisted, err, data)
	}
}
