package backend

import (
	"reflect"
	"strings"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func lifecycleRows(t *testing.T, f *sqlTableFixture, query string, want []sql.Row) {
	t.Helper()
	got := f.query(t, query)
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: rows = %#v, want %#v", query, got, want)
	}
}

func TestSQLLifecycleIndexRenameAndDrop(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE index_lifecycle")
	f.query(t, "USE index_lifecycle")
	f.query(t, "CREATE TABLE entries (id INT PRIMARY KEY, label VARCHAR(16), n INT, UNIQUE INDEX by_label(label), INDEX by_n(n))")
	f.query(t, "INSERT INTO entries VALUES (1,'alpha',10),(2,'beta',20)")
	indexes := "SELECT INDEX_NAME FROM information_schema.statistics WHERE TABLE_SCHEMA='index_lifecycle' AND TABLE_NAME='entries' ORDER BY INDEX_NAME"
	initial := []sql.Row{{"by_label"}, {"by_n"}, {"PRIMARY"}}
	for _, query := range []string{
		"ALTER TABLE entries RENAME INDEX by_label TO by_n",
		"ALTER TABLE entries ADD INDEX by_n(n)",
	} {
		if err := foreignKeyQueryError(f, query); !sql.ErrDuplicateKey.Is(err) {
			t.Fatalf("%s: want duplicate index error, got %v", query, err)
		}
		lifecycleRows(t, f, indexes, initial)
		lifecycleRows(t, f, "SELECT id FROM entries WHERE label='alpha' AND n=10", []sql.Row{{int32(1)}})
	}
	for _, query := range []string{
		"ALTER TABLE entries DROP INDEX absent",
		"ALTER TABLE entries RENAME INDEX absent TO replacement",
	} {
		if err := foreignKeyQueryError(f, query); !sql.ErrCantDropFieldOrKey.Is(err) {
			t.Fatalf("%s: want missing index error, got %v", query, err)
		}
		lifecycleRows(t, f, indexes, initial)
	}
	for _, query := range []string{
		"ALTER TABLE entries DROP INDEX `PRIMARY`",
		"ALTER TABLE entries RENAME INDEX `PRIMARY` TO renamed",
		"ALTER TABLE entries RENAME INDEX by_label TO `PRIMARY`",
	} {
		if err := foreignKeyQueryError(f, query); err == nil || !strings.Contains(strings.ToLower(err.Error()), "primary") {
			t.Fatalf("%s: want primary-key restriction, got %v", query, err)
		}
		lifecycleRows(t, f, indexes, initial)
		lifecycleRows(t, f, "SELECT label FROM entries WHERE id=2", []sql.Row{{"beta"}})
	}
	f.query(t, "ALTER TABLE entries RENAME INDEX by_label TO renamed_label")
	f.query(t, "ALTER TABLE entries RENAME INDEX renamed_label TO renamed_label")
	lifecycleRows(t, f, indexes, []sql.Row{{"by_n"}, {"PRIMARY"}, {"renamed_label"}})
	if err := foreignKeyQueryError(f, "INSERT INTO entries VALUES (3,'alpha',30)"); !sql.ErrUniqueKeyViolation.Is(err) {
		t.Fatalf("rename lost unique enforcement: %v", err)
	}
	lifecycleRows(t, f, "SELECT id FROM entries WHERE label='alpha'", []sql.Row{{int32(1)}})
	f.query(t, "DROP INDEX renamed_label ON entries")
	f.query(t, "INSERT INTO entries VALUES (3,'alpha',30)")
	lifecycleRows(t, f, "SELECT id FROM entries WHERE label='alpha' ORDER BY id", []sql.Row{{int32(1)}, {int32(3)}})
	f.query(t, "ALTER TABLE entries DROP INDEX by_n")
	lifecycleRows(t, f, indexes, []sql.Row{{"PRIMARY"}})
	lifecycleRows(t, f, "SELECT id FROM entries WHERE n>=20 ORDER BY id", []sql.Row{{int32(2)}, {int32(3)}})
}

