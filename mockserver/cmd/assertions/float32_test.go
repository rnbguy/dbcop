package main

import "testing"

func TestTPCCMySQLFloatTransport(t *testing.T) {
	var amount tpccDecimal
	if err := amount.Scan(float32(0.1)); err != nil {
		t.Fatal(err)
	}
	if got := amount.value.RatString(); got != "1/10" {
		t.Fatalf("SQL FLOAT 0.1 = %s, want 1/10", got)
	}
}
