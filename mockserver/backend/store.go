// Package backend implements a MonkeyDB-style MVCC backend for go-mysql-server.
//
// The engine (github.com/dolthub/go-mysql-server) provides the complete MySQL
// dialect: parser, planner, joins, aggregations, secondary indexes. This
// package provides the storage seam: versioned tables plus sessions that
// resolve reads through a dbcop isolation oracle, exactly like MonkeyDB
// (Biswas et al., OOPSLA 2021): the native default enumerates the writes the
// chosen isolation level allows and picks one (seedable RNG for reproducibility).
//
// Design: each cell is an append-only version chain. Writes stamp a global
// write id. Reads ask the oracle (delegating to dbcop_core checkers over the
// history so far) which committed version is visible, then return it.
//
// NewApalacheStore reuses this storage and SQL layer with an external TLA+/SMT
// successor explorer. It selects one solver model, not a seeded uniform sample.
package backend

import (
	"fmt"
	"runtime"
	"strings"
	"sync"

	"github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"
)

// Version identifies one committed cell write; doubles as the dbcop Version.
type Version uint64

// Cell holds the full version chain for one (table, pkey, column).
type Cell struct {
	Versions []CellVersion
}

// CellVersion is a single committed write.
type CellVersion struct {
	ID    Version
	Value interface{}
}

// Isolation selects the dbcop consistency level the oracle enforces.
// All seven levels of the hierarchy are available; the FFI checker
// decides admission per the level's axioms.
type Isolation int

const (
	// ReadCommitted: committed sources must respect within-transaction read order.
	ReadCommitted Isolation = iota
	// RepeatableRead: same-variable rereads must see the same writer.
	RepeatableRead
	// AtomicRead: no fractured reads across variables.
	AtomicRead
	// Causal: (wr+so)+ predecessors respected (cross-key chains).
	Causal
	// Prefix: reads see a consistent prefix of the commit order.
	Prefix
	// SnapshotIsolation: snapshot reads + no concurrent same-key writes.
	SnapshotIsolation
	// Serializable: fully prefix-consistent reads.
	Serializable
)

// Store is the versioned KV heap behind all tables, plus the single
// authoritative event log the oracle builds histories from.
type Store struct {
	mu     sync.RWMutex
	cells  map[string]*Cell
	nextID Version
	rng    *lockedRand
	// reg maps every committed write-id to its (key, writer session, seq).
	reg map[Version]WriteInfo
	// log is the global committed-transaction log in commit order.
	// Each entry preserves original session, seq, writes, and reads.
	log []LoggedTxn
	// seq issues commit sequence numbers.
	seq int
	// tables indexes physical table namespaces, including dropped generations.
	tables map[string]*Table
	// variables interns collision-free numeric IDs for the native oracle.
	variables map[string]uint64
	oracles   *oracleSet
	// weakRandom picks uniformly among admitted candidates by default.
	weakRandom           bool
	readPolicy           string
	deviationProbability float64
	deviationBudget      int
	// schedule, when non-empty, is a replayable choice cursor: the i-th
	// read takes schedule[i % len(schedule)] clamped to admitted range.
	// Set via SetSchedule; -1 entries fall back to default policy.
	// Every candidate index is reachable, so any admitted trace replays.
	schedule []int
	schedPos int
	// sqlReplay is opt-in for controlled workloads; ordinary reads do not trace.
	sqlReplay               *sqlReplayController
	candidateKeysForTesting func(*Table) []string
	// admissions logs per-read (key, committed, admitted, selected)
	// for admitted-vs-selected instrumentation.
	admissions []Admission
	// Initialization is an explicit, one-time conventional loading phase.
	initializing  bool
	initialSealed bool
	// commitMu serializes transaction execution like the artifact
	// (Mutex<SQLdb>): one live transaction at a time across sessions.
	// Read choices stay weak (per-candidate oracle trials); only the
	// interleaving is removed. Held from first staged write through
	// commit/rollback.
	commitMu sync.Mutex
}

// Admission records one oracle decision for instrumentation.
type Admission struct {
	Key       string
	Committed int
	Admitted  int
	Selected  Version
	Initial   bool
}

// SetSchedule installs a replayable choice schedule.
func (s *Store) SetSchedule(sched []int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schedule = append([]int(nil), sched...)
	s.schedPos = 0
}

// replayChoice returns the scheduled index for n candidates, or -1.
func (s *Store) replayChoice(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.schedule) == 0 || n == 0 {
		return -1
	}
	idx := s.schedule[s.schedPos%len(s.schedule)]
	s.schedPos++
	if idx < 0 {
		return -1
	}
	if idx >= n {
		return n - 1
	}
	return idx
}

