//! Append-only checking without replaying the committed raw history.
//!
//! Successful commits are the invariant: the committed prefix already satisfies
//! `level`. Versions are positive and unique per variable; `None` and `Some(0)`
//! denote the initial value. Reserving zero prevents a future write from changing
//! the meaning of an already validated initial read.
//!
//! All versions (including overwritten local versions) and final writers remain
//! indexed. Only the search projection is reduced: retain externally-reading
//! transactions and writers of externally-read variables. For SI also retain all
//! variables written by externally-reading transactions and their writers. An
//! omitted transaction has no external reads and writes neither observed values
//! nor a variable locked by a reading transaction. Insert its adjacent read/write
//! phases in its session-order slot to lift a projected witness; its other write
//! conflicts are only with other blind writers. Moving each blind read phase
//! immediately before its write phase only shrinks its lock interval, so these
//! phases can be serialized. Restricting session order preserves every
//! path through omitted transactions. Future observations promote old writers
//! from the complete indexes, so this is not deletion of committed constraints.
//!
//! Causal visibility, commit-order closure, per-variable write/read graphs and
//! session order persist. A mutation journal removes exactly a trial's additions.
//! Writerless initial reads keep a compact reader index; their per-variable
//! graphs are materialized when a real writer arrives. Such variables impose
//! no search ordering until then, but their reads remain indexed and validated.
//! Atomic Read derives write order from the unsaturated base; Causal and stronger
//! levels use only the session/write-read closure, never inferred commit edges.
//! NPC orders extend without search when the candidate reads the order's final
//! values (in particular every blind-write tail), or its read phase fits a snapshot
//! without moving any old phase. Otherwise the existing solver searches the
//! changed projection; its old DFS nogoods are deliberately not reused across
//! changed constraints. No complete raw history is stored or rebuilt.

use alloc::collections::BTreeSet;
use alloc::vec::Vec;

use hashbrown::{HashMap, HashSet};

use super::error::Error;
use super::linearization::constrained_linearization::ConstrainedLinearizationSolver;
use super::linearization::prefix::PrefixConsistencySolver;
use super::linearization::serializable::SerializabilitySolver;
use super::linearization::snapshot_isolation::SnapshotIsolationSolver;
use super::Consistency;
use crate::graph::digraph::DiGraph;
use crate::history::atomic::types::{
    AtomicTransactionHistory, AtomicTransactionInfo, TransactionId,
};
use crate::history::atomic::AtomicTransactionPO;
use crate::history::raw::error::Error as RawError;
use crate::history::raw::types::{Event, EventId};

type CheckResult<T> = Result<T, Error<u64, u64>>;

#[derive(Clone, Copy)]
struct Write {
    id: EventId,
    final_id: EventId,
    final_version: u64,
}

enum Writers {
    One(TransactionId),
    Many(Vec<TransactionId>),
}

impl Writers {
    fn as_slice(&self) -> &[TransactionId] {
        match self {
            Self::One(tid) => core::slice::from_ref(tid),
            Self::Many(tids) => tids,
        }
    }

    fn push(&mut self, tid: TransactionId) {
        match self {
            Self::One(first) => *self = Self::Many(Vec::from([*first, tid])),
            Self::Many(tids) => tids.push(tid),
        }
    }
}

struct Read {
    variable: u64,
    writer: EventId,
}

struct Candidate {
    id: TransactionId,
    writes: HashMap<u64, (u64, EventId)>,
    versions: Vec<(u64, u64, EventId)>,
    reads: Vec<Read>,
}

#[derive(Clone, Copy)]
enum Relation {
    Session,
    Base,
    Visibility,
    Union,
    WriteRead(u64),
}

enum Undo {
    Observed(u64),
    InitialReader(u64),
    Transaction(TransactionId),
    Write(TransactionId, u64),
    Read(TransactionId, u64, Option<TransactionId>),
    Vertex(Relation, TransactionId),
    Edge(Relation, TransactionId, TransactionId),
}

struct LiveRead {
    first: EventId,
}

struct LivePrefix {
    id: TransactionId,
    length: u64,
    reads: HashMap<u64, LiveRead>,
    sources: HashSet<TransactionId>,
    journal: Vec<Undo>,
    order: Vec<(TransactionId, bool)>,
    write_positions: HashMap<TransactionId, usize>,
    read_position: usize,
}

impl LivePrefix {
    fn new(id: TransactionId) -> Self {
        Self {
            id,
            length: 0,
            reads: HashMap::default(),
            sources: HashSet::default(),
            journal: Vec::new(),
            order: Vec::new(),
            write_positions: HashMap::default(),
            read_position: 0,
        }
    }

    fn replace_order(&mut self, order: Vec<(TransactionId, bool)>) {
        self.write_positions.clear();
        self.read_position = order.iter().position(|&(tid, _)| tid == self.id).unwrap();
        for (position, &(tid, write_phase)) in order.iter().enumerate() {
            if write_phase {
                self.write_positions.insert(tid, position);
            }
        }
        self.order = order;
    }
}

/// A persistent checker for one store, accessed serially by its caller.
///
/// Session zero and write version zero are reserved. A trial never publishes a
/// transaction or consumes its session height; a failed commit has the same
/// isolation. The return value is a verdict; potentially large witnesses remain
/// internal rather than being copied on every event.
pub struct IncrementalChecker {
    level: Consistency,
    heights: HashMap<u64, u64>,
    versions: HashMap<(u64, u64), Write>,
    writers: HashMap<u64, Writers>,
    observed: HashSet<u64>,
    initial_readers: HashMap<u64, Writers>,
    sessions: HashMap<u64, BTreeSet<u64>>,
    po: AtomicTransactionPO<u64>,
    base: DiGraph<TransactionId>,
    base_reverse: HashMap<TransactionId, HashSet<TransactionId>>,
    reverse: HashMap<TransactionId, HashSet<TransactionId>>,
    // Serializable uses only write phases; Prefix and SI retain both phases.
    order: Vec<(TransactionId, bool)>,
    latest: HashMap<u64, TransactionId>,
    // The committed order/cache stay untouched while a live witness evolves.
    live: Option<LivePrefix>,
    #[cfg(test)]
    validated_reads: core::cell::Cell<usize>,
    #[cfg(test)]
    searched_reads: core::cell::Cell<usize>,
}

impl IncrementalChecker {
    #[must_use]
    pub fn new(level: Consistency) -> Self {
        Self {
            level,
            heights: HashMap::default(),
            versions: HashMap::default(),
            writers: HashMap::default(),
            observed: HashSet::default(),
            initial_readers: HashMap::default(),
            sessions: HashMap::default(),
            po: AtomicTransactionPO::from(AtomicTransactionHistory(HashMap::default())),
            base: DiGraph::default(),
            base_reverse: HashMap::default(),
            reverse: HashMap::default(),
            order: Vec::new(),
            latest: HashMap::default(),
            live: None,
            #[cfg(test)]
            validated_reads: core::cell::Cell::new(0),
            #[cfg(test)]
            searched_reads: core::cell::Cell::new(0),
        }
    }

