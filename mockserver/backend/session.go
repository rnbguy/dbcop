// Package backend: session identity and transaction state.
package backend

import (
	"context"
	"fmt"
	"sync"

	"github.com/dolthub/go-mysql-server/sql"
)

// Session embeds BaseSession and carries the live txn. All committed
// history lives in the Store's global event log so the oracle sees every
// session's transactions with true identity and order.
type Session struct {
	*sql.BaseSession
	store *Store
	iso   Isolation
	sid   int

	txnMu       sync.Mutex
	txn         *Txn
	explicit    bool
	holdsCommit bool
	// readLatest, when true, selects the newest admitted candidate
	// instead of the stalest. Writers doing read-modify-write need it:
	// stalest-first would read initial versions, fail WHERE matches,
	// and UPDATE zero rows. Set per-connection by the workload.
	readLatest bool
	// readErr preserves infrastructure failures across oracleRead's bool API.
	readErr error
}

var _ sql.TransactionSession = (*Session)(nil)
var _ sql.LifecycleAwareSession = (*Session)(nil)

// NewSession builds a session bound to store with isolation iso.
// sid assigns the dbcop session identity; callers must pass distinct ids.
func NewSession(store *Store, iso Isolation, sid int) *Session {
	return &Session{
		BaseSession: sql.NewBaseSession(),
		store:       store,
		iso:         iso,
		sid:         sid,
	}
}

// SetTransaction also abandons backend state when the engine clears a failed statement.
func (s *Session) SetTransaction(tx sql.Transaction) {
	if tx == nil {
		_ = s.Rollback(nil, nil)
		return
	}
	s.BaseSession.SetTransaction(tx)
}

func (s *Session) CommandBegin() error {
	if s.liveTxn() != nil {
		s.beginTxn()
	}
	return nil
}

func (s *Session) CommandEnd() {
	s.txnMu.Lock()
	defer s.txnMu.Unlock()
	s.readErr = nil
	if s.txn != nil && s.holdsCommit && s.txn.checkpoint() == (txnCheckpoint{}) {
		// JDBC configures all connections before starting work; empty setup
		// transactions must not reserve execution while another connection opens.
		s.holdsCommit = false
		s.store.commitMu.Unlock()
	}
}

// SessionEnd rolls back work abandoned by disconnect or connection reset.
func (s *Session) SessionEnd() {
	_ = s.Rollback(nil, nil)
}

// oracleRead preserves the bool API used by native callers and tests.
// Native reads enumerate admitted committed/root versions before selection;
// explorer reads select one successor from the whole source universe.
// Both preserve read-your-writes without logging an external read.
func (s *Session) oracleRead(tab, pkey, col string) (interface{}, Version, bool) {
	return s.oracleReadContext(context.Background(), tab, pkey, col)
}

