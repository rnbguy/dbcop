package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func foreignKeyQueryError(f *sqlTableFixture, query string) error {
	_, iter, _, err := f.engine.Query(f.ctx, query)
	if err != nil {
		return err
	}
	_, err = sql.RowIterToRows(f.ctx, iter)
	return sql.UnwrapError(err)
}

func TestSQLForeignKeyInsertAndRestrict(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE relationships")
	f.query(t, "USE relationships")
	f.query(t, "CREATE TABLE parents (id INT NOT NULL PRIMARY KEY)")
	f.query(t, "CREATE TABLE children (id INT NOT NULL PRIMARY KEY, parent_id INT, CONSTRAINT child_parent FOREIGN KEY (parent_id) REFERENCES parents (id))")
	f.query(t, "INSERT INTO parents VALUES (1),(2)")
	f.query(t, "INSERT INTO children VALUES (1,1),(2,NULL)")
	before := len(f.store.Admissions())
	if err := foreignKeyQueryError(f, "INSERT INTO children VALUES (3,2),(4,999)"); !sql.ErrForeignKeyChildViolation.Is(err) {
		t.Fatalf("missing parent must reject the statement: %v", err)
	}
	if len(f.store.Admissions()) == before {
		t.Fatal("foreign key checks bypassed the KV oracle")
	}
	if rows := f.query(t, "SELECT id,parent_id FROM children ORDER BY id"); !reflect.DeepEqual(rows, []sql.Row{{int32(1), int32(1)}, {int32(2), nil}}) {
		t.Fatalf("failed insert retained earlier rows: %v", rows)
	}
	for _, query := range []string{"DELETE FROM parents WHERE id=1", "DELETE FROM parents", "UPDATE parents SET id=9 WHERE id=1"} {
		if err := foreignKeyQueryError(f, query); !sql.ErrForeignKeyParentViolation.Is(err) {
			t.Fatalf("referenced parent must be protected for %s: %v", query, err)
		}
	}
	if err := foreignKeyQueryError(f, "UPDATE children SET parent_id=999 WHERE id=1"); !sql.ErrForeignKeyChildViolation.Is(err) {
		t.Fatalf("child update must check its reference: %v", err)
	}
	if err := foreignKeyQueryError(f, "DROP TABLE parents"); !sql.ErrForeignKeyDropTable.Is(err) {
		t.Fatalf("dropping a referenced parent must fail: %v", err)
	}
	if err := foreignKeyQueryError(f, "ALTER TABLE children DROP INDEX child_parent"); !sql.ErrForeignKeyDropIndex.Is(err) {
		t.Fatalf("dropping the child lookup index must fail: %v", err)
	}
	f.query(t, "DELETE FROM children WHERE parent_id=1")
	f.query(t, "DELETE FROM parents WHERE id=1")
}

func TestSQLForeignKeyCascadeAndRollback(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE cascade_rows")
	f.query(t, "USE cascade_rows")
	f.query(t, "CREATE TABLE parents (id INT NOT NULL PRIMARY KEY)")
	f.query(t, "CREATE TABLE children (id INT NOT NULL PRIMARY KEY, parent_id INT, CONSTRAINT child_parent FOREIGN KEY (parent_id) REFERENCES parents (id) ON DELETE CASCADE ON UPDATE CASCADE)")
	f.query(t, "CREATE TABLE grandchildren (id INT NOT NULL PRIMARY KEY, child_id INT, CONSTRAINT grandchild_parent FOREIGN KEY (child_id) REFERENCES children (id))")
	f.query(t, "INSERT INTO parents VALUES (1)")
	f.query(t, "INSERT INTO children VALUES (10,1)")
	f.query(t, "INSERT INTO grandchildren VALUES (100,10)")
	if err := foreignKeyQueryError(f, "DELETE FROM parents WHERE id=1"); !sql.ErrForeignKeyParentViolation.Is(err) {
		t.Fatalf("nested restriction must abort a cascade: %v", err)
	}
	if rows := f.query(t, "SELECT id FROM parents"); !reflect.DeepEqual(rows, []sql.Row{{int32(1)}}) {
		t.Fatalf("failed cascade retained its parent deletion: %v", rows)
	}
	if rows := f.query(t, "SELECT id FROM children"); !reflect.DeepEqual(rows, []sql.Row{{int32(10)}}) {
		t.Fatalf("failed cascade removed its child: %v", rows)
	}
	f.query(t, "DROP TABLE grandchildren")
	f.query(t, "START TRANSACTION")
	f.query(t, "UPDATE parents SET id=2 WHERE id=1")
	if rows := f.query(t, "SELECT parent_id FROM children"); !reflect.DeepEqual(rows, []sql.Row{{int32(2)}}) {
		t.Fatalf("parent update did not cascade through KV writes: %v", rows)
	}
	f.query(t, "DELETE FROM parents WHERE id=2")
	if rows := f.query(t, "SELECT id FROM children"); len(rows) != 0 {
		t.Fatalf("cascade did not remove child in the transaction: %v", rows)
	}
	f.query(t, "ROLLBACK")
	if rows := f.query(t, "SELECT parent_id FROM children"); !reflect.DeepEqual(rows, []sql.Row{{int32(1)}}) {
		t.Fatalf("rollback did not restore cascaded edits: %v", rows)
	}
	f.query(t, "DELETE FROM parents")
	if rows := f.query(t, "SELECT id FROM children"); len(rows) != 0 {
		t.Fatalf("committed delete did not cascade: %v", rows)
	}
}

