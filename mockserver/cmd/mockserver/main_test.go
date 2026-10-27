package main

import (
	"context"
	"testing"

	"github.com/rnbguy/dbcop/mockserver/backend"
)

func TestParseIsolation(t *testing.T) {
	for name, want := range map[string]backend.Isolation{
		"read-committed":     backend.ReadCommitted,
		"repeatable-read":    backend.RepeatableRead,
		"atomic-read":        backend.AtomicRead,
		"causal":             backend.Causal,
		"prefix":             backend.Prefix,
		"snapshot-isolation": backend.SnapshotIsolation,
		"serializable":       backend.Serializable,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseIsolation(name)
			if err != nil || got != want {
				t.Fatalf("parseIsolation(%q) = %v, %v; want %v", name, got, err, want)
			}
		})
	}
	for _, name := range []string{"", "READ-COMMITTED", "snapshot", "unknown"} {
		if _, err := parseIsolation(name); err == nil {
			t.Errorf("parseIsolation(%q) accepted an unknown level", name)
		}
	}
}

func TestServeRejectsUnknownIsolationBeforeOpeningListener(t *testing.T) {
	if err := serve(context.Background(), "not-an-address", "unknown", 1); err == nil || err.Error() != "unknown isolation level \"unknown\"" {
		t.Fatalf("serve returned %v, want unknown isolation error", err)
	}
}

func TestServeCancelledContextShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serve(ctx, "127.0.0.1:0", "serializable", 1); err != nil {
		t.Fatal(err)
	}
}