func TestSQLLifecycleTruncateImplicitCommitAndIndexes(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE truncate_lifecycle")
	f.query(t, "USE truncate_lifecycle")
	f.query(t, "CREATE TABLE keyed (id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, label VARCHAR(16), UNIQUE INDEX by_label(label))")
	f.query(t, "CREATE TABLE keyless (label VARCHAR(16), INDEX by_label(label))")
	f.query(t, "INSERT INTO keyed VALUES (40,'kept'),(50,'deleted')")
	f.query(t, "DELETE FROM keyed WHERE id=50")
	f.query(t, "INSERT INTO keyless VALUES ('same'),('same'),('other')")
	f.query(t, "BEGIN")
	f.query(t, "INSERT INTO keyed (label) VALUES ('staged')")
	f.query(t, "INSERT INTO keyless VALUES ('staged')")
	f.query(t, "TRUNCATE TABLE keyed")
	f.query(t, "TRUNCATE TABLE keyless")
	lifecycleRows(t, f, "SELECT id FROM keyed WHERE label='kept'", nil)
	lifecycleRows(t, f, "SELECT label FROM keyless WHERE label='same'", nil)
	f.query(t, "ROLLBACK")
	// TRUNCATE is DDL: its implicit commit cannot be undone by ROLLBACK.
	// https://dev.mysql.com/doc/refman/8.4/en/truncate-table.html
	lifecycleRows(t, f, "SELECT id,label FROM keyed ORDER BY id", nil)
	lifecycleRows(t, f, "SELECT label FROM keyless ORDER BY label", nil)
	f.query(t, "BEGIN")
	f.query(t, "TRUNCATE TABLE keyed")
	f.query(t, "TRUNCATE TABLE keyless")
	f.query(t, "COMMIT")
	lifecycleRows(t, f, "SELECT id FROM keyed", nil)
	lifecycleRows(t, f, "SELECT label FROM keyless", nil)
	f.query(t, "INSERT INTO keyed (label) VALUES ('kept')")
	f.query(t, "INSERT INTO keyless VALUES ('same')")
	lifecycleRows(t, f, "SELECT id,label FROM keyed WHERE label='kept'", []sql.Row{{uint64(1), "kept"}})
	lifecycleRows(t, f, "SELECT label FROM keyless WHERE label='same'", []sql.Row{{"same"}})
	if err := foreignKeyQueryError(f, "INSERT INTO keyed (label) VALUES ('kept')"); !sql.ErrUniqueKeyViolation.Is(err) {
		t.Fatalf("truncate removed unique-index enforcement: %v", err)
	}
	lifecycleRows(t, f, "SELECT id,label FROM keyed", []sql.Row{{uint64(1), "kept"}})
}

func TestSQLLifecycleNamespaceRecreation(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE namespace_left")
	f.query(t, "CREATE DATABASE namespace_right")
	f.query(t, "CREATE TABLE namespace_left.entries (id INT PRIMARY KEY, v INT, INDEX by_v(v))")
	f.query(t, "CREATE TABLE namespace_right.entries (id INT PRIMARY KEY, v INT, INDEX by_v(v))")
	f.query(t, "INSERT INTO namespace_left.entries VALUES (1,10)")
	f.query(t, "INSERT INTO namespace_right.entries VALUES (1,20)")
	for _, query := range []string{
		"CREATE DATABASE namespace_left",
		"CREATE TABLE namespace_left.entries (id INT PRIMARY KEY)",
		"DROP TABLE namespace_left.absent",
		"DROP DATABASE namespace_absent",
	} {
		if err := foreignKeyQueryError(f, query); err == nil {
			t.Fatalf("%s unexpectedly succeeded", query)
		}
		lifecycleRows(t, f, "SELECT v FROM namespace_left.entries WHERE id=1", []sql.Row{{int32(10)}})
		lifecycleRows(t, f, "SELECT v FROM namespace_right.entries WHERE id=1", []sql.Row{{int32(20)}})
	}
	f.query(t, "DROP TABLE namespace_left.entries")
	f.query(t, "CREATE TABLE namespace_left.entries (id INT PRIMARY KEY, v INT, INDEX by_v(v))")
	lifecycleRows(t, f, "SELECT id FROM namespace_left.entries WHERE v=10", nil)
	f.query(t, "INSERT INTO namespace_left.entries VALUES (1,30)")
	lifecycleRows(t, f, "SELECT v FROM namespace_left.entries WHERE id=1", []sql.Row{{int32(30)}})
	f.query(t, "DROP DATABASE namespace_left")
	f.query(t, "CREATE DATABASE namespace_left")
	f.query(t, "CREATE TABLE namespace_left.entries (id INT PRIMARY KEY, v INT, INDEX by_v(v))")
	lifecycleRows(t, f, "SELECT id FROM namespace_left.entries WHERE id=1 OR v=30", nil)
	f.query(t, "INSERT INTO namespace_left.entries VALUES (1,40)")
	lifecycleRows(t, f, "SELECT v FROM namespace_left.entries WHERE v=40", []sql.Row{{int32(40)}})
	lifecycleRows(t, f, "SELECT v FROM namespace_right.entries WHERE id=1", []sql.Row{{int32(20)}})
	lifecycleRows(t, f, "SELECT TABLE_SCHEMA,TABLE_NAME FROM information_schema.tables WHERE TABLE_SCHEMA IN ('namespace_left','namespace_right') ORDER BY TABLE_SCHEMA,TABLE_NAME", []sql.Row{{"namespace_left", "entries"}, {"namespace_right", "entries"}})
}

