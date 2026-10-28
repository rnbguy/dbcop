package backend

import (
	"testing"
)

// UPDATE reproducer: read-old-then-write-new in one txn must keep program
// order in the checker history. A writes x=1; B reads x=1 then UPDATEs
// x=2 (read event BEFORE write event). Under causal the commit must pass:
// the read observes the predecessor, the write overwrites it. A
// writes-before-reads emission would invert this and spuriously reject.
func TestUpdateKeepsProgramOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	s := NewStore(1, 2)
	kx := cellKey("k", "x", "x")
	s.mu.Lock()
	s.cells[kx] = &Cell{Versions: []CellVersion{{ID: 1, Value: 1}}}
	s.reg[1] = WriteInfo{Key: kx, Session: 1, Seq: 0}
	s.nextID = 1
	s.log = []LoggedTxn{
		{Session: 1, Seq: 0,
			Writes: []CommittedWrite{{Key: kx, ID: 1}},
			Events: []LoggedEvent{
				{Write: &CommittedWrite{Key: kx, ID: 1}},
			}},
	}
	s.mu.Unlock()
	// B's live txn: read x=1 then stage write x=2 (UPDATE shape).
	sess := &Session{store: s, iso: Causal, sid: 2}
	sess.txnMu.Lock()
	sess.txn = &Txn{}
	sess.txnMu.Unlock()
	txn := sess.liveTxn()
	txn.logRead(ReadRecord{Session: 2, Key: kx, WriteID: 1, Found: true})
	txn.stage(StagedWrite{Tab: "k", Pkey: "x", Col: "x", Value: 2})
	// Tentative history must show read-before-write.
	live := liveEvents(sess)
	if len(live) != 2 || !live[0].IsRead {
		t.Fatalf("live stream must be [read, write], got %+v", live)
	}
	sessions := buildSessions(s.logSnapshot(), 2, live)
	// The live txn events: read x=1 (external). Staged write excluded
	// (uncommitted). History: A:W(x1), B-live:R(x1). Causal admits.
	if !checkLevel(sessions, "causal") {
		t.Fatal("UPDATE read-old must be admittable under causal")
	}
	// Commit and assert the persisted entry keeps [Read, Write] order
	// with the resulting version.
	sess.txnMu.Lock()
	sess.explicit = true
	sess.txnMu.Unlock()
	if err := sess.CommitTransaction(nil, nil); err != nil {
		t.Fatalf("UPDATE commit rejected: %v", err)
	}
	stored := s.logSnapshot()
	if len(stored) != 2 {
		t.Fatalf("want 2 logged txns, got %d", len(stored))
	}
	got := stored[1]
	if len(got.Events) != 2 || got.Events[0].Read == nil || got.Events[1].Write == nil {
		t.Fatalf("persisted txn must be [Read, Write], got %+v", got.Events)
	}
	if got.Events[0].Read.WriteID != 1 {
		t.Fatalf("persisted read must reference v1, got %d", got.Events[0].Read.WriteID)
	}
	if got.Events[1].Write.ID != 2 {
		t.Fatalf("persisted write must carry version 2, got %d", got.Events[1].Write.ID)
	}
	vers := s.committedVersions(kx)
	if len(vers) != 2 || vers[1].ID != 2 {
		t.Fatalf("cell chain must append v2, got %+v", vers)
	}
}