// recordAdmission logs an oracle decision.
func (s *Store) recordAdmission(key string, committed, admitted int, initial bool, sel Version) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admissions = append(s.admissions, Admission{Key: key, Committed: committed, Admitted: admitted, Selected: sel, Initial: initial})
}

// Admissions returns a copy of the decision log.
func (s *Store) Admissions() []Admission {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Admission(nil), s.admissions...)
}

// LoggedTxn is one committed transaction with its original identity and
// events in execution order. Writes/Reads preserve the grouped view;
// Events is authoritative for checker histories.
type LoggedTxn struct {
	Session int
	Seq     int
	Writes  []CommittedWrite
	Reads   []LoggedRead
	Events  []LoggedEvent
}

// LoggedEvent is one committed event in execution order.
type LoggedEvent struct {
	Write *CommittedWrite
	Read  *LoggedRead
}

// LoggedRead is one external read with its read-from write-id.
// Initial marks reads of the initial (root) value: no writer version.
type LoggedRead struct {
	Key     string
	WriteID Version
	Initial bool
}

// WriteInfo locates one committed version for history building.
type WriteInfo struct {
	Key     string
	Session int
	Seq     int
}

// newStore owns common storage and cleanup without retaining the Store in cleanup state.
func newStore(seed1, seed2 uint64, oracles *oracleSet) *Store {
	store := &Store{
		cells: make(map[string]*Cell), rng: newLockedRand(seed1, seed2),
		reg: make(map[Version]WriteInfo), tables: make(map[string]*Table),
		variables: make(map[string]uint64), oracles: oracles, weakRandom: true,
		readPolicy: "uniform", deviationProbability: 0.01, deviationBudget: 1,
	}
	runtime.AddCleanup(store, (*oracleSet).close, store.oracles)
	return store
}

func newStoreWithExplorer(seed1, seed2 uint64, explorer Explorer) (*Store, error) {
	if explorer == nil {
		return nil, fmt.Errorf("explorer must not be nil")
	}
	return newStore(seed1, seed2, newOracleSet(nil, explorer)), nil
}

// Close releases the persistent solver state and optional explorer.
func (s *Store) Close() { s.oracles.close() }

// BeginInitialization enables latest-value reads while loading an empty store.
// NewStore otherwise keeps its normal weak-read behavior.
func (s *Store) BeginInitialization() error {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	s.oracles.mu.Lock()
	defer s.oracles.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oracles.closed {
		return fmt.Errorf("cannot initialize a closed store")
	}
	if s.initializing || s.initialSealed {
		return fmt.Errorf("initialization has already begun")
	}
	if len(s.cells) != 0 || len(s.reg) != 0 || len(s.log) != 0 || s.nextID != 0 {
		return fmt.Errorf("initialization requires an empty store")
	}
	s.initializing = true
	return nil
}

func (s *Store) isInitializing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.initializing
}

// SealInitialState atomically makes the loaded values the oracle's initial roots.
// It keeps the choice schedule and seeded RNG, restarting only the schedule cursor.
func (s *Store) SealInitialState() error {
	s.commitMu.Lock()
	defer s.commitMu.Unlock()
	s.oracles.mu.Lock()
	defer s.oracles.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.oracles.closed {
		return fmt.Errorf("cannot seal a closed store")
	}
	if !s.initializing {
		return fmt.Errorf("initialization is not active")
	}
	for _, cell := range s.cells {
		if len(cell.Versions) == 0 {
			continue
		}
		cell.Versions[0] = CellVersion{Value: cell.Versions[len(cell.Versions)-1].Value}
		clear(cell.Versions[1:])
		cell.Versions = cell.Versions[:1]
	}
	s.oracles.resetLocked()
	clear(s.reg)
	clear(s.variables)
	s.log = nil
	s.seq = 0
	s.admissions = nil
	s.schedPos = 0
	s.initializing = false
	s.initialSealed = true
	return nil
}

func (s *Store) registerTable(namespace string, table *Table) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tables[namespace] = table
}

func (s *Store) variableID(key string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id, ok := s.variables[key]; ok {
		return id
	}
	id := uint64(len(s.variables)) + 1
	s.variables[key] = id
	return id
}

// key builds an unambiguous address while keeping simple keys readable.
func key(tab string, pkey, col string) string {
	if strings.HasPrefix(tab, "@") || strings.Contains(tab, ".") || strings.Contains(pkey, ".") || strings.Contains(col, ".") {
		return fmt.Sprintf("@%d:%s%d:%s%d:%s", len(tab), tab, len(pkey), pkey, len(col), col)
	}
	return fmt.Sprintf("%s.%s.%s", tab, pkey, col)
}

