package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/expression"
	"github.com/dolthub/go-mysql-server/sql/types"
)

func prefixTestTable(t *testing.T, typ sql.Type, length int64, unique bool) (*Table, *sql.Context, *tableIndex) {
	t.Helper()
	table, _, ctx := stagedTable(t, TableDef{Name: "prefix_values", PkeyIdx: 0,
		Columns: []*sql.Column{intCol("id"), {Name: "value", Type: typ, Nullable: true}, intCol("version")}})
	def := sql.IndexDef{Name: "by_prefix", Columns: []sql.IndexColumn{{Name: "value", Length: length}, {Name: "version"}}}
	if unique {
		def.Constraint = sql.IndexConstraint_Unique
	}
	if err := table.CreateIndex(ctx, def); err != nil {
		t.Fatal(err)
	}
	return table, ctx, table.state.indexes["by_prefix"]
}

func TestUniquePrefixUsesCharactersOrBinaryBytesAndCollation(t *testing.T) {
	text, err := types.LongText.(sql.TypeWithCollation).WithNewCollation(sql.Collation_utf8mb4_bin)
	if err != nil {
		t.Fatal(err)
	}
	folded, err := types.LongText.(sql.TypeWithCollation).WithNewCollation(sql.Collation_utf8mb4_0900_ai_ci)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		typ       sql.Type
		length    int64
		first     interface{}
		duplicate interface{}
		distinct  interface{}
	}{
		{"ascii", text, 3, "abc-one", "abc-two", "abd-one"},
		{"unicode_characters", text, 1, "\u00e9-one", "\u00e9-two", "\u00ea-one"},
		{"supplementary_character", text, 1, "\U0001f600-one", "\U0001f600-two", "\U0001f601-one"},
		{"replacement_character", text, 1, "\ufffd-one", "\ufffd-two", "x-one"},
		{"case_and_accent_collation", folded, 1, "\u00c9-one", "e-two", "f-one"},
		{"binary_bytes", types.LongBlob, 1, []byte("\u00e9-one"), []byte("\u00ea-two"), []byte("x-one")},
		{"binary_nul", types.LongBlob, 2, []byte{0, 255, 1}, []byte{0, 255, 2}, []byte{0, 254, 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			table, ctx, index := prefixTestTable(t, test.typ, test.length, true)
			writer := table.Inserter(ctx)
			if err := writer.Insert(ctx, sql.Row{int64(1), test.first, int64(1)}); err != nil {
				t.Fatal(err)
			}
			if err := writer.Insert(ctx, sql.Row{int64(2), test.duplicate, int64(1)}); !sql.ErrUniqueKeyViolation.Is(err) {
				t.Fatalf("same prefix with different trailing bytes must conflict: %v", err)
			}
			if err := writer.Insert(ctx, sql.Row{int64(3), test.distinct, int64(1)}); err != nil {
				t.Fatalf("distinct prefix must remain valid: %v", err)
			}
			if err := writer.Insert(ctx, sql.Row{int64(4), test.duplicate, int64(2)}); err != nil {
				t.Fatalf("other composite key column must distinguish prefixes: %v", err)
			}
			for _, id := range []int64{5, 6} {
				if err := writer.Insert(ctx, sql.Row{id, nil, int64(1)}); err != nil {
					t.Fatalf("nullable unique prefix rejected NULL: %v", err)
				}
			}
			ranges := sql.MySQLRangeCollection{{sql.ClosedRangeColumnExpr(test.first, test.first, test.typ),
				sql.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64)}}
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
			iter, err := indexed.PartitionRows(ctx, part)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := sql.RowIterToRows(ctx, iter)
			if err != nil || !reflect.DeepEqual(rows, []sql.Row{{int64(1), test.first, int64(1)}}) {
				t.Fatalf("prefix equality lost its full matching row: %v, %v", rows, err)
			}
			if err := table.Updater(ctx).Update(ctx,
				sql.Row{int64(3), test.distinct, int64(1)}, sql.Row{int64(3), test.duplicate, int64(1)}); !sql.ErrUniqueKeyViolation.Is(err) {
				t.Fatalf("update must enforce prefix uniqueness: %v", err)
			}
		})
	}
}

