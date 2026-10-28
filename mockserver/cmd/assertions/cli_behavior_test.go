//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"testing"
)

func TestMainReportsSQLAssertionsWithoutTreatingViolationAsExecutionError(t *testing.T) {
	db, dsn := assertionSQLFixture(t, "CREATE TABLE accounts (custid BIGINT)",
		"CREATE TABLE savings (custid BIGINT, bal VARCHAR(80))", "CREATE TABLE checking (custid BIGINT, bal VARCHAR(80))",
		"INSERT INTO accounts VALUES (1)", "INSERT INTO savings VALUES (1,'2500')", "INSERT INTO checking VALUES (1,'2500')")
	for _, holds := range []bool{true, false} {
		if !holds {
			assertionSQLExec(t, db, "UPDATE checking SET bal='2500.01'")
		}
		func() {
			oldFlags, oldArgs, oldOut := flag.CommandLine, os.Args, os.Stdout
			flag.CommandLine = flag.NewFlagSet("assertions", flag.ContinueOnError)
			os.Args = []string{"assertions", "--dsn", dsn, "--benchmark", "smallbank", "--timeout", "5s"}
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			os.Stdout = writer
			defer func() { flag.CommandLine, os.Args, os.Stdout = oldFlags, oldArgs, oldOut }()
			main()
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Benchmark  string      `json:"benchmark"`
				Assertions []Assertion `json:"assertions"`
				Error      string      `json:"error"`
			}
			decoder := json.NewDecoder(reader)
			if err := decoder.Decode(&result); err != nil {
				t.Fatal(err)
			}
			if result.Benchmark != "smallbank" || result.Error != "" || len(result.Assertions) != 1 || result.Assertions[0] != (Assertion{"A13", holds}) {
				t.Fatalf("CLI response = %+v, want A13 holds=%v without execution error", result, holds)
			}
			if err := decoder.Decode(new(any)); err != io.EOF {
				t.Fatalf("unexpected trailing CLI JSON: %v", err)
			}
		}()
	}
}

func TestMainEncodesExecutionFailureAndFailsExitStatus(t *testing.T) {
	if mode := os.Getenv("DBCOP_ASSERTIONS_MAIN_FAILURE"); mode != "" {
		os.Args = []string{"assertions", "--benchmark", "smallbank"}
		if mode == "encode" {
			file, err := os.Open(os.DevNull)
			if err != nil {
				t.Fatal(err)
			}
			os.Stdout = file // Read-only output must fail encoding, not report success.
		}
		main()
		return
	}
	for _, mode := range []string{"check", "encode"} {
		t.Run(mode, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestMainEncodesExecutionFailureAndFailsExitStatus$")
			command.Env = append(os.Environ(), "DBCOP_ASSERTIONS_MAIN_FAILURE="+mode)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 1 {
				t.Fatalf("CLI status=%v stdout=%s stderr=%s", err, &stdout, &stderr)
			}
			if mode == "check" {
				var result struct {
					Benchmark  string      `json:"benchmark"`
					Assertions []Assertion `json:"assertions"`
					Error      string      `json:"error"`
				}
				// Decode the actual JSON record, allowing Go's coverage trailer after it.
				if err := json.NewDecoder(&stdout).Decode(&result); err != nil {
					t.Fatal(err)
				}
				if result.Benchmark != "smallbank" || result.Assertions != nil || result.Error != "--dsn is required" {
					t.Fatalf("execution failure JSON = %+v", result)
				}
			} else if err := json.NewDecoder(&stdout).Decode(new(any)); err == nil {
				t.Fatalf("failed output encoding unexpectedly produced JSON: %s", &stdout)
			}
		})
	}
}

func TestTPCCRejectsOrphanDomainsAndMissingCustomer(t *testing.T) {
	district := tpccDistrictKey{warehouse: 1, district: 1}
	t.Run("pending district absent", func(t *testing.T) {
		s := tpccTestState(t)
		s.newOrders[tpccOrderKey{district: tpccDistrictKey{warehouse: 9, district: 1}, id: 1}] = struct{}{}
		tpccTestAssertions(t, s, 2, 3, 5, 11)
	})
	t.Run("customer absent", func(t *testing.T) {
		s := tpccTestState(t)
		delete(s.customers, tpccCustomerKey{district: district, id: 1})
		tpccTestAssertions(t, s, 10, 12)
	})
	t.Run("district without orders", func(t *testing.T) {
		s := tpccTestState(t)
		for key := range s.orders {
			if key.district == district {
				delete(s.orders, key)
				delete(s.lines, key)
				delete(s.newOrders, key)
			}
		}
		tpccTestAssertions(t, s, 2, 4, 11)
	})
	t.Run("same sized but wrong warehouse domain", func(t *testing.T) {
		s := tpccTestState(t)
		s.warehouses[9] = s.warehouses[1]
		delete(s.warehouses, 1)
		tpccTestAssertions(t, s, 1, 8)
	})
}
