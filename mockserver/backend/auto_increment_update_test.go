package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func TestSQLAutoIncrementAfterPrimaryKeyUpdate(t *testing.T) {
	f := newSQLTableFixture(t)
	for _, query := range []string{
		"CREATE DATABASE updated_ids", "USE updated_ids",
		"CREATE TABLE records (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY)",
		"INSERT INTO records VALUES (NULL)",
		"UPDATE records SET id=2 WHERE id=1",
		"INSERT INTO records VALUES (NULL)",
	} {
		f.query(t, query)
	}
	if rows := f.query(t, "SELECT id FROM records ORDER BY id"); !reflect.DeepEqual(rows, []sql.Row{{int64(2)}, {int64(3)}}) {
		t.Fatalf("higher primary key must advance the next generated ID: %v", rows)
	}
	f.query(t, "UPDATE records SET id=1 WHERE id=3")
	f.query(t, "INSERT INTO records VALUES (NULL)")
	if rows := f.query(t, "SELECT id FROM records ORDER BY id"); !reflect.DeepEqual(rows, []sql.Row{{int64(1)}, {int64(2)}, {int64(4)}}) {
		t.Fatalf("lower primary key must not rewind the next generated ID: %v", rows)
	}
}
