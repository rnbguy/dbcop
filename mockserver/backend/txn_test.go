package backend

import (
	"errors"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func beginSessionTransaction(t *testing.T, sess *Session) sql.Transaction {
	t.Helper()
	tx, err := sess.StartTransaction(nil, sql.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	sess.SetTransaction(tx)
	return tx
}

func assertSessionTransactionEnded(t *testing.T, sess *Session) {
	t.Helper()
	if sess.liveTxn() != nil || sess.GetTransaction() != nil || sess.GetIgnoreAutoCommit() || sess.inExplicitTxn() {
		t.Fatal("backend and engine transaction state must both be cleared")
	}
	if !sess.store.commitMu.TryLock() {
		t.Fatal("transaction execution lock was not released")
	}
	sess.store.commitMu.Unlock()
}

func TestEmptyTransactionCommitDoesNotPublish(t *testing.T) {
	store := NewStore(1, 2)
	sess := NewSession(store, SnapshotIsolation, 1)
	t.Cleanup(sess.SessionEnd)
	tx := beginSessionTransaction(t, sess)
	sess.SetIgnoreAutoCommit(true)
	if err := sess.CommitTransaction(nil, tx); err != nil {
		t.Fatal(err)
	}
	assertSessionTransactionEnded(t, sess)
	if len(store.logSnapshot()) != 0 || store.seq != 0 || store.nextID != 0 {
		t.Fatal("empty engine transactions must not publish history or consume sequence/version IDs")
	}
}

func TestEngineTransactionResetAbortsPendingWork(t *testing.T) {
	store := NewStore(1, 2)
	sess := NewSession(store, SnapshotIsolation, 1)
	t.Cleanup(sess.SessionEnd)
	tx := beginSessionTransaction(t, sess)
	txn := sess.liveTxn()
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 10})
	txn.stageMembership("items", "1", true)
	txn.logRead(ReadRecord{Session: 1, Key: cellKey("items", "2", "value"), Initial: true})
	sess.SetIgnoreAutoCommit(true)

	// The engine clears the handle after an implicit statement build error.
	sess.SetTransaction(nil)
	assertSessionTransactionEnded(t, sess)
	if len(store.logSnapshot()) != 0 || len(store.cells) != 0 || store.seq != 0 || store.nextID != 0 {
		t.Fatal("reset must not publish abandoned writes or reads")
	}
	if err := sess.CommitTransaction(nil, tx); err != nil {
		t.Fatal(err)
	}
	beginSessionTransaction(t, sess)
	if len(sess.liveTxn().eventsSnapshot()) != 0 || len(sess.liveTxn().memberSnapshot()) != 0 {
		t.Fatal("the next transaction inherited abandoned work")
	}
}

func TestSessionEndAbortsImplicitAndExplicitTransactions(t *testing.T) {
	for _, test := range []struct {
		name     string
		explicit bool
	}{
		{name: "implicit"},
		{name: "explicit", explicit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := NewStore(1, 2)
			sess := NewSession(store, SnapshotIsolation, 1)
			t.Cleanup(sess.SessionEnd)
			if err := sql.SessionCommandBegin(sess); err != nil {
				t.Fatal(err)
			}
			beginSessionTransaction(t, sess)
			if !sess.inExplicitTxn() {
				t.Fatal("both kinds of engine transaction must own writer completion")
			}
			sess.SetIgnoreAutoCommit(test.explicit)
			txn := sess.liveTxn()
			txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 10})
			txn.logRead(ReadRecord{Session: 1, Key: cellKey("items", "2", "value"), Initial: true})
			sql.SessionCommandEnd(sess)
			if sess.liveTxn() != txn {
				t.Fatal("command completion must leave engine-managed boundaries alone")
			}
			sql.SessionEnd(sess)
			assertSessionTransactionEnded(t, sess)
			if len(store.logSnapshot()) != 0 || len(store.cells) != 0 {
				t.Fatal("disconnect/reset must not publish abandoned work")
			}
		})
	}
}

func TestRepeatedResetDoesNotUnlockAnotherSession(t *testing.T) {
	store := NewStore(1, 2)
	first := NewSession(store, SnapshotIsolation, 1)
	t.Cleanup(first.SessionEnd)
	tx := beginSessionTransaction(t, first)
	if err := first.CommitTransaction(nil, tx); err != nil {
		t.Fatal(err)
	}
	second := NewSession(store, SnapshotIsolation, 2)
	t.Cleanup(second.SessionEnd)
	beginSessionTransaction(t, second)
	first.SetTransaction(nil)
	sql.SessionEnd(first)
	if store.commitMu.TryLock() {
		t.Fatal("repeated cleanup released a different session's transaction lock")
	}
}

