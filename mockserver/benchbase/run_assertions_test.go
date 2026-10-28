package main

import (
	"strings"
	"testing"
)

func TestWorkloadHistogramsSeparateFailuresFromCompletions(t *testing.T) {
	text := `Completed Transactions:
com.oltpbenchmark.benchmarks.tpcc.procedures.NewOrder/01 [ 12] ***
com.oltpbenchmark.benchmarks.tpcc.procedures.Payment/02 [ 8] **
Aborted Transactions:
<EMPTY>
Rejected Transactions (Server Retry):
com.oltpbenchmark.benchmarks.tpcc.procedures.Payment/02 [ 5] **
Rejected Transactions (Retry Different):
<EMPTY>
Unexpected SQL Errors:
com.oltpbenchmark.benchmarks.tpcc.procedures.NewOrder/01 [ 3] *
Unknown Status Transactions:
<EMPTY>
`
	got, err := parseHistograms(text)
	if err != nil {
		t.Fatal(err)
	}
	for label, want := range map[string]int{"Completed Transactions": 20, "Aborted Transactions": 0, "Rejected Transactions (Server Retry)": 5, "Unexpected SQL Errors": 3} {
		if got[label] != want {
			t.Errorf("%s = %d, want %d", label, got[label], want)
		}
	}
	for _, bad := range []string{
		strings.Replace(text, "Unexpected SQL Errors:", "Missing SQL Errors:", 1),
		strings.Replace(text, "[ 3]", "[ -3]", 1),
		strings.Replace(text, "[ 3]", "[ bad]", 1),
		strings.Replace(text, "<EMPTY>", "", 1),
	} {
		if _, err := parseHistograms(bad); err == nil {
			t.Fatal("incomplete or malformed histogram accepted as a valid experiment")
		}
	}
}
