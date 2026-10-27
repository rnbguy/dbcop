package backend

import (
	"context"
	"fmt"

	"github.com/dolthub/go-mysql-server/sql"
)

// Explorer selects one permitted observable successor without enumerating witnesses.
type Explorer interface {
	Step(context.Context, StepRequest) (StepResult, error)
	Close() error
}

// ReadSource identifies root, a committed version, or a one-based staged-write position.
// Initial and Local are mutually exclusive; local reads never enter the external log.
type ReadSource struct {
	Version Version
	Initial bool
	Local   bool
}

// StepRequest contains an operation's copied history and ordered live prefix.
type StepRequest struct {
	Kind        string
	Isolation   Isolation
	Session     int
	Committed   []LoggedTxn
	Live        []TxnEvent
	Key         string
	Candidates  []ReadSource
	Write       StagedWrite
	Transaction LoggedTxn
	Latest      bool
}

type StepResult struct {
	Enabled bool
	Source  ReadSource
}

func (s *Session) stepRequest(kind string) StepRequest {
	s.txnMu.Lock()
	latest := s.readLatest
	s.txnMu.Unlock()
	return StepRequest{
		Kind: kind, Isolation: s.iso, Session: s.sid,
		Committed: s.store.CommittedHistory(), Live: liveEvents(s), Latest: latest,
	}
}

func (s *Session) explore(ctx context.Context, request StepRequest) (StepResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.store.oracles.explorer.Step(ctx, request)
}

func explorerContext(ctx *sql.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (s *Session) stageWrite(ctx *sql.Context, txn *Txn, write StagedWrite) error {
	if err := s.exploreWrite(ctx, write); err != nil {
		return err
	}
	txn.stage(write)
	return nil
}

func (s *Session) stageMembership(ctx *sql.Context, txn *Txn, tab, pkey string, present bool) error {
	write := StagedWrite{Tab: tab, Pkey: pkey, Col: rowExistenceColumn, Value: present}
	if err := s.exploreWrite(ctx, write); err != nil {
		return err
	}
	txn.stageMembership(tab, pkey, present)
	return nil
}

func (s *Session) exploreWrite(ctx *sql.Context, write StagedWrite) error {
	if s.store.oracles.explorer == nil || s.store.isInitializing() {
		return nil
	}
	request := s.stepRequest("write")
	request.Key = cellKey(write.Tab, write.Pkey, write.Col)
	request.Write = write
	result, err := s.explore(explorerContext(ctx), request)
	if err != nil {
		return err
	}
	if !result.Enabled {
		return fmt.Errorf("explorer write step is disabled")
	}
	return nil
}
