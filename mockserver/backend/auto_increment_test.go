package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
)

func TestAutoIncrementNativeExpressionsAndExplicitInserter(t *testing.T) {
	table, session, ctx := stagedTable(t, TableDef{Name: "generated", PkeyIdx: 0,
		Columns: []*sql.Column{{Name: "id", Type: types.Int64, AutoIncrement: true, PrimaryKey: true}}})
	if err := table.Inserter(ctx).Insert(ctx, sql.Row{int64(41)}); err != nil {
		t.Fatal(err)
	}
	if next, err := table.PeekNextAutoIncrementValue(ctx); err != nil || next != 42 {
		t.Fatalf("next ID after explicit insert = %d, %v; want 42", next, err)
	}
	eval := func(value interface{}, want int64) {
		t.Helper()
		expr, err := expression.NewAutoIncrement(ctx, table, expression.NewLiteral(value, types.Int64))
		if err != nil {
			t.Fatal(err)
		}
		got, err := expr.Eval(ctx, nil)
		if err != nil || got != want {
			t.Fatalf("AUTO_INCREMENT(%v) = %v, %v; want %v", value, got, err, want)
		}
	}
	eval(nil, 42)
	eval(int64(0), 43)
	eval(int64(-5), -5)
	if err := ctx.SetSessionVariable(ctx, "sql_mode", "NO_AUTO_VALUE_ON_ZERO"); err != nil {
		t.Fatal(err)
	}
	eval(int64(0), 0)
	eval(nil, 44)
	if err := session.Rollback(ctx, nil); err != nil {
		t.Fatal(err)
	}
	eval(nil, 45)
	eval(int64(90), 90)
	eval(nil, 91)
	setter := table.AutoIncrementSetter(ctx)
	if err := setter.SetAutoIncrementValue(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := setter.Close(ctx); err != nil {
		t.Fatal(err)
	}
	eval(nil, 100)
}

func TestSQLAutoIncrementGeneratedIDsAndRollbackGap(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE generated_ids")
	f.query(t, "USE generated_ids")
	f.query(t, "CREATE TABLE records (id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY, label VARCHAR(20))")
	f.query(t, "INSERT INTO records VALUES (41, 'loaded')")
	insert := func(query string, wantID, wantAffected uint64) {
		t.Helper()
		rows := f.query(t, query)
		if len(rows) != 1 || len(rows[0]) != 1 {
			t.Fatalf("%s returned %v, want native OK result", query, rows)
		}
		result, ok := rows[0][0].(types.OkResult)
		if !ok || result.InsertID != wantID || result.RowsAffected != wantAffected {
			t.Fatalf("%s returned %#v, want InsertID=%d RowsAffected=%d", query, rows[0][0], wantID, wantAffected)
		}
		if rows := f.query(t, "SELECT LAST_INSERT_ID()"); !reflect.DeepEqual(rows, []sql.Row{{wantID}}) {
			t.Fatalf("LAST_INSERT_ID() = %v, want %d", rows, wantID)
		}
	}
	insert("INSERT INTO records (label) VALUES ('generated')", 42, 1)
	insert("INSERT INTO records (label) VALUES ('first'), ('second')", 43, 2)
	f.query(t, "BEGIN")
	insert("INSERT INTO records (label) VALUES ('rolled back')", 45, 1)
	f.query(t, "ROLLBACK")
	insert("INSERT INTO records (label) VALUES ('after rollback')", 46, 1)
	want := []sql.Row{{uint64(41)}, {uint64(42)}, {uint64(43)}, {uint64(44)}, {uint64(46)}}
	if rows := f.query(t, "SELECT id FROM records ORDER BY id"); !reflect.DeepEqual(rows, want) {
		t.Fatalf("rollback must remove the row but retain its reserved ID: %v", rows)
	}
}

func TestAutoIncrementUint64ExhaustionDoesNotWrap(t *testing.T) {
	table, _, ctx := stagedTable(t, TableDef{Name: "generated", PkeyIdx: 0,
		Columns: []*sql.Column{{Name: "id", Type: types.Uint64, AutoIncrement: true, PrimaryKey: true}}})
	const max = ^uint64(0)
	if err := table.AutoIncrementSetter(ctx).SetAutoIncrementValue(ctx, max); err != nil {
		t.Fatal(err)
	}
	if got, err := table.GetNextAutoIncrementValue(ctx, nil); err != nil || got != max {
		t.Fatalf("final generated ID = %d, %v; want %d", got, err, max)
	}
	if _, err := table.GetNextAutoIncrementValue(ctx, nil); !sql.ErrValueOutOfRange.Is(err) {
		t.Fatalf("exhausted AUTO_INCREMENT must not wrap to zero: %v", err)
	}
}
