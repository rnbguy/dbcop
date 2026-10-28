package backend

import (
	"errors"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/plan"
)

func (p *Provider) ExternalStoredProcedure(_ *sql.Context, name string, numOfParams int) (*sql.ExternalStoredProcedureDetails, error) {
	return p.procedures.LookupByNameAndParamCount(name, numOfParams)
}

func (p *Provider) ExternalStoredProcedures(_ *sql.Context, name string) ([]sql.ExternalStoredProcedureDetails, error) {
	return p.procedures.LookupByName(name)
}

func (p *Provider) sealInitialState(ctx *sql.Context) (sql.RowIter, error) {
	session, ok := ctx.Session.(*Session)
	if !ok || session.store != p.store {
		return nil, errors.New("sealing initial state requires a session for this store")
	}
	autocommit, err := plan.IsSessionAutocommit(ctx)
	if err != nil {
		return nil, err
	}
	if ctx.GetIgnoreAutoCommit() || !autocommit {
		return &procedureErrorIter{errors.New("cannot seal initial state inside a user transaction")}, nil
	}
	txn := session.liveTxn()
	if txn != nil {
		handle, ok := ctx.GetTransaction().(*backendTxn)
		state := txn.checkpoint()
		if !ok || handle.session != session || state != (txnCheckpoint{}) {
			return &procedureErrorIter{errors.New("cannot seal initial state with pending transaction work")}, nil
		}
		// The engine's empty CALL transaction holds the lock needed by sealing.
		session.clearTxn()
	}
	if err := p.store.SealInitialState(); err != nil {
		return nil, err
	}
	return sql.RowsToRowIter(), nil
}

// Report rejection during iteration so engine build-error cleanup cannot discard user work.
type procedureErrorIter struct{ err error }

func (i *procedureErrorIter) Next(*sql.Context) (sql.Row, error) { return nil, i.err }
func (i *procedureErrorIter) Close(*sql.Context) error           { return i.err }