// cellKey builds the canonical cell address for oracle history.
func cellKey(tab, pkey, col string) string { return key(tab, pkey, col) }

// CommittedWrite records one published version for oracle history.
type CommittedWrite struct {
	Key    string
	ID     Version
	Writer int
}

// Write appends a version and returns its id. The caller must record the
// writer session/seq via RegisterWriter right after commit staging.
func (s *Store) Write(tab, pkey, col string, val interface{}) Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	k := key(tab, pkey, col)
	c := s.cells[k]
	if c == nil {
		c = &Cell{}
		s.cells[k] = c
	}
	c.Versions = append(c.Versions, CellVersion{ID: s.nextID, Value: val})
	s.reg[s.nextID] = WriteInfo{Key: k}
	return s.nextID
}

// RegisterWriter attaches writer identity to a committed version.
func (s *Store) RegisterWriter(id Version, session, seq int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if w, ok := s.reg[id]; ok {
		w.Session = session
		w.Seq = seq
		s.reg[id] = w
	}
}

// appendLog records one committed transaction in commit order.
func (s *Store) appendLog(session, seq int, writes []CommittedWrite, reads []LoggedRead) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, LoggedTxn{Session: session, Seq: seq, Writes: writes, Reads: reads})
}

// logSnapshot copies the global commit log.
func (s *Store) logSnapshot() []LoggedTxn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]LoggedTxn(nil), s.log...)
}

// CommittedHistory returns an independent history snapshot without application values.
func (s *Store) CommittedHistory() []LoggedTxn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	log := append([]LoggedTxn(nil), s.log...)
	for i := range log {
		txn := &log[i]
		txn.Writes = append([]CommittedWrite(nil), txn.Writes...)
		txn.Reads = append([]LoggedRead(nil), txn.Reads...)
		txn.Events = append([]LoggedEvent(nil), txn.Events...)
		for j := range txn.Events {
			event := &txn.Events[j]
			if event.Write != nil {
				write := *event.Write
				event.Write = &write
			}
			if event.Read != nil {
				read := *event.Read
				event.Read = &read
			}
		}
	}
	return log
}

// nextSeq issues the next commit sequence number for a session.
func (s *Store) nextSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// appendAutoRead logs an autocommit (single-statement) read as its own
// one-txn entry so the history preserves it with true session identity.
func (s *Store) appendAutoRead(session int, key string, wid Version) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, LoggedTxn{Session: session, Seq: s.seq, Reads: []LoggedRead{{Key: key, WriteID: wid}}})
	s.seq++
}

// appendAutoReadInitial logs an autocommit initial-value read.
func (s *Store) appendAutoReadInitial(session int, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.log = append(s.log, LoggedTxn{Session: session, Seq: s.seq, Reads: []LoggedRead{{Key: key, Initial: true}}})
	s.seq++
}

// applyMember retains known keys; existence itself is a versioned cell.
func (s *Store) applyMember(tab, pkey string, present bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	applyMemberLocked(s, tab, pkey, present)
}

// applyMemberLocked preserves tombstoned keys for historical reads.
func applyMemberLocked(s *Store, tab, pkey string, _ bool) {
	if table := s.tables[tab]; table != nil {
		table.state.mu.Lock()
		table.state.keys[pkey] = struct{}{}
		table.state.mu.Unlock()
	}
}

// reserveIDs atomically reserves n version ids without publishing.
func (s *Store) reserveIDs(n int) Version {
	s.mu.Lock()
	defer s.mu.Unlock()
	base := s.nextID
	s.nextID += Version(n)
	return base
}

// committedVersions returns a copy of the committed chain for key.
func (s *Store) committedVersions(k string) []CellVersion {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cells[k]
	if c == nil {
		return nil
	}
	return append([]CellVersion(nil), c.Versions...)
}

// latestVersion avoids copying a chain for conventional initialization reads.
func (s *Store) latestVersion(k string) (CellVersion, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	c := s.cells[k]
	if c == nil || len(c.Versions) == 0 {
		return CellVersion{}, false
	}
	return c.Versions[len(c.Versions)-1], true
}

// lookup returns registry info for a write-id.
func (s *Store) lookup(id Version) (WriteInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	w, ok := s.reg[id]
	return w, ok
}

// Schema helpers shared by table implementations.

func intCol(name string) *sql.Column {
	return &sql.Column{Name: name, Type: types.Int64, Nullable: true}
}
func strCol(name string) *sql.Column {
	return &sql.Column{Name: name, Type: types.Text, Nullable: true}
}

// NOTE: candidate filtering lives in oracle.go via the real dbcop checker.
// There is intentionally no per-cell heuristic here.
