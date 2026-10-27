// Package backend: transaction staging over the versioned store.
//
// Writes are staged per-transaction and published to the Store only on
// commit. Reads see staged (uncommitted) writes first — read-your-writes —
// then fall back to the committed oracle below. Rollback and
// DiscardChanges drop staged writes. This is what makes the version log
// contain committed versions only, which the isolation oracle assumes.
package backend

import (
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/dolthub/go-mysql-server/sql"
)

// errCommitRejected aborts a commit whose whole-history gate failed.
var errCommitRejected = sql.ErrLockDeadlock.New("history would violate isolation level")

// StagedWrite is one uncommitted cell write.
type StagedWrite struct {
	Tab   string
	Pkey  string
	Col   string
	Value interface{}
}

// TxnEvent is one live-txn operation in execution order: either a staged
// write or an external read (value copies, not aliases into growing lists).
type TxnEvent struct {
	Write  StagedWrite
	Read   ReadRecord
	IsRead bool
}

// Txn holds staged writes, staged membership changes, and the ordered
// event stream for oracle history.
type Txn struct {
	mu     sync.Mutex
	writes []StagedWrite
	latest map[[3]string]int
	member []StagedMember
	// events records writes and external reads in execution order.
	events []TxnEvent
	// reads records (key, serving write-id) for history export + oracle.
	reads       []ReadRecord
	oracles     *oracleSet
	keylessRows map[string][]keylessRow
	savepoints  []namedSavepoint
	deviations  int
}

type txnCheckpoint struct {
	writes     int
	member     int
	events     int
	reads      int
	deviations int
}

type namedSavepoint struct {
	name       string
	checkpoint txnCheckpoint
}

func (t *Txn) checkpoint() txnCheckpoint {
	t.mu.Lock()
	defer t.mu.Unlock()
	return txnCheckpoint{writes: len(t.writes), member: len(t.member), events: len(t.events), reads: len(t.reads), deviations: t.deviations}
}

// restore drops work after a statement boundary or named savepoint.
func (t *Txn) restore(c txnCheckpoint) {
	t.mu.Lock()
	defer func() {
		t.mu.Unlock()
		t.resetOracle()
	}()
	t.writes = slices.Delete(t.writes, c.writes, len(t.writes))
	t.member = slices.Delete(t.member, c.member, len(t.member))
	t.events = slices.Delete(t.events, c.events, len(t.events))
	t.reads = slices.Delete(t.reads, c.reads, len(t.reads))
	t.deviations = c.deviations
	clear(t.latest)
	clear(t.keylessRows)
	for i, write := range t.writes {
		t.latest[[3]string{write.Tab, write.Pkey, write.Col}] = i
	}
	for i, point := range t.savepoints {
		p := point.checkpoint
		if p.writes > c.writes || p.member > c.member || p.events > c.events || p.reads > c.reads || p.deviations > c.deviations {
			t.savepoints = slices.Delete(t.savepoints, i, len(t.savepoints))
			break
		}
	}
}

// StagedMember is one uncommitted membership change.
type StagedMember struct {
	Tab     string
	Pkey    string
	Present bool
}

// ReadRecord logs one external read for the dbcop history.
type ReadRecord struct {
	Session int
	Key     string
	WriteID Version
	Found   bool
	Initial bool
}

func (t *Txn) stage(w StagedWrite) {
	t.mu.Lock()
	t.writes = append(t.writes, w)
	if t.latest == nil {
		t.latest = make(map[[3]string]int)
	}
	t.latest[[3]string{w.Tab, w.Pkey, w.Col}] = len(t.writes) - 1
	t.events = append(t.events, TxnEvent{Write: w})
	t.mu.Unlock()
}

// stageMembership records a membership change (insert present / delete gone).
func (t *Txn) stageMembership(tab, pkey string, present bool) {
	t.mu.Lock()
	t.member = append(t.member, StagedMember{Tab: tab, Pkey: pkey, Present: present})
	w := StagedWrite{Tab: tab, Pkey: pkey, Col: rowExistenceColumn, Value: present}
	t.writes = append(t.writes, w)
	if t.latest == nil {
		t.latest = make(map[[3]string]int)
	}
	t.latest[[3]string{tab, pkey, rowExistenceColumn}] = len(t.writes) - 1
	t.events = append(t.events, TxnEvent{Write: w})
	t.mu.Unlock()
}

// memberSnapshot copies staged membership changes under lock.
func (t *Txn) memberSnapshot() []StagedMember {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]StagedMember(nil), t.member...)
}

// staged returns the latest staged value for a cell, if any.
func (t *Txn) staged(tab, pkey, col string) (interface{}, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i, ok := t.latest[[3]string{tab, pkey, col}]; ok {
		return t.writes[i].Value, true
	}
	return nil, false
}

