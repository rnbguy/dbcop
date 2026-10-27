package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

var namedSavepointLevels = []Isolation{ReadCommitted, RepeatableRead, AtomicRead, Causal, Prefix, SnapshotIsolation, Serializable}

func namedSavepointFixture(t *testing.T, iso Isolation, setup ...string) *sqlTableFixture {
	t.Helper()
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.provider.iso = iso
	f.ctx.Session.(*Session).iso = iso
	t.Cleanup(func() {
		f.ctx.Session.(*Session).SessionEnd()
		f.store.Close()
	})
	if err := f.store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	for _, query := range append([]string{
		"CREATE DATABASE savepoints",
		"USE savepoints",
		"CREATE TABLE items (id INT PRIMARY KEY, v INT UNIQUE)",
		"INSERT INTO items VALUES (1,10),(2,20)",
		"CREATE TABLE probes (id INT PRIMARY KEY, v INT)",
		"INSERT INTO probes VALUES (1,30)",
	}, setup...) {
		f.query(t, query)
	}
	if err := f.store.SealInitialState(); err != nil {
		t.Fatal(err)
	}
	return f
}

func namedSavepointRows(t *testing.T, f *sqlTableFixture, query string, want []sql.Row) {
	t.Helper()
	if got := f.query(t, query); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", query, got, want)
	}
}

func namedSavepointMissing(t *testing.T, f *sqlTableFixture, query string) {
	t.Helper()
	if err := foreignKeyQueryError(f, query); !sql.ErrSavepointDoesNotExist.Is(err) {
		t.Fatalf("%s: expected missing savepoint, got %v", query, err)
	}
}

func namedSavepointPublished(t *testing.T, store *Store, iso Isolation, want []TxnEvent) {
	t.Helper()
	log := store.logSnapshot()
	if len(log) != 1 || len(log[0].Events) != len(want) {
		t.Fatalf("abandoned work changed committed history: log=%+v, want events=%+v", log, want)
	}
	for i, event := range want {
		got := log[0].Events[i]
		if event.IsRead {
			expected := LoggedRead{Key: event.Read.Key, WriteID: event.Read.WriteID, Initial: event.Read.Initial}
			if got.Write != nil || got.Read == nil || *got.Read != expected {
				t.Fatalf("event %d: got %+v, want read %+v", i, got, expected)
			}
		} else if got.Read != nil || got.Write == nil || got.Write.Key != cellKey(event.Write.Tab, event.Write.Pkey, event.Write.Col) {
			t.Fatalf("event %d: got %+v, want write %+v", i, got, event.Write)
		}
	}
	if !checkLevel(buildSessions(log, log[0].Session, nil), levelName(iso)) {
		t.Fatal("committed savepoint history violates the batch oracle")
	}
}

func TestNamedSavepointSQLPrefixRestoration(t *testing.T) {
	for _, iso := range namedSavepointLevels {
		t.Run(levelName(iso), func(t *testing.T) {
			f := namedSavepointFixture(t, iso)
			f.query(t, "BEGIN")
			f.query(t, "UPDATE items SET v=11 WHERE id=1")
			f.query(t, "SAVEPOINT KeEp")
			txn := f.ctx.Session.(*Session).liveTxn()
			prefix := txn.checkpoint()
			events, members := txn.eventsSnapshot(), txn.memberSnapshot()
			for _, query := range []string{
				"UPDATE items SET id=3,v=33 WHERE id=2",
				"INSERT INTO items VALUES (4,44)",
				"DELETE FROM items WHERE id=1",
				"SELECT v FROM probes WHERE id=1",
				"SAVEPOINT later",
				"ROLLBACK TO SAVEPOINT keep",
			} {
				f.query(t, query)
			}
			if txn.checkpoint() != prefix || !reflect.DeepEqual(txn.eventsSnapshot(), events) || !reflect.DeepEqual(txn.memberSnapshot(), members) {
				t.Fatal("rollback-to did not restore the exact staged prefix")
			}
			namedSavepointMissing(t, f, "ROLLBACK TO later")
			f.query(t, "ROLLBACK TO KEEP") // The target survives rollback-to.
			f.query(t, "COMMIT")
			namedSavepointPublished(t, f.store, iso, events)
			if len(txn.savepoints) != 0 {
				t.Fatal("commit retained named checkpoints")
			}
			for _, event := range events {
				if event.IsRead {
					continue
				}
				for _, id := range []string{"3", "4"} {
					for _, column := range []string{rowExistenceColumn, "id", "v"} {
						if versions := f.store.committedVersions(cellKey(event.Write.Tab, id, column)); len(versions) != 0 {
							t.Fatalf("rolled-back key entered the admitted candidate universe: %s/%s: %v", id, column, versions)
						}
					}
				}
				break
			}
			namedSavepointRows(t, f, "SELECT id,v FROM items ORDER BY id", []sql.Row{{int32(1), int32(11)}, {int32(2), int32(20)}})
			for _, id := range []string{"3", "4"} {
				namedSavepointRows(t, f, "SELECT v FROM items WHERE id="+id, nil)
			}
		})
	}
}