func TestNonemptyTransactionsStillPublish(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	for _, kind := range []string{"read", "write", "membership"} {
		t.Run(kind, func(t *testing.T) {
			store := NewStore(1, 2)
			sess := NewSession(store, SnapshotIsolation, 1)
			t.Cleanup(sess.SessionEnd)
			tx := beginSessionTransaction(t, sess)
			txn := sess.liveTxn()
			switch kind {
			case "read":
				txn.logRead(ReadRecord{Session: 1, Key: cellKey("items", "1", "value"), Initial: true})
			case "write":
				txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 10})
			case "membership":
				txn.stageMembership("items", "1", true)
			}
			if err := sess.CommitTransaction(nil, tx); err != nil {
				t.Fatal(err)
			}
			assertSessionTransactionEnded(t, sess)
			logged := store.logSnapshot()
			if len(logged) != 1 || logged[0].Session != 1 || store.seq != 1 {
				t.Fatalf("nonempty transaction was omitted: %+v", logged)
			}
			if kind == "read" && (len(logged[0].Reads) != 1 || len(logged[0].Events) != 1) {
				t.Fatal("read-only transaction lost its recorded read")
			}
			if kind == "write" && (len(logged[0].Writes) != 1 || len(store.committedVersions(cellKey("items", "1", "value"))) != 1) {
				t.Fatal("write-only transaction lost its committed write")
			}
		})
	}
}

func TestRejectedReadOnlyCommitClearsEngineState(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	store := NewStore(1, 2)
	sess := NewSession(store, SnapshotIsolation, 1)
	t.Cleanup(sess.SessionEnd)
	tx := beginSessionTransaction(t, sess)
	sess.SetIgnoreAutoCommit(true)
	sess.liveTxn().logRead(ReadRecord{Session: 1, Key: cellKey("items", "1", "value"), WriteID: 999, Found: true})
	if err := sess.CommitTransaction(nil, tx); !errors.Is(err, errCommitRejected) {
		t.Fatalf("read-only commit must still check the history, got %v", err)
	}
	assertSessionTransactionEnded(t, sess)
	if len(store.logSnapshot()) != 0 || len(store.cells) != 0 {
		t.Fatal("rejected transaction must not be published")
	}
}

func TestStatementCheckpointRestoresAllStagedState(t *testing.T) {
	txn := &Txn{}
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 10})
	txn.stageMembership("items", "1", true)
	txn.logRead(ReadRecord{Session: 1, Key: cellKey("items", "2", "value"), Initial: true})
	before := txn.checkpoint()
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 20})
	txn.stageMembership("items", "1", false)
	txn.logRead(ReadRecord{Session: 1, Key: cellKey("items", "3", "value"), Initial: true})

	txn.restore(before)
	if got := txn.checkpoint(); got != before {
		t.Fatalf("failed statement left staged state: got %+v, want %+v", got, before)
	}
	if value, ok := txn.staged("items", "1", "value"); !ok || value != 10 {
		t.Fatal("restore lost the preceding statement's cell write")
	}
	if value, ok := txn.staged("items", "1", rowExistenceColumn); !ok || value != true {
		t.Fatal("restore lost the preceding statement's existence write")
	}
	if present, ok := txn.stagedPresent("items", "1"); !ok || !present {
		t.Fatal("restore retained the failed statement's deletion")
	}
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 30})
	if got := txn.eventsSnapshot(); len(got) != before.events+1 || got[len(got)-1].Write.Value != 30 {
		t.Fatal("subsequent writes did not append to the restored event stream")
	}
}

func TestDiscardClearsEntireTransaction(t *testing.T) {
	txn := &Txn{}
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 10})
	txn.stageMembership("items", "1", true)
	txn.logRead(ReadRecord{Session: 1, Key: cellKey("items", "2", "value"), Initial: true})
	txn.discard()
	if got := txn.checkpoint(); got != (txnCheckpoint{}) {
		t.Fatalf("discard retained transaction events: %+v", got)
	}
	txn.stage(StagedWrite{Tab: "items", Pkey: "1", Col: "value", Value: 20})
	if got := txn.eventsSnapshot(); len(got) != 1 || got[0].IsRead || got[0].Write.Value != 20 {
		t.Fatal("discarded writes or reads reappeared in a later commit stream")
	}
}
