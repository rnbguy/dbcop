package backend

import (
	"testing"
)

// Every admitted candidate is selectable via schedule: with two committed
// versions of x plus initial, schedule {0,1,2} must select each in turn.
// Proves the cursor reaches every candidate (no permanent stalest-only).
func TestScheduleReachesEveryCandidate(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	mk := func() *Store {
		s := NewStore(1, 2)
		k := cellKey("k", "x", "x")
		s.mu.Lock()
		s.cells[k] = &Cell{Versions: []CellVersion{{ID: 1, Value: 1}, {ID: 2, Value: 2}}}
		s.reg[1] = WriteInfo{Key: k, Session: 1, Seq: 0}
		s.reg[2] = WriteInfo{Key: k, Session: 1, Seq: 1}
		s.log = []LoggedTxn{
			{Session: 1, Seq: 0, Writes: []CommittedWrite{{Key: k, ID: 1}}},
			{Session: 1, Seq: 1, Writes: []CommittedWrite{{Key: k, ID: 2}}},
		}
		s.mu.Unlock()
		return s
	}
	for want := 0; want < 3; want++ {
		s := mk()
		s.SetSchedule([]int{want})
		sess := &Session{store: s, iso: ReadCommitted, sid: 2}
		_, id, ok := sess.oracleRead("k", "x", "x")
		if !ok {
			t.Fatalf("schedule %d: read failed", want)
		}
		adms := s.Admissions()
		if len(adms) != 1 {
			t.Fatalf("schedule %d: want 1 admission, got %d", want, len(adms))
		}
		if adms[0].Admitted != 3 {
			t.Fatalf("schedule %d: want 3 admitted, got %d", want, adms[0].Admitted)
		}
		if want == 0 && !adms[0].Initial {
			t.Fatal("schedule 0 must select initial")
		}
		if want > 0 && (adms[0].Initial || id != Version(want)) {
			t.Fatalf("schedule %d must select version %d, got id=%d initial=%v", want, want, id, adms[0].Initial)
		}
	}
}

// Violating trace requiring non-oldest FIRST choice: C reads y (one
// version) then x=newest(v2) via schedule {2}, skipping initial and v1.
// Stalest-only would pick initial first and never exhibit this shape.
// The pair validates under repeatable-read; the live txn holds both
// reads in execution order.
func TestNonOldestFirstViolation(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	s := NewStore(1, 2)
	kx := cellKey("k", "x", "x")
	ky := cellKey("k", "y", "y")
	s.mu.Lock()
	s.cells[kx] = &Cell{Versions: []CellVersion{{ID: 1, Value: 1}, {ID: 2, Value: 2}}}
	s.cells[ky] = &Cell{Versions: []CellVersion{{ID: 3, Value: 3}}}
	s.reg[1] = WriteInfo{Key: kx, Session: 1, Seq: 0}
	s.reg[2] = WriteInfo{Key: kx, Session: 1, Seq: 1}
	s.reg[3] = WriteInfo{Key: ky, Session: 2, Seq: 2}
	s.log = []LoggedTxn{
		{Session: 1, Seq: 0, Writes: []CommittedWrite{{Key: kx, ID: 1}}},
		{Session: 1, Seq: 1, Writes: []CommittedWrite{{Key: kx, ID: 2}}},
		{Session: 2, Seq: 2, Writes: []CommittedWrite{{Key: ky, ID: 3}}, Reads: []LoggedRead{{Key: kx, WriteID: 1}}},
	}
	s.mu.Unlock()
	s.SetSchedule([]int{2})
	sess := &Session{store: s, iso: RepeatableRead, sid: 3}
	sess.txnMu.Lock()
	sess.txn = &Txn{}
	sess.txnMu.Unlock()
	_, yID, ok := sess.oracleRead("k", "y", "y")
	if !ok || yID != 3 {
		t.Fatalf("y read must select v3, got id=%d ok=%v", yID, ok)
	}
	_, xID, ok := sess.oracleRead("k", "x", "x")
	if !ok {
		t.Fatal("x read failed")
	}
	if xID != 2 {
		t.Fatalf("schedule {2} must select x newest v2, got %d", xID)
	}
	evs := sess.liveTxn().eventsSnapshot()
	if len(evs) != 2 || !evs[0].IsRead || !evs[1].IsRead {
		t.Fatalf("live txn must hold 2 reads in order, got %+v", evs)
	}
}

// Identical-version reread in one txn is valid at every level: the
// committed-read checker must not emit a writer-to-itself edge (vacuous
// per the RC axiom). Regression for the self-loop cycle bug.
func TestIdenticalRereadAdmitted(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	for _, iso := range []Isolation{ReadCommitted, RepeatableRead, Causal} {
		s := NewStore(1, 2)
		k := cellKey("k", "x", "x")
		s.mu.Lock()
		s.cells[k] = &Cell{Versions: []CellVersion{{ID: 1, Value: 1}, {ID: 2, Value: 2}}}
		s.reg[1] = WriteInfo{Key: k, Session: 1, Seq: 0}
		s.reg[2] = WriteInfo{Key: k, Session: 1, Seq: 1}
		s.log = []LoggedTxn{
			{Session: 1, Seq: 0, Writes: []CommittedWrite{{Key: k, ID: 1}}},
			{Session: 1, Seq: 1, Writes: []CommittedWrite{{Key: k, ID: 2}}},
		}
		s.mu.Unlock()
		s.SetSchedule([]int{2, 2})
		sess := &Session{store: s, iso: iso, sid: 3}
		sess.txnMu.Lock()
		sess.txn = &Txn{}
		sess.txnMu.Unlock()
		if _, _, ok := sess.oracleRead("k", "x", "x"); !ok {
			t.Fatalf("iso %d: first read failed", iso)
		}
		if _, _, ok := sess.oracleRead("k", "x", "x"); !ok {
			t.Fatalf("iso %d: identical reread rejected", iso)
		}
	}
}
