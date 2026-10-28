package backend

import (
	"context"
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/memory"
	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
)

func tableAPIFixture(t *testing.T, create, insert string) (*sqlTableFixture, *Table) {
	t.Helper()
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	t.Cleanup(f.store.Close)
	f.query(t, "CREATE DATABASE storage_api")
	f.query(t, "USE storage_api")
	f.query(t, create)
	f.query(t, insert)
	db, err := f.provider.Database(f.ctx, "storage_api")
	if err != nil {
		t.Fatal(err)
	}
	raw, found, err := db.GetTableInsensitive(f.ctx, "records")
	if err != nil || !found {
		t.Fatalf("fixture table: found=%v err=%v", found, err)
	}
	return f, raw.(*Table)
}

func tableAPIIndex(t *testing.T, ctx *sql.Context, table sql.IndexAddressable, name string) sql.Index {
	t.Helper()
	indexes, err := table.GetIndexes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range indexes {
		if index.ID() == name {
			return index
		}
	}
	t.Fatalf("missing index %s", name)
	return nil
}

func tableAPILookupRows(t *testing.T, ctx *sql.Context, table sql.IndexedTable, lookup sql.IndexLookup) []sql.Row {
	t.Helper()
	partitions, err := table.LookupPartitions(ctx, lookup)
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

func TestTableAPIIndexedProjectionRetainsRangeAndResidual(t *testing.T) {
	f, table := tableAPIFixture(t,
		"CREATE TABLE records (tenant BIGINT, id BIGINT, tag BIGINT, value BIGINT, secret BIGINT, PRIMARY KEY(tenant,id), INDEX by_tag(tag))",
		"INSERT INTO records VALUES (1,1,NULL,10,91),(1,2,7,20,92),(1,3,NULL,30,93),(2,1,NULL,40,94)")
	lookup := sql.NewIndexLookup(tableAPIIndex(t, f.ctx, table, "PRIMARY"), sql.MySQLRangeCollection{{
		sql.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64),
		sql.ClosedRangeColumnExpr(int64(1), int64(2), types.Int64),
	}}, false, false, false, false)
	indexed := table.IndexedAccess(f.ctx, lookup)
	filter := expression.NewNullSafeEquals(expression.NewGetFieldWithTable(2, 0, types.Int64, "storage_api", "records", "tag", true), expression.NewLiteral(nil, types.Null))
	filtered := indexed.(sql.FilteredTable).WithFilters(f.ctx, []sql.Expression{filter})
	projected := filtered.(sql.ProjectedTable).WithProjections([]string{"value", "tenant"})
	if schema := projected.Schema(); len(schema) != 2 || schema[0].Name != "value" || schema[1].Name != "tenant" {
		t.Fatalf("projection schema disagrees with requested row order: %v", schema)
	}
	before := len(f.store.Admissions())
	if rows := tableAPILookupRows(t, f.ctx, projected.(sql.IndexedTable), lookup); !reflect.DeepEqual(rows, []sql.Row{{int64(10), int64(1)}}) {
		t.Fatalf("projection lost range or NULL residual: %v", rows)
	}
	reads := f.store.Admissions()[before:]
	wantKeys := map[string]bool{}
	for _, column := range []string{rowExistenceColumn, "tenant", "id", "tag", "value"} {
		wantKeys[cellKey(table.storageName, "1#1", column)] = true
	}
	for _, column := range []string{rowExistenceColumn, "tenant", "id", "tag"} {
		wantKeys[cellKey(table.storageName, "1#2", column)] = true
	}
	for _, read := range reads {
		if !wantKeys[read.Key] {
			t.Fatalf("projection oracle-read an unrelated row or column: %+v", read)
		}
		delete(wantKeys, read.Key)
	}
	if len(wantKeys) != 0 {
		t.Fatalf("range, residual, or projected values were not oracle-read: %v", wantKeys)
	}
	// Empty projections still spool matching rows, without fetching payload cells.
	empty := filtered.(sql.ProjectedTable).WithProjections([]string{})
	before = len(f.store.Admissions())
	if rows := tableAPILookupRows(t, f.ctx, empty.(sql.IndexedTable), lookup); !reflect.DeepEqual(rows, []sql.Row{{}}) || len(empty.Schema()) != 0 {
		t.Fatalf("empty projection changed row cardinality: %v", rows)
	}
	for _, read := range f.store.Admissions()[before:] {
		if read.Key == cellKey(table.storageName, "1#1", "value") || read.Key == cellKey(table.storageName, "1#1", "secret") {
			t.Fatalf("empty projection fetched payload: %+v", read)
		}
	}
	unfiltered := projected.(sql.FilteredTable).WithFilters(f.ctx, nil)
	if rows := tableAPILookupRows(t, f.ctx, unfiltered.(sql.IndexedTable), lookup); !reflect.DeepEqual(rows, []sql.Row{{int64(10), int64(1)}, {int64(20), int64(1)}}) {
		t.Fatalf("clearing residual lost index range or projection: %v", rows)
	}
	if rows := tableAPILookupRows(t, f.ctx, projected.(sql.IndexedTable), lookup); !reflect.DeepEqual(rows, []sql.Row{{int64(10), int64(1)}}) {
		t.Fatalf("filter/projection copies mutated their source: %v", rows)
	}
}