func TestSQLLifecycleForeignKeyParentRecreation(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	for _, database := range []string{"fk_lifecycle_parent", "fk_lifecycle_a", "fk_lifecycle_b"} {
		f.query(t, "CREATE DATABASE "+database)
	}
	f.query(t, "CREATE TABLE fk_lifecycle_parent.parents (id INT PRIMARY KEY)")
	f.query(t, "INSERT INTO fk_lifecycle_parent.parents VALUES (1)")
	for _, database := range []string{"fk_lifecycle_a", "fk_lifecycle_b"} {
		f.query(t, "CREATE TABLE "+database+".children (id INT PRIMARY KEY, parent_id INT, CONSTRAINT child_parent FOREIGN KEY (parent_id) REFERENCES fk_lifecycle_parent.parents(id) ON DELETE CASCADE ON UPDATE CASCADE)")
	}
	f.query(t, "SET foreign_key_checks=0")
	f.query(t, "DROP TABLE fk_lifecycle_parent.parents")
	f.query(t, "CREATE TABLE fk_lifecycle_parent.parents (id INT PRIMARY KEY)")
	lifecycleRows(t, f, "SELECT id FROM fk_lifecycle_parent.parents", nil)
	f.query(t, "INSERT INTO fk_lifecycle_parent.parents VALUES (2)")
	f.query(t, "SET foreign_key_checks=1")
	for _, database := range []string{"fk_lifecycle_a", "fk_lifecycle_b"} {
		f.query(t, "INSERT INTO "+database+".children VALUES (1,2)")
		if err := foreignKeyQueryError(f, "INSERT INTO "+database+".children VALUES (2,1)"); !sql.ErrForeignKeyChildViolation.Is(err) {
			t.Fatalf("recreated parent reused old namespace for %s: %v", database, err)
		}
	}
	f.query(t, "UPDATE fk_lifecycle_parent.parents SET id=3 WHERE id=2")
	for _, database := range []string{"fk_lifecycle_a", "fk_lifecycle_b"} {
		lifecycleRows(t, f, "SELECT id,parent_id FROM "+database+".children", []sql.Row{{int32(1), int32(3)}})
	}
	f.query(t, "DROP DATABASE fk_lifecycle_a")
	f.query(t, "DELETE FROM fk_lifecycle_parent.parents WHERE id=3")
	lifecycleRows(t, f, "SELECT id FROM fk_lifecycle_b.children", nil)
	f.query(t, "SET foreign_key_checks=0")
	f.query(t, "DROP DATABASE fk_lifecycle_parent")
	f.query(t, "CREATE DATABASE fk_lifecycle_parent")
	f.query(t, "CREATE TABLE fk_lifecycle_parent.parents (id INT PRIMARY KEY)")
	f.query(t, "INSERT INTO fk_lifecycle_parent.parents VALUES (4)")
	f.query(t, "SET foreign_key_checks=1")
	f.query(t, "INSERT INTO fk_lifecycle_b.children VALUES (2,4)")
	f.query(t, "DELETE FROM fk_lifecycle_parent.parents WHERE id=4")
	lifecycleRows(t, f, "SELECT id FROM fk_lifecycle_b.children", nil)
	f.query(t, "DROP TABLE fk_lifecycle_b.children")
	f.query(t, "DROP TABLE fk_lifecycle_parent.parents")
}

