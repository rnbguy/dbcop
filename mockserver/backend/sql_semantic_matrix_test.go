package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	sqle "github.com/dolthub/go-mysql-server"
	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// This is a storage/lowering differential: both engines share the installed
// parser and executor. The memory provider is not an independent MySQL oracle.
type sqlSemanticReference struct {
	custom *sqlTableFixture
	engine *sqle.Engine
	ctx    *sql.Context
}

func newSQLSemanticReference(t *testing.T, setup []string) *sqlSemanticReference {
	t.Helper()
	f := newSQLTableFixture(t)
	provider := memory.NewDBProviderWithOpts(memory.NativeIndexProvider(true))
	engine := sqle.New(analyzer.NewDefault(provider), nil)
	ctx := sql.NewContext(context.Background(), sql.WithSession(memory.NewSession(sql.NewBaseSession(), provider)))
	r := &sqlSemanticReference{custom: f, engine: engine, ctx: ctx}
	t.Cleanup(func() {
		_, _ = sqlReplayQuery(engine, ctx, "ROLLBACK", false)
		if err := engine.Close(); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("reference engine close: %v", err)
		}
	})
	if err := f.store.BeginInitialization(); err != nil {
		t.Fatal(err)
	}
	for _, query := range setup {
		r.compare(t, query, false, "")
	}
	if err := f.store.SealInitialState(); err != nil {
		t.Fatal(err)
	}
	return r
}

func sqlSemanticError(err error) string {
	if joined, ok := err.(interface{ Unwrap() []error }); ok && len(joined.Unwrap()) == 1 {
		return sqlSemanticError(joined.Unwrap()[0])
	}
	if wrapped, ok := err.(sql.WrappedInsertError); ok {
		return sqlSemanticError(wrapped.Cause)
	}
	switch {
	case err == nil:
		return ""
	case sql.ErrPrimaryKeyViolation.Is(err):
		return "primary"
	case sql.ErrUniqueKeyViolation.Is(err):
		return "unique"
	case sql.ErrForeignKeyChildViolation.Is(err):
		return "foreign-child"
	case sql.ErrForeignKeyParentViolation.Is(err):
		return "foreign-parent"
	default:
		return fmt.Sprintf("unsupported error %T: %v", err, err)
	}
}

func (r *sqlSemanticReference) compare(t *testing.T, query string, rows bool, wantError string) []TypedRow {
	t.Helper()
	got, gotErr := sqlReplayQuery(r.custom.engine, r.custom.ctx, query, rows)
	want, wantErr := sqlReplayQuery(r.engine, r.ctx, query, rows)
	if sqlSemanticError(gotErr) != wantError || sqlSemanticError(wantErr) != wantError {
		t.Fatalf("storage differential %s: custom=%s reference=%s; want category %q", query, sqlSemanticError(gotErr), sqlSemanticError(wantErr), wantError)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("storage differential %s: custom=%v reference=%v", query, got, want)
	}
	return got
}

func semanticInts(values ...int) []TypedRow {
	rows := make([]TypedRow, 0, len(values))
	for _, value := range values {
		rows = append(rows, TypedRow{{Kind: "int", Value: fmt.Sprint(value)}})
	}
	return rows
}

func semanticWant(t *testing.T, got, want []TypedRow) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hand-declared SQL rows: got %v want %v", got, want)
	}
}