func TestNamedSavepointDuplicatePlacementAndRelease(t *testing.T) {
	for _, iso := range namedSavepointLevels {
		t.Run(levelName(iso), func(t *testing.T) {
			f := namedSavepointFixture(t, iso)
			for _, query := range []string{
				"BEGIN",
				"UPDATE items SET v=11 WHERE id=1",
				"SAVEPOINT `First`",
				"UPDATE items SET v=12 WHERE id=1",
				"SAVEPOINT middle",
				"UPDATE items SET v=13 WHERE id=1",
				"SAVEPOINT `fIrSt`",
				"UPDATE items SET v=14 WHERE id=1",
				"SAVEPOINT tail",
				"UPDATE items SET v=15 WHERE id=1",
				"ROLLBACK TO `FIRST`",
			} {
				f.query(t, query)
			}
			namedSavepointRows(t, f, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(13)}})
			namedSavepointMissing(t, f, "RELEASE SAVEPOINT tail")
			f.query(t, "ROLLBACK TO `first`")
			f.query(t, "ROLLBACK TO MIDDLE")
			namedSavepointRows(t, f, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(12)}})
			namedSavepointMissing(t, f, "ROLLBACK TO `first`")
			f.query(t, "SAVEPOINT later")
			f.query(t, "RELEASE SAVEPOINT MiDdLe")
			namedSavepointMissing(t, f, "RELEASE SAVEPOINT middle")
			namedSavepointMissing(t, f, "ROLLBACK TO middle")
			f.query(t, "UPDATE items SET v=16 WHERE id=1")
			f.query(t, "ROLLBACK TO later")
			f.query(t, "RELEASE SAVEPOINT LATER")
			namedSavepointRows(t, f, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(12)}})
			events := f.ctx.Session.(*Session).liveTxn().eventsSnapshot()
			f.query(t, "COMMIT")
			namedSavepointPublished(t, f.store, iso, events)
			namedSavepointRows(t, f, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(12)}})
		})
	}
}

func TestNamedSavepointUnknownAndTransactionBoundaries(t *testing.T) {
	for _, iso := range namedSavepointLevels {
		for _, boundary := range []string{"commit", "rollback", "disconnect", "engine-reset"} {
			t.Run(levelName(iso)+"/"+boundary, func(t *testing.T) {
				f := namedSavepointFixture(t, iso)
				session := f.ctx.Session.(*Session)
				if err := session.CreateSavepoint(f.ctx, nil, "none"); err == nil || err.Error() != "savepoint requires an active transaction" {
					t.Fatalf("savepoint without a transaction: %v", err)
				}
				if err := session.RollbackToSavepoint(f.ctx, nil, "none"); !sql.ErrSavepointDoesNotExist.Is(err) {
					t.Fatalf("rollback-to without a transaction: %v", err)
				}
				if err := session.ReleaseSavepoint(f.ctx, nil, "none"); !sql.ErrSavepointDoesNotExist.Is(err) {
					t.Fatalf("release without a transaction: %v", err)
				}
				f.query(t, "BEGIN")
				namedSavepointMissing(t, f, "ROLLBACK TO `none`")
				namedSavepointMissing(t, f, "RELEASE SAVEPOINT `none`")
				f.query(t, "UPDATE items SET v=11 WHERE id=1")
				f.query(t, "SAVEPOINT abandoned")
				txn := session.liveTxn()
				switch boundary {
				case "commit":
					f.query(t, "COMMIT")
				case "rollback":
					f.query(t, "ROLLBACK")
				case "disconnect":
					session.SessionEnd()
				case "engine-reset":
					session.SetTransaction(nil)
				}
				assertSessionTransactionEnded(t, session)
				if len(txn.savepoints) != 0 {
					t.Fatal("transaction boundary retained named checkpoints")
				}
				wantLogs := 0
				wantValue := int32(10)
				if boundary == "commit" {
					wantLogs, wantValue = 1, 11
				}
				if len(f.store.logSnapshot()) != wantLogs {
					t.Fatal("abandoned transaction published history")
				}
				f.query(t, "BEGIN")
				namedSavepointMissing(t, f, "ROLLBACK TO abandoned")
				namedSavepointMissing(t, f, "RELEASE SAVEPOINT abandoned")
				namedSavepointRows(t, f, "SELECT v FROM items WHERE id=1", []sql.Row{{wantValue}})
				f.query(t, "UPDATE items SET v=12 WHERE id=1")
				f.query(t, "COMMIT")
			})
		}
	}
}