    /// Check the complete tentative transaction, not the prior history.
    ///
    /// Only a successful `commit = true` call advances the committed prefix.
    /// Reads of a locally written variable must name its latest preceding local
    /// write, even if that version is overwritten later in the transaction.
    ///
    /// # Errors
    /// Returns structural read/version errors or a violation of the configured
    /// consistency level. Session zero, exhausted heights and write version zero
    /// are invalid inputs and return `Error::Invalid`.
    pub fn check_transaction(
        &mut self,
        session_id: u64,
        events: &[Event<u64, u64>],
        commit: bool,
    ) -> CheckResult<()> {
        self.reset_transaction();
        let candidate = self.validate(session_id, events)?;
        let mut journal = Vec::new();
        let mut dirty = HashSet::default();
        let result = self.extend(&candidate, &mut journal, &mut dirty);
        let result = result.and_then(|invalidated| {
            self.saturate(&mut journal, &mut dirty)?;
            if !self.is_npc() || !self.po.history.0.contains_key(&candidate.id) {
                return Ok(None);
            }
            if !invalidated
                && candidate.reads.iter().all(|r| {
                    self.latest.get(&r.variable).copied().unwrap_or_default()
                        == r.writer.transaction_id()
                })
            {
                // A serial tail also satisfies Prefix and SI (no overlapping
                // phases), independently of changes to the saturated graph.
                Ok(None)
            } else if !invalidated {
                self.reuse_snapshot(&candidate)
                    .map_or_else(|| self.search().map(Some), |order| Ok(Some(order)))
            } else {
                self.search().map(Some)
            }
        });
        match result {
            Ok(replacement) if commit => {
                if self.is_npc() && self.po.history.0.contains_key(&candidate.id) {
                    if let Some(order) = replacement {
                        self.order = order;
                        self.latest.clear();
                        for &(tid, write_phase) in &self.order {
                            if write_phase {
                                for &variable in &self.po.history.0[&tid].writes {
                                    self.latest.insert(variable, tid);
                                }
                            }
                        }
                    } else {
                        if !matches!(self.level, Consistency::Serializable) {
                            self.order.push((candidate.id, false));
                        }
                        self.order.push((candidate.id, true));
                        for &variable in &self.po.history.0[&candidate.id].writes {
                            self.latest.insert(variable, candidate.id);
                        }
                    }
                }
                for &(variable, version, id) in &candidate.versions {
                    let &(final_version, final_id) = &candidate.writes[&variable];
                    self.versions.insert(
                        (variable, version),
                        Write {
                            id,
                            final_id,
                            final_version,
                        },
                    );
                }
                for &variable in candidate.writes.keys() {
                    self.writers
                        .entry(variable)
                        .and_modify(|writers| writers.push(candidate.id))
                        .or_insert(Writers::One(candidate.id));
                }
                self.heights
                    .insert(session_id, candidate.id.session_height + 1);
                Ok(())
            }
            Ok(_) => {
                self.rollback(journal);
                Ok(())
            }
            Err(error) => {
                self.rollback(journal);
                Err(error)
            }
        }
    }

    /// Check one new external read against this session's retained live prefix.
    ///
    /// `accept = false` is a trial; `true` retains a successful read and its
    /// graph mutations, without publishing versions or consuming session height.
    /// Local reads remain the responsibility of `check_transaction`.
    ///
    /// # Errors
    /// Returns the same read/consistency errors as `check_transaction`. Session
    /// zero, exhausted heights, or changing the live session without resetting
    /// return `Error::Invalid`.
    pub fn check_read(
        &mut self,
        session_id: u64,
        variable: u64,
        version: Option<u64>,
        accept: bool,
    ) -> CheckResult<()> {
        let mut live = self.live.take();
        let result = self.append_read(session_id, variable, version, accept, &mut live);
        self.live = live;
        result
    }

    /// Discard accepted live reads and restore the committed graph and witness.
    pub fn reset_transaction(&mut self) {
        if let Some(live) = self.live.take() {
            self.rollback(live.journal);
        }
    }

    #[allow(clippy::too_many_lines)]
    fn append_read(
        &mut self,
        session_id: u64,
        variable: u64,
        version: Option<u64>,
        accept: bool,
        live: &mut Option<LivePrefix>,
    ) -> CheckResult<()> {
        let id = self.transaction_id(session_id)?;
        if live.as_ref().is_some_and(|prefix| prefix.id != id) {
            return Err(Error::Invalid(self.level));
        }
        let position = live.as_ref().map_or(0, |prefix| prefix.length);
        if position == u64::MAX {
            return Err(Error::Invalid(self.level));
        }
        let event_id = EventId {
            session_id,
            session_height: id.session_height,
            transaction_height: position,
        };
        let event = Event::Read { variable, version };
        let writer = self.read_source(variable, version, event_id, &event, &HashMap::default())?;
        let prior = live.as_ref().and_then(|prefix| prefix.reads.get(&variable));
        if !matches!(self.level, Consistency::CommittedRead) {
            if let Some(previous) = prior {
                if previous.first != writer {
                    return Err(RawError::NonRepeatableRead {
                        read_event: event,
                        read_event_id: event_id,
                        write_event_ids: [previous.first, writer],
                    }
                    .into());
                }
            }
        }
        let source = writer.transaction_id();
        let unchanged = !matches!(
            self.level,
            Consistency::CommittedRead | Consistency::RepeatableRead
        ) && self
            .po
            .history
            .0
            .get(&id)
            .and_then(|info| info.reads.get(&variable))
            == Some(&source);
        let mut journal = Vec::new();
        let mut dirty = HashSet::default();
        let result = if unchanged {
            Ok(None)
        } else {
            (|| {
                self.observe(variable, &mut journal, &mut dirty)?;
                self.transaction(id, &mut journal, &mut dirty)?;
                self.add_read(id, &Read { variable, writer }, &mut journal, &mut dirty)?;
                self.order_prior_reads(
                    variable,
                    source,
                    live.iter()
                        .flat_map(|prefix| prefix.sources.iter().copied()),
                    &mut journal,
                    &mut dirty,
                )?;
                self.saturate(&mut journal, &mut dirty)?;
                if !self.is_npc() {
                    return Ok(None);
                }
                if let Some(prefix) = live {
                    if prefix.write_positions.len() == self.po.history.0.len()
                        && self.live_source(prefix, variable) == source
                    {
                        Ok(None)
                    } else {
                        self.search().map(Some)
                    }
                } else {
                    self.first_read_order(id, variable, writer).map(Some)
                }
            })()
        };
        match result {
            Ok(order) if accept => {
                let prefix = live.get_or_insert_with(|| LivePrefix::new(id));
                if let Some(order) = order {
                    prefix.replace_order(order);
                }
                // Reset removes this tentative transaction, including its reads.
                journal.retain(|undo| !matches!(undo, Undo::Read(tid, _, _) if *tid == id));
                prefix.journal.append(&mut journal);
                prefix
                    .reads
                    .entry(variable)
                    .or_insert(LiveRead { first: writer });
                if matches!(
                    self.level,
                    Consistency::CommittedRead | Consistency::RepeatableRead
                ) && source != TransactionId::root()
                {
                    prefix.sources.insert(source);
                }
                prefix.length += 1;
                Ok(())
            }
            Ok(_) => {
                self.rollback(journal);
                Ok(())
            }
            Err(error) => {
                self.rollback(journal);
                Err(error)
            }
        }
    }