// stagedSource includes the one-based position in the full staged-write stream.
func (t *Txn) stagedSource(tab, pkey, col string) (interface{}, Version, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if i, ok := t.latest[[3]string{tab, pkey, col}]; ok {
		return t.writes[i].Value, Version(i + 1), true
	}
	return nil, 0, false
}

// stagedPresent reports whether pkey is staged present/absent, if staged.
func (t *Txn) stagedPresent(tab, pkey string) (bool, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.member) - 1; i >= 0; i-- {
		m := t.member[i]
		if m.Tab == tab && m.Pkey == pkey {
			return m.Present, true
		}
	}
	return false, false
}

func (t *Txn) logRead(r ReadRecord) {
	t.mu.Lock()
	t.reads = append(t.reads, r)
	t.events = append(t.events, TxnEvent{Read: r, IsRead: true})
	t.mu.Unlock()
}

// readsSnapshot copies the txn read log under lock.
func (t *Txn) readsSnapshot() []ReadRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]ReadRecord(nil), t.reads...)
}

// eventsSnapshot copies the ordered event stream under lock.
func (t *Txn) eventsSnapshot() []TxnEvent {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]TxnEvent(nil), t.events...)
}

// pending returns staged writes and clears the buffer (commit path).
func (t *Txn) pending() []StagedWrite {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.writes
	t.writes = nil
	t.latest = nil
	return out
}

// pendingMember returns staged membership and clears it (commit path).
func (t *Txn) pendingMember() []StagedMember {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.member
	t.member = nil
	return out
}

func (t *Txn) resetOracle() {
	if t.oracles == nil {
		return
	}
	t.oracles.mu.Lock()
	defer t.oracles.mu.Unlock()
	for _, state := range t.oracles.states {
		if state.txn == t {
			state.resetLive()
		}
	}
}

// discard drops the transaction's staged state and event stream.
func (t *Txn) discard() {
	t.mu.Lock()
	t.writes = nil
	t.latest = nil
	t.member = nil
	t.events = nil
	t.reads = nil
	t.keylessRows = nil
	t.savepoints = nil
	t.deviations = 0
	t.mu.Unlock()
	t.resetOracle()
}

// Session transaction state: at most one live Txn per session.

func (s *Session) clearTxn() {
	s.txnMu.Lock()
	txn := s.txn
	s.txn = nil
	s.explicit = false
	s.readErr = nil
	held := s.holdsCommit
	s.holdsCommit = false
	s.txnMu.Unlock()
	if txn != nil {
		txn.mu.Lock()
		txn.savepoints = nil
		txn.mu.Unlock()
	}
	s.resetOracleTransaction(txn)
	if s.BaseSession != nil {
		s.BaseSession.SetTransaction(nil)
		s.BaseSession.SetIgnoreAutoCommit(false)
	}
	if held {
		s.store.commitMu.Unlock()
	}
}

// beginTxn returns the live txn and acquires execution ownership if needed.
// Empty command boundaries release ownership without ending the transaction;
// observed data and staged writes keep it until commit/rollback.
func (s *Session) beginTxn() *Txn {
	s.txnMu.Lock()
	defer s.txnMu.Unlock()
	if s.txn == nil {
		s.txn = &Txn{oracles: s.store.oracles}
	}
	if !s.holdsCommit {
		s.store.commitMu.Lock()
		s.holdsCommit = true
	}
	return s.txn
}

// liveTxn returns the live txn or nil.
func (s *Session) liveTxn() *Txn {
	s.txnMu.Lock()
	defer s.txnMu.Unlock()
	return s.txn
}

// StartTransaction begins staging for both implicit and explicit engine transactions.
func (s *Session) StartTransaction(_ *sql.Context, _ sql.TransactionCharacteristic) (sql.Transaction, error) {
	s.txnMu.Lock()
	s.explicit = true
	s.txnMu.Unlock()
	s.beginTxn()
	return &backendTxn{session: s}, nil
}

// inExplicitTxn reports whether the engine owns transaction completion.
func (s *Session) inExplicitTxn() bool {
	s.txnMu.Lock()
	defer s.txnMu.Unlock()
	return s.explicit
}

