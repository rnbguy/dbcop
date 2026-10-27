//go:build cgo

package backend

import (
	"context"
	"testing"

	"github.com/dolthub/go-mysql-server/sql"
)

func TestApalacheLocalReadFailureRetainsWritePrefix(t *testing.T) {
	url := apalacheTestURL(t)
	proxy := newSQLRPCProxy(t, url)
	f, recorder := newSQLExplorerFixture(t, Serializable, proxy.server.URL)
	f.query(t, "BEGIN")
	f.query(t, "UPDATE items SET v=11 WHERE id=1")
	injected := false
	recorder.before = func(ctx context.Context, request StepRequest) (context.Context, func()) {
		if !injected && request.Kind == "read" && len(request.Candidates) == 1 && request.Candidates[0].Local {
			proxy.arm("unknown", nil)
			injected = true
		}
		return ctx, func() {}
	}
	err := foreignKeyQueryError(f, "SELECT v FROM items WHERE id=1")
	recorder.before = nil
	if !injected || err == nil || sql.ErrLockDeadlock.Is(err) {
		t.Fatalf("local RPC failure became a value or isolation rejection: injected=%t error=%v", injected, err)
	}
	f.query(t, "COMMIT")
	namedSavepointRows(t, f, "SELECT v FROM items WHERE id=1", []sql.Row{{int32(11)}})
	verifyTLCObservations(t, sqlExplorerValidate(t, recorder))
}

type disabledWriteExplorer struct {
	Explorer
	writes int
}

func (e *disabledWriteExplorer) Step(ctx context.Context, request StepRequest) (StepResult, error) {
	result, err := e.Explorer.Step(ctx, request)
	if err == nil && request.Kind == "write" {
		e.writes++
		if e.writes == 2 {
			// Fault only the negative boundary after the real model has completed and disposed.
			result.Enabled = false
		}
	}
	return result, err
}

func TestApalacheDisabledWriteRestoresStatement(t *testing.T) {
	f, recorder := newSQLExplorerFixture(t, Serializable, apalacheTestURL(t))
	fault := &disabledWriteExplorer{Explorer: recorder.real}
	recorder.real = fault
	f.query(t, "BEGIN")
	err := foreignKeyQueryError(f, "UPDATE items SET v=11,n=101 WHERE id=1")
	if err == nil || sql.ErrLockDeadlock.Is(err) || fault.writes != 2 {
		t.Fatalf("disabled second write did not fail the statement: writes=%d error=%v", fault.writes, err)
	}
	f.query(t, "COMMIT")
	namedSavepointRows(t, f, "SELECT v,n FROM items WHERE id=1", []sql.Row{{int32(10), nil}})
	for _, txn := range f.store.CommittedHistory() {
		if len(txn.Writes) != 0 {
			t.Fatal("disabled write left a partial statement in committed history")
		}
	}
	verifyTLCObservations(t, sqlExplorerValidate(t, recorder))
}