func TestSQLForeignKeyDDLValidationAndMetadata(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE fk_ddl")
	f.query(t, "USE fk_ddl")
	f.query(t, "CREATE TABLE parents (id INT NOT NULL PRIMARY KEY)")
	f.query(t, "CREATE TABLE children (id INT NOT NULL PRIMARY KEY, parent_id INT)")
	f.query(t, "INSERT INTO children VALUES (1,99)")
	if err := foreignKeyQueryError(f, "ALTER TABLE children ADD CONSTRAINT child_parent FOREIGN KEY (parent_id) REFERENCES parents(id)"); !sql.ErrForeignKeyChildViolation.Is(err) {
		t.Fatalf("adding a foreign key must validate existing KV rows: %v", err)
	}
	f.query(t, "INSERT INTO parents VALUES (99)")
	f.query(t, "ALTER TABLE children ADD CONSTRAINT child_parent FOREIGN KEY (parent_id) REFERENCES parents(id)")
	db, err := f.provider.Database(f.ctx, "fk_ddl")
	if err != nil {
		t.Fatal(err)
	}
	parent, _, _ := db.GetTableInsensitive(f.ctx, "parents")
	child, _, _ := db.GetTableInsensitive(f.ctx, "children")
	declared, err := child.(sql.ForeignKeyTable).GetDeclaredForeignKeys(f.ctx)
	if err != nil || len(declared) != 1 || !declared[0].IsResolved {
		t.Fatalf("missing resolved declaration: %v, %v", declared, err)
	}
	referenced, err := parent.(sql.ForeignKeyTable).GetReferencedForeignKeys(f.ctx)
	if err != nil || !reflect.DeepEqual(declared, referenced) {
		t.Fatalf("parent metadata does not match its declaration: %v, %v", referenced, err)
	}
	f.query(t, "ALTER TABLE children DROP FOREIGN KEY child_parent")
	f.query(t, "INSERT INTO children VALUES (2,100)")
	f.query(t, "DROP TABLE children")
	f.query(t, "CREATE TABLE children (id INT NOT NULL PRIMARY KEY, parent_id INT, CONSTRAINT child_parent FOREIGN KEY (parent_id) REFERENCES parents(id))")
	f.query(t, "DROP DATABASE fk_ddl")
	f.query(t, "CREATE DATABASE fk_ddl")
	f.query(t, "USE fk_ddl")
	f.query(t, "CREATE TABLE parents (id INT NOT NULL PRIMARY KEY)")
	f.query(t, "CREATE TABLE children (id INT NOT NULL PRIMARY KEY, parent_id INT, CONSTRAINT child_parent FOREIGN KEY (parent_id) REFERENCES parents(id))")
}

func TestSQLForeignKeyCrossDatabaseTruncateProtection(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE fk_parents")
	f.query(t, "CREATE DATABASE fk_children")
	f.query(t, "USE fk_parents")
	f.query(t, "CREATE TABLE parents (id INT NOT NULL PRIMARY KEY)")
	f.query(t, "INSERT INTO parents VALUES (1)")
	f.query(t, "USE fk_children")
	f.query(t, "CREATE TABLE children (id INT NOT NULL PRIMARY KEY, parent_id INT, FOREIGN KEY (parent_id) REFERENCES fk_parents.parents(id))")
	f.query(t, "INSERT INTO children VALUES (1,1)")
	f.query(t, "USE fk_parents")
	if err := foreignKeyQueryError(f, "TRUNCATE TABLE parents"); !sql.ErrTruncateReferencedFromForeignKey.Is(err) {
		t.Fatalf("cross-database foreign key must protect truncation: %v", err)
	}
	if err := foreignKeyQueryError(f, "DELETE FROM parents WHERE id=1"); !sql.ErrForeignKeyParentViolation.Is(err) {
		t.Fatalf("cross-database foreign key must protect parent deletion: %v", err)
	}
	if rows := f.query(t, "SELECT id FROM parents"); !reflect.DeepEqual(rows, []sql.Row{{int32(1)}}) {
		t.Fatalf("failed truncate or delete removed referenced data: %v", rows)
	}
}
