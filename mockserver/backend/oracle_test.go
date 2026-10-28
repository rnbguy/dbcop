package backend

import (
	"testing"
)

// Cross-key causal chain: A writes x=1; B reads x=1 then writes y=1;
// C reads y=1 then must NOT read x=0 under causal (stale x violates
// (wr+so)+). Under read-committed the stale read is allowed.
// This is the regression gate for the formal oracle: a per-cell
// approximation would admit x=0 under causal; the real checker rejects it.
func TestCrossKeyCausalChain(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so built at ../../../target/debug")
	}
	mkLog := func() []LoggedTxn {
		return []LoggedTxn{
			{Session: 1, Seq: 0, Writes: []CommittedWrite{{Key: "k.x", ID: 1}}},
			{Session: 2, Seq: 1, Writes: []CommittedWrite{{Key: "k.y", ID: 2}}, Reads: []LoggedRead{{Key: "k.x", WriteID: 1}}},
		}
	}
	live := []TxnEvent{
		{Read: ReadRecord{Session: 3, Key: "k.y", WriteID: 2, Found: true}, IsRead: true},
		{Read: ReadRecord{Session: 3, Key: "k.x", Initial: true}, IsRead: true},
	}
	sessions := buildSessions(mkLog(), 3, live)
	if checkLevel(sessions, "causal") {
		t.Fatal("causal must reject the transitive cross-key stale read")
	}
	if !checkLevel(sessions, "committed-read") {
		t.Fatal("read-committed must allow the same stale read")
	}
}