    fn live_source(&self, prefix: &LivePrefix, variable: u64) -> TransactionId {
        self.writers
            .get(&variable)
            .into_iter()
            .flat_map(Writers::as_slice)
            .filter_map(|tid| {
                prefix
                    .write_positions
                    .get(tid)
                    .map(|&position| (position, *tid))
            })
            .filter(|&(position, _)| position < prefix.read_position)
            .max_by_key(|&(position, _)| position)
            .map_or_else(TransactionId::root, |(_, tid)| tid)
    }

    fn first_read_order(
        &self,
        id: TransactionId,
        variable: u64,
        writer: EventId,
    ) -> CheckResult<Vec<(TransactionId, bool)>> {
        let phases = if matches!(self.level, Consistency::Serializable) {
            1
        } else {
            2
        };
        if self.order.len() / phases + 1 == self.po.history.0.len() {
            let latest = self.order.iter().rev().find_map(|&(tid, write_phase)| {
                (write_phase && self.po.history.0[&tid].writes.contains(&variable)).then_some(tid)
            });
            if latest.unwrap_or_default() == writer.transaction_id() {
                let mut order = self.order.clone();
                if phases == 2 {
                    order.push((id, false));
                }
                order.push((id, true));
                return Ok(order);
            }
            let candidate = Candidate {
                id,
                writes: HashMap::default(),
                versions: Vec::new(),
                reads: Vec::from([Read { variable, writer }]),
            };
            if let Some(order) = self.reuse_snapshot(&candidate) {
                return Ok(order);
            }
        }
        self.search()
    }

    const fn is_npc(&self) -> bool {
        matches!(
            self.level,
            Consistency::Prefix | Consistency::SnapshotIsolation | Consistency::Serializable
        )
    }

    const fn closed(&self) -> bool {
        matches!(
            self.level,
            Consistency::Causal
                | Consistency::Prefix
                | Consistency::SnapshotIsolation
                | Consistency::Serializable
        )
    }

    fn transaction_id(&self, session_id: u64) -> CheckResult<TransactionId> {
        let height = self.heights.get(&session_id).copied().unwrap_or(0);
        if session_id == 0 || height == u64::MAX {
            return Err(Error::Invalid(self.level));
        }
        Ok(TransactionId {
            session_id,
            session_height: height,
        })
    }

    fn validate(&self, session_id: u64, events: &[Event<u64, u64>]) -> CheckResult<Candidate> {
        let id = self.transaction_id(session_id)?;
        let height = id.session_height;
        let mut candidate = Candidate {
            id,
            writes: HashMap::default(),
            versions: Vec::new(),
            reads: Vec::new(),
        };
        let mut local_versions: HashMap<(u64, u64), EventId> = HashMap::default();
        for (position, event) in events.iter().enumerate() {
            if let Event::Write { variable, version } = *event {
                if version == 0 {
                    return Err(Error::Invalid(self.level));
                }
                let event_id = EventId {
                    session_id,
                    session_height: height,
                    transaction_height: position as u64,
                };
                if let Some(previous) = self
                    .versions
                    .get(&(variable, version))
                    .map(|w| w.id)
                    .or_else(|| local_versions.insert((variable, version), event_id))
                {
                    return Err(RawError::SameVersionWrite {
                        event: event.clone(),
                        ids: [event_id, previous],
                    }
                    .into());
                }
                candidate.versions.push((variable, version, event_id));
                candidate.writes.insert(variable, (version, event_id));
            }
        }
        let mut local: HashMap<u64, (u64, EventId)> = HashMap::default();
        let mut first_reads: HashMap<u64, EventId> = HashMap::default();
        for (position, event) in events.iter().enumerate() {
            let event_id = EventId {
                session_id,
                session_height: height,
                transaction_height: position as u64,
            };
            match *event {
                Event::Write { variable, version } => {
                    local.insert(variable, (version, event_id));
                }
                Event::Read { variable, version } => {
                    if let Some(&(local_version, write_event_id)) = local.get(&variable) {
                        if version != Some(local_version) {
                            return Err(RawError::InconsistentLocalRead {
                                read_event_id: event_id,
                                write_event_id,
                                read_event: event.clone(),
                            }
                            .into());
                        }
                        continue;
                    }
                    let writer =
                        self.read_source(variable, version, event_id, event, &local_versions)?;
                    if !matches!(self.level, Consistency::CommittedRead) {
                        if let Some(&previous) = first_reads.get(&variable) {
                            if previous != writer {
                                return Err(RawError::NonRepeatableRead {
                                    read_event: event.clone(),
                                    read_event_id: event_id,
                                    write_event_ids: [previous, writer],
                                }
                                .into());
                            }
                        } else {
                            first_reads.insert(variable, writer);
                        }
                    }
                    candidate.reads.push(Read { variable, writer });
                }
            }
        }
        Ok(candidate)
    }

    fn read_source(
        &self,
        variable: u64,
        version: Option<u64>,
        event_id: EventId,
        event: &Event<u64, u64>,
        local_versions: &HashMap<(u64, u64), EventId>,
    ) -> CheckResult<EventId> {
        #[cfg(test)]
        {
            self.validated_reads.set(self.validated_reads.get() + 1);
        }
        let Some(version) = version.filter(|&version| version != 0) else {
            return Ok(EventId {
                session_id: 0,
                session_height: 0,
                transaction_height: 0,
            });
        };
        if let Some(&write_event_id) = local_versions.get(&(variable, version)) {
            return Err(RawError::InconsistentLocalRead {
                read_event_id: event_id,
                write_event_id,
                read_event: event.clone(),
            }
            .into());
        }
        let write =
            self.versions
                .get(&(variable, version))
                .ok_or_else(|| RawError::IncompleteHistory {
                    event: event.clone(),
                    id: event_id,
                })?;
        if write.id != write.final_id {
            return Err(RawError::OverwrittenRead {
                read_event: event.clone(),
                read_event_id: event_id,
                overwritten_write_event_id: write.id,
                committed_write_event: Event::Write {
                    variable,
                    version: write.final_version,
                },
                committed_write_event_id: write.final_id,
            }
            .into());
        }
        Ok(write.id)
    }