func (s *Session) oracleReadContext(ctx context.Context, tab, pkey, col string) (interface{}, Version, bool) {
	s.takeReadError()
	initializing := s.store.isInitializing()
	if txn := s.liveTxn(); txn != nil {
		if v, position, ok := txn.stagedSource(tab, pkey, col); ok {
			if s.store.oracles.explorer != nil && !initializing {
				source := ReadSource{Version: position, Local: true}
				request := s.stepRequest("read")
				request.Key = cellKey(tab, pkey, col)
				request.Candidates = []ReadSource{source}
				result, err := s.explore(ctx, request)
				if err != nil {
					s.latchReadError(err)
					return nil, 0, false
				}
				if !result.Enabled {
					return nil, 0, false
				}
				if result.Source != source {
					s.latchReadError(fmt.Errorf("explorer selected a source outside the local read universe"))
					return nil, 0, false
				}
			}
			return v, 0, true
		}
	}
	key := cellKey(tab, pkey, col)
	if initializing {
		version, _ := s.store.latestVersion(key)
		return version.Value, version.ID, true
	}
	vers := s.store.committedVersions(key)
	var initial interface{}
	if len(vers) != 0 && vers[0].ID == 0 {
		initial = vers[0].Value
		vers = vers[1:]
	}
	if s.store.oracles.explorer != nil {
		request := s.stepRequest("read")
		request.Key = key
		request.Candidates = make([]ReadSource, 1, len(vers)+1)
		request.Candidates[0] = ReadSource{Initial: true}
		for _, v := range vers {
			request.Candidates = append(request.Candidates, ReadSource{Version: v.ID})
		}
		result, err := s.explore(ctx, request)
		if err != nil {
			s.latchReadError(err)
			return nil, 0, false
		}
		if !result.Enabled {
			return nil, 0, false
		}
		source := result.Source
		value := initial
		valid := source == (ReadSource{Initial: true})
		if !valid {
			for _, v := range vers {
				if source == (ReadSource{Version: v.ID}) {
					value, valid = v.Value, true
					break
				}
			}
		}
		if !valid {
			s.latchReadError(fmt.Errorf("explorer selected a source outside the committed read universe"))
			return nil, 0, false
		}
		s.store.recordAdmission(key, len(vers), -1, source.Initial, source.Version)
		s.logPick(key, source.Version, source.Initial)
		return value, source.Version, true
	}
	cands := make([]sqlReadCandidate, 0, len(vers)+1)
	// Initial candidate first (stalest): admitted only if the level's
	// axioms allow observing the initial state here.
	if s.admits(key, 0, true) {
		cands = append(cands, sqlReadCandidate{val: initial, initial: true})
	}
	for _, v := range vers {
		if s.admits(key, v.ID, false) {
			cands = append(cands, sqlReadCandidate{val: v.Value, id: v.ID})
		}
	}
	if len(cands) == 0 {
		if controller := s.store.sqlReplay; controller != nil {
			controller.fail("trace admission: no admitted source for %q/%q/%q", tab, pkey, col)
		}
		return nil, 0, false
	}
	pick := cands[0]
	deviated := false
	if controller := s.store.sqlReplay; controller != nil {
		idx, selectedDeviation, ok := controller.selectRead(s, tab, pkey, col, cands)
		if !ok {
			return nil, 0, false
		}
		pick = cands[idx]
		deviated = selectedDeviation
	} else if idx := s.store.replayChoice(len(cands)); idx >= 0 {
		pick = cands[idx]
	} else if s.readLatest {
		// Writer policy: newest admitted (last non-initial candidate).
		// Candidates order initial-first then oldest-to-newest, so the
		// tail is the freshest committed version admitted by the level.
		pick = cands[len(cands)-1]
	} else {
		txn := s.liveTxn()
		var index int
		index, deviated = s.store.selectReadIndex(len(cands), cands[0].initial, txn)
		pick = cands[index]
	}
	if !s.checkRead(key, pick.id, pick.initial, true) {
		if controller := s.store.sqlReplay; controller != nil {
			controller.fail("trace admission: selected source was rejected for %q/%q/%q", tab, pkey, col)
		}
		return nil, 0, false
	}
	if deviated {
		if txn := s.liveTxn(); txn != nil {
			txn.mu.Lock()
			txn.deviations++
			txn.mu.Unlock()
		}
	}
	s.store.recordAdmission(key, len(vers), len(cands), pick.initial, pick.id)
	s.logPick(key, pick.id, pick.initial)
	return pick.val, pick.id, true
}

func (s *Session) latchReadError(err error) {
	s.txnMu.Lock()
	defer s.txnMu.Unlock()
	if s.readErr == nil {
		s.readErr = err
	}
}

func (s *Session) takeReadError() error {
	s.txnMu.Lock()
	defer s.txnMu.Unlock()
	err := s.readErr
	s.readErr = nil
	return err
}

// logPick records the chosen read in the live txn or auto log.
func (s *Session) logPick(key string, id Version, initial bool) {
	if txn := s.liveTxn(); txn != nil {
		txn.logRead(ReadRecord{Session: s.sid, Key: key, WriteID: id, Found: !initial, Initial: initial})
	} else {
		if initial {
			s.store.appendAutoReadInitial(s.sid, key)
		} else {
			s.store.appendAutoRead(s.sid, key, id)
		}
		s.resetOracleTransaction(nil)
	}
}
