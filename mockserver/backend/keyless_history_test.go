package backend

import (
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func TestKeylessEditsUseSelectedHistoricalRows(t *testing.T) {
	for _, operation := range []string{"UPDATE records SET notification=2 WHERE id=1 AND notification IS NULL", "DELETE FROM records WHERE id=1 AND notification IS NULL"} {
		t.Run(operation, func(t *testing.T) {
			f := newSQLTableFixture(t)
			if err := f.store.BeginInitialization(); err != nil {
				t.Fatal(err)
			}
			f.query(t, "CREATE DATABASE keyless_history")
			f.query(t, "USE keyless_history")
			f.query(t, "CREATE TABLE records (id INT NOT NULL, notification INT)")
			f.query(t, "INSERT INTO records VALUES (1,NULL)")
			f.query(t, "CALL dbcop_seal_initial_state()")
			f.query(t, "UPDATE records SET notification=1 WHERE id=1")
			f.store.SetSchedule([]int{0})
			f.query(t, operation)
			f.store.SetSchedule([]int{1 << 20})
			want := []sql.Row{{int32(1), int32(2)}}
			if operation[0] == 'D' {
				want = nil
			}
			if got := f.query(t, "SELECT * FROM records"); !reflect.DeepEqual(got, want) {
				t.Fatalf("historical selection mutated the wrong row: got %v, want %v", got, want)
			}
		})
	}
}
