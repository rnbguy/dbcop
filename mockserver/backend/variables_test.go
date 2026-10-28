package backend

import (
	"context"
	"io"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
	_ "github.com/dolthub/go-mysql-server/sql/variables"
)

func TestReadLatestCanBeDisabled(t *testing.T) {
	RegisterVariables()
	store := NewStore(1, 2)
	store.weakRandom = false
	t.Cleanup(store.Close)
	table := NewTable(TableDef{Name: "policy", Columns: []*sql.Column{intCol("id")}}, store, ReadCommitted)
	table.state.keys["1"] = struct{}{}
	exists := store.Write("policy", "1", rowExistenceColumn, true)
	id := store.Write("policy", "1", "id", int64(1))
	store.appendLog(1, 0, []CommittedWrite{
		{Key: cellKey("policy", "1", rowExistenceColumn), ID: exists},
		{Key: cellKey("policy", "1", "id"), ID: id},
	}, nil)
	session := NewSession(store, ReadCommitted, 2)
	ctx := sql.NewContext(context.Background(), sql.WithSession(session))
	if err := session.SetSessionVariable(ctx, "dbcop_read_latest", int8(1)); err != nil {
		t.Fatal(err)
	}
	rows, err := table.PartitionRows(ctx, &singlePartition{})
	if err != nil {
		t.Fatal(err)
	}
	row, err := rows.Next(ctx)
	if err != nil || row[0] != int64(1) {
		t.Fatalf("latest read: want row 1, got %v, %v", row, err)
	}
	if err := rows.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := session.SetSessionVariable(ctx, "dbcop_read_latest", int8(0)); err != nil {
		t.Fatal(err)
	}
	rows, err = table.PartitionRows(ctx, &singlePartition{})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close(ctx)
	if row, err := rows.Next(ctx); err != io.EOF {
		t.Fatalf("disabled latest policy must select initial absence, got %v, %v", row, err)
	}
}