    fn graph(&mut self, relation: Relation) -> &mut DiGraph<TransactionId> {
        match relation {
            Relation::Session => &mut self.po.session_order,
            Relation::Base => &mut self.base,
            Relation::Visibility => &mut self.po.visibility_relation,
            Relation::Union => &mut self.po.wr_union,
            Relation::WriteRead(variable) => {
                self.po.write_read_relation.entry(variable).or_default()
            }
        }
    }

    const fn reverse_graph(
        &mut self,
        relation: Relation,
    ) -> Option<&mut HashMap<TransactionId, HashSet<TransactionId>>> {
        match relation {
            Relation::Base if self.closed() => Some(&mut self.base_reverse),
            Relation::Visibility => Some(&mut self.reverse),
            _ => None,
        }
    }

    fn vertex(&mut self, relation: Relation, tid: TransactionId, journal: &mut Vec<Undo>) {
        if !self.graph(relation).adj_map.contains_key(&tid) {
            self.graph(relation).add_vertex(tid);
            journal.push(Undo::Vertex(relation, tid));
        }
    }

    fn edge(
        &mut self,
        relation: Relation,
        a: TransactionId,
        b: TransactionId,
        journal: &mut Vec<Undo>,
    ) -> bool {
        self.vertex(relation, a, journal);
        self.vertex(relation, b, journal);
        if self.graph(relation).adj_map.get_mut(&a).unwrap().insert(b) {
            if let Some(reverse) = self.reverse_graph(relation) {
                reverse.entry(b).or_default().insert(a);
            }
            journal.push(Undo::Edge(relation, a, b));
            true
        } else {
            false
        }
    }

    fn reachable(&self, source: TransactionId, target: TransactionId) -> bool {
        let mut seen: HashSet<TransactionId> = HashSet::default();
        let mut stack = Vec::from([source]);
        while let Some(tid) = stack.pop() {
            if tid == target {
                return true;
            }
            if seen.insert(tid) {
                if let Some(next) = self.po.visibility_relation.adj_map.get(&tid) {
                    stack.extend(next.iter().copied());
                }
            }
        }
        false
    }

    fn close_edge(
        &mut self,
        relation: Relation,
        a: TransactionId,
        b: TransactionId,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) {
        let mut ancestors: Vec<_> = self
            .reverse_graph(relation)
            .unwrap()
            .get(&a)
            .into_iter()
            .flat_map(|v| v.iter().copied())
            .collect();
        ancestors.push(a);
        let mut descendants: Vec<_> = self
            .graph(relation)
            .adj_map
            .get(&b)
            .into_iter()
            .flat_map(|v| v.iter().copied())
            .collect();
        descendants.push(b);
        for source in ancestors {
            for &target in &descendants {
                if self.edge(relation, source, target, journal)
                    && matches!(relation, Relation::Base)
                {
                    if let Some(info) = self.po.history.0.get(&source) {
                        dirty.extend(info.writes.iter().copied());
                    }
                }
            }
        }
    }

    fn visible(
        &mut self,
        a: TransactionId,
        b: TransactionId,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) -> CheckResult<()> {
        if self.po.visibility_relation.has_edge(&a, &b) {
            return Ok(());
        }
        if a == b
            || if self.closed() {
                self.po.visibility_relation.has_edge(&b, &a)
            } else {
                self.reachable(b, a)
            }
        {
            return Err(Error::Cycle {
                level: self.level,
                a,
                b,
            });
        }
        if self.closed() {
            self.close_edge(Relation::Visibility, a, b, journal, dirty);
        } else {
            self.edge(Relation::Visibility, a, b, journal);
        }
        Ok(())
    }

    fn base_edge(
        &mut self,
        a: TransactionId,
        b: TransactionId,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) -> CheckResult<()> {
        if self.base.has_edge(&a, &b) {
            return Ok(());
        }
        self.visible(a, b, journal, dirty)?;
        if self.closed() {
            self.close_edge(Relation::Base, a, b, journal, dirty);
        } else {
            self.edge(Relation::Base, a, b, journal);
            if let Some(info) = self.po.history.0.get(&a) {
                dirty.extend(info.writes.iter().copied());
            }
        }
        Ok(())
    }

    fn transaction(
        &mut self,
        tid: TransactionId,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) -> CheckResult<bool> {
        if self.po.history.0.contains_key(&tid) {
            return Ok(false);
        }
        self.po.history.0.insert(
            tid,
            AtomicTransactionInfo {
                reads: HashMap::default(),
                writes: HashSet::default(),
            },
        );
        journal.push(Undo::Transaction(tid));
        let others: Vec<_> = self
            .sessions
            .entry(tid.session_id)
            .or_default()
            .iter()
            .copied()
            .collect();
        self.sessions
            .get_mut(&tid.session_id)
            .unwrap()
            .insert(tid.session_height);
        let root = TransactionId::root();
        self.edge(Relation::Session, root, tid, journal);
        self.base_edge(root, tid, journal, dirty)?;
        for height in others {
            let other = TransactionId {
                session_id: tid.session_id,
                session_height: height,
            };
            let (a, b) = if height < tid.session_height {
                (other, tid)
            } else {
                (tid, other)
            };
            self.edge(Relation::Session, a, b, journal);
            self.base_edge(a, b, journal, dirty)?;
        }
        Ok(true)
    }

    fn write(
        &mut self,
        tid: TransactionId,
        variable: u64,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) {
        if self
            .po
            .history
            .0
            .get_mut(&tid)
            .unwrap()
            .writes
            .insert(variable)
        {
            journal.push(Undo::Write(tid, variable));
            dirty.insert(variable);
        }
        if !self.po.write_read_relation.contains_key(&variable) {
            let readers = self
                .initial_readers
                .get(&variable)
                .map_or(&[][..], Writers::as_slice)
                .to_vec();
            for reader in readers {
                self.edge(
                    Relation::WriteRead(variable),
                    TransactionId::root(),
                    reader,
                    journal,
                );
            }
        }
        self.vertex(Relation::WriteRead(variable), tid, journal);
        self.vertex(Relation::Union, tid, journal);
    }

    fn observe(
        &mut self,
        variable: u64,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) -> CheckResult<bool> {
        if !self.observed.insert(variable) {
            return Ok(false);
        }
        journal.push(Undo::Observed(variable));
        let writers = self
            .writers
            .get(&variable)
            .map_or(&[][..], Writers::as_slice)
            .to_vec();
        let invalidated = !writers.is_empty();
        for tid in writers {
            self.transaction(tid, journal, dirty)?;
            self.write(tid, variable, journal, dirty);
        }
        Ok(invalidated)
    }