func TestSQLSemanticMatrixLatestPredicates(t *testing.T) {
	r := newSQLSemanticReference(t, []string{
		"CREATE DATABASE semanticdb", "USE semanticdb",
		"CREATE TABLE entries (branch INT NOT NULL, id INT NOT NULL, label VARCHAR(32) COLLATE utf8mb4_0900_ai_ci, raw VARBINARY(16), n BIGINT, u BIGINT UNSIGNED, d DECIMAL(20,2), PRIMARY KEY(branch,id), INDEX by_label(branch,label(2)), INDEX by_n(n), INDEX by_u(u), INDEX by_d(d), INDEX by_raw(raw(2)))",
		"INSERT INTO entries VALUES (1,1,'Alpha',X'4100',-9223372036854775808,0,-1.25),(1,2,'alpha-tail',X'6100',0,9223372036854775808,0.00),(1,3,NULL,NULL,9223372036854775807,18446744073709551615,1.25),(2,1,'Beta',X'4101',NULL,1,NULL)",
	})
	for _, test := range []struct {
		predicate string
		ids       []int
	}{
		{"branch=1 AND id=2", []int{2}},
		{"branch=1 AND id>=1 AND id<3", []int{1, 2}},
		{"branch=1 AND id=9", nil},
		{"branch=1 AND label='ALPHA'", []int{1}},
		{"branch=1 AND label>'Alpha' AND label<'b'", []int{2}},
		{"branch=1 AND label IS NULL", []int{3}},
		{"branch=1 AND label <=> NULL", []int{3}},
		{"branch=1 AND label=NULL", nil},
		{"raw=X'4100'", []int{1}},
		{"raw>X'4100' AND raw<X'6100'", []int{1}},
		{"n=-9223372036854775808", []int{1}},
		{"n>=0 AND n<9223372036854775807", []int{2}},
		{"u>9223372036854775807", []int{2, 3}},
		{"u=18446744073709551615", []int{3}},
		{"branch=1 AND id='2'", []int{2}},
		{"d>-1.25 AND d<=1.25", []int{2, 3}},
	} {
		t.Run(test.predicate, func(t *testing.T) {
			got := r.compare(t, "SELECT id FROM entries WHERE "+test.predicate+" ORDER BY branch,id", true, "")
			semanticWant(t, got, semanticInts(test.ids...))
		})
	}
	// Only frozen/latest state is partitioned; no independently sampled weak reads.
	for _, predicate := range []string{"n>0", "label='ALPHA'", "d<=0"} {
		all := r.compare(t, "SELECT branch,id FROM entries ORDER BY branch,id", true, "")
		partitioned := make([]TypedRow, 0)
		for _, condition := range []string{predicate, "NOT (" + predicate + ")", "(" + predicate + ") IS NULL"} {
			partitioned = append(partitioned, r.compare(t, "SELECT branch,id FROM entries WHERE "+condition+" ORDER BY branch,id", true, "")...)
		}
		sort.Slice(partitioned, func(i, j int) bool { return semanticJSON(partitioned[i]) < semanticJSON(partitioned[j]) })
		semanticWant(t, partitioned, all)
	}
}

func TestSQLSemanticMatrixMutations(t *testing.T) {
	r := newSQLSemanticReference(t, []string{
		"CREATE DATABASE semanticdb", "USE semanticdb",
		"CREATE TABLE keyed (branch INT NOT NULL,id INT NOT NULL,v INT,PRIMARY KEY(branch,id),INDEX by_v(v))",
		"CREATE TABLE duplicates (label VARCHAR(12),v INT,INDEX by_v(v))",
		"INSERT INTO keyed VALUES(1,1,7)", "INSERT INTO duplicates VALUES('same',7),('same',7)",
	})
	for _, query := range []string{"BEGIN", "UPDATE keyed SET branch=2,id=3,v=9 WHERE branch=1 AND id=1", "UPDATE duplicates SET v=8 WHERE v=7", "COMMIT"} {
		r.compare(t, query, false, "")
	}
	semanticWant(t, r.compare(t, "SELECT v FROM keyed WHERE branch=1 AND id=1", true, ""), []TypedRow{})
	semanticWant(t, r.compare(t, "SELECT v FROM keyed WHERE branch=2 AND id=3", true, ""), semanticInts(9))
	semanticWant(t, r.compare(t, "SELECT v FROM duplicates WHERE v=8 ORDER BY v", true, ""), semanticInts(8, 8))
	logs := r.custom.store.logSnapshot()
	if len(logs) < 1 {
		t.Fatal("mutations did not publish a transaction")
	}
	table := semanticTable(t, r.custom, "keyed")
	var membershipKeys []string
	for _, write := range logs[0].Writes {
		if write.Key == cellKey(table.storageName, "1#1", rowExistenceColumn) || write.Key == cellKey(table.storageName, "2#3", rowExistenceColumn) {
			membershipKeys = append(membershipKeys, write.Key)
			versions := r.custom.store.committedVersions(write.Key)
			if versions[len(versions)-1].Value != (len(membershipKeys) == 2) {
				t.Fatalf("incorrect moved membership value: %v", versions)
			}
		}
	}
	if !reflect.DeepEqual(membershipKeys, []string{cellKey(table.storageName, "1#1", rowExistenceColumn), cellKey(table.storageName, "2#3", rowExistenceColumn)}) {
		t.Fatalf("primary-key move must pair old/new physical membership before cell writes: %v", membershipKeys)
	}
	duplicates := semanticTable(t, r.custom, "duplicates")
	keys := duplicates.knownKeys(nil)
	if len(keys) != 2 {
		t.Fatalf("duplicate physical identities lost: %v", keys)
	}
	for _, key := range keys {
		cell := cellKey(duplicates.storageName, key, "v")
		present := cellKey(duplicates.storageName, key, rowExistenceColumn)
		seenMembership, seenCell, writes := false, false, 0
		for _, event := range logs[0].Events {
			if event.Read != nil && event.Read.Key == present {
				seenMembership = true
			}
			if event.Read != nil && event.Read.Key == cell {
				seenCell = true
			}
			if event.Write != nil && event.Write.Key == cell {
				if !seenMembership || !seenCell {
					t.Fatalf("keyless cell mutation lost its selected physical membership/provenance: %q", key)
				}
				writes++
			}
		}
		if writes != 1 {
			t.Fatalf("each selected duplicate must mutate its own physical cell exactly once: %q writes=%d", key, writes)
		}
	}
	// A rollback after both membership and duplicate-cell changes must publish nothing.
	before := len(r.custom.store.logSnapshot())
	for _, query := range []string{"BEGIN", "UPDATE keyed SET id=4 WHERE branch=2 AND id=3", "DELETE FROM duplicates WHERE v=8", "ROLLBACK"} {
		r.compare(t, query, false, "")
	}
	if len(r.custom.store.logSnapshot()) != before {
		t.Fatal("full rollback published abandoned work")
	}
	semanticWant(t, r.compare(t, "SELECT id FROM keyed WHERE branch=2 ORDER BY id", true, ""), semanticInts(3))
	semanticWant(t, r.compare(t, "SELECT v FROM duplicates ORDER BY v", true, ""), semanticInts(8, 8))
}

