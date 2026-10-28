package backend

import (
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

func TestAutoIncrementInvalidExplicitValueDoesNotReserveID(t *testing.T) {
	table, session, ctx := stagedTable(t, TableDef{Name: "generated_boundary", PkeyIdx: 0,
		Columns: []*sql.Column{{Name: "id", Type: types.Uint64, AutoIncrement: true, PrimaryKey: true}}})
	t.Cleanup(func() {
		session.SessionEnd()
		session.store.Close()
	})
	setter := table.AutoIncrementSetter(ctx)
	if err := setter.SetAutoIncrementValue(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := table.GetNextAutoIncrementValue(ctx, nil); err != nil || got != 1 {
		t.Fatalf("zero reset must allocate ID 1, got %d, %v", got, err)
	}
	for _, value := range []interface{}{"not-an-id", struct{}{}} {
		if _, err := table.GetNextAutoIncrementValue(ctx, value); err == nil {
			t.Fatalf("invalid explicit ID %v was accepted", value)
		}
	}
	if got, err := table.GetNextAutoIncrementValue(ctx, nil); err != nil || got != 2 {
		t.Fatalf("invalid explicit IDs changed the next allocation: %d, %v; want 2", got, err)
	}
}
