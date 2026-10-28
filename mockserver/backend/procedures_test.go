package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/mysql"
)

func initializationSQLFixture(t *testing.T) *sqlTableFixture {
	t.Helper()
	f := newSQLTableFixture(t)
	if err := f.store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	f.query(t, "CREATE DATABASE warehouse")
	f.query(t, "USE warehouse")
	f.query(t, "CREATE TABLE items (id INT PRIMARY KEY, value VARCHAR(20), INDEX by_value (value))")
	f.query(t, "INSERT INTO items VALUES (1,'old'), (2,NULL), (3,'gone')")
	return f
}

func requireSealCallError(t *testing.T, f *sqlTableFixture) {
	t.Helper()
	_, iter, _, err := f.engine.Query(f.ctx, "CALL dbcop_seal_initial_state()")
	if err == nil {
		_, err = sql.RowIterToRows(f.ctx, iter)
	}
	if err == nil {
		t.Fatal("sealing unexpectedly succeeded")
	}
}

func TestSQLSealInitialState(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := initializationSQLFixture(t)
	f.query(t, "UPDATE items SET value='loaded' WHERE id=1")
	f.query(t, "DELETE FROM items WHERE id=3")
	if len(f.store.logSnapshot()) != 0 || len(f.store.Admissions()) != 0 {
		t.Fatal("preparation entered measured history")
	}
	nextID := f.store.nextID
	f.query(t, "CALL dbcop_seal_initial_state()")
	assertSessionTransactionEnded(t, f.ctx.Session.(*Session))
	if f.store.isInitializing() || f.store.nextID != nextID || len(f.store.logSnapshot()) != 0 {
		t.Fatal("sealing must retain IDs and start empty measured history")
	}
	for key, cell := range f.store.cells {
		if len(cell.Versions) != 1 || cell.Versions[0].ID != 0 {
			t.Fatalf("cell %q was not collapsed to an initial root: %+v", key, cell.Versions)
		}
	}
	f.store.SetSchedule([]int{0})
	if rows := f.query(t, "SELECT id,value FROM items ORDER BY id"); !reflect.DeepEqual(rows, []sql.Row{{int32(1), "loaded"}, {int32(2), nil}}) {
		t.Fatalf("baseline values: %v", rows)
	}
	if rows := f.query(t, "SELECT id FROM items WHERE value='loaded'"); !reflect.DeepEqual(rows, []sql.Row{{int32(1)}}) {
		t.Fatalf("current indexed baseline value missing: %v", rows)
	}
	if rows := f.query(t, "SELECT id FROM items WHERE value='old'"); len(rows) != 0 {
		t.Fatalf("preparation index entries exposed discarded versions: %v", rows)
	}
	f.query(t, "CREATE INDEX after_seal ON items (value)")
	if rows := f.query(t, "SELECT id FROM items WHERE value='loaded'"); !reflect.DeepEqual(rows, []sql.Row{{int32(1)}}) {
		t.Fatalf("index created after seal lost baseline values: %v", rows)
	}
}

func TestSQLSealRejectsUserTransactionsWithoutDiscardingWork(t *testing.T) {
	for _, mode := range []string{"explicit empty", "explicit dirty", "autocommit disabled", "pending autocommit"} {
		t.Run(mode, func(t *testing.T) {
			f := initializationSQLFixture(t)
			session := f.ctx.Session.(*Session)
			switch mode {
			case "explicit empty":
				f.query(t, "BEGIN")
			case "explicit dirty", "pending autocommit":
				f.query(t, "BEGIN")
				f.query(t, "UPDATE items SET value='pending' WHERE id=1")
				if mode == "pending autocommit" {
					session.SetIgnoreAutoCommit(false)
				}
			case "autocommit disabled":
				f.query(t, "SET autocommit=0")
				f.query(t, "UPDATE items SET value='pending' WHERE id=1")
			}
			txn := session.liveTxn()
			before := txn.checkpoint()
			handle := session.GetTransaction()
			requireSealCallError(t, f)
			if session.liveTxn() != txn || txn.checkpoint() != before || session.GetTransaction() != handle || !f.store.isInitializing() {
				t.Fatal("rejected seal changed the user's transaction or initialization mode")
			}
			if mode != "explicit empty" {
				// Preserve the pending transaction while checking read-your-writes.
				session.SetIgnoreAutoCommit(true)
				if rows := f.query(t, "SELECT value FROM items WHERE id=1"); !reflect.DeepEqual(rows, []sql.Row{{"pending"}}) {
					t.Fatalf("rejected seal lost staged writes: %v", rows)
				}
			}
			f.query(t, "ROLLBACK")
			f.query(t, "SET autocommit=1")
			if rows := f.query(t, "SELECT value FROM items WHERE id=1"); !reflect.DeepEqual(rows, []sql.Row{{"old"}}) {
				t.Fatalf("rollback after rejection published staged writes: %v", rows)
			}
			f.query(t, "CALL dbcop_seal_initial_state()")
		})
	}
}

func TestSQLRepeatSealPreservesMeasuredHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := initializationSQLFixture(t)
	f.query(t, "CALL dbcop_seal_initial_state()")
	f.query(t, "UPDATE items SET value='measured' WHERE id=1")
	before := f.store.logSnapshot()
	nextID := f.store.nextID
	registered := len(f.store.reg)
	requireSealCallError(t, f)
	if !reflect.DeepEqual(f.store.logSnapshot(), before) || f.store.nextID != nextID || len(f.store.reg) != registered {
		t.Fatal("repeat seal changed measured history or version identity")
	}
	if rows := f.query(t, "SELECT value FROM items WHERE id=1"); !reflect.DeepEqual(rows, []sql.Row{{"measured"}}) {
		t.Fatalf("repeat seal changed measured data: %v", rows)
	}
}

func TestProviderMissingDatabaseUsesMySQLError(t *testing.T) {
	f := newSQLTableFixture(t)
	_, _, _, err := f.engine.Query(f.ctx, "USE Uncreated")
	wireErr, ok := mysql.NewSQLErrorFromError(err).(*mysql.SQLError)
	if !ok || wireErr.Number() != mysql.ERBadDb || wireErr.SQLState() != "42000" || wireErr.Message != "Unknown database 'Uncreated'" {
		t.Fatalf("missing database must retain MySQL 1049/42000 diagnostics: %v", err)
	}
	if f.provider.HasDatabase(f.ctx, "Uncreated") {
		t.Fatal("looking up a missing database created it")
	}
	f.query(t, "CREATE DATABASE Uncreated")
	f.query(t, "USE Uncreated")
	if !f.provider.HasDatabase(f.ctx, "uncreated") {
		t.Fatal("normal CREATE DATABASE did not recover from a missing database")
	}
}
