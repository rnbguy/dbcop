package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func TestSQLUnorderedIndexCandidatesRetainSortAndLimit(t *testing.T) {
	f := newSQLTableFixture(t)
	for _, query := range []string{
		"CREATE DATABASE ordered_rows", "USE ordered_rows",
		"CREATE TABLE entries (id BIGINT PRIMARY KEY, score BIGINT, INDEX by_score(score))",
		"INSERT INTO entries VALUES (10,1),(2,10),(1,2)",
		"CREATE TABLE unsigned_values (v BIGINT UNSIGNED PRIMARY KEY)",
		"INSERT INTO unsigned_values VALUES (0),(9223372036854775808),(18446744073709551615)",
	} {
		f.query(t, query)
	}
	for _, test := range []struct {
		query string
		want  []sql.Row
	}{
		{"SELECT id FROM entries ORDER BY id", []sql.Row{{int64(1)}, {int64(2)}, {int64(10)}}},
		{"SELECT id FROM entries WHERE id>=1 ORDER BY id DESC LIMIT 2", []sql.Row{{int64(10)}, {int64(2)}}},
		{"SELECT id FROM entries ORDER BY score", []sql.Row{{int64(10)}, {int64(1)}, {int64(2)}}},
		{"SELECT id FROM entries WHERE score>0 ORDER BY score DESC LIMIT 2", []sql.Row{{int64(2)}, {int64(1)}}},
		{"SELECT v FROM unsigned_values WHERE v>9223372036854775807 ORDER BY v", []sql.Row{{uint64(9223372036854775808)}, {uint64(18446744073709551615)}}},
	} {
		t.Run(test.query, func(t *testing.T) {
			if rows := f.query(t, test.query); !reflect.DeepEqual(rows, test.want) {
				t.Fatalf("unordered candidate keys must not replace SQL sorting: got %v, want %v", rows, test.want)
			}
		})
	}
}