func TestNamedSavepointFailedStatementsRetainEarlierWork(t *testing.T) {
	for _, iso := range namedSavepointLevels {
		for _, constraint := range []string{"unique", "foreign-key"} {
			t.Run(levelName(iso)+"/"+constraint, func(t *testing.T) {
				f := namedSavepointFixture(t, iso,
					"CREATE TABLE parents (id INT PRIMARY KEY)",
					"CREATE TABLE children (id INT PRIMARY KEY, parent_id INT, FOREIGN KEY (parent_id) REFERENCES parents(id))",
					"INSERT INTO parents VALUES (1),(2)")
				f.query(t, "BEGIN")
				f.query(t, "INSERT INTO items VALUES (5,50)")
				f.query(t, "INSERT INTO children VALUES (10,1)")
				f.query(t, "SELECT v FROM probes WHERE id=1")
				f.query(t, "SAVEPOINT valid")
				txn := f.ctx.Session.(*Session).liveTxn()
				prefix, events, members := txn.checkpoint(), txn.eventsSnapshot(), txn.memberSnapshot()
				query := "INSERT INTO items VALUES (3,30),(4,10)"
				if constraint == "foreign-key" {
					query = "INSERT INTO children VALUES (11,2),(12,999)"
				}
				err := foreignKeyQueryError(f, query)
				if constraint == "unique" && !sql.ErrUniqueKeyViolation.Is(err) || constraint == "foreign-key" && !sql.ErrForeignKeyChildViolation.Is(err) {
					t.Fatalf("wrong failed-statement category: %v", err)
				}
				if f.ctx.Session.(*Session).liveTxn() != txn || txn.checkpoint() != prefix || !reflect.DeepEqual(txn.eventsSnapshot(), events) || !reflect.DeepEqual(txn.memberSnapshot(), members) {
					t.Fatal("constraint failure lost earlier work or retained failed statement side effects")
				}
				f.query(t, "ROLLBACK TO valid")
				namedSavepointRows(t, f, "SELECT id,v FROM items ORDER BY id", []sql.Row{{int32(1), int32(10)}, {int32(2), int32(20)}, {int32(5), int32(50)}})
				namedSavepointRows(t, f, "SELECT id,parent_id FROM children ORDER BY id", []sql.Row{{int32(10), int32(1)}})
				for _, query := range []string{"SELECT v FROM items WHERE id=3", "SELECT parent_id FROM children WHERE id=11"} {
					namedSavepointRows(t, f, query, nil)
				}
				events = txn.eventsSnapshot()
				f.query(t, "COMMIT")
				namedSavepointPublished(t, f.store, iso, events)
			})
		}
	}
}

