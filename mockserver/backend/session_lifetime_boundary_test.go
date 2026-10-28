package backend

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func TestSessionStrongerIsolationRejectsWeakCommittedPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	t.Cleanup(f.store.Close)
	if err := f.store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"CREATE DATABASE lifetime", "USE lifetime",
		"CREATE TABLE entries (id INT PRIMARY KEY, v INT)",
		"INSERT INTO entries VALUES (1,0),(2,0),(3,0)",
	} {
		f.query(t, query)
	}
	if err := f.store.SealInitialState(); err != nil {
		t.Fatal(err)
	}
	newContext := func(iso Isolation, sid int) *sql.Context {
		reader := NewSession(f.store, iso, sid)
		t.Cleanup(reader.SessionEnd)
		return sql.NewContext(context.Background(), sql.WithSession(reader))
	}
	query := func(ctx *sql.Context, text string, typed bool) []TypedRow {
		rows, err := sqlReplayQuery(f.engine, ctx, text, typed)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		return rows
	}

	// Session 1 reads initial y and writes x; session 2 reads initial x and writes y.
	f.store.SetSchedule([]int{0})
	query(f.ctx, "BEGIN", false)
	semanticWant(t, query(f.ctx, "SELECT v FROM lifetime.entries WHERE id=2", true), semanticInts(0))
	query(f.ctx, "UPDATE lifetime.entries SET v=1 WHERE id=1", false)
	query(f.ctx, "COMMIT", false)
	weak := newContext(ReadCommitted, 2)
	query(weak, "BEGIN", false)
	semanticWant(t, query(weak, "SELECT v FROM lifetime.entries WHERE id=1", true), semanticInts(0))
	query(weak, "UPDATE lifetime.entries SET v=1 WHERE id=2", false)
	query(weak, "COMMIT", false)

	beforeLog := f.store.logSnapshot()
	if len(beforeLog) != 2 {
		t.Fatalf("want two committed weak transactions, got %+v", beforeLog)
	}
	history := buildSessions(beforeLog, beforeLog[0].Session, nil)
	if !checkLevel(history, levelName(ReadCommitted)) || checkLevel(history, levelName(Serializable)) {
		t.Fatal("fixture must be admitted at read-committed but contain nonserializable write skew")
	}
	beforeAdmissions := f.store.Admissions()
	beforeID, beforeSeq := f.store.nextID, f.store.seq
	beforeCells := make(map[string][]CellVersion, len(f.store.cells))
	for key := range f.store.cells {
		beforeCells[key] = f.store.committedVersions(key)
	}

	strong := newContext(Serializable, 3)
	query(strong, "BEGIN", false)
	rows, err := sqlReplayQuery(f.engine, strong, "SELECT v FROM lifetime.entries WHERE id=3", true)
	if err == nil || !strings.Contains(err.Error(), "no admissible existence version") || rows != nil {
		t.Fatalf("incompatible committed prefix must fail closed, not return an absent row: rows=%v err=%v", rows, err)
	}
	if txn := strong.Session.(*Session).liveTxn(); txn != nil && txn.checkpoint() != (txnCheckpoint{}) {
		t.Fatal("rejected read retained transaction observations or writes")
	}
	query(strong, "ROLLBACK", false)
	if !reflect.DeepEqual(f.store.logSnapshot(), beforeLog) || !reflect.DeepEqual(f.store.Admissions(), beforeAdmissions) || f.store.nextID != beforeID || f.store.seq != beforeSeq {
		t.Fatal("rejected stronger-isolation read or rollback published history, admissions, or version IDs")
	}
	if len(f.store.cells) != len(beforeCells) {
		t.Fatal("rejected read changed the committed cell set")
	}
	for key, versions := range beforeCells {
		if got := f.store.committedVersions(key); !reflect.DeepEqual(got, versions) {
			t.Fatalf("rejected read changed committed versions for %q: got %v want %v", key, got, versions)
		}
	}

	f.store.SetSchedule([]int{1 << 20})
	healthy := newContext(ReadCommitted, 4)
	query(healthy, "BEGIN", false)
	semanticWant(t, query(healthy, "SELECT v FROM lifetime.entries ORDER BY id", true), semanticInts(1, 1, 0))
	query(healthy, "ROLLBACK", false)
	if !reflect.DeepEqual(f.store.logSnapshot(), beforeLog) || f.store.nextID != beforeID || f.store.seq != beforeSeq {
		t.Fatal("healthy read rollback changed committed history or consumed publication IDs")
	}
}
