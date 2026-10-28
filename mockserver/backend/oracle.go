// Package backend: formal oracle over the dbcop specification language.
//
// This oracle implements MonkeyDB's read-extern rule with the real
// checkers. Committed transactions and the accepted live read prefix stay
// indexed in dbcop_core; each candidate adds one reversible read through
// the scalar C ABI. Only admitted candidates are returned for selection.
package backend

import (
	"log/slog"
	"sync"
)

// historyEvent mirrors dbcop_core Event<u64,u64> JSON.
type historyEvent struct {
	Read  *historyRW `json:"Read,omitempty"`
	Write *historyRW `json:"Write,omitempty"`
}

type historyRW struct {
	Variable uint64  `json:"variable"`
	Version  *uint64 `json:"version,omitempty"`
}

type historyTxn struct {
	Events    []historyEvent `json:"events"`
	Committed bool           `json:"committed"`
}

type nativeOracle struct {
	driver   nativeDriver
	revision int
	invalid  bool
	session  int
	txn      *Txn
	reads    int
}

type nativeDriver interface {
	read(session int, variable uint64, version Version, initial, accept bool) bool
	check(session int, events []historyEvent, commit bool) bool
	reset()
	close()
}

type oracleSet struct {
	mu       sync.Mutex
	states   map[Isolation]*nativeOracle
	closed   bool
	factory  func(Isolation) nativeDriver
	explorer Explorer
}

func newOracleSet(factory func(Isolation) nativeDriver, explorer Explorer) *oracleSet {
	return &oracleSet{states: make(map[Isolation]*nativeOracle), factory: factory, explorer: explorer}
}

func (o *oracleSet) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.resetLocked()
	o.states = nil
	o.closed = true
	if o.explorer != nil {
		if err := o.explorer.Close(); err != nil {
			slog.Error("explorer cleanup failed", "reason", err, "operation", "close")
		}
	}
}

// resetLocked frees all native history while retaining the cleanup-owned set.
// The caller holds o.mu, before taking the store mutex if both are needed.
func (o *oracleSet) resetLocked() {
	for _, state := range o.states {
		state.driver.close()
	}
	clear(o.states)
}

func (state *nativeOracle) forgetLive() {
	state.session = 0
	state.txn = nil
	state.reads = 0
}

func (state *nativeOracle) resetLive() {
	state.driver.reset()
	state.forgetLive()
}

func callOracleRead(state *nativeOracle, session int, variable uint64, version Version, initial, accept bool) bool {
	return state.driver.read(session, variable, version, initial, accept)
}

func callOracle(state *nativeOracle, session int, events []historyEvent, commit bool) bool {
	state.forgetLive()
	return state.driver.check(session, events, commit)
}

// oracleState synchronizes only newly committed transactions; callers hold set.mu.
func (s *Store) oracleState(iso Isolation) *nativeOracle {
	set := s.oracles
	if set.closed {
		panic("consistency check on a closed store")
	}
	state := set.states[iso]
	if state == nil {
		if set.factory == nil {
			panic("native consistency check on an explorer store")
		}
		state = &nativeOracle{driver: set.factory(iso)}
		set.states[iso] = state
	}
	if state.invalid {
		return nil
	}
	s.mu.RLock()
	if state.revision > len(s.log) {
		s.mu.RUnlock()
		panic("oracle commit was not published to the KV history")
	}
	pending := append([]LoggedTxn(nil), s.log[state.revision:]...)
	s.mu.RUnlock()
	for _, txn := range pending {
		events := s.transactionEvents(txn)
		if len(events) != 0 && !callOracle(state, txn.Session, events, true) {
			state.invalid = true
			return nil
		}
		state.revision++
	}
	return state
}

func (s *Store) checkTransaction(iso Isolation, session int, events []historyEvent, commit bool) bool {
	set := s.oracles
	set.mu.Lock()
	defer set.mu.Unlock()
	state := s.oracleState(iso)
	if state == nil {
		return false
	}
	if !callOracle(state, session, events, commit) {
		return false
	}
	if commit {
		// The caller holds the execution mutex and publishes this entry next.
		state.revision++
	}
	return true
}

func (s *Store) readEvent(read LoggedRead) historyEvent {
	value := historyRW{Variable: s.variableID(read.Key)}
	if !read.Initial {
		version := uint64(read.WriteID)
		value.Version = &version
	}
	return historyEvent{Read: &value}
}

func (s *Store) transactionEvents(txn LoggedTxn) []historyEvent {
	events := make([]historyEvent, 0, len(txn.Writes)+len(txn.Reads))
	write := func(w CommittedWrite) {
		version := uint64(w.ID)
		events = append(events, historyEvent{Write: &historyRW{Variable: s.variableID(w.Key), Version: &version}})
	}
	if len(txn.Events) != 0 {
		for _, event := range txn.Events {
			if event.Read != nil {
				events = append(events, s.readEvent(*event.Read))
			} else if event.Write != nil {
				write(*event.Write)
			}
		}
		return events
	}
	for _, w := range txn.Writes {
		write(w)
	}
	for _, read := range txn.Reads {
		events = append(events, s.readEvent(read))
	}
	return events
}

// levelName maps Isolation to the FFI level string.
func levelName(iso Isolation) string {
	switch iso {
	case RepeatableRead:
		return "repeatable-read"
	case AtomicRead:
		return "atomic-read"
	case Causal:
		return "causal"
	case Prefix:
		return "prefix"
	case SnapshotIsolation:
		return "snapshot-isolation"
	case Serializable:
		return "serializable"
	default:
		return "committed-read"
	}
}