func TestNamedSavepointStatementRestoreInvalidatesLaterNames(t *testing.T) {
	for _, iso := range namedSavepointLevels {
		t.Run(levelName(iso), func(t *testing.T) {
			f := namedSavepointFixture(t, iso)
			f.query(t, "BEGIN")
			f.query(t, "UPDATE items SET v=11 WHERE id=1")
			f.query(t, "SAVEPOINT retained")
			session := f.ctx.Session.(*Session)
			txn := session.liveTxn()
			prefix, events := txn.checkpoint(), txn.eventsSnapshot()
			db, err := f.provider.Database(f.ctx, "savepoints")
			if err != nil {
				t.Fatal(err)
			}
			table, ok, err := db.GetTableInsensitive(f.ctx, "items")
			if err != nil || !ok {
				t.Fatalf("items table: %v", err)
			}
			writer := table.(sql.InsertableTable).Inserter(f.ctx)
			writer.StatementBegin(f.ctx)
			if err := writer.Insert(f.ctx, sql.Row{int32(3), int32(30)}); err != nil {
				t.Fatal(err)
			}
			// A callback inside a statement must not outlive that statement's rollback.
			if err := session.CreateSavepoint(f.ctx, session.GetTransaction(), "inside"); err != nil {
				t.Fatal(err)
			}
			err = writer.Insert(f.ctx, sql.Row{int32(1), int32(10)})
			if !sql.ErrPrimaryKeyViolation.Is(err) {
				t.Fatalf("expected duplicate primary key: %v", err)
			}
			if err := writer.DiscardChanges(f.ctx, err); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(f.ctx); err != nil {
				t.Fatal(err)
			}
			if txn.checkpoint() != prefix || !reflect.DeepEqual(txn.eventsSnapshot(), events) {
				t.Fatal("statement restore did not retain the preceding prefix")
			}
			if err := session.RollbackToSavepoint(f.ctx, session.GetTransaction(), "inside"); !sql.ErrSavepointDoesNotExist.Is(err) {
				t.Fatalf("statement restore retained an out-of-range savepoint: %v", err)
			}
			f.query(t, "ROLLBACK TO retained")
			f.query(t, "COMMIT")
			namedSavepointPublished(t, f.store, iso, events)
		})
	}
}

func TestNamedSavepointRestoresNativeAdmissionPrefix(t *testing.T) {
	for _, iso := range namedSavepointLevels {
		t.Run(levelName(iso), func(t *testing.T) {
			store, session := liveOracleFixture(t, iso)
			z := store.Write("items", "1", "z", int64(30))
			store.appendLog(3, store.nextSeq(), []CommittedWrite{{Key: cellKey("items", "1", "z"), ID: z}}, nil)
			tx := beginSessionTransaction(t, session)
			txn := session.liveTxn()
			if _, version, ok := session.oracleRead("items", "1", "x"); !ok || version != 1 {
				t.Fatal("prefix read failed")
			}
			if err := session.CreateSavepoint(nil, tx, "prefix"); err != nil {
				t.Fatal(err)
			}
			prefix, events := txn.checkpoint(), txn.eventsSnapshot()
			if _, version, ok := session.oracleRead("items", "1", "z"); !ok || version != z {
				t.Fatal("later read failed")
			}
			if err := session.RollbackToSavepoint(nil, tx, "PREFIX"); err != nil {
				t.Fatal(err)
			}
			if txn.checkpoint() != prefix || !reflect.DeepEqual(txn.eventsSnapshot(), events) {
				t.Fatal("rollback lost the retained read prefix")
			}
			for _, key := range []string{cellKey("items", "1", "x"), cellKey("items", "1", "z")} {
				assertLiveAdmissionMatchesBatch(t, session, key, 0, true)
				for _, candidate := range store.committedVersions(key) {
					assertLiveAdmissionMatchesBatch(t, session, key, candidate.ID, false)
				}
			}
			store.SetSchedule([]int{0})
			if _, version, ok := session.oracleRead("items", "1", "z"); !ok || version != 0 {
				t.Fatal("abandoned observation still constrained admission")
			}
			if err := session.CommitTransaction(nil, tx); err != nil {
				t.Fatalf("restored prefix did not commit: %v", err)
			}
			log := store.logSnapshot()
			want := []LoggedRead{{Key: cellKey("items", "1", "x"), WriteID: 1}, {Key: cellKey("items", "1", "z"), Initial: true}}
			if len(log) != 3 || !reflect.DeepEqual(log[2].Reads, want) || len(log[2].Writes) != 0 || len(log[2].Events) != 2 {
				t.Fatalf("abandoned observation leaked into committed history: %+v", log)
			}
		})
	}
}