func TestCreateUniquePrefixValidatesExistingRows(t *testing.T) {
	table, _, ctx := stagedTable(t, TableDef{Name: "existing_prefixes", PkeyIdx: 0,
		Columns: []*sql.Column{intCol("id"), {Name: "value", Type: types.LongText}}})
	for _, row := range []sql.Row{{int64(1), "same-one"}, {int64(2), "same-two"}} {
		if err := table.Inserter(ctx).Insert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	def := sql.IndexDef{Name: "unique_prefix", Constraint: sql.IndexConstraint_Unique,
		Columns: []sql.IndexColumn{{Name: "value", Length: 4}}}
	if err := table.CreateIndex(ctx, def); !sql.ErrUniqueKeyViolation.Is(err) {
		t.Fatalf("building unique index must reject existing duplicate prefixes: %v", err)
	}
	if table.state.indexes["unique_prefix"] != nil {
		t.Fatal("failed unique build published its index")
	}
}

func TestPrefixLookupRetainsFullBoundariesAndHistoricalCells(t *testing.T) {
	table, ctx, index := prefixTestTable(t, types.LongText, 1, false)
	old := sql.Row{int64(1), "apple", int64(2)}
	updated := sql.Row{int64(1), "berry", int64(1)}
	for _, row := range []sql.Row{old, {int64(2), "applepie", int64(1)}, {int64(3), "apricot", int64(1)}} {
		if err := table.Inserter(ctx).Insert(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := table.Updater(ctx).Update(ctx, old, updated); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		bound     sql.MySQLRangeColumnExpr
		keys      []string
		supported bool
	}{
		{"point", sql.ClosedRangeColumnExpr("apple", "apple", types.LongText), []string{"1"}, true},
		{"exclusive_boundary", sql.GreaterThanRangeColumnExpr("apple", types.LongText), []string{"1", "2", "3"}, false},
		{"within_prefix", sql.OpenRangeColumnExpr("apple", "apricot", types.LongText), []string{"2"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			bounds := sql.MySQLRange{test.bound, sql.ClosedRangeColumnExpr(int64(1), int64(1), types.Int64)}
			if got := index.CanSupport(ctx, bounds); got != test.supported {
				t.Fatalf("range support = %v, want %v", got, test.supported)
			}
			keys, err := table.candidateKeys(ctx, index, sql.MySQLRangeCollection{bounds})
			if err != nil || !reflect.DeepEqual(keys, test.keys) {
				t.Fatalf("lost full-value or independently historical candidates: keys=%v err=%v", keys, err)
			}
		})
	}
	field := expression.NewGetField(1, types.LongText, "value", true)
	filter := expression.NewEquals(field, expression.NewLiteral("berry", types.LongText))
	pushed := table.WithFilters(ctx, []sql.Expression{filter}).(*Table)
	if rows := tableRows(t, ctx, pushed); !reflect.DeepEqual(rows, []sql.Row{updated}) {
		t.Fatalf("prefix postings or residual filtering lost exact lookup: %v", rows)
	}
}

func TestPrefixRejectsMalformedUnicodeButAcceptsBinary(t *testing.T) {
	table, ctx, index := prefixTestTable(t, types.LongText, 1, false)
	if _, err := table.indexValue(ctx, index, 0, string([]byte{0xff, 'x'})); err == nil {
		t.Fatal("malformed UTF-8 prefix was silently accepted")
	}
	binary, binaryCtx, binaryIndex := prefixTestTable(t, types.LongBlob, 1, false)
	if got, err := binary.indexValue(binaryCtx, binaryIndex, 0, []byte{0xff, 'x'}); err != nil || got != string([]byte{0xff}) {
		t.Fatalf("binary prefix must preserve arbitrary bytes: %v, %v", got, err)
	}
}

func TestPrefixDefinitionsRejectInvalidLengths(t *testing.T) {
	table, _, ctx := stagedTable(t, TableDef{Name: "invalid_prefixes", PkeyIdx: 0,
		Columns: []*sql.Column{intCol("id"), {Name: "value", Type: types.LongText}}})
	for _, column := range []sql.IndexColumn{{Name: "value", Length: -1}, {Name: "value", Length: 65536}, {Name: "id", Length: 1}} {
		if err := table.CreateIndex(ctx, sql.IndexDef{Name: "invalid", Columns: []sql.IndexColumn{column}}); !sql.ErrInvalidIndexPrefix.Is(err) {
			t.Fatalf("invalid prefix definition %v was not rejected: %v", column, err)
		}
	}
}

func TestSQLPrefixDDLAndForeignKeyIndexEligibility(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE prefix_ddl")
	f.query(t, "USE prefix_ddl")
	f.query(t, `CREATE TABLE strings (
		id INT PRIMARY KEY, label VARCHAR(64), body TEXT, bytes VARBINARY(64), payload BLOB,
		INDEX by_label(label(8)), INDEX by_body(body(16)), INDEX by_bytes(bytes(8)),
		UNIQUE INDEX by_payload(payload(16)))`)
	rows := f.query(t, "SELECT INDEX_NAME,SUB_PART FROM information_schema.statistics WHERE TABLE_SCHEMA='prefix_ddl' AND TABLE_NAME='strings' ORDER BY INDEX_NAME")
	want := []sql.Row{{"by_body", int64(16)}, {"by_bytes", int64(8)}, {"by_label", int64(8)}, {"by_payload", int64(16)}, {"PRIMARY", nil}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("native index metadata did not retain prefix lengths: %v", rows)
	}
	f.query(t, "CREATE TABLE parents (id INT PRIMARY KEY, label VARCHAR(64), INDEX by_label(label(8)))")
	if err := foreignKeyQueryError(f, "CREATE TABLE invalid_child (label VARCHAR(64), FOREIGN KEY (label) REFERENCES parents(label))"); err == nil {
		t.Fatal("a prefix-only parent index was accepted for a foreign key")
	}
	f.query(t, "CREATE TABLE children (id INT PRIMARY KEY, parent_id INT, label VARCHAR(64), INDEX by_label(label(8)), FOREIGN KEY (parent_id) REFERENCES parents(id))")
	f.query(t, "INSERT INTO parents VALUES (1,'parent')")
	f.query(t, "INSERT INTO children VALUES (1,1,'child')")
	if err := foreignKeyQueryError(f, "INSERT INTO children VALUES (2,99,'child')"); !sql.ErrForeignKeyChildViolation.Is(err) {
		t.Fatalf("prefix metadata interfered with native full-key foreign key enforcement: %v", err)
	}
}