func TestSQLSemanticMatrixStatementErrorsRetainPrefix(t *testing.T) {
	r := newSQLSemanticReference(t, []string{
		"CREATE DATABASE semanticdb", "USE semanticdb",
		"CREATE TABLE parents (id INT PRIMARY KEY,label VARCHAR(12),UNIQUE INDEX by_label(label))",
		"CREATE TABLE children (id INT PRIMARY KEY,parent_id INT,INDEX by_parent(parent_id),CONSTRAINT child_parent FOREIGN KEY(parent_id) REFERENCES parents(id))",
		"INSERT INTO parents VALUES(1,'one')", "INSERT INTO children VALUES(1,1)",
	})
	r.compare(t, "BEGIN", false, "")
	r.compare(t, "INSERT INTO parents VALUES(2,'two')", false, "")
	r.compare(t, "INSERT INTO children VALUES(2,2)", false, "")
	txn := r.custom.ctx.Session.(*Session).liveTxn()
	for _, failure := range []struct{ query, category string }{
		{"INSERT INTO parents VALUES(3,'three'),(4,'one')", "unique"},
		{"INSERT INTO children VALUES(3,2),(4,999)", "foreign-child"},
		{"DELETE FROM parents WHERE id=2", "foreign-parent"},
	} {
		before := txn.checkpoint()
		writes := append([]StagedWrite(nil), txn.writes...)
		events := append([]TxnEvent(nil), txn.events...)
		r.compare(t, failure.query, false, failure.category)
		if txn.checkpoint() != before || !reflect.DeepEqual(txn.writes, writes) || !reflect.DeepEqual(txn.events, events) {
			t.Fatalf("failing statement changed retained write/read/event prefix: %s", failure.query)
		}
		semanticWant(t, r.compare(t, "SELECT id FROM parents ORDER BY id", true, ""), semanticInts(1, 2))
		semanticWant(t, r.compare(t, "SELECT id FROM children ORDER BY id", true, ""), semanticInts(1, 2))
		semanticWant(t, r.compare(t, "SELECT id FROM parents WHERE label='three'", true, ""), []TypedRow{})
	}
	r.compare(t, "COMMIT", false, "")
	logs := r.custom.store.logSnapshot()
	if len(logs) == 0 || len(logs[0].Writes) != 6 {
		t.Fatalf("only the two earlier inserts may be published: %+v", logs)
	}
	allowed := make(map[string]bool)
	for _, name := range []string{"parents", "children"} {
		table := semanticTable(t, r.custom, name)
		allowed[cellKey(table.storageName, "2", rowExistenceColumn)] = true
		for _, column := range table.def.Columns {
			allowed[cellKey(table.storageName, "2", column.Name)] = true
		}
	}
	for _, write := range logs[0].Writes {
		if !allowed[write.Key] {
			t.Fatalf("failed statement leaked publication: %+v", write)
		}
	}
	semanticWant(t, r.compare(t, "SELECT id FROM children WHERE parent_id=2 ORDER BY id", true, ""), semanticInts(2))
}