// CommitTransaction builds proposed versions off-store, replays the live
// txn's ordered event stream through the persistent oracle, and publishes
// only on success. Rejection leaves committed versions and history unchanged.
func (s *Session) CommitTransaction(ctx *sql.Context, _ sql.Transaction) error {
	txn := s.liveTxn()
	if txn == nil {
		return nil
	}
	writes := txn.pending()
	members := txn.pendingMember()
	if state := txn.checkpoint(); len(writes) == 0 && len(members) == 0 && state.events == 0 {
		s.clearTxn()
		return nil
	}
	initializing := s.store.isInitializing()
	// Proposed versions use reserved ids without touching the store.
	base := s.store.reserveIDs(len(writes))
	lt := LoggedTxn{Session: s.sid}
	if !initializing {
		lt.Seq = s.store.nextSeq()
		stream := txn.eventsSnapshot()
		evs := make([]LoggedEvent, 0, len(stream))
		wi := 0
		for _, e := range stream {
			if e.IsRead {
				r := LoggedRead{Key: e.Read.Key, WriteID: e.Read.WriteID, Initial: e.Read.Initial}
				lt.Reads = append(lt.Reads, r)
				rr := r
				evs = append(evs, LoggedEvent{Read: &rr})
				continue
			}
			w := writes[wi]
			wi++
			k := key(w.Tab, w.Pkey, w.Col)
			id := base + Version(wi)
			cw := CommittedWrite{Key: k, ID: id, Writer: lt.Seq}
			lt.Writes = append(lt.Writes, cw)
			cc := cw
			evs = append(evs, LoggedEvent{Write: &cc})
		}
		lt.Events = evs
		if s.store.oracles.explorer != nil {
			request := s.stepRequest("commit")
			request.Transaction = lt
			result, err := s.explore(explorerContext(ctx), request)
			if err != nil {
				s.clearTxn()
				return err
			}
			if !result.Enabled {
				s.clearTxn()
				return errCommitRejected
			}
		} else if !s.store.checkTransaction(s.iso, s.sid, s.store.transactionEvents(lt), true) {
			s.clearTxn()
			return errCommitRejected
		}
	}
	s.store.mu.Lock()
	for i, w := range writes {
		k := key(w.Tab, w.Pkey, w.Col)
		id := base + Version(i+1)
		c := s.store.cells[k]
		if c == nil {
			c = &Cell{}
			s.store.cells[k] = c
		}
		c.Versions = append(c.Versions, CellVersion{ID: id, Value: w.Value})
		if !initializing {
			s.store.reg[id] = WriteInfo{Key: k, Session: s.sid, Seq: lt.Seq}
		}
	}
	if !initializing {
		s.store.log = append(s.store.log, lt)
	}
	for _, m := range members {
		applyMemberLocked(s.store, m.Tab, m.Pkey, m.Present)
	}
	s.store.mu.Unlock()
	s.clearTxn()
	return nil
}

// Rollback drops staged writes (implements sql.TransactionSession).
func (s *Session) Rollback(_ *sql.Context, _ sql.Transaction) error {
	if txn := s.liveTxn(); txn != nil {
		txn.discard()
	}
	s.clearTxn()
	return nil
}

// CreateSavepoint records a named transaction prefix, replacing an older name.
func (s *Session) CreateSavepoint(_ *sql.Context, _ sql.Transaction, name string) error {
	txn := s.liveTxn()
	if txn == nil {
		return errors.New("savepoint requires an active transaction")
	}
	txn.mu.Lock()
	defer txn.mu.Unlock()
	for i, point := range txn.savepoints {
		if strings.EqualFold(point.name, name) {
			txn.savepoints = slices.Delete(txn.savepoints, i, i+1)
			break
		}
	}
	checkpoint := txnCheckpoint{writes: len(txn.writes), member: len(txn.member), events: len(txn.events), reads: len(txn.reads), deviations: txn.deviations}
	txn.savepoints = append(txn.savepoints, namedSavepoint{name: name, checkpoint: checkpoint})
	return nil
}

// RollbackToSavepoint retains the named prefix and discards later savepoints.
func (s *Session) RollbackToSavepoint(_ *sql.Context, _ sql.Transaction, name string) error {
	if txn := s.liveTxn(); txn != nil {
		txn.mu.Lock()
		for i, point := range txn.savepoints {
			if strings.EqualFold(point.name, name) {
				txn.savepoints = slices.Delete(txn.savepoints, i+1, len(txn.savepoints))
				txn.mu.Unlock()
				txn.restore(point.checkpoint)
				s.takeReadError()
				return nil
			}
		}
		txn.mu.Unlock()
	}
	return sql.ErrSavepointDoesNotExist.New(name)
}

// ReleaseSavepoint removes only the named checkpoint, retaining staged work.
func (s *Session) ReleaseSavepoint(_ *sql.Context, _ sql.Transaction, name string) error {
	if txn := s.liveTxn(); txn != nil {
		txn.mu.Lock()
		defer txn.mu.Unlock()
		for i, point := range txn.savepoints {
			if strings.EqualFold(point.name, name) {
				txn.savepoints = slices.Delete(txn.savepoints, i, i+1)
				return nil
			}
		}
	}
	return sql.ErrSavepointDoesNotExist.New(name)
}

// String identifies the transaction handle.
func (b *backendTxn) String() string { return "mock-txn" }

// IsReadOnly reports a read-write transaction.
func (b *backendTxn) IsReadOnly() bool { return false }

// backendTxn is the opaque transaction handle.
type backendTxn struct {
	session *Session
}