    fn extend(
        &mut self,
        candidate: &Candidate,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) -> CheckResult<bool> {
        let mut invalidated = false;
        for read in &candidate.reads {
            invalidated |= self.observe(read.variable, journal, dirty)?;
        }
        if matches!(self.level, Consistency::SnapshotIsolation) && !candidate.reads.is_empty() {
            for &variable in candidate.writes.keys() {
                invalidated |= self.observe(variable, journal, dirty)?;
            }
        }
        if candidate.reads.is_empty() && !candidate.writes.keys().any(|v| self.observed.contains(v))
        {
            return Ok(false);
        }
        self.transaction(candidate.id, journal, dirty)?;
        for &variable in candidate.writes.keys() {
            if self.observed.contains(&variable) {
                self.write(candidate.id, variable, journal, dirty);
            }
        }
        for (position, read) in candidate.reads.iter().enumerate() {
            self.add_read(candidate.id, read, journal, dirty)?;
            self.order_prior_reads(
                read.variable,
                read.writer.transaction_id(),
                candidate.reads[..position]
                    .iter()
                    .map(|prior| prior.writer.transaction_id()),
                journal,
                dirty,
            )?;
        }
        Ok(invalidated)
    }

    fn order_prior_reads(
        &mut self,
        variable: u64,
        source: TransactionId,
        previous: impl Iterator<Item = TransactionId>,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) -> CheckResult<()> {
        if matches!(
            self.level,
            Consistency::CommittedRead | Consistency::RepeatableRead
        ) {
            for writer in previous {
                if writer != source
                    && writer != TransactionId::root()
                    && self
                        .writers
                        .get(&variable)
                        .is_some_and(|writers| writers.as_slice().contains(&writer))
                {
                    self.base_edge(writer, source, journal, dirty)?;
                }
            }
        }
        Ok(())
    }

    fn add_read(
        &mut self,
        id: TransactionId,
        read: &Read,
        journal: &mut Vec<Undo>,
        dirty: &mut HashSet<u64>,
    ) -> CheckResult<()> {
        let writer = read.writer.transaction_id();
        if writer == TransactionId::root()
            && !self.po.write_read_relation.contains_key(&read.variable)
        {
            if let Some(readers) = self.initial_readers.get_mut(&read.variable) {
                if !readers.as_slice().contains(&id) {
                    readers.push(id);
                    journal.push(Undo::InitialReader(read.variable));
                }
            } else {
                // The first reader is undone with its new observation.
                self.initial_readers.insert(read.variable, Writers::One(id));
            }
        } else {
            self.edge(Relation::WriteRead(read.variable), writer, id, journal);
        }
        self.edge(Relation::Union, writer, id, journal);
        self.base_edge(writer, id, journal, dirty)?;
        let previous = self
            .po
            .history
            .0
            .get_mut(&id)
            .unwrap()
            .reads
            .insert(read.variable, writer);
        if previous != Some(writer) {
            journal.push(Undo::Read(id, read.variable, previous));
        }
        dirty.insert(read.variable);
        Ok(())
    }

    fn saturate(&mut self, journal: &mut Vec<Undo>, dirty: &mut HashSet<u64>) -> CheckResult<()> {
        if matches!(
            self.level,
            Consistency::CommittedRead | Consistency::RepeatableRead
        ) {
            return Ok(());
        }
        let variables = core::mem::take(dirty);
        for variable in variables {
            let ww = self.po.causal_ww_for(&variable, &self.base);
            for (a, targets) in ww.adj_map {
                for b in targets {
                    self.visible(a, b, journal, dirty)?;
                }
            }
        }
        Ok(())
    }

    fn reuse_snapshot(&self, candidate: &Candidate) -> Option<Vec<(TransactionId, bool)>> {
        if matches!(self.level, Consistency::Serializable) {
            return None;
        }
        // Keep every old phase in its old order. A new write phase at the end
        // cannot change old observations. Its read phase needs a matching
        // snapshot after its session predecessor; SI additionally needs every
        // earlier conflicting writer to have committed before that read phase.
        let mut earliest = 0;
        for (index, &(tid, write_phase)) in self.order.iter().enumerate() {
            if write_phase
                && (tid.session_id == candidate.id.session_id
                    || (matches!(self.level, Consistency::SnapshotIsolation)
                        && self.po.history.0[&tid]
                            .writes
                            .iter()
                            .any(|v| candidate.writes.contains_key(v))))
            {
                earliest = index + 1;
            }
        }
        let mut values: HashMap<u64, TransactionId> = HashMap::default();
        for boundary in 0..=self.order.len() {
            if boundary >= earliest
                && candidate.reads.iter().all(|read| {
                    values.get(&read.variable).copied().unwrap_or_default()
                        == read.writer.transaction_id()
                })
            {
                let mut order = self.order.clone();
                order.insert(boundary, (candidate.id, false));
                order.push((candidate.id, true));
                return Some(order);
            }
            if let Some(&(tid, true)) = self.order.get(boundary) {
                for &variable in &self.po.history.0[&tid].writes {
                    values.insert(variable, tid);
                }
            }
        }
        None
    }

    fn search(&self) -> CheckResult<Vec<(TransactionId, bool)>> {
        #[cfg(test)]
        self.searched_reads.set(
            self.searched_reads.get()
                + self
                    .po
                    .history
                    .0
                    .values()
                    .map(|info| info.reads.len())
                    .sum::<usize>(),
        );
        // Only an invalidated NPC witness pays for a search-state copy. The
        // retained relation fixed point is the input, not raw-history replay.
        // Writerless variables cannot constrain a search order; a first writer
        // materializes their retained root readers before this projection.
        let po = AtomicTransactionPO {
            root: self.po.root,
            history: AtomicTransactionHistory(
                self.po
                    .history
                    .0
                    .iter()
                    .map(|(&tid, info)| {
                        (
                            tid,
                            AtomicTransactionInfo {
                                reads: info
                                    .reads
                                    .iter()
                                    .filter(|(variable, _)| {
                                        self.po.write_read_relation.contains_key(*variable)
                                    })
                                    .map(|(&variable, &writer)| (variable, writer))
                                    .collect(),
                                writes: info.writes.clone(),
                            },
                        )
                    })
                    .collect(),
            ),
            session_order: self.po.session_order.clone(),
            write_read_relation: self.po.write_read_relation.clone(),
            wr_union: self.po.wr_union.clone(),
            visibility_relation: self.po.visibility_relation.clone(),
        };
        match self.level {
            Consistency::Prefix => PrefixConsistencySolver::from(po).get_linearization(),
            Consistency::SnapshotIsolation => SnapshotIsolationSolver::from(po).get_linearization(),
            Consistency::Serializable => SerializabilitySolver::from(po)
                .get_linearization()
                .map(|order| order.into_iter().map(|tid| (tid, true)).collect()),
            _ => unreachable!("search is only used for NPC levels"),
        }
        .ok_or(Error::Invalid(self.level))
    }

