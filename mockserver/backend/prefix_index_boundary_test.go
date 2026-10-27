package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

func TestUniquePrefixCoercionAndRejectedInsertPreserveRows(t *testing.T) {
	for _, test := range []struct {
		name string
		typ  sql.Type
	}{
		{"text", types.LongText},
		{"binary", types.LongBlob},
	} {
		t.Run(test.name, func(t *testing.T) {
			table, ctx, _ := prefixTestTable(t, test.typ, 3, true)
			session := ctx.Session.(*Session)
			t.Cleanup(func() {
				session.SessionEnd()
				session.store.Close()
			})
			writer := table.Inserter(ctx)
			if err := writer.Insert(ctx, sql.Row{int64(1), int64(12345), int64(1)}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Insert(ctx, sql.Row{int64(2), "12399", int64(1)}); !sql.ErrUniqueKeyViolation.Is(err) {
				t.Fatalf("coerced numeric prefix did not conflict with text: %v", err)
			}
			if err := writer.Insert(ctx, sql.Row{int64(3), struct{}{}, int64(1)}); err == nil {
				t.Fatal("unsupported prefix value was accepted")
			}
			if err := writer.Insert(ctx, sql.Row{int64(4), "12400", int64(1)}); err != nil {
				t.Fatal(err)
			}
			want := []sql.Row{{int64(1), int64(12345), int64(1)}, {int64(4), "12400", int64(1)}}
			if rows := tableRows(t, ctx, table); !reflect.DeepEqual(rows, want) {
				t.Fatalf("rejected prefix inserts changed retained rows: %v, want %v", rows, want)
			}
		})
	}
}

func TestPrefixRangesRejectNonPointAndMalformedBounds(t *testing.T) {
	table, ctx, index := prefixTestTable(t, types.LongText, 1, false)
	session := ctx.Session.(*Session)
	t.Cleanup(func() {
		session.SessionEnd()
		session.store.Close()
	})
	for _, bounds := range []sql.MySQLRange{
		{sql.ClosedRangeColumnExpr("apple", "apricot", types.LongText)},
		{sql.ClosedRangeColumnExpr(struct{}{}, "invalid", types.LongText)},
		{sql.ClosedRangeColumnExpr("apple", "apple", types.LongText),
			sql.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64),
			sql.ClosedRangeColumnExpr(int64(2), int64(2), types.Int64)},
	} {
		if index.CanSupport(ctx, bounds) {
			t.Fatalf("prefix index promised an unsupported or malformed range: %v", bounds)
		}
	}
	if err := table.Inserter(ctx).Insert(ctx, sql.Row{int64(1), "apple", int64(1)}); err != nil {
		t.Fatal(err)
	}
	if rows := tableRows(t, ctx, table); !reflect.DeepEqual(rows, []sql.Row{{int64(1), "apple", int64(1)}}) {
		t.Fatalf("rejected range planning changed readable rows: %v", rows)
	}
}