func TestNamedSavepointKeylessMutationAfterRollback(t *testing.T) {
	for _, iso := range namedSavepointLevels {
		t.Run(levelName(iso), func(t *testing.T) {
			f := namedSavepointFixture(t, iso, "CREATE TABLE bag (v INT)", "INSERT INTO bag VALUES (7),(7)")
			for _, query := range []string{
				"BEGIN", "INSERT INTO bag VALUES (9)", "SAVEPOINT bag_prefix",
				"UPDATE bag SET v=8 WHERE v=7", "DELETE FROM bag WHERE v=9",
				"ROLLBACK TO bag_prefix", "DELETE FROM bag WHERE v=7 LIMIT 1",
			} {
				f.query(t, query)
			}
			txn := f.ctx.Session.(*Session).liveTxn()
			if len(txn.keylessRows) != 0 {
				t.Fatal("completed or rolled-back keyless statement retained selected identities")
			}
			namedSavepointRows(t, f, "SELECT v FROM bag ORDER BY v", []sql.Row{{int32(7)}, {int32(9)}})
			events := txn.eventsSnapshot()
			for _, event := range events {
				if !event.IsRead && event.Write.Value == int32(8) {
					t.Fatal("rolled-back keyless cell provenance survived")
				}
			}
			f.query(t, "COMMIT")
			namedSavepointPublished(t, f.store, iso, events)
			namedSavepointRows(t, f, "SELECT v FROM bag ORDER BY v", []sql.Row{{int32(7)}, {int32(9)}})
		})
	}
}

func TestNamedSavepointRejectedSQLCommitClearsState(t *testing.T) {
	f := namedSavepointFixture(t, Serializable)
	for _, query := range []string{
		"BEGIN",
		"SELECT v FROM items WHERE id=2",
		"UPDATE items SET v=11 WHERE id=1",
		"COMMIT",
	} {
		f.query(t, query)
	}
	session := NewSession(f.store, Serializable, 2)
	t.Cleanup(session.SessionEnd)
	other := &sqlTableFixture{
		store: f.store, provider: f.provider, engine: f.engine,
		ctx: sql.NewContext(context.Background(), sql.WithSession(session)),
	}
	f.store.SetSchedule([]int{0})
	for _, query := range []string{
		"BEGIN",
		"SELECT v FROM savepoints.items WHERE id=1",
		"UPDATE savepoints.items SET v=21 WHERE id=2",
		"SAVEPOINT rejected",
		"INSERT INTO savepoints.items VALUES (3,30)",
		"ROLLBACK TO rejected",
	} {
		other.query(t, query)
	}
	txn := session.liveTxn()
	if err := foreignKeyQueryError(other, "COMMIT"); !sql.ErrLockDeadlock.Is(err) {
		t.Fatalf("write-skew commit should be rejected, got %v", err)
	}
	assertSessionTransactionEnded(t, session)
	if len(txn.savepoints) != 0 || len(f.store.logSnapshot()) != 1 {
		t.Fatal("rejected commit retained names or published work")
	}
	f.store.SetSchedule([]int{1 << 20})
	other.query(t, "BEGIN")
	namedSavepointMissing(t, other, "ROLLBACK TO rejected")
	namedSavepointMissing(t, other, "RELEASE SAVEPOINT rejected")
	namedSavepointRows(t, other, "SELECT id,v FROM savepoints.items ORDER BY id", []sql.Row{{int32(1), int32(11)}, {int32(2), int32(20)}})
	namedSavepointRows(t, other, "SELECT v FROM savepoints.items WHERE id=3", nil)
	other.query(t, "UPDATE savepoints.items SET v=22 WHERE id=2")
	other.query(t, "COMMIT")
	if !checkLevel(buildSessions(f.store.logSnapshot(), session.sid, nil), levelName(Serializable)) {
		t.Fatal("a rejected savepoint transaction poisoned its valid successor")
	}
}

