package backend

import (
	"testing"
)

func TestWriteAssignsDistinctIDs(t *testing.T) {
	s := NewStore(1, 2)
	a := s.Write("tab", "p", "c", 6)
	b := s.Write("tab", "p", "c", 6)
	if a == b {
		t.Fatalf("same value writes must get distinct ids: %d == %d", a, b)
	}
	if got := len(s.committedVersions(cellKey("tab", "p", "c"))); got != 2 {
		t.Fatalf("expected 2 versions, got %d", got)
	}
}

func TestReadMissingKey(t *testing.T) {
	s := NewStore(1, 2)
	if got := s.committedVersions(cellKey("tab", "nope", "c")); len(got) != 0 {
		t.Fatal("missing key must read nothing")
	}
}

func TestBuildSessionsPreservesIdentity(t *testing.T) {
	log := []LoggedTxn{
		{Session: 7, Seq: 0, Writes: []CommittedWrite{{Key: "k.x", ID: 1}}},
		{Session: 9, Seq: 1, Writes: []CommittedWrite{{Key: "k.y", ID: 2}}, Reads: []LoggedRead{{Key: "k.x", WriteID: 1}}},
	}
	live := []TxnEvent{{Read: ReadRecord{Session: 9, Key: "k.y", WriteID: 2, Found: true}, IsRead: true}}
	sessions := buildSessions(log, 9, live)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(sessions))
	}
	// Session 9 keeps both its committed txn AND the live read txn.
	if len(sessions[1]) != 2 {
		t.Fatalf("expected live session to have 2 txns, got %d", len(sessions[1]))
	}
	last := sessions[1][1]
	if len(last.Events) != 1 {
		t.Fatalf("live txn must hold the tentative read, got %d events", len(last.Events))
	}
}

func TestCompositePkey(t *testing.T) {
	def := TableDef{Name: "d", PkeyIdxs: []int{0, 1}}
	tbl := NewTable(def, NewStore(1, 2), ReadCommitted)
	if got := tbl.pkeyOf([]interface{}{1, 2}); got != "1#2" {
		t.Fatalf("composite key = %q, want 1#2", got)
	}
	if got := tbl.pkeyOf([]interface{}{1, 3}); got == "1#2" {
		t.Fatal("distinct districts must not collide")
	}
}
