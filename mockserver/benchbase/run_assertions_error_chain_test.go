package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCommandDeadlineRetainsProcessFailure(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DBCOP_TEST_CHECKER_PROCESS", "timeout")
	log := filepath.Join(t.TempDir(), "deadline.log")
	data, err := command("", log, 2*time.Second, nil, binary, "-test.run=^TestAssertionCheckerProcess$")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost deadline: %v", err)
	}
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Fatalf("lost subprocess failure after deadline: %v", err)
	}
	persisted, readErr := os.ReadFile(log)
	if readErr != nil || string(persisted) != string(data) {
		t.Fatalf("deadline log = %q, %v; want %q", persisted, readErr, data)
	}
}