func TestNamedSavepointControlledReplayProvenance(t *testing.T) {
	for _, level := range []string{"read-committed", "repeatable-read", "atomic-read", "causal", "prefix", "snapshot-isolation", "serializable"} {
		t.Run(level, func(t *testing.T) {
			workload := sqlReplayFixture()
			workload.Isolation = level
			workload.Transactions[0].Statements = []SQLStatement{
				{ID: "prefix", SQL: "UPDATE replaydb.items SET v=1 WHERE id=1"},
				{ID: "save", SQL: "SAVEPOINT keep"},
				{ID: "abandoned-move", SQL: "UPDATE replaydb.items SET id=2,v=2 WHERE id=1"},
				{ID: "abandoned-insert", SQL: "INSERT INTO replaydb.items VALUES(3,3)"},
				{ID: "save-later", SQL: "SAVEPOINT later"},
				{ID: "restore", SQL: "ROLLBACK TO keep"},
				{ID: "release", SQL: "RELEASE SAVEPOINT keep"},
				{ID: "surviving-insert", SQL: "INSERT INTO replaydb.items VALUES(4,4)"},
			}
			workload.Transactions[1].Statements[0].SQL = "SELECT id,v FROM replaydb.items ORDER BY id"
			workload.Assertion.SQL = "SELECT id,v FROM replaydb.items ORDER BY id"
			workload.Assertion.BaselineRows = []TypedRow{{{Kind: "int", Value: "1"}, {Kind: "int", Value: "0"}}}
			workload.Assertion.WantRows = workload.Assertion.BaselineRows
			result, err := RunSQLWorkload(workload, []int{})
			if err != nil {
				t.Fatal(err)
			}
			wantRows := []TypedRow{{{Kind: "int", Value: "1"}, {Kind: "int", Value: "1"}}, {{Kind: "int", Value: "4"}, {Kind: "int", Value: "4"}}}
			if result.Failure == nil || !reflect.DeepEqual(result.ObservedRows, wantRows) {
				t.Fatalf("wrong savepoint result: %+v", result)
			}
			prefixRead, insertedRead := false, false
			for _, decision := range result.Choices {
				for _, source := range decision.Candidates {
					if strings.HasPrefix(source.StatementID, "abandoned-") {
						t.Fatalf("rolled-back write became a durable source: %+v", source)
					}
				}
				if decision.TransactionID != "reader" || decision.Selected.Initial {
					continue
				}
				source := decision.Selected
				if source.TransactionID != "writer" {
					t.Fatalf("unexpected producer: %+v", source)
				}
				var address []string
				if err := json.Unmarshal([]byte(source.Key), &address); err != nil || len(address) != 5 {
					t.Fatalf("invalid canonical address %q: %v", source.Key, err)
				}
				switch source.StatementID {
				case "prefix":
					if address[3] != "1" || address[4] != "v" || source.WriteOrdinal != 1 {
						t.Fatalf("prefix provenance changed: %+v", source)
					}
					prefixRead = true
				case "surviving-insert":
					ordinals := map[string]int{rowExistenceColumn: 1, "id": 2, "v": 3}
					if address[3] != "4" || source.WriteOrdinal != ordinals[address[4]] {
						t.Fatalf("post-rollback provenance changed: %+v", source)
					}
					insertedRead = true
				default:
					t.Fatalf("unexpected surviving statement: %+v", source)
				}
			}
			if !prefixRead || !insertedRead {
				t.Fatal("fixture did not consume both retained and post-rollback write provenance")
			}
			var history [][]historyTxn
			if err := json.Unmarshal(result.History, &history); err != nil {
				t.Fatal(err)
			}
			writes := 0
			for _, session := range history {
				for _, transaction := range session {
					for _, event := range transaction.Events {
						if event.Write != nil {
							writes++
						}
					}
				}
			}
			iso, _ := sqlIsolation(level)
			if writes != 4 || !checkLevel(history, levelName(iso)) {
				t.Fatalf("rollback leaked publications or invalid history: writes=%d history=%s", writes, result.History)
			}
			artifact, err := CaptureSQLFailure(workload, result)
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := ReplaySQLArtifact(cloneSQLArtifact(t, artifact))
			if err != nil {
				t.Fatal(err)
			}
			if fresh.Failure == nil || *fresh.Failure != artifact.Failure || !reflect.DeepEqual(fresh.Choices, result.Choices) || !reflect.DeepEqual(fresh.ObservedRows, wantRows) || !bytes.Equal(fresh.History, result.History) {
				t.Fatal("fresh replay changed savepoint semantics or durable provenance")
			}
		})
	}
}
