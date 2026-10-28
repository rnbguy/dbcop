package backend

import "testing"

func TestConcurrentWriterMergeRetainsAdmissibleVersions(t *testing.T) {
	for _, iso := range []Isolation{Causal, Prefix} {
		t.Run(levelName(iso), func(t *testing.T) {
			store := NewStore(1, 2)
			t.Cleanup(store.Close)
			x := cellKey("items", "1", "x")
			y := cellKey("items", "1", "y")
			z := cellKey("items", "1", "z")
			x1 := store.Write("items", "1", "x", int64(46))
			y1 := store.Write("items", "1", "y", int64(48))
			x2 := store.Write("items", "1", "x", int64(49))
			z1 := store.Write("items", "1", "z", int64(56))
			// Minimized from stock SmallBank: concurrent writers merge through z.
			store.mu.Lock()
			store.log = []LoggedTxn{
				{Session: 1, Seq: 0, Events: []LoggedEvent{
					{Write: &CommittedWrite{Key: x, ID: x1}},
					{Read: &LoggedRead{Key: y, Initial: true}},
				}},
				{Session: 2, Seq: 1, Events: []LoggedEvent{
					{Write: &CommittedWrite{Key: y, ID: y1}},
					{Read: &LoggedRead{Key: x, Initial: true}},
					{Write: &CommittedWrite{Key: x, ID: x2}},
				}},
				{Session: 2, Seq: 2, Events: []LoggedEvent{
					{Write: &CommittedWrite{Key: z, ID: z1}},
				}},
			}
			store.mu.Unlock()
			session := NewSession(store, iso, 1)
			t.Cleanup(session.SessionEnd)
			session.readLatest = true
			tx := beginSessionTransaction(t, session)
			if _, version, ok := session.oracleRead("items", "1", "z"); !ok || version != z1 {
				t.Fatal("the merging read must be admissible")
			}
			assertLiveAdmissionMatchesBatch(t, session, x, 0, true)
			if session.admits(x, 0, true) {
				t.Fatal("a merged observer must not miss both causal x writers")
			}
			for _, version := range []Version{x1, x2} {
				assertLiveAdmissionMatchesBatch(t, session, x, version, false)
				if !session.admits(x, version, false) {
					t.Fatalf("concurrent writer %d must remain admissible after rejected root trial", version)
				}
			}
			if value, version, ok := session.oracleRead("items", "1", "x"); !ok || version != x2 || value != int64(49) {
				t.Fatalf("valid prefix lost every x version: value=%v version=%d accepted=%v", value, version, ok)
			}
			if err := session.CommitTransaction(nil, tx); err != nil {
				t.Fatalf("valid merged transaction failed commit: %v", err)
			}
		})
	}
}