func TestSQLLifecycleForeignKeyMetadataChangesReachSQL(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE fk_metadata_lifecycle")
	f.query(t, "USE fk_metadata_lifecycle")
	f.query(t, "CREATE TABLE parents (id INT PRIMARY KEY)")
	f.query(t, "CREATE TABLE children (id INT PRIMARY KEY, first_id INT, second_id INT, CONSTRAINT first_parent FOREIGN KEY(first_id) REFERENCES parents(id), CONSTRAINT second_parent FOREIGN KEY(second_id) REFERENCES parents(id))")
	f.query(t, "INSERT INTO parents VALUES (1),(2)")
	f.query(t, "INSERT INTO children VALUES (1,1,2)")
	f.query(t, "CREATE TABLE other_children (id INT PRIMARY KEY, parent_id INT)")
	if err := foreignKeyQueryError(f, "ALTER TABLE other_children ADD CONSTRAINT first_parent FOREIGN KEY(parent_id) REFERENCES parents(id)"); !sql.ErrForeignKeyDuplicateName.Is(err) {
		t.Fatalf("foreign key names must be unique within their database: %v", err)
	}
	f.query(t, "INSERT INTO other_children VALUES (1,99)")
	lifecycleRows(t, f, "SELECT parent_id FROM other_children", []sql.Row{{int32(99)}})
	db, err := f.provider.Database(f.ctx, "fk_metadata_lifecycle")
	if err != nil {
		t.Fatal(err)
	}
	raw, ok, err := db.GetTableInsensitive(f.ctx, "children")
	if err != nil || !ok {
		t.Fatalf("child table: %v, %v", ok, err)
	}
	child := raw.(sql.ForeignKeyTable)
	keys, err := child.GetDeclaredForeignKeys(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var first sql.ForeignKeyConstraint
	for _, key := range keys {
		if key.Name == "first_parent" {
			first = key
		}
	}
	if first.Name == "" {
		t.Fatal("missing foreign key needed for metadata edit")
	}
	replacement := first
	replacement.Name = "renamed_parent"
	replacement.OnDelete = sql.ForeignKeyReferentialAction_SetNull
	if err := child.UpdateForeignKey(f.ctx, "missing_parent", replacement); !sql.ErrForeignKeyNotFound.Is(err) {
		t.Fatalf("updating a nonexistent constraint: %v", err)
	}
	duplicate := replacement
	duplicate.Name = "second_parent"
	if err := child.UpdateForeignKey(f.ctx, "first_parent", duplicate); !sql.ErrForeignKeyDuplicateName.Is(err) {
		t.Fatalf("renaming onto another constraint: %v", err)
	}
	if err := foreignKeyQueryError(f, "ALTER TABLE children DROP FOREIGN KEY missing_parent"); !sql.ErrForeignKeyNotFound.Is(err) {
		t.Fatalf("dropping a nonexistent constraint: %v", err)
	}
	if err := foreignKeyQueryError(f, "DELETE FROM parents WHERE id=1"); !sql.ErrForeignKeyParentViolation.Is(err) {
		t.Fatalf("rejected metadata edits weakened original restriction: %v", err)
	}
	lifecycleRows(t, f, "SELECT id,first_id,second_id FROM children", []sql.Row{{int32(1), int32(1), int32(2)}})
	if err := child.UpdateForeignKey(f.ctx, "first_parent", replacement); err != nil {
		t.Fatal(err)
	}
	f.query(t, "DELETE FROM parents WHERE id=1")
	lifecycleRows(t, f, "SELECT id,first_id,second_id FROM children", []sql.Row{{int32(1), nil, int32(2)}})
	if err := foreignKeyQueryError(f, "DELETE FROM parents WHERE id=2"); !sql.ErrForeignKeyParentViolation.Is(err) {
		t.Fatalf("metadata edit weakened the other foreign key: %v", err)
	}
	f.query(t, "ALTER TABLE children DROP FOREIGN KEY renamed_parent")
	f.query(t, "UPDATE children SET first_id=99 WHERE id=1")
	lifecycleRows(t, f, "SELECT first_id,second_id FROM children", []sql.Row{{int32(99), int32(2)}})
}

func TestSQLLifecyclePrefixResidualAndBinaryBounds(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE prefix_lifecycle")
	f.query(t, "USE prefix_lifecycle")
	f.query(t, "CREATE TABLE entries (id INT PRIMARY KEY, label VARCHAR(16) COLLATE utf8mb4_bin, raw VARBINARY(8), n INT, UNIQUE INDEX label_prefix(label(2),n), INDEX raw_prefix(raw(1),n))")
	f.query(t, "INSERT INTO entries VALUES (1,'apple',X'00FF',-1),(2,'apricot',X'00FF00',0),(3,'berry',X'0100',1),(4,'1234',X'FF00',2),(5,NULL,NULL,3)")
	for _, test := range []struct {
		query string
		want  []sql.Row
	}{
		{"SELECT id FROM entries WHERE label>'apple' AND label<'berry' ORDER BY id", []sql.Row{{int32(2)}}},
		{"SELECT id FROM entries WHERE label='apricot' AND n=0", []sql.Row{{int32(2)}}},
		{"SELECT id FROM entries WHERE label=1234", []sql.Row{{int32(4)}}},
		{"SELECT id FROM entries WHERE label IS NULL", []sql.Row{{int32(5)}}},
		{"SELECT id FROM entries WHERE raw=X'00FF'", []sql.Row{{int32(1)}}},
		{"SELECT id FROM entries WHERE raw>X'00FF' AND raw<X'0100' ORDER BY id", []sql.Row{{int32(2)}}},
		{"SELECT id FROM entries WHERE raw>=X'0100' AND n>=1 ORDER BY id", []sql.Row{{int32(3)}, {int32(4)}}},
		{"SELECT id FROM entries WHERE raw=X'FF00' AND n=2", []sql.Row{{int32(4)}}},
	} {
		lifecycleRows(t, f, test.query, test.want)
	}
	if err := foreignKeyQueryError(f, "UPDATE entries SET label='apex',n=-1 WHERE id=3"); !sql.ErrUniqueKeyViolation.Is(err) {
		t.Fatalf("update bypassed composite prefix uniqueness: %v", err)
	}
	lifecycleRows(t, f, "SELECT label,n FROM entries WHERE id=3", []sql.Row{{"berry", int32(1)}})
	f.query(t, "UPDATE entries SET label='apex',n=4,raw=X'00FE' WHERE id=3")
	lifecycleRows(t, f, "SELECT id FROM entries WHERE raw<X'00FF'", []sql.Row{{int32(3)}})
	lifecycleRows(t, f, "SELECT id FROM entries WHERE label='berry'", nil)
	lifecycleRows(t, f, "SELECT id FROM entries WHERE label='apex' AND n=4", []sql.Row{{int32(3)}})
}

func TestSQLLifecycleAutoIncrementExplicitBoundsAndReset(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE allocation_lifecycle")
	f.query(t, "USE allocation_lifecycle")
	f.query(t, "CREATE TABLE entries (id BIGINT AUTO_INCREMENT PRIMARY KEY, label VARCHAR(16))")
	f.query(t, "SET sql_mode='STRICT_TRANS_TABLES,NO_AUTO_VALUE_ON_ZERO'")
	f.query(t, "INSERT INTO entries VALUES (-5,'negative'),(0,'zero'),(10,'explicit')")
	f.query(t, "INSERT INTO entries (label) VALUES ('generated')")
	lifecycleRows(t, f, "SELECT id,label FROM entries ORDER BY id", []sql.Row{{int64(-5), "negative"}, {int64(0), "zero"}, {int64(10), "explicit"}, {int64(11), "generated"}})
	f.query(t, "ALTER TABLE entries AUTO_INCREMENT=100")
	f.query(t, "BEGIN")
	f.query(t, "INSERT INTO entries (label) VALUES ('reserved')")
	f.query(t, "ROLLBACK")
	f.query(t, "INSERT INTO entries (label) VALUES ('after rollback')")
	lifecycleRows(t, f, "SELECT id FROM entries WHERE label='after rollback'", []sql.Row{{int64(101)}})
	lifecycleRows(t, f, "SELECT id FROM entries WHERE label='reserved'", nil)
	f.query(t, "CREATE TABLE exhausted (id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, label VARCHAR(16))")
	f.query(t, "INSERT INTO exhausted VALUES (18446744073709551615,'last explicit')")
	if err := foreignKeyQueryError(f, "INSERT INTO exhausted (label) VALUES ('overflow')"); !sql.ErrValueOutOfRange.Is(err) {
		t.Fatalf("auto increment wrapped after largest explicit ID: %v", err)
	}
	lifecycleRows(t, f, "SELECT id,label FROM exhausted", []sql.Row{{uint64(18446744073709551615), "last explicit"}})
	f.query(t, "TRUNCATE TABLE exhausted")
	f.query(t, "INSERT INTO exhausted (label) VALUES ('reset')")
	lifecycleRows(t, f, "SELECT id,label FROM exhausted", []sql.Row{{uint64(1), "reset"}})
}
