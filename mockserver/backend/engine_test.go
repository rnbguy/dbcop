package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/vitess/go/vt/sqlparser"
)

func TestBoundAliasedFiltersDoNotReadUnmatchedRows(t *testing.T) {
	if testing.Short() {
		t.Skip("needs dbcop_ffi.so")
	}
	f := newSQLTableFixture(t)
	f.query(t, "CREATE DATABASE bound_filters")
	f.query(t, "USE bound_filters")
	f.query(t, "CREATE TABLE accounts (branch INT NOT NULL, id INT NOT NULL, label VARCHAR(20), balance INT, secret VARCHAR(20), PRIMARY KEY(branch,id), INDEX by_label(branch,label))")
	f.query(t, "INSERT INTO accounts VALUES (1,1,'one',10,'private'),(1,2,'two',20,'private')")
	db, _ := f.provider.Database(f.ctx, "bound_filters")
	raw, _, _ := db.GetTableInsensitive(f.ctx, "accounts")
	table := raw.(*Table)
	query := "SELECT a.balance FROM accounts a WHERE a.branch=:branch AND a.label=:label"
	for _, test := range []struct {
		label   string
		key     string
		balance int32
	}{{"two", "1#2", 20}, {"one", "1#1", 10}} {
		before := len(f.store.Admissions())
		_, iter, _, err := f.engine.QueryWithBindings(f.ctx, query, nil, map[string]sqlparser.Expr{
			"branch": sqlparser.NewIntVal([]byte("1")),
			"label":  sqlparser.NewStrVal([]byte(test.label)),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := sql.RowIterToRows(f.ctx, iter)
		if err != nil || !reflect.DeepEqual(rows, []sql.Row{{test.balance}}) {
			t.Fatalf("bound lookup %q: rows=%v err=%v", test.label, rows, err)
		}
		allowed := make(map[string]bool)
		for _, column := range []string{rowExistenceColumn, "branch", "label", "balance"} {
			allowed[cellKey(table.storageName, test.key, column)] = true
		}
		for _, read := range f.store.Admissions()[before:] {
			if !allowed[read.Key] {
				t.Fatalf("bound lookup %q added unrelated oracle read %q", test.label, read.Key)
			}
		}
	}
}
