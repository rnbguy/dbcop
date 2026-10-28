package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestActualStockBenchBaseCoverageRetainsCompletedWork(t *testing.T) {
	client, server, checker := os.Getenv("DBCOP_BENCHBASE_MYSQL_DIR"), os.Getenv("DBCOP_NATIVE_SERVER"), os.Getenv("DBCOP_ASSERTIONS_BIN")
	if client == "" && server == "" && checker == "" {
		t.Skip("requires provisioned stock BenchBase MySQL distribution, native server, and assertion executable")
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
	oldPolicy, oldProbability, oldBudget, oldCoverage := *readPolicy, *deviationProbability, *deviationBudget, *collectCoverage
	*readPolicy, *deviationProbability, *deviationBudget, *collectCoverage = "sparse", 0, 1, true
	defer func() {
		*readPolicy, *deviationProbability, *deviationBudget, *collectCoverage = oldPolicy, oldProbability, oldBudget, oldCoverage
	}()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	config, err := filepath.Abs("smallbank.xml")
	if err != nil {
		t.Fatal(err)
	}
	got := experiment(server, checker, client, java, config, t.TempDir(), "smallbank", "serializable", address, 42, 5*time.Minute)
	if got.Phase != "complete" || got.Error != "" {
		t.Fatalf("stock experiment failed: %+v", got)
	}
	if got.Coverage == nil || got.Measurement == nil {
		t.Fatalf("completed experiment lost its coverage/measurement evidence: %+v", got)
	}
	// Every successful transaction in this fixed, nonempty SmallBank fixture commits SQL work.
	if got.Coverage.Transactions != got.Histograms["Completed Transactions"] || got.Coverage.Transactions <= 0 {
		t.Fatalf("initialization or rolled-back work contaminated committed coverage: coverage=%+v histogram=%v", got.Coverage, got.Histograms)
	}
	if got.Measurement.MeasuredRequests < got.Histograms["Completed Transactions"] {
		t.Fatalf("measurement lost completed client requests: %+v", got.Measurement)
	}
	root, committedSource := false, false
	for _, observation := range got.Coverage.Features {
		root = root || observation.Feature.Initial
		committedSource = committedSource || !observation.Feature.Initial
	}
	if !root || !committedSource {
		t.Fatalf("collector lost root or published-writer provenance: %+v", got.Coverage.Features)
	}
	if len(got.Final.Assertions) != 1 || got.Final.Assertions[0].ID != "A13" {
		t.Fatalf("coverage capture lost the stock assertion outcome: %+v", got.Final)
	}
}
