package backend

import "testing"

func TestPersistentOracleTrialsDoNotContaminateCommittedState(t *testing.T) {
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	store.appendLog(1, 0, []CommittedWrite{{Key: "x", ID: 1}, {Key: "y", ID: 2}}, nil)
	readX := store.readEvent(LoggedRead{Key: "x", WriteID: 1})
	readY := store.readEvent(LoggedRead{Key: "y", WriteID: 2})
	initialY := store.readEvent(LoggedRead{Key: "y", Initial: true})
	if !store.checkTransaction(Causal, 2, []historyEvent{readX}, false) {
		t.Fatal("reading the initialized x must be permitted")
	}
	if store.checkTransaction(Causal, 2, []historyEvent{readX, initialY}, false) {
		t.Fatal("a fractured read of one atomic initialization must be rejected")
	}
	if !store.checkTransaction(Causal, 2, []historyEvent{initialY}, false) {
		t.Fatal("rejected trials must not retain the earlier x observation")
	}
	if store.checkTransaction(Causal, 2, []historyEvent{readX, initialY}, true) {
		t.Fatal("the same fractured transaction must not commit")
	}
	if !store.checkTransaction(Causal, 2, []historyEvent{readX, readY}, true) {
		t.Fatal("a rejected commit must not consume or poison the session")
	}
	store.appendLog(2, 1, nil, []LoggedRead{{Key: "x", WriteID: 1}, {Key: "y", WriteID: 2}})
	if store.checkTransaction(Causal, 2, []historyEvent{initialY}, false) {
		t.Fatal("the accepted commit must constrain subsequent session reads")
	}
	if !store.checkTransaction(Causal, 3, []historyEvent{initialY}, false) {
		t.Fatal("an independent session may still observe the initial state")
	}
}

func TestCellAddressesCannotAlias(t *testing.T) {
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	store.Write("a.b", "c", "d", int64(1))
	store.Write("a", "b.c", "d", int64(2))
	first := store.committedVersions(cellKey("a.b", "c", "d"))
	second := store.committedVersions(cellKey("a", "b.c", "d"))
	if len(first) != 1 || len(second) != 1 || first[0].Value != int64(1) || second[0].Value != int64(2) {
		t.Fatalf("distinct SQL addresses leaked values: first=%v second=%v", first, second)
	}
}

func liveOracleFixture(t *testing.T, iso Isolation) (*Store, *Session) {
	t.Helper()
	store := NewStore(1, 2)
	t.Cleanup(store.Close)
	x := store.Write("items", "1", "x", int64(10))
	y := store.Write("items", "1", "y", int64(20))
	store.appendLog(1, 0, []CommittedWrite{
		{Key: cellKey("items", "1", "x"), ID: x},
		{Key: cellKey("items", "1", "y"), ID: y},
	}, nil)
	session := NewSession(store, iso, 2)
	session.readLatest = true
	t.Cleanup(session.SessionEnd)
	return store, session
}

func assertLiveAdmissionMatchesBatch(t *testing.T, session *Session, key string, version Version, initial bool) {
	t.Helper()
	live := append(liveEvents(session), TxnEvent{IsRead: true, Read: ReadRecord{
		Session: session.sid, Key: key, WriteID: version, Initial: initial, Found: !initial,
	}})
	want := checkLevel(buildSessions(session.store.logSnapshot(), session.sid, live), levelName(session.iso))
	if got := session.admits(key, version, initial); got != want {
		t.Fatalf("candidate (%q, %d, initial=%v): incremental=%v batch=%v", key, version, initial, got, want)
	}
}

func TestLiveOracleSustainedReads(t *testing.T) {
	for _, iso := range []Isolation{ReadCommitted, RepeatableRead, AtomicRead, Causal, Prefix, SnapshotIsolation, Serializable} {
		t.Run(levelName(iso), func(t *testing.T) {
			store, session := liveOracleFixture(t, iso)
			tx := beginSessionTransaction(t, session)
			for i := range 1024 {
				column := "x"
				want := Version(1)
				if i%2 != 0 {
					column, want = "y", 2
				}
				if _, version, ok := session.oracleRead("items", "1", column); !ok || version != want {
					t.Fatalf("read %d failed: version=%d accepted=%v", i, version, ok)
				}
				if i == 0 || i == 31 || i == 255 || i == 1023 {
					key := cellKey("items", "1", column)
					assertLiveAdmissionMatchesBatch(t, session, key, 0, true)
					assertLiveAdmissionMatchesBatch(t, session, key, want, false)
					if session.admits(key, 999, false) {
						t.Fatal("a nonexistent writer must be rejected without poisoning the prefix")
					}
				}
			}
			if err := session.CommitTransaction(nil, tx); err != nil {
				t.Fatalf("sustained accepted reads did not commit: %v", err)
			}
			log := store.logSnapshot()
			if len(log) != 2 || len(log[1].Reads) != 1024 {
				t.Fatal("accepted reads were dropped or trial reads leaked into committed history")
			}
		})
	}
}