func TestTableAPIFilterNegotiationAndCompositePoint(t *testing.T) {
	f, table := tableAPIFixture(t,
		"CREATE TABLE records (tenant BIGINT, id BIGINT, value BIGINT, PRIMARY KEY(tenant,id))",
		"INSERT INTO records VALUES (1,1,10),(1,2,20),(2,1,30)")
	field := func(tableName, column string, ordinal int) sql.Expression {
		return expression.NewGetFieldWithTable(ordinal, 0, types.Int64, "storage_api", tableName, column, false)
	}
	filters := []sql.Expression{
		expression.NewEquals(expression.NewLiteral(int64(1), types.Int64), field("records", "tenant", 0)),
		expression.NewEquals(field("records", "id", 1), expression.NewLiteral(int64(2), types.Int64)),
	}
	declined := []sql.Expression{
		expression.NewEquals(field("other_table", "id", 1), expression.NewLiteral(int64(1), types.Int64)),
		expression.NewEquals(field("records", "id", 1), field("records", "tenant", 0)),
		expression.NewEquals(field("records", "missing", 1), expression.NewLiteral(int64(1), types.Int64)),
		expression.NewEquals(field("records", "value", 2), expression.NewLiteral(int64(20), types.Int64)),
	}
	handled := table.HandledFilters(append(append([]sql.Expression{}, filters...), declined...))
	if !reflect.DeepEqual(handled, filters) {
		t.Fatalf("negotiated filters outside this table's indexed literal comparisons: %v", handled)
	}
	selected := table.WithFilters(f.ctx, handled).(sql.ProjectedTable).WithProjections([]string{"value"})
	before := len(f.store.Admissions())
	if rows := tableRows(t, f.ctx, selected); !reflect.DeepEqual(rows, []sql.Row{{int64(20)}}) {
		t.Fatalf("composite point filters lost reversed literal comparison: %v", rows)
	}
	for _, read := range f.store.Admissions()[before:] {
		allowed := false
		for _, column := range []string{rowExistenceColumn, "tenant", "id", "value"} {
			allowed = allowed || read.Key == cellKey(table.storageName, "1#2", column)
		}
		if !allowed {
			t.Fatalf("composite point filter read outside its physical identity: %+v", read)
		}
	}
}

