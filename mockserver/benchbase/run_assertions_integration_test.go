package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestActualBenchBaseWorkflowRetainsValidatedOutcomes(t *testing.T) {
	client := os.Getenv("DBCOP_BENCHBASE_MYSQL_DIR")
	server := os.Getenv("DBCOP_NATIVE_SERVER")
	checker := os.Getenv("DBCOP_ASSERTIONS_BIN")
	if client == "" && server == "" && checker == "" {
		t.Skip("requires provisioned BenchBase MySQL distribution, native server, and assertion executable")
	}
	if client == "" || server == "" || checker == "" {
		t.Fatal("set DBCOP_BENCHBASE_MYSQL_DIR, DBCOP_NATIVE_SERVER, and DBCOP_ASSERTIONS_BIN together")
	}
	java, err := exec.LookPath("java")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("mysql"); err != nil {
		t.Fatal(err)
	}
	oldPolicy, oldProbability := *readPolicy, *deviationProbability
	*readPolicy, *deviationProbability = "sparse", 0
	defer func() { *readPolicy, *deviationProbability = oldPolicy, oldProbability }()
	for _, tc := range []struct {
		benchmark string
		config    string
		ids       []string
	}{
		{"voter", "voter.xml", []string{"A14"}},
		{"smallbank", "smallbank_no_amalgamate.xml", []string{"A13"}},
		{"wikipedia", "wikipedia.xml", []string{"A15", "A16", "A17"}},
	} {
		t.Run(tc.benchmark, func(t *testing.T) {
			data, err := os.ReadFile(tc.config)
			if err != nil {
				t.Fatal(err)
			}
			config, err := replaceElement(string(data), "terminals", "1")
			if err != nil {
				t.Fatal(err)
			}
			config, err = replaceElement(config, "time", "1")
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			configPath := filepath.Join(root, "input.xml")
			if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			got := experiment(server, checker, client, java, configPath, root, tc.benchmark, "serializable", address, 42, time.Minute)
			t.Logf("real BenchBase benchmark=%s seed=%d phase=%s final=%+v histograms=%v", tc.benchmark, got.Seed, got.Phase, got.Final, got.Histograms)
			if got.Phase != "complete" || got.Error != "" || got.Seed != 42 || got.ReadPolicy != "sparse" || got.DeviationProbability != 0 {
				t.Fatalf("actual benchmark did not finish a validated experiment: %+v; artifacts=%s", got, root)
			}
			for _, checked := range []checkResult{got.Baseline, got.Final} {
				if checked.Benchmark != tc.benchmark || checked.Error != "" || len(checked.Assertions) != len(tc.ids) {
					t.Fatalf("lost baseline/final observations: %+v", checked)
				}
				for i, id := range tc.ids {
					if checked.Assertions[i].ID != id {
						t.Fatalf("assertion identity = %s, want %s", checked.Assertions[i].ID, id)
					}
				}
			}
			for _, initial := range got.Baseline.Assertions {
				if !initial.Holds {
					t.Fatalf("invalid initialized baseline counted as a completed run: %+v", got.Baseline)
				}
			}
			if got.Histograms["Completed Transactions"] <= 0 || got.Histograms["Unexpected SQL Errors"] != 0 || got.Histograms["Unknown Status Transactions"] != 0 {
				t.Fatalf("workload did not execute successful SQL independently of assertion outcomes: %v", got.Histograms)
			}
		})
	}
	for _, tc := range []struct {
		name, phase string
		timeout     time.Duration
	}{
		{"expired_sql", "create", 0},
		{"missing_client", "load", time.Minute},
		{"missing_checker", "baseline", time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			config, err := filepath.Abs("voter.xml")
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			clientPath, checkerPath := client, checker
			if tc.name == "missing_client" {
				clientPath = root
			}
			if tc.name == "missing_checker" {
				checkerPath = filepath.Join(root, "missing-checker")
			}
			got := experiment(server, checkerPath, clientPath, java, config, root, "voter", "serializable", address, 42, tc.timeout)
			if got.Phase != tc.phase || got.Error == "" || got.Baseline.Assertions != nil || got.Final.Assertions != nil || got.Histograms != nil {
				t.Fatalf("failed real workflow manufactured results or lost its phase: %+v", got)
			}
		})
	}
}