func TestLiveOracleResetBoundaries(t *testing.T) {
	for _, iso := range []Isolation{ReadCommitted, RepeatableRead, AtomicRead, Causal, Prefix, SnapshotIsolation, Serializable} {
		for _, boundary := range []string{"rollback", "statement", "engine-reset", "disconnect"} {
			t.Run(levelName(iso)+"/"+boundary, func(t *testing.T) {
				store, session := liveOracleFixture(t, iso)
				z := store.Write("items", "1", "z", int64(30))
				store.appendLog(3, store.nextSeq(), []CommittedWrite{{Key: cellKey("items", "1", "z"), ID: z}}, nil)
				tx := beginSessionTransaction(t, session)
				txn := session.liveTxn()
				if _, version, ok := session.oracleRead("items", "1", "x"); !ok || version != 1 {
					t.Fatal("initial accepted read failed")
				}
				checkpoint := txn.checkpoint()
				if _, version, ok := session.oracleRead("items", "1", "z"); !ok || version != z {
					t.Fatal("statement's accepted read failed")
				}
				switch boundary {
				case "statement":
					txn.restore(checkpoint)
				case "rollback":
					if err := session.Rollback(nil, tx); err != nil {
						t.Fatal(err)
					}
					tx = beginSessionTransaction(t, session)
				case "engine-reset":
					session.SetTransaction(nil)
					tx = beginSessionTransaction(t, session)
				case "disconnect":
					session.SessionEnd()
					tx = beginSessionTransaction(t, session)
				}
				key := cellKey("items", "1", "z")
				assertLiveAdmissionMatchesBatch(t, session, key, 0, true)
				assertLiveAdmissionMatchesBatch(t, session, key, z, false)
				assertLiveAdmissionMatchesBatch(t, session, cellKey("items", "1", "x"), 0, true)
				store.SetSchedule([]int{0})
				if _, version, ok := session.oracleRead("items", "1", "z"); !ok || version != 0 {
					t.Fatal("abandoned read still constrained the restored transaction")
				}
				if err := session.CommitTransaction(nil, tx); err != nil {
					t.Fatalf("valid transaction after %s did not commit: %v", boundary, err)
				}
				log := store.logSnapshot()
				wantReads := 1
				if boundary == "statement" {
					wantReads = 2
				}
				if len(log) != 3 || len(log[2].Reads) != wantReads {
					t.Fatalf("rollback boundary published abandoned reads: %+v", log)
				}
			})
		}
	}
}

func TestLiveOracleAutocommitAndSessionSwitch(t *testing.T) {
	for _, iso := range []Isolation{ReadCommitted, RepeatableRead, AtomicRead, Causal, Prefix, SnapshotIsolation, Serializable} {
		t.Run(levelName(iso), func(t *testing.T) {
			store, first := liveOracleFixture(t, iso)
			if _, version, ok := first.oracleRead("items", "1", "x"); !ok || version != 1 {
				t.Fatal("autocommit read failed")
			}
			other := NewSession(store, iso, 3)
			t.Cleanup(other.SessionEnd)
			store.SetSchedule([]int{0})
			if _, version, ok := other.oracleRead("items", "1", "y"); !ok || version != 0 {
				t.Fatal("another session inherited the prior autocommit read")
			}
			store.SetSchedule(nil)
			assertLiveAdmissionMatchesBatch(t, first, cellKey("items", "1", "y"), 0, true)
			if _, version, ok := first.oracleRead("items", "1", "y"); !ok || version != 2 {
				t.Fatal("newly published autocommit history was not synchronized")
			}
			log := store.logSnapshot()
			if len(log) != 4 || len(log[1].Reads) != 1 || len(log[2].Reads) != 1 || len(log[3].Reads) != 1 {
				t.Fatal("direct helper reads must remain separate one-read transactions")
			}
		})
	}
}

func TestLiveOracleReseedsAfterOtherOracleCalls(t *testing.T) {
	for _, iso := range []Isolation{ReadCommitted, RepeatableRead, AtomicRead, Causal, Prefix, SnapshotIsolation, Serializable} {
		t.Run(levelName(iso), func(t *testing.T) {
			store, session := liveOracleFixture(t, iso)
			tx := beginSessionTransaction(t, session)
			if _, version, ok := session.oracleRead("items", "1", "x"); !ok || version != 1 {
				t.Fatal("accepted live read failed")
			}
			key := cellKey("items", "1", "x")
			other := NewSession(store, iso, 3)
			if !other.admits(key, 0, true) {
				t.Fatal("an independent session inherited the retained transaction")
			}
			assertLiveAdmissionMatchesBatch(t, session, key, 0, true)
			initial := store.readEvent(LoggedRead{Key: key, Initial: true})
			if !store.checkTransaction(iso, 3, []historyEvent{initial}, false) {
				t.Fatal("an independent whole-transaction trial failed")
			}
			assertLiveAdmissionMatchesBatch(t, session, key, 0, true)
			missing := store.readEvent(LoggedRead{Key: key, WriteID: 999})
			if store.checkTransaction(iso, 3, []historyEvent{missing}, true) {
				t.Fatal("a whole transaction with an unknown writer must be rejected")
			}
			assertLiveAdmissionMatchesBatch(t, session, key, 0, true)
			if _, version, ok := session.oracleRead("items", "1", "y"); !ok || version != 2 {
				t.Fatal("valid read failed after accepted-prefix reseeding")
			}
			if err := session.CommitTransaction(nil, tx); err != nil {
				t.Fatal(err)
			}
			if log := store.logSnapshot(); len(log) != 2 || len(log[1].Reads) != 2 {
				t.Fatal("oracle trials changed the retained transaction's published reads")
			}
		})
	}
}