func TestTableAPIRejectedIndexesPreserveConstraintsAndRows(t *testing.T) {
	f, table := tableAPIFixture(t,
		"CREATE TABLE records (id BIGINT PRIMARY KEY, value BIGINT, UNIQUE KEY by_value(value), INDEX by_id(id))",
		"INSERT INTO records VALUES (1,10),(2,20)")
	alter := sql.IndexAlterableTable(table)
	indexes, err := table.GetIndexes(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		run  func() error
		is   func(error) bool
	}{
		{"drop missing", func() error { return alter.DropIndex(f.ctx, "absent") }, sql.ErrIndexNotFound.Is},
		{"drop primary", func() error { return alter.DropIndex(f.ctx, "pRiMaRy") }, nil},
		{"rename missing", func() error { return alter.RenameIndex(f.ctx, "absent", "new_name") }, sql.ErrIndexNotFound.Is},
		{"rename primary", func() error { return alter.RenameIndex(f.ctx, "PRIMARY", "new_name") }, nil},
		{"rename to primary", func() error { return alter.RenameIndex(f.ctx, "by_value", "PRIMARY") }, nil},
		{"rename duplicate", func() error { return alter.RenameIndex(f.ctx, "by_value", "BY_ID") }, sql.ErrDuplicateKey.Is},
		{"unknown column", func() error {
			return alter.CreateIndex(f.ctx, sql.IndexDef{Name: "bad", Columns: []sql.IndexColumn{{Name: "absent"}}})
		}, nil},
		{"empty columns", func() error { return alter.CreateIndex(f.ctx, sql.IndexDef{Name: "bad"}) }, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); err == nil || test.is != nil && !test.is(err) {
				t.Fatalf("rejected index request returned %v", err)
			}
			after, err := table.GetIndexes(f.ctx)
			if err != nil || !reflect.DeepEqual(after, indexes) {
				t.Fatalf("rejected request changed indexes: %v, %v", after, err)
			}
			f.query(t, "BEGIN")
			writer := table.Inserter(f.ctx)
			writer.StatementBegin(f.ctx)
			if err := writer.Insert(f.ctx, sql.Row{int64(3), int64(10)}); !sql.ErrUniqueKeyViolation.Is(err) {
				t.Fatalf("rejected DDL lost unique enforcement: %v", err)
			}
			if err := writer.DiscardChanges(f.ctx, context.Canceled); err != nil {
				t.Fatal(err)
			}
			f.query(t, "COMMIT")
			if rows := f.query(t, "SELECT id,value FROM records WHERE value>=10 ORDER BY id"); !reflect.DeepEqual(rows, []sql.Row{{int64(1), int64(10)}, {int64(2), int64(20)}}) {
				t.Fatalf("rejected DDL or insert changed indexed data: %v", rows)
			}
		})
	}
}

func TestTableAPILookupRejectionsAndNumericBoundaries(t *testing.T) {
	f, table := tableAPIFixture(t,
		"CREATE TABLE records (id TINYINT PRIMARY KEY, value BIGINT)",
		"INSERT INTO records VALUES (-128,10),(127,20)")
	primary := tableAPIIndex(t, f.ctx, table, "PRIMARY")
	lookup := sql.NewIndexLookup(primary, sql.MySQLRangeCollection{{sql.AllRangeColumnExpr(types.Int8)}}, false, false, false, false)
	indexed := table.IndexedAccess(f.ctx, lookup)
	for _, test := range []struct {
		name   string
		lookup sql.IndexLookup
	}{
		// A real upstream index is a valid SDK value, but is not this backend's index.
		{"foreign index", sql.IndexLookup{Index: &memory.Index{TableName: "records", Name: "external"}, Ranges: lookup.Ranges}},
		{"missing range collection", sql.IndexLookup{Index: primary}},
		{"too many columns", sql.NewIndexLookup(primary, sql.MySQLRangeCollection{{sql.AllRangeColumnExpr(types.Int8), sql.AllRangeColumnExpr(types.Int8)}}, false, false, false, false)},
		{"invalid unsigned conversion", sql.NewIndexLookup(primary, sql.MySQLRangeCollection{{sql.ClosedRangeColumnExpr("not-a-number", "not-a-number", types.Uint64)}}, false, false, false, false)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "foreign index" || test.name == "missing range collection" {
				if got := table.IndexedAccess(f.ctx, test.lookup); got != nil {
					t.Fatalf("unsupported lookup advertised indexed access: %T", got)
				}
			}
			before := len(f.store.Admissions())
			if partitions, err := indexed.LookupPartitions(f.ctx, test.lookup); err == nil {
				_ = partitions.Close(f.ctx)
				t.Fatal("invalid lookup was accepted")
			}
			if len(f.store.Admissions()) != before {
				t.Fatal("rejected range admitted workload reads")
			}
		})
	}
	outside := sql.NewIndexLookup(primary, sql.MySQLRangeCollection{{sql.ClosedRangeColumnExpr(int64(128), int64(128), types.Int64)}}, false, false, false, false)
	if rows := tableAPILookupRows(t, f.ctx, indexed, outside); len(rows) != 0 {
		t.Fatalf("out-of-range primary key aliased a boundary row: %v", rows)
	}
	if rows := tableAPILookupRows(t, f.ctx, indexed, lookup); !reflect.DeepEqual(rows, []sql.Row{{int8(-128), int64(10)}, {int8(127), int64(20)}}) {
		t.Fatalf("rejected lookups changed boundary rows: %v", rows)
	}
	f.query(t, "ROLLBACK")
	f.query(t, "UPDATE records SET id=-127,value=11 WHERE id=-128")
	f.query(t, "DELETE FROM records WHERE id=127")
	if rows := f.query(t, "SELECT id,value FROM records ORDER BY id"); !reflect.DeepEqual(rows, []sql.Row{{int8(-127), int64(11)}}) {
		t.Fatalf("boundary move/delete damaged membership or cells: %v", rows)
	}
}

