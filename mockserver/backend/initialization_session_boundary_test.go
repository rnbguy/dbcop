package backend

import (
	"context"
	"reflect"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func TestSealRejectsForeignSessionsWithoutChangingInitialState(t *testing.T) {
	owned := initializationSQLFixture(t)
	foreign := initializationSQLFixture(t)
	for _, session := range []sql.Session{foreign.ctx.Session, sql.NewBaseSession()} {
		ctx := sql.NewContext(context.Background(), sql.WithSession(session))
		if _, err := owned.provider.sealInitialState(ctx); err == nil {
			t.Fatal("a session from another store sealed the initial state")
		}
		if !owned.store.isInitializing() || !foreign.store.isInitializing() {
			t.Fatal("rejected cross-store seal changed initialization ownership")
		}
	}
	for _, fixture := range []*sqlTableFixture{owned, foreign} {
		fixture.query(t, "CALL dbcop_seal_initial_state()")
		fixture.store.SetSchedule([]int{0})
		want := []sql.Row{{int32(1), "old"}, {int32(2), nil}, {int32(3), "gone"}}
		if rows := fixture.query(t, "SELECT id,value FROM items ORDER BY id"); !reflect.DeepEqual(rows, want) {
			t.Fatalf("cross-store seal rejection lost initialized rows: %v, want %v", rows, want)
		}
	}
}