func semanticJSON(value interface{}) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func semanticTable(t *testing.T, f *sqlTableFixture, name string) *Table {
	t.Helper()
	db, err := f.provider.Database(f.ctx, "semanticdb")
	if err != nil {
		t.Fatal(err)
	}
	raw, found, err := db.GetTableInsensitive(f.ctx, name)
	if err != nil || !found {
		t.Fatalf("table %s: %v", name, err)
	}
	return raw.(*Table)
}

// Explicit universes include absent point probes: knownKeys alone would erase
// the initial-existence observation for an empty-table point lookup.
func semanticUniverse(t *testing.T, keys map[string][]string) func(*Table) []string {
	t.Helper()
	return func(table *Table) []string {
		declared, ok := keys[table.def.Name]
		if !ok {
			t.Fatalf("undeclared full-scan table %s", table.def.Name)
		}
		out := append([]string(nil), declared...)
		sort.Strings(out)
		for _, key := range table.knownKeys(nil) {
			position := sort.SearchStrings(out, key)
			if position == len(out) || out[position] != key {
				t.Fatalf("physical key %q missing from declared universe %v", key, out)
			}
		}
		return out
	}
}

func TestSQLSemanticMatrixCandidateUniverse(t *testing.T) {
	f := newSQLTableFixture(t)
	for _, query := range []string{"CREATE DATABASE semanticdb", "USE semanticdb", "CREATE TABLE entries(id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,INDEX by_pair(a,b))", "INSERT INTO entries VALUES(1,1,0),(2,2,2)", "UPDATE entries SET a=0,b=1 WHERE id=1"} {
		f.query(t, query)
	}
	db, err := f.provider.Database(f.ctx, "semanticdb")
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := db.GetTableInsensitive(f.ctx, "entries")
	if err != nil {
		t.Fatal(err)
	}
	table := raw.(*Table)
	universe := semanticUniverse(t, map[string][]string{"entries": {"1", "2", "9"}})
	if got := universe(table); !reflect.DeepEqual(got, []string{"1", "2", "9"}) {
		t.Fatal(got)
	}
	for _, test := range []struct {
		index  string
		ranges sql.MySQLRangeCollection
		want   []string
	}{
		{"primary", sql.MySQLRangeCollection{{sql.ClosedRangeColumnExpr(int64(9), int64(9), types.Int64)}}, []string{"9"}},
		{"by_pair", sql.MySQLRangeCollection{{sql.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64), sql.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64)}}, []string{"1"}},
		{"by_pair", sql.MySQLRangeCollection{{sql.ClosedRangeColumnExpr(int64(2), int64(2), types.Int64), sql.ClosedRangeColumnExpr(int64(2), int64(2), types.Int64)}}, []string{"2"}},
	} {
		index := table.state.indexes[test.index]
		got, err := table.candidateKeys(f.ctx, index, test.ranges)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("hand-declared directory candidates %s: %v %v; want %v", test.index, got, err, test.want)
		}
		f.store.candidateKeysForTesting = universe
		lookup := sql.NewIndexLookup(index, test.ranges, false, false, false, false)
		parts, err := table.IndexedAccess(f.ctx, lookup).LookupPartitions(f.ctx, lookup)
		if err != nil {
			t.Fatal(err)
		}
		part, nextErr := parts.Next(f.ctx)
		closeErr := parts.Close(f.ctx)
		if nextErr != nil || closeErr != nil {
			t.Fatalf("partition: %v %v", nextErr, closeErr)
		}
		if got := part.(*lookupPartition).keys; !reflect.DeepEqual(got, []string{"1", "2", "9"}) {
			t.Fatalf("hook was not used: %v", got)
		}
		f.store.candidateKeysForTesting = nil
	}
}