// checkRead replays the live prefix only after invalidation, never per candidate.
func (s *Session) checkRead(key string, wid Version, initial, accept bool) bool {
	txn := s.liveTxn()
	set := s.store.oracles
	set.mu.Lock()
	defer set.mu.Unlock()
	state := s.store.oracleState(s.iso)
	if state == nil {
		return false
	}
	if state.session != s.sid || state.txn != txn {
		state.resetLive()
		state.session, state.txn = s.sid, txn
	}
	if txn != nil {
		txn.mu.Lock()
		if txn.oracles == nil {
			txn.oracles = set
		}
		if state.reads > len(txn.reads) {
			txn.mu.Unlock()
			panic("live oracle prefix was not reset after transaction restore")
		}
		for _, read := range txn.reads[state.reads:] {
			if !callOracleRead(state, s.sid, s.store.variableID(read.Key), read.WriteID, read.Initial, true) {
				txn.mu.Unlock()
				state.resetLive()
				return false
			}
			state.reads++
		}
		txn.mu.Unlock()
	}
	if !callOracleRead(state, s.sid, s.store.variableID(key), wid, initial, accept) {
		return false
	}
	if accept && txn != nil {
		state.reads++
	}
	return true
}

func (s *Session) admits(key string, wid Version, initial bool) bool {
	return s.checkRead(key, wid, initial, false)
}

func (s *Session) resetOracleTransaction(txn *Txn) {
	set := s.store.oracles
	set.mu.Lock()
	defer set.mu.Unlock()
	for _, state := range set.states {
		if state.session == s.sid && state.txn == txn {
			state.resetLive()
		}
	}
}

// liveEvents copies this session's live-txn event stream in order.
func liveEvents(s *Session) []TxnEvent {
	txn := s.liveTxn()
	if txn == nil {
		return nil
	}
	return txn.eventsSnapshot()
}

// buildSessions converts the global commit log plus this session's live
// external reads into dbcop sessions. Committed txns keep original
// session, position, writes, and reads in execution order. The live txn
// contributes only its external reads in execution order plus the
// tentative read: staged writes are invisible to the checker (they resolve
// via read-your-writes before the oracle, and uncommitted reads would fail
// committed-read validation). No sorting, no last-wins, no observer txns.
// Versions are global write-ids, unique by construction.
func buildSessions(log []LoggedTxn, liveSess int, live []TxnEvent) [][]historyTxn {
	order := []int{}
	seenSess := make(map[int]bool)
	for _, t := range log {
		if !seenSess[t.Session] {
			seenSess[t.Session] = true
			order = append(order, t.Session)
		}
	}
	if !seenSess[liveSess] {
		order = append(order, liveSess)
	}
	idx := make(map[int]int, len(order))
	for i, s := range order {
		idx[s] = i
	}
	sessions := make([][]historyTxn, len(order))
	for i := range sessions {
		sessions[i] = []historyTxn{}
	}
	txnOf := func(t LoggedTxn) historyTxn {
		// Prefer the recorded execution order when present; legacy
		// entries without Events fall back to writes-then-reads.
		if len(t.Events) > 0 {
			evs := make([]historyEvent, 0, len(t.Events))
			for _, e := range t.Events {
				if e.Read != nil {
					ro := readOf(*e.Read)
					evs = append(evs, historyEvent{Read: &ro})
				} else if e.Write != nil {
					vid := uint64(e.Write.ID)
					evs = append(evs, historyEvent{Write: &historyRW{Variable: varNum(e.Write.Key), Version: &vid}})
				}
			}
			return historyTxn{Events: evs, Committed: true}
		}
		evs := make([]historyEvent, 0, len(t.Writes)+len(t.Reads))
		for _, w := range t.Writes {
			vid := uint64(w.ID)
			evs = append(evs, historyEvent{Write: &historyRW{Variable: varNum(w.Key), Version: &vid}})
		}
		for _, r := range t.Reads {
			ro := readOf(r)
			evs = append(evs, historyEvent{Read: &ro})
		}
		return historyTxn{Events: evs, Committed: true}
	}
	for _, t := range log {
		sessions[idx[t.Session]] = append(sessions[idx[t.Session]], txnOf(t))
	}
	liveReads := make([]LoggedRead, 0, len(live))
	for _, e := range live {
		if e.IsRead {
			liveReads = append(liveReads, LoggedRead{Key: e.Read.Key, WriteID: e.Read.WriteID, Initial: e.Read.Initial})
		}
	}
	if len(liveReads) > 0 {
		evs := make([]historyEvent, 0, len(liveReads))
		for _, r := range liveReads {
			ro := readOf(r)
			evs = append(evs, historyEvent{Read: &ro})
		}
		sessions[idx[liveSess]] = append(sessions[idx[liveSess]], historyTxn{Events: evs, Committed: true})
	}
	return sessions
}

// readOf maps a read record to its checker event (null = initial).
func readOf(r LoggedRead) historyRW {
	ev := historyRW{Variable: varNum(r.Key)}
	if !r.Initial {
		ver := uint64(r.WriteID)
		ev.Version = &ver
	}
	return ev
}

// varNum hashes a cell key to a dense variable id, stable per call.
func varNum(key string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}
	return h
}
