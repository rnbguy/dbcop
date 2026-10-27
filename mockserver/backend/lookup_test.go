package backend

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

func TestCompositePrefixLookupBoundsDetailRows(t *testing.T) {
	table, _, ctx := stagedTable(t, TableDef{Name: "details", PkeyIdxs: []int{0, 1, 2, 3},
		Columns: []*sql.Column{intCol("tenant"), intCol("group_id"), intCol("record_id"), intCol("sequence"), intCol("payload")}})
	writer := table.Inserter(ctx)
	for tenant := range 2 {
		for group := range 2 {
			for record := range 4 {
				for sequence := range 6 {
					row := sql.Row{int64(tenant + 1), int64(group + 1), int64(record + 1), int64(sequence + 1), int64(99)}
					if err := writer.Insert(ctx, row); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	indexes, _ := table.GetIndexes(ctx)
	ranges := sql.MySQLRangeCollection{{
		sql.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64),
		sql.ClosedRangeColumnExpr(int64(2), int64(2), types.Int64),
		sql.ClosedRangeColumnExpr(int64(3), int64(3), types.Int64),
		sql.AllRangeColumnExpr(types.Int64),
	}}
	lookup := sql.NewIndexLookup(indexes[0], ranges, false, false, false, false)
	indexed := table.WithProjections([]string{"payload"}).(*Table).IndexedAccess(ctx, lookup)
	partitions, err := indexed.LookupPartitions(ctx, lookup)
	if err != nil {
		t.Fatal(err)
	}
	defer partitions.Close(ctx)
	partition, err := partitions.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if count := len(partition.(*lookupPartition).keys); count != 6 {
		t.Fatalf("three-column prefix produced %d candidates, want exactly six details", count)
	}
	iter, err := indexed.PartitionRows(ctx, partition)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := sql.RowIterToRows(ctx, iter)
	if err != nil || len(rows) != 6 {
		t.Fatalf("bounded detail lookup: rows=%v err=%v", rows, err)
	}
}

func TestNullableLookupKeepsInitialAndStoredNullCandidates(t *testing.T) {
	table, _, ctx := stagedTable(t, TableDef{Name: "nullable_records", PkeyIdx: 0,
		Columns: []*sql.Column{intCol("id"), intCol("value")}})
	if err := table.CreateIndex(ctx, sql.IndexDef{Name: "by_value", Columns: []sql.IndexColumn{{Name: "value"}}}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []sql.Row{{int64(1), nil}, {int64(2), int64(7)}} {
		if err := table.Inserter(ctx).Insert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	indexes, _ := table.GetIndexes(ctx)
	var index sql.Index
	for _, candidate := range indexes {
		if candidate.ID() == "by_value" {
			index = candidate
		}
	}
	ranges := sql.MySQLRangeCollection{{sql.NullRangeColumnExpr(types.Int64)}}
	lookup := sql.NewIndexLookup(index, ranges, false, false, false, false)
	indexed := table.IndexedAccess(ctx, lookup)
	parts, err := indexed.LookupPartitions(ctx, lookup)
	if err != nil {
		t.Fatal(err)
	}
	defer parts.Close(ctx)
	part, err := parts.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if keys := part.(*lookupPartition).keys; len(keys) != 2 {
		t.Fatalf("initial NULL possibility was pruned: %v", keys)
	}
	iter, err := indexed.PartitionRows(ctx, part)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := sql.RowIterToRows(ctx, iter)
	if err != nil || len(rows) != 1 || rows[0][0] != int64(1) {
		t.Fatalf("stored NULL was not distinguished from selected non-NULL: rows=%v err=%v", rows, err)
	}
}

func BenchmarkCompositePrefixLookup(b *testing.B) {
	store := NewStore(1, 2)
	b.Cleanup(store.Close)
	table := NewTable(TableDef{Name: "details", PkeyIdxs: []int{0, 1, 2, 3},
		Columns: []*sql.Column{intCol("tenant"), intCol("group_id"), intCol("record_id"), intCol("sequence")}}, store, ReadCommitted)
	ctx := sql.NewEmptyContext()
	for record := range 500 {
		for sequence := range 10 {
			row := sql.Row{int64(1), int64(1), int64(record + 1), int64(sequence + 1)}
			key := table.pkeyOf(row)
			table.state.keys[key] = struct{}{}
			if err := table.addIndexRow(ctx, key, row); err != nil {
				b.Fatal(err)
			}
		}
	}
	indexes, _ := table.GetIndexes(ctx)
	index := indexes[0].(*tableIndex)
	for _, prefix := range []int{1, 3} {
		b.Run(fmt.Sprintf("EqualColumns%d", prefix), func(b *testing.B) {
			bounds := sql.MySQLRange{sql.AllRangeColumnExpr(types.Int64), sql.AllRangeColumnExpr(types.Int64), sql.AllRangeColumnExpr(types.Int64), sql.AllRangeColumnExpr(types.Int64)}
			for i := range prefix {
				bounds[i] = sql.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				keys, err := table.candidateKeys(ctx, index, sql.MySQLRangeCollection{bounds})
				if err != nil {
					b.Fatal(err)
				}
				if len(keys) != 5000 && prefix == 1 || len(keys) != 10 && prefix == 3 {
					b.Fatalf("unexpected bounded candidate count %d", len(keys))
				}
			}
		})
	}
}

func TestCompositeJoinWorkIsBoundedByInputRows(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE join_work")
	f.query(t, "USE join_work")
	f.query(t, "CREATE TABLE parents (id INT NOT NULL, bucket INT NOT NULL, tenant INT NOT NULL, status INT, PRIMARY KEY(tenant,bucket,id))")
	f.query(t, "CREATE TABLE children (parent_id INT NOT NULL, bucket INT NOT NULL, tenant INT NOT NULL, sequence INT NOT NULL, delivered DATETIME, PRIMARY KEY(tenant,bucket,parent_id,sequence))")
	var parents, children []string
	for tenant := range 2 {
		for bucket := range 2 {
			for id := range 4 {
				status := "NULL"
				if id == 1 {
					status = "1"
				}
				parents = append(parents, fmt.Sprintf("(%d,%d,%d,%s)", id+1, bucket+1, tenant+1, status))
				for sequence := range 6 {
					children = append(children, fmt.Sprintf("(%d,%d,%d,%d,NULL)", id+1, bucket+1, tenant+1, sequence+1))
				}
			}
		}
	}
	f.query(t, "INSERT INTO parents VALUES "+strings.Join(parents, ","))
	f.query(t, "INSERT INTO children VALUES "+strings.Join(children, ","))
	before := len(f.store.Admissions())
	rows := f.query(t, `SELECT COUNT(*) FROM parents, children
		WHERE parents.id=children.parent_id AND parents.bucket=children.bucket
		AND parents.tenant=children.tenant AND parents.tenant=1
		AND ((children.delivered IS NULL AND parents.status IS NOT NULL)
			OR (parents.status IS NULL AND children.delivered IS NOT NULL))`)
	if !reflect.DeepEqual(rows, []sql.Row{{int64(12)}}) {
		t.Fatalf("composite join returned wrong matching detail count: %v", rows)
	}
	reads := len(f.store.Admissions()) - before
	// Permit a hash scan or bounded per-detail point lookups, but not a full
	// parent-prefix scan repeated for every child. No optimizer plan is pinned.
	bound := 12 * (len(parents) + len(children))
	if reads == 0 || reads > bound {
		t.Fatalf("join performed %d oracle reads; input-linear bound is %d", reads, bound)
	}
}
