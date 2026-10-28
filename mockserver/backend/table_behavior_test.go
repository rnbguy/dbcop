package backend

import (
	"context"
	"errors"
	"reflect"
	"testing"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

func stagedTable(t *testing.T, def TableDef) (*Table, *Session, *sql.Context) {
	t.Helper()
	RegisterVariables()
	store := NewStore(1, 2)
	table := NewTable(def, store, ReadCommitted)
	session := NewSession(store, ReadCommitted, 1)
	ctx := sql.NewContext(context.Background(), sql.WithSession(session))
	session.beginTxn()
	t.Cleanup(func() { _ = session.Rollback(ctx, nil) })
	return table, session, ctx
}

func tableRows(t *testing.T, ctx *sql.Context, table sql.Table) []sql.Row {
	t.Helper()
	partitions, err := table.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer partitions.Close(ctx)
	partition, err := partitions.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	iter, err := table.PartitionRows(ctx, partition)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := sql.RowIterToRows(ctx, iter)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestKeylessRowsKeepDuplicatesAndDeleteOneIdentity(t *testing.T) {
	table, _, ctx := stagedTable(t, TableDef{Name: "events", PkeyIdx: -1,
		Columns: []*sql.Column{{Name: "owner", Type: types.Int64, Nullable: true}, {Name: "amount", Type: types.Int64}}})
	row := sql.Row{nil, int64(12)}
	writer := table.Inserter(ctx)
	writer.StatementBegin(ctx)
	for range 3 {
		if err := writer.Insert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if rows := tableRows(t, ctx, table); len(rows) != 3 {
		t.Fatalf("duplicate keyless rows lost: %v", rows)
	}
	deleter := table.Deleter(ctx)
	deleter.StatementBegin(ctx)
	if err := deleter.Delete(ctx, row); err != nil {
		t.Fatal(err)
	}
	if rows := tableRows(t, ctx, table); len(rows) != 2 {
		t.Fatalf("deleting one duplicate must retain two: %v", rows)
	}
}

func TestCompositePrimaryKeyMoveAndStatementRollback(t *testing.T) {
	table, _, ctx := stagedTable(t, TableDef{Name: "balances", PkeyIdxs: []int{1, 0},
		Columns: []*sql.Column{intCol("id"), intCol("branch"), intCol("balance")}})
	old := sql.Row{int64(2), int64(1), int64(10)}
	insert := table.Inserter(ctx)
	insert.StatementBegin(ctx)
	if err := insert.Insert(ctx, old); err != nil {
		t.Fatal(err)
	}
	update := table.Updater(ctx)
	update.StatementBegin(ctx)
	moved := sql.Row{int64(3), int64(2), int64(20)}
	if err := update.Update(ctx, old, moved); err != nil {
		t.Fatal(err)
	}
	if rows := tableRows(t, ctx, table); !reflect.DeepEqual(rows, []sql.Row{moved}) {
		t.Fatalf("primary key move left an old row or lost new row: %v", rows)
	}
	if err := update.DiscardChanges(ctx, context.Canceled); err != nil {
		t.Fatal(err)
	}
	if rows := tableRows(t, ctx, table); !reflect.DeepEqual(rows, []sql.Row{old}) {
		t.Fatalf("failed statement discarded previous statement or retained move: %v", rows)
	}
	if err := table.Inserter(ctx).Insert(ctx, old); !sql.ErrPrimaryKeyViolation.Is(err) {
		t.Fatalf("duplicate primary key must reach engine as constraint error: %v", err)
	}
}

func TestCompositeTextKeysCannotAliasDelimiter(t *testing.T) {
	table, _, ctx := stagedTable(t, TableDef{Name: "pairs", PkeyIdxs: []int{0, 1},
		Columns: []*sql.Column{{Name: "left", Type: types.LongText}, {Name: "right", Type: types.LongText}}})
	for _, row := range []sql.Row{{"a#b", "c"}, {"a", "b#c"}} {
		if err := table.Inserter(ctx).Insert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if rows := tableRows(t, ctx, table); len(rows) != 2 {
		t.Fatalf("composite text identities aliased: %v", rows)
	}
}

type sqlTableFixture struct {
	store    *Store
	provider *Provider
	engine   *sqle.Engine
	ctx      *sql.Context
}

func newSQLTableFixture(t *testing.T) *sqlTableFixture {
	t.Helper()
	RegisterVariables()
	store := NewStore(1, 2)
	store.SetSchedule([]int{1 << 20}) // deterministic newest-admitted fixture
	provider := NewProvider(store, ReadCommitted, nil)
	engine := NewEngine(provider)
	session := NewSession(store, ReadCommitted, 1)
	ctx := sql.NewContext(context.Background(), sql.WithSession(session))
	t.Cleanup(func() {
		_ = session.Rollback(ctx, nil)
		_ = engine.Close()
	})
	return &sqlTableFixture{store: store, provider: provider, engine: engine, ctx: ctx}
}

func (f *sqlTableFixture) query(t *testing.T, query string) []sql.Row {
	t.Helper()
	_, iter, _, err := f.engine.Query(f.ctx, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	rows, err := sql.RowIterToRows(f.ctx, iter)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return rows
}

func TestSQLLookupReadsOnlyMatchingRowsAndRequiredColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE ledger")
	f.query(t, "USE ledger")
	f.query(t, "CREATE TABLE accounts (branch INT NOT NULL, id INT NOT NULL, label VARCHAR(20), balance INT, secret VARCHAR(20), PRIMARY KEY (branch,id), INDEX by_label (branch,label))")
	f.query(t, "INSERT INTO accounts VALUES (1,1,'one',10,'private'), (1,2,'two',20,'private'), (2,1,'three',30,'private')")
	db, err := f.provider.Database(f.ctx, "ledger")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, _ := db.GetTableInsensitive(f.ctx, "accounts")
	table := raw.(*Table)
	for _, test := range []struct {
		query, key string
		want       int32
		columns    []string
	}{
		{"SELECT balance FROM accounts WHERE branch=1 AND id=1", "1#1", 10, []string{"branch", "id", "balance"}},
		{"SELECT balance FROM accounts WHERE branch=1 AND label='two'", "1#2", 20, []string{"branch", "label", "balance"}},
		{"SELECT balance FROM accounts WHERE branch=1 AND label>'one'", "1#2", 20, []string{"branch", "label", "balance"}},
		{"SELECT balance FROM accounts WHERE branch=1 AND 'two'=label", "1#2", 20, []string{"branch", "label", "balance"}},
		{"SELECT balance FROM accounts WHERE branch=2", "2#1", 30, []string{"branch", "balance"}},
	} {
		before := len(f.store.Admissions())
		rows := f.query(t, test.query)
		if !reflect.DeepEqual(rows, []sql.Row{{test.want}}) {
			t.Fatalf("point lookup returned %v", rows)
		}
		allowed := make(map[string]bool)
		allowed[cellKey(table.storageName, test.key, rowExistenceColumn)] = true
		for _, column := range test.columns {
			allowed[cellKey(table.storageName, test.key, column)] = true
		}
		for _, read := range f.store.Admissions()[before:] {
			if !allowed[read.Key] {
				t.Fatalf("unrelated row/column entered oracle history for %s: %q", test.query, read.Key)
			}
		}
	}
}

func TestDeletedRowsRemainHistoricallyReadableAndRecorded(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE archive")
	f.query(t, "USE archive")
	f.query(t, "CREATE TABLE records (id INT NOT NULL PRIMARY KEY, value INT)")
	f.query(t, "INSERT INTO records VALUES (1,7)")
	f.query(t, "DELETE FROM records WHERE id=1")
	if rows := f.query(t, "SELECT value FROM records WHERE id=1"); len(rows) != 0 {
		t.Fatalf("newest existence must hide deleted row: %v", rows)
	}
	// A fresh RC reader may select the committed pre-deletion existence and cells.
	reader := NewSession(f.store, ReadCommitted, 2)
	ctx := sql.NewContext(context.Background(), sql.WithSession(reader))
	t.Cleanup(func() { _ = reader.Rollback(ctx, nil) })
	db, _ := f.provider.Database(ctx, "archive")
	raw, _, _ := db.GetTableInsensitive(ctx, "records")
	table := raw.(*Table)
	f.store.SetSchedule([]int{1})
	before := len(f.store.Admissions())
	rows := tableRows(t, ctx, table.WithProjections([]string{"value"}))
	if !reflect.DeepEqual(rows, []sql.Row{{int32(7)}}) {
		t.Fatalf("old row was removed from candidate universe: %v", rows)
	}
	reads := f.store.Admissions()[before:]
	if len(reads) != 2 || reads[0].Key != cellKey(table.storageName, "1", rowExistenceColumn) || reads[0].Initial {
		t.Fatalf("historical membership must be an oracle read: %+v", reads)
	}
}

func TestSQLPrimaryKeyMovesPublishBothMembershipChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE moves")
	f.query(t, "USE moves")
	f.query(t, "CREATE TABLE rows_by_key (group_id INT NOT NULL, id INT NOT NULL, value INT, PRIMARY KEY(group_id,id))")
	f.query(t, "INSERT INTO rows_by_key VALUES (1,1,7)")
	f.query(t, "UPDATE rows_by_key SET group_id=2,id=3,value=9 WHERE group_id=1 AND id=1")
	if rows := f.query(t, "SELECT value FROM rows_by_key WHERE group_id=1 AND id=1"); len(rows) != 0 {
		t.Fatalf("old physical identity survived primary-key move: %v", rows)
	}
	if rows := f.query(t, "SELECT value FROM rows_by_key WHERE group_id=2 AND id=3"); !reflect.DeepEqual(rows, []sql.Row{{int32(9)}}) {
		t.Fatalf("new physical identity missing after primary-key move: %v", rows)
	}
}

func TestIndexLookupPreservesMixedHistoricalCellCandidates(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE mixtures")
	f.query(t, "USE mixtures")
	f.query(t, "CREATE TABLE pairs (id INT NOT NULL PRIMARY KEY, a INT, b INT, INDEX by_pair(a,b))")
	f.query(t, "INSERT INTO pairs VALUES (1,1,0)")
	f.query(t, "UPDATE pairs SET a=0,b=1 WHERE id=1")
	reader := NewSession(f.store, ReadCommitted, 2)
	reader.SetCurrentDatabase("mixtures")
	f.ctx = sql.NewContext(context.Background(), sql.WithSession(reader))
	t.Cleanup(func() { _ = reader.Rollback(f.ctx, nil) })
	// Read old existence and a, newest b, then the unchanged PK.
	// No committed SQL row had (a,b)=(1,1), but the KV oracle may admit it.
	f.store.SetSchedule([]int{1, 0, 1, 0})
	if rows := f.query(t, "SELECT id FROM pairs WHERE a=1 AND b=1"); !reflect.DeepEqual(rows, []sql.Row{{int32(1)}}) {
		t.Fatalf("index directory suppressed an admitted per-cell history: %v", rows)
	}
}

func TestSQLKeylessDuplicatesPersistWithoutReadingUnusedColumns(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE duplicates")
	f.query(t, "USE duplicates")
	f.query(t, `CREATE TABLE history (
		h_c_id INT NOT NULL, h_c_d_id INT NOT NULL, h_c_w_id INT NOT NULL,
		h_d_id INT NOT NULL, h_w_id INT NOT NULL, h_date DATETIME,
		h_amount DECIMAL(6,2), h_data VARCHAR(24),
		INDEX idx_h_w_id(h_w_id), INDEX idx_h_c_w_id(h_c_w_id))`)
	f.query(t, `INSERT INTO history VALUES
		(1,1,1,1,1,'2026-10-01 12:00:00',12.34,'same'),
		(1,1,1,1,1,'2026-10-01 12:00:00',12.34,'same')`)
	db, _ := f.provider.Database(f.ctx, "duplicates")
	raw, _, _ := db.GetTableInsensitive(f.ctx, "history")
	table := raw.(*Table)
	before := len(f.store.Admissions())
	if rows := f.query(t, "SELECT COUNT(*) FROM history"); !reflect.DeepEqual(rows, []sql.Row{{int64(2)}}) {
		t.Fatalf("committed duplicate history rows were lost: %v", rows)
	}
	allowed := make(map[string]bool)
	for _, key := range table.knownKeys(nil) {
		allowed[cellKey(table.storageName, key, rowExistenceColumn)] = true
	}
	for _, read := range f.store.Admissions()[before:] {
		if !allowed[read.Key] {
			t.Fatalf("COUNT(*) read an unused history column: %q", read.Key)
		}
	}
}

func TestUpdatePreservesRawChangesUnderCaseInsensitiveCollation(t *testing.T) {
	text, err := types.LongText.(sql.TypeWithCollation).WithNewCollation(sql.Collation_utf8mb4_0900_ai_ci)
	if err != nil {
		t.Fatal(err)
	}
	table, _, ctx := stagedTable(t, TableDef{Name: "labels", PkeyIdx: 0,
		Columns: []*sql.Column{intCol("id"), {Name: "label", Type: text}, intCol("counter")}})
	old := sql.Row{int64(1), "a", int64(0)}
	if err := table.Inserter(ctx).Insert(ctx, old); err != nil {
		t.Fatal(err)
	}
	next := sql.Row{int64(1), "A", int64(1)}
	if err := table.Updater(ctx).Update(ctx, old, next); err != nil {
		t.Fatal(err)
	}
	if rows := tableRows(t, ctx, table); !reflect.DeepEqual(rows, []sql.Row{next}) {
		t.Fatalf("collation equality discarded a raw stored-value change: %v", rows)
	}
}

func TestPushedFiltersSeeStagedIndexedUpdatesAndRejectRolledBackValues(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE staged_filters")
	f.query(t, "USE staged_filters")
	f.query(t, "CREATE TABLE entries (group_id INT NOT NULL, id INT NOT NULL, label VARCHAR(20), PRIMARY KEY(group_id,id), INDEX by_label(group_id,label))")
	f.query(t, "INSERT INTO entries VALUES (1,1,'old'),(1,2,'other')")
	f.query(t, "START TRANSACTION")
	f.query(t, "UPDATE entries SET label='new' WHERE group_id=1 AND id=1")
	if rows := f.query(t, "SELECT id FROM entries WHERE group_id=1 AND label='new'"); !reflect.DeepEqual(rows, []sql.Row{{int32(1)}}) {
		t.Fatalf("pushed filter lost a same-PK staged indexed update: %v", rows)
	}
	f.query(t, "ROLLBACK")
	if rows := f.query(t, "SELECT id FROM entries WHERE group_id=1 AND label='new'"); len(rows) != 0 {
		t.Fatalf("candidate metadata published a rolled-back value: %v", rows)
	}
}

func TestCancelledScanStopsBeforeReadingVersions(t *testing.T) {
	table, session, ctx := stagedTable(t, TableDef{Name: "cancelled",
		Columns: []*sql.Column{{Name: "id", Type: types.Int64}}})
	writer := table.Inserter(ctx)
	writer.StatementBegin(ctx)
	if err := writer.Insert(ctx, sql.Row{int64(1)}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	cancelledCtx := sql.NewContext(cancelled, sql.WithSession(session))
	if _, err := table.PartitionRows(cancelledCtx, &singlePartition{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scan returned %v, want context cancellation", err)
	}
	if got := len(session.store.Admissions()); got != 0 {
		t.Fatalf("cancelled scan observed %d versions", got)
	}
}
