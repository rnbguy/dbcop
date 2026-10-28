package main

import (
	"math/big"
	"testing"
)

func appTestBalances(t *testing.T, values map[uint64]string) map[uint64]*big.Rat {
	t.Helper()
	balances := make(map[uint64]*big.Rat, len(values))
	for id, raw := range values {
		value, err := appParseBalance(raw)
		if err != nil {
			t.Fatal(err)
		}
		balances[id] = value
	}
	return balances
}

func TestAppSmallBankConservation(t *testing.T) {
	accounts := map[uint64]uint64{1: 1, 2: 1}
	for _, test := range []struct {
		name     string
		savings  map[uint64]string
		checking map[uint64]string
		want     bool
	}{
		{"normalized fixture", map[uint64]string{1: "2500", 2: "2500"}, map[uint64]string{1: "2500", 2: "2500"}, true},
		{"transfer between customers", map[uint64]string{1: "2500", 2: "2500"}, map[uint64]string{1: "2495", 2: "2505"}, true},
		{"amalgamation", map[uint64]string{1: "0", 2: "2500"}, map[uint64]string{1: "0", 2: "7500"}, true},
		{"exact fractional sum", map[uint64]string{1: "0.1", 2: "2499.9"}, map[uint64]string{1: "2500", 2: "5000"}, true},
		{"lost transfer debit", map[uint64]string{1: "2500", 2: "2500"}, map[uint64]string{1: "2500", 2: "2505"}, false},
		{"small exact deficit", map[uint64]string{1: "2499.999999999999999999", 2: "2500"}, map[uint64]string{1: "2500", 2: "2500"}, false},
		{"missing customer", map[uint64]string{1: "5000"}, map[uint64]string{1: "2500", 2: "2500"}, false},
		{"wrong customer equal total", map[uint64]string{1: "2500", 3: "2500"}, map[uint64]string{1: "2500", 2: "2500"}, false},
		{"extra customer", map[uint64]string{1: "2500", 2: "2500", 3: "0"}, map[uint64]string{1: "2500", 2: "2500"}, false},
		{"unadjusted upstream loader", map[uint64]string{1: "20000", 2: "30000"}, map[uint64]string{1: "15000", 2: "25000"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := appSmallBankAssertion(accounts, appTestBalances(t, test.savings), appTestBalances(t, test.checking))
			if err != nil || got.ID != "A13" || got.Holds != test.want {
				t.Fatalf("A13 = %+v, %v; want holds=%v", got, err, test.want)
			}
		})
	}
	if _, err := appSmallBankAssertion(nil, nil, nil); err == nil {
		t.Fatal("empty account fixture accepted")
	}
	if _, err := appSmallBankAssertion(map[uint64]uint64{1: 2}, nil, nil); err == nil {
		t.Fatal("duplicate account fixture accepted")
	}
}

func TestAppBalanceParsing(t *testing.T) {
	for raw, want := range map[string]string{"2500": "2500", "2.5e3": "2500", "-0.1": "-1/10", "+1.25": "5/4"} {
		got, err := appParseBalance(raw)
		if err != nil || got.RatString() != want {
			t.Errorf("parse balance %q = %v, %v; want %s", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "NULL", "NaN", "Inf", "-Infinity", "1/2", " 2500", "1_000", "0x1p2", "1e999", "broken"} {
		if _, err := appParseBalance(raw); err == nil {
			t.Errorf("invalid balance %q accepted", raw)
		}
	}
}

func TestAppVoterMaintainedClientCap(t *testing.T) {
	for _, test := range []struct {
		name  string
		votes map[uint64]uint64
		want  bool
	}{
		{"fresh fixture", nil, true},
		{"paper cap is not maintained cap", map[uint64]uint64{2125551234: 3}, true},
		{"at maintained client cap", map[uint64]uint64{2125551234: 1000, 4155551234: 999}, true},
		{"one concurrent excess vote", map[uint64]uint64{2125551234: 1001}, false},
		{"violation on another phone", map[uint64]uint64{2125551234: 1, 4155551234: 1001}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := appVoteCapHolds(test.votes); got != test.want {
				t.Fatalf("A14 holds=%v; want %v", got, test.want)
			}
		})
	}
}

func TestAppWikipediaAgreement(t *testing.T) {
	for _, test := range []struct {
		name   string
		users  map[uint64]uint64
		recent map[uint64]uint64
		logs   map[uint64]uint64
		want   [3]bool
	}{
		{"normalized fresh fixture", map[uint64]uint64{0: 0, 1: 0}, nil, nil, [3]bool{true, true, true}},
		{"committed page edits", map[uint64]uint64{0: 0, 1: 2, 2: 1, 3: 0}, map[uint64]uint64{1: 2, 2: 1}, map[uint64]uint64{1: 2, 2: 1}, [3]bool{true, true, true}},
		{"lost editcount increment", map[uint64]uint64{1: 1}, map[uint64]uint64{1: 2}, map[uint64]uint64{1: 2}, [3]bool{false, false, true}},
		{"lost recentchanges row", map[uint64]uint64{1: 2}, map[uint64]uint64{1: 1}, map[uint64]uint64{1: 2}, [3]bool{false, true, false}},
		{"lost logging row", map[uint64]uint64{1: 2}, map[uint64]uint64{1: 2}, map[uint64]uint64{1: 1}, [3]bool{true, false, false}},
		{"orphan histories", map[uint64]uint64{1: 0}, map[uint64]uint64{2: 1}, map[uint64]uint64{2: 1}, [3]bool{false, false, true}},
		{"disjoint keys equal totals", map[uint64]uint64{1: 1}, map[uint64]uint64{2: 1}, map[uint64]uint64{3: 1}, [3]bool{false, false, false}},
		{"extra history key", map[uint64]uint64{1: 1}, map[uint64]uint64{1: 1, 2: 1}, map[uint64]uint64{1: 1}, [3]bool{false, true, false}},
		{"unadjusted upstream loader", map[uint64]uint64{1: 12}, nil, nil, [3]bool{false, false, true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := appWikipediaAssertions(test.users, test.recent, test.logs)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 3 {
				t.Fatalf("got %d assertions, want 3", len(got))
			}
			for i, id := range []string{"A15", "A16", "A17"} {
				if got[i].ID != id || got[i].Holds != test.want[i] {
					t.Errorf("assertion %d = %+v, want %s holds=%v", i, got[i], id, test.want[i])
				}
			}
		})
	}
	if _, err := appWikipediaAssertions(nil, nil, nil); err == nil {
		t.Fatal("empty user fixture accepted")
	}
}