func TestTableAPIRejectedRowShapesRetainEarlierStatement(t *testing.T) {
	f, table := tableAPIFixture(t,
		"CREATE TABLE records (id BIGINT PRIMARY KEY, value BIGINT)",
		"INSERT INTO records VALUES (1,10)")
	before := len(f.store.logSnapshot())
	f.query(t, "BEGIN")
	f.query(t, "INSERT INTO records VALUES (2,20)")
	insert, update, deleteRow := table.Inserter(f.ctx), table.Updater(f.ctx), table.Deleter(f.ctx)
	for _, test := range []struct {
		name   string
		editor sql.EditOpenerCloser
		run    func() error
	}{
		{"insert short row", insert, func() error { return insert.Insert(f.ctx, sql.Row{int64(3)}) }},
		{"update short old row", update, func() error { return update.Update(f.ctx, sql.Row{int64(1)}, sql.Row{int64(1), int64(99)}) }},
		{"update short new row", update, func() error { return update.Update(f.ctx, sql.Row{int64(1), int64(10)}, sql.Row{int64(1)}) }},
		{"delete short row", deleteRow, func() error { return deleteRow.Delete(f.ctx, sql.Row{int64(1)}) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.editor.StatementBegin(f.ctx)
			if err := test.run(); err == nil {
				t.Fatal("malformed row accepted")
			} else if err := test.editor.DiscardChanges(f.ctx, err); err != nil {
				t.Fatal(err)
			}
			if rows := tableRows(t, f.ctx, table); !reflect.DeepEqual(rows, []sql.Row{{int64(1), int64(10)}, {int64(2), int64(20)}}) {
				t.Fatalf("failed statement changed rows or discarded its predecessor: %v", rows)
			}
		})
	}
	f.query(t, "COMMIT")
	writes := 0
	for _, txn := range f.store.logSnapshot()[before:] {
		for _, write := range txn.Writes {
			writes++
			if write.Key != cellKey(table.storageName, "2", rowExistenceColumn) && write.Key != cellKey(table.storageName, "2", "id") && write.Key != cellKey(table.storageName, "2", "value") {
				t.Fatalf("rejected row callback published a write: %+v", write)
			}
		}
	}
	if writes != 3 {
		t.Fatalf("earlier insert should publish exactly membership and two cells, got %d writes", writes)
	}
	if rows := f.query(t, "SELECT id,value FROM records ORDER BY id"); !reflect.DeepEqual(rows, []sql.Row{{int64(1), int64(10)}, {int64(2), int64(20)}}) {
		t.Fatalf("statement restoration did not survive commit: %v", rows)
	}
}