    fn rollback(&mut self, journal: Vec<Undo>) {
        for undo in journal.into_iter().rev() {
            match undo {
                Undo::Observed(variable) => {
                    self.observed.remove(&variable);
                    self.po.write_read_relation.remove(&variable);
                    self.initial_readers.remove(&variable);
                }
                Undo::InitialReader(variable) => {
                    let readers = self.initial_readers.get_mut(&variable).unwrap();
                    let Writers::Many(tids) = readers else {
                        unreachable!("the first reader is undone with its observation");
                    };
                    tids.pop();
                    if tids.len() == 1 {
                        *readers = Writers::One(tids[0]);
                    }
                }
                Undo::Transaction(tid) => {
                    self.po.history.0.remove(&tid);
                    let heights = self.sessions.get_mut(&tid.session_id).unwrap();
                    heights.remove(&tid.session_height);
                    if heights.is_empty() {
                        self.sessions.remove(&tid.session_id);
                    }
                }
                Undo::Write(tid, variable) => {
                    self.po
                        .history
                        .0
                        .get_mut(&tid)
                        .unwrap()
                        .writes
                        .remove(&variable);
                }
                Undo::Read(tid, variable, previous) => {
                    let reads = &mut self.po.history.0.get_mut(&tid).unwrap().reads;
                    if let Some(previous) = previous {
                        reads.insert(variable, previous);
                    } else {
                        reads.remove(&variable);
                    }
                }
                Undo::Vertex(relation, tid) => {
                    self.graph(relation).adj_map.remove(&tid);
                    if let Relation::WriteRead(variable) = relation {
                        if self.po.write_read_relation[&variable].adj_map.is_empty() {
                            self.po.write_read_relation.remove(&variable);
                        }
                    }
                    if let Some(reverse) = self.reverse_graph(relation) {
                        reverse.remove(&tid);
                    }
                }
                Undo::Edge(relation, a, b) => {
                    self.graph(relation).adj_map.get_mut(&a).unwrap().remove(&b);
                    if let Some(reverse) = self.reverse_graph(relation) {
                        let parents = reverse.get_mut(&b).unwrap();
                        parents.remove(&a);
                        if parents.is_empty() {
                            reverse.remove(&b);
                        }
                    }
                }
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use alloc::vec;

    use super::*;
    use crate::consistency::check;
    use crate::history::raw::types::{Session, Transaction};

    const LEVELS: [Consistency; 7] = [
        Consistency::CommittedRead,
        Consistency::RepeatableRead,
        Consistency::AtomicRead,
        Consistency::Causal,
        Consistency::Prefix,
        Consistency::SnapshotIsolation,
        Consistency::Serializable,
    ];

    fn initial(variable: u64) -> Event<u64, u64> {
        Event::Read {
            variable,
            version: None,
        }
    }

    fn read(variable: u64, version: u64) -> Event<u64, u64> {
        Event::Read {
            variable,
            version: Some(version),
        }
    }

    fn write(variable: u64, version: u64) -> Event<u64, u64> {
        Event::Write { variable, version }
    }

    fn compare(
        checker: &mut IncrementalChecker,
        history: &mut Vec<Session<u64, u64>>,
        level: Consistency,
        session: usize,
        events: Vec<Event<u64, u64>>,
        commit: bool,
    ) -> bool {
        history[session].push(Transaction::committed(events));
        let expected = check(history, level).is_ok();
        let actual = checker
            .check_transaction(
                session as u64 + 1,
                &history[session].last().unwrap().events,
                commit,
            )
            .is_ok();
        assert_eq!(
            actual, expected,
            "level={level:?}, commit={commit}, history={history:?}"
        );
        if !commit || !expected {
            history[session].pop();
        }
        actual
    }

    fn append_read(
        checker: &mut IncrementalChecker,
        history: &mut [Session<u64, u64>],
        session: usize,
        prefix: &mut Vec<Event<u64, u64>>,
        event: Event<u64, u64>,
        accept: bool,
    ) -> bool {
        let Event::Read { variable, version } = event else {
            unreachable!()
        };
        let mut events = prefix.clone();
        events.push(event.clone());
        history[session].push(Transaction::committed(events));
        let expected = check(history, checker.level).is_ok();
        history[session].pop();
        let actual = checker
            .check_read(session as u64 + 1, variable, version, accept)
            .is_ok();
        assert_eq!(
            actual, expected,
            "level={:?}, accept={accept}, prefix={prefix:?}, read={event:?}",
            checker.level
        );
        if accept && actual {
            prefix.push(event);
        }
        actual
    }

    #[test]
    fn live_reads_match_batch_through_rejections_reset_and_rmw_commit() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![vec![], vec![], vec![]];
            assert!(compare(
                &mut checker,
                &mut history,
                level,
                0,
                vec![write(1, 1), write(2, 1)],
                true,
            ));
            assert!(compare(
                &mut checker,
                &mut history,
                level,
                1,
                vec![read(1, 1), write(1, 2), write(3, 1)],
                true,
            ));
            let mut prefix = Vec::new();
            for event in [
                read(1, 1),
                read(1, 2),
                initial(1),
                read(3, 1),
                read(2, 1),
                initial(2),
                read(2, 99_999),
                read(1, 1),
            ] {
                append_read(
                    &mut checker,
                    &mut history,
                    2,
                    &mut prefix,
                    event.clone(),
                    false,
                );
                append_read(&mut checker, &mut history, 2, &mut prefix, event, true);
            }
            assert_eq!(checker.heights.get(&3), None);
            checker.reset_transaction();
            prefix.clear();
            assert!(append_read(
                &mut checker,
                &mut history,
                2,
                &mut prefix,
                initial(3),
                false,
            ));
            for event in [read(1, 2), read(3, 1), read(1, 2)] {
                assert!(append_read(
                    &mut checker,
                    &mut history,
                    2,
                    &mut prefix,
                    event,
                    true,
                ));
            }
            prefix.push(write(1, 3));
            assert!(compare(&mut checker, &mut history, level, 2, prefix, true,));
            assert_eq!(checker.heights.get(&3), Some(&1));
            assert!(checker.check_read(1, 1, Some(3), true).is_ok());
            assert!(checker
                .check_transaction(1, &[read(2, 99_999)], false)
                .is_err());
            assert!(checker.check_read(2, 1, Some(3), true).is_ok());
            checker.reset_transaction();
        }
    }

    #[test]
    fn live_writer_promotion_and_old_snapshots_match_batch() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![vec![], vec![], vec![], vec![]];
            for (session, events) in [
                (0, vec![write(1, 1), write(2, 1)]),
                (1, vec![write(1, 2), write(3, 1)]),
                (2, vec![read(1, 1)]),
            ] {
                assert!(compare(
                    &mut checker,
                    &mut history,
                    level,
                    session,
                    events,
                    true,
                ));
            }
            let mut prefix = Vec::new();
            for event in [read(1, 1), initial(3), read(2, 1), read(3, 1), read(1, 1)] {
                append_read(
                    &mut checker,
                    &mut history,
                    3,
                    &mut prefix,
                    event.clone(),
                    false,
                );
                append_read(&mut checker, &mut history, 3, &mut prefix, event, true);
            }
            checker.reset_transaction();
            prefix.clear();
            for event in [read(3, 1), read(1, 2), read(2, 1)] {
                assert!(append_read(
                    &mut checker,
                    &mut history,
                    3,
                    &mut prefix,
                    event,
                    true,
                ));
            }
            assert!(compare(&mut checker, &mut history, level, 3, prefix, false,));
            assert!(checker.check_read(1, 1, Some(1), true).is_ok());
            checker.reset_transaction();
        }
    }

    #[test]
    fn live_stale_initial_read_rejects_a_later_dependency_without_leaking_it() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![vec![], vec![]];
            assert!(compare(
                &mut checker,
                &mut history,
                level,
                0,
                vec![write(1, 1), write(2, 1)],
                true,
            ));
            let mut prefix = Vec::new();
            assert!(append_read(
                &mut checker,
                &mut history,
                1,
                &mut prefix,
                initial(1),
                true,
            ));
            for accept in [false, true] {
                let accepted = append_read(
                    &mut checker,
                    &mut history,
                    1,
                    &mut prefix,
                    read(2, 1),
                    accept,
                );
                assert_eq!(
                    accepted,
                    matches!(
                        level,
                        Consistency::CommittedRead | Consistency::RepeatableRead
                    ),
                );
            }
            checker.reset_transaction();
            prefix.clear();
            for event in [read(1, 1), read(2, 1)] {
                assert!(append_read(
                    &mut checker,
                    &mut history,
                    1,
                    &mut prefix,
                    event,
                    true,
                ));
            }
        }
    }

    #[test]
    fn live_bounded_read_prefixes_match_batch() {
        for level in LEVELS {
            for seed in 1..=8_u64 {
                let mut checker = IncrementalChecker::new(level);
                let mut history = vec![vec![], vec![], vec![]];
                for version in 1..=3 {
                    assert!(compare(
                        &mut checker,
                        &mut history,
                        level,
                        (usize::try_from(version).expect("bounded version") - 1) % 2,
                        vec![write(1, version), write(2, version), write(3, version)],
                        true,
                    ));
                }
                let mut state = seed;
                let mut prefix = Vec::new();
                for attempt in 0..24 {
                    state = state
                        .wrapping_mul(6_364_136_223_846_793_005)
                        .wrapping_add(1);
                    let variable = (state >> 32) % 3 + 1;
                    let source = (state >> 40) % 5;
                    let event = if source == 0 {
                        initial(variable)
                    } else {
                        read(variable, source)
                    };
                    append_read(
                        &mut checker,
                        &mut history,
                        2,
                        &mut prefix,
                        event.clone(),
                        false,
                    );
                    append_read(&mut checker, &mut history, 2, &mut prefix, event, true);
                    if attempt % 6 == 5 {
                        checker.reset_transaction();
                        prefix.clear();
                    }
                }
            }
        }
    }

    #[test]
    fn live_large_prefix_validates_only_the_appended_read() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let writes: Vec<_> = (1..=512).map(|variable| write(variable, 1)).collect();
            assert!(checker.check_transaction(1, &writes, true).is_ok());
            for variable in 1..=512 {
                assert!(checker.check_read(2, variable, Some(1), true).is_ok());
            }
            assert_eq!(checker.validated_reads.get(), 512);
            assert_eq!(checker.searched_reads.get(), usize::from(checker.is_npc()));
            assert!(checker.check_read(2, 513, None, false).is_ok());
            assert!(checker.check_read(2, 512, Some(1), true).is_ok());
            assert_eq!(checker.validated_reads.get(), 514);
            checker.reset_transaction();
            assert!(checker.check_read(3, 512, None, true).is_ok());
        }
    }

    #[test]
    fn live_initial_scan_retains_linear_compact_constraints() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let variables = 2048;
            let count = usize::try_from(variables).expect("bounded field count");
            for variable in 1..=variables {
                assert!(checker.check_read(1, variable, None, false).is_ok());
                assert!(checker.check_read(1, variable, None, true).is_ok());
            }
            assert_eq!(checker.live.as_ref().unwrap().reads.len(), count);
            assert_eq!(checker.observed.len(), count);
            assert!(
                checker.po.write_read_relation.is_empty(),
                "writerless reads must not allocate per-variable graphs: {level:?}"
            );
            let retained = checker.live.as_ref().unwrap().journal.len();
            assert!(retained <= count + 32, "{level:?}: {retained}");
            for variable in 1..=variables {
                assert!(checker.check_read(1, variable, None, true).is_ok());
            }
            assert_eq!(checker.live.as_ref().unwrap().journal.len(), retained);
            checker.reset_transaction();
            assert!(checker.observed.is_empty());
            assert!(checker.po.history.0.is_empty());
            assert!(checker.po.write_read_relation.is_empty());
        }
    }

    #[test]
    fn compact_initial_readers_survive_trials_and_promote_for_future_writers() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![vec![], vec![], vec![]];
            assert!(compare(
                &mut checker,
                &mut history,
                level,
                0,
                vec![initial(1), write(2, 1)],
                true,
            ));
            assert!(checker.check_read(3, 1, None, false).is_ok());
            assert_eq!(checker.initial_readers[&1].as_slice().len(), 1);
            assert!(checker.check_read(3, 1, None, true).is_ok());
            checker.reset_transaction();
            assert_eq!(checker.initial_readers[&1].as_slice().len(), 1);
            assert!(compare(
                &mut checker,
                &mut history,
                level,
                1,
                vec![read(2, 1), initial(1), write(2, 2)],
                true,
            ));
            assert_eq!(checker.initial_readers[&1].as_slice().len(), 2);
            assert!(!checker.po.write_read_relation.contains_key(&1));
            let events = vec![read(2, 1), write(1, 1)];
            for commit in [false, true] {
                let accepted =
                    compare(&mut checker, &mut history, level, 2, events.clone(), commit);
                assert_eq!(accepted, !matches!(level, Consistency::Serializable));
                if !commit || !accepted {
                    assert!(!checker.po.write_read_relation.contains_key(&1));
                } else {
                    let readers =
                        &checker.po.write_read_relation[&1].adj_map[&TransactionId::root()];
                    for reader in checker.initial_readers[&1].as_slice() {
                        assert!(readers.contains(reader));
                    }
                }
            }
        }
    }

    #[test]
    fn bounded_histories_match_batch_after_trials_and_rejections() {
        for level in LEVELS {
            for seed in 1..=12_u64 {
                let mut checker = IncrementalChecker::new(level);
                let mut history = vec![vec![], vec![], vec![]];
                let mut state = seed;
                let mut next = || {
                    state = state
                        .wrapping_mul(6_364_136_223_846_793_005)
                        .wrapping_add(1);
                    state >> 32
                };
                for attempt in 1..=18_u64 {
                    let session = (next() % 3) as usize;
                    let variable = next() % 3 + 1;
                    let other = next() % 3 + 1;
                    let version = attempt * 3 + seed * 100;
                    let available: Vec<_> = history
                        .iter()
                        .flatten()
                        .flat_map(|txn: &Transaction<u64, u64>| txn.events.iter())
                        .filter_map(|event| {
                            if let Event::Write {
                                variable: v,
                                version,
                            } = *event
                            {
                                (v == variable).then_some(version)
                            } else {
                                None
                            }
                        })
                        .collect();
                    let source = match next() % (available.len() as u64 + 2) {
                        0 => initial(variable),
                        1 => read(variable, 99_999),
                        index => read(
                            variable,
                            available[usize::try_from(index).expect("bounded candidate index") - 2],
                        ),
                    };
                    let events = match next() % 5 {
                        0 => vec![write(variable, version)],
                        1 => vec![source, write(other, version)],
                        2 => vec![source, initial(other)],
                        3 => vec![
                            write(variable, version),
                            read(variable, version),
                            write(variable, version + 1),
                        ],
                        _ => vec![source],
                    };
                    compare(
                        &mut checker,
                        &mut history,
                        level,
                        session,
                        events.clone(),
                        false,
                    );
                    compare(
                        &mut checker,
                        &mut history,
                        level,
                        session,
                        events,
                        attempt % 3 != 0,
                    );
                    // A known invalid candidate must neither consume its write
                    // version nor leave promoted vertices/closure edges behind.
                    let failed = vec![read(variable, 88_888), write(other, version + 2)];
                    assert!(!compare(
                        &mut checker,
                        &mut history,
                        level,
                        session,
                        failed,
                        true
                    ));
                    compare(
                        &mut checker,
                        &mut history,
                        level,
                        session,
                        vec![write(other, version + 2)],
                        true,
                    );
                    if history.iter().map(Vec::len).sum::<usize>() >= 8 {
                        break;
                    }
                }
            }
        }
    }

    #[test]
    fn rejected_relation_extension_rolls_back_before_replacement() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![vec![], vec![], vec![]];
            compare(
                &mut checker,
                &mut history,
                level,
                0,
                vec![write(1, 1), write(2, 1)],
                true,
            );
            compare(
                &mut checker,
                &mut history,
                level,
                1,
                vec![read(1, 1), write(1, 2), write(3, 1)],
                true,
            );
            let rejected = vec![read(1, 1), read(3, 1), write(2, 2)];
            compare(
                &mut checker,
                &mut history,
                level,
                2,
                rejected.clone(),
                false,
            );
            compare(&mut checker, &mut history, level, 2, rejected, true);
            compare(
                &mut checker,
                &mut history,
                level,
                2,
                vec![read(1, 2), read(3, 1), write(2, 3)],
                true,
            );
            compare(
                &mut checker,
                &mut history,
                level,
                0,
                vec![initial(2)],
                false,
            );
            compare(&mut checker, &mut history, level, 0, vec![read(2, 3)], true);
        }
    }

    #[test]
    fn blind_initialization_is_promoted_when_later_observed() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![vec![], vec![], vec![]];
            for variable in 1..=128 {
                // Different sessions ensure promotion must recover the right
                // session predecessor, not merely the most recent commit.
                let session = usize::try_from(variable).expect("bounded test variable") % 2;
                let events = vec![write(variable, variable)];
                assert!(checker
                    .check_transaction(session as u64 + 1, &events, true)
                    .is_ok());
                history[session].push(Transaction::committed(events));
            }
            compare(
                &mut checker,
                &mut history,
                level,
                2,
                vec![read(1, 1), read(128, 128), write(129, 129)],
                false,
            );
            compare(
                &mut checker,
                &mut history,
                level,
                2,
                vec![read(1, 1), read(128, 128), write(129, 129)],
                true,
            );
            compare(
                &mut checker,
                &mut history,
                level,
                1,
                vec![initial(128)],
                true,
            );
            compare(
                &mut checker,
                &mut history,
                level,
                0,
                vec![read(129, 129)],
                true,
            );
        }
    }

    #[test]
    fn local_reads_versions_and_sparse_session_identity() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            assert!(checker.check_transaction(0, &[], true).is_err());
            assert!(checker.check_transaction(91, &[write(1, 0)], true).is_err());
            assert!(checker
                .check_transaction(91, &[write(1, 1), read(1, 1), write(1, 2)], true)
                .is_ok());
            assert!(checker.check_transaction(7, &[read(1, 1)], true).is_err());
            assert!(checker.check_transaction(7, &[read(1, 2)], false).is_ok());
            assert!(checker
                .check_transaction(7, &[read(1, 2), write(1, 3), initial(1)], true)
                .is_err());
            assert!(checker
                .check_transaction(7, &[read(1, 2), write(1, 3), read(1, 3)], true)
                .is_ok());
            assert!(checker.check_transaction(91, &[write(1, 3)], true).is_err());
            assert!(checker.check_transaction(91, &[read(1, 3)], true).is_ok());
            assert!(checker.check_transaction(7, &[read(2, 0)], true).is_ok());
        }
    }

    #[test]
    fn old_snapshot_order_survives_new_tail_writes_and_trials() {
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![vec![], vec![], vec![], vec![]];
            compare(
                &mut checker,
                &mut history,
                level,
                0,
                vec![write(1, 1)],
                true,
            );
            compare(&mut checker, &mut history, level, 2, vec![read(1, 1)], true);
            compare(
                &mut checker,
                &mut history,
                level,
                1,
                vec![write(1, 2)],
                true,
            );
            compare(
                &mut checker,
                &mut history,
                level,
                3,
                vec![read(1, 1)],
                false,
            );
            compare(&mut checker, &mut history, level, 3, vec![read(1, 1)], true);
            compare(
                &mut checker,
                &mut history,
                level,
                1,
                vec![read(1, 1)],
                false,
            );
            compare(&mut checker, &mut history, level, 1, vec![read(1, 2)], true);
        }
    }

    #[test]
    fn si_retains_blind_write_conflicts_between_reading_transactions() {
        // Neither transaction reads z, but their snapshots must overlap:
        // each reads the initial value overwritten by the other. Blind writes
        // to z therefore distinguish SI from Prefix.
        for level in LEVELS {
            let mut checker = IncrementalChecker::new(level);
            let mut history = vec![vec![], vec![], vec![]];
            compare(
                &mut checker,
                &mut history,
                level,
                0,
                vec![initial(1), write(2, 1), write(3, 1)],
                true,
            );
            compare(
                &mut checker,
                &mut history,
                level,
                1,
                vec![initial(2), write(1, 1), write(3, 2)],
                false,
            );
            compare(
                &mut checker,
                &mut history,
                level,
                1,
                vec![initial(2), write(1, 1), write(3, 2)],
                true,
            );
        }
    }
}
