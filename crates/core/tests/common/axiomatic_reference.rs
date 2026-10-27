#![allow(dead_code)]
#![allow(clippy::too_many_lines)]

//! Independent finite reference for completed, committed transactions.
//!
//! Source: Biswas and Enea, Figure 2, Table 1, and Section 2.1:
//! <https://arxiv.org/html/1908.04509>
//! This file uses raw events and public witness data, never production history
//! conversion, validation, graph operations, saturation, DFS, or SAT.
//! `committed=false` is outside this domain, not an instruction to discard events.
//! In particular this is not a specification of aborted-transaction observations.

use std::collections::{BTreeMap, BTreeSet};

use dbcop_core::consistency::Witness;
use dbcop_core::history::atomic::types::TransactionId;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::Consistency;

pub const LEVELS: [Consistency; 7] = [
    Consistency::CommittedRead,
    Consistency::RepeatableRead,
    Consistency::AtomicRead,
    Consistency::Causal,
    Consistency::Prefix,
    Consistency::SnapshotIsolation,
    Consistency::Serializable,
];

pub type History = Vec<Session<u64, u64>>;

#[derive(Debug)]
pub enum ReferenceResult {
    Invalid(String),
    Consistent(Vec<TransactionId>),
    Inconsistent,
}

#[derive(Debug)]
struct Read {
    reader: usize,
    variable: u64,
    source: usize,
    event: usize,
}

struct Model {
    ids: Vec<TransactionId>,
    nonempty: Vec<bool>,
    writes: Vec<BTreeSet<u64>>,
    reads: Vec<Read>,
    // Only raw SO union WR, never constraints inferred from a commit order.
    edges: Vec<Vec<bool>>,
    reachable: Vec<Vec<bool>>,
}

impl Model {
    fn scan(sessions: &[Session<u64, u64>]) -> Result<Self, String> {
        let mut ids = vec![TransactionId::root()];
        let mut transactions = Vec::new();
        for (session, txs) in sessions.iter().enumerate() {
            for (height, tx) in txs.iter().enumerate() {
                ids.push(TransactionId {
                    session_id: u64::try_from(session).unwrap() + 1,
                    session_height: u64::try_from(height).unwrap(),
                });
                transactions.push(tx);
            }
        }
        let n = ids.len();
        let mut writes = vec![BTreeSet::new(); n];
        let mut last = vec![BTreeMap::new(); n];
        let mut provenance = BTreeMap::new();
        for (offset, tx) in transactions.iter().enumerate() {
            let index = offset + 1;
            for (event, operation) in tx.events.iter().enumerate() {
                let variable = match operation {
                    Event::Read { variable, .. } | Event::Write { variable, .. } => *variable,
                };
                // Initialization writes every mentioned variable, even if no read
                // explicitly selects it and even if an explicit version zero exists.
                writes[0].insert(variable);
                if let Event::Write { version, .. } = operation {
                    if let Some(previous) = provenance.insert((variable, *version), (index, event))
                    {
                        return Err(format!(
                            "duplicate write ({variable},{version}) at {:?}:{event}; previous {previous:?}",
                            ids[index]
                        ));
                    }
                    writes[index].insert(variable);
                    last[index].insert(variable, event);
                }
            }
        }
        let mut edges = vec![vec![false; n]; n];
        for (index, id) in ids.iter().enumerate().skip(1) {
            edges[0][index] = true;
            for (earlier, row) in ids.iter().zip(&mut edges).take(index).skip(1) {
                if earlier.session_id == id.session_id {
                    row[index] = true;
                }
            }
        }
        let mut reads = Vec::new();
        for (offset, tx) in transactions.iter().enumerate() {
            let index = offset + 1;
            let mut local = BTreeMap::new();
            for (event, operation) in tx.events.iter().enumerate() {
                match operation {
                    Event::Write { variable, version } => {
                        local.insert(*variable, *version);
                    }
                    Event::Read { variable, version } => {
                        if let Some(latest) = local.get(variable) {
                            // None always denotes initialization, including after w(x,0).
                            if *version != Some(*latest) {
                                return Err(format!(
                                    "read at {:?}:{event} does not see latest preceding local write {latest}",
                                    ids[index]
                                ));
                            }
                            continue;
                        }
                        let (source, source_event) = match version {
                            None => (0, 0),
                            Some(v) => match provenance.get(&(*variable, *v)) {
                                Some(&origin) => origin,
                                None if *v == 0 => (0, 0),
                                None => {
                                    return Err(format!(
                                        "missing writer ({variable},{v}) at {:?}:{event}",
                                        ids[index]
                                    ));
                                }
                            },
                        };
                        if source == index {
                            return Err(format!(
                                "read at {:?}:{event} selects a non-preceding local write",
                                ids[index]
                            ));
                        }
                        if source != 0 {
                            if !transactions[source - 1].committed {
                                return Err(format!(
                                    "read at {:?}:{event} selects uncommitted {:?}",
                                    ids[index], ids[source]
                                ));
                            }
                            if last[source].get(variable) != Some(&source_event) {
                                return Err(format!(
                                    "read at {:?}:{event} selects overwritten write in {:?}",
                                    ids[index], ids[source]
                                ));
                            }
                        }
                        edges[source][index] = true;
                        reads.push(Read {
                            reader: index,
                            variable: *variable,
                            source,
                            event,
                        });
                    }
                }
            }
        }
        if let Some(index) = transactions.iter().position(|tx| !tx.committed) {
            return Err(format!(
                "{:?} is outside the completed-committed reference domain",
                ids[index + 1]
            ));
        }
        let mut reachable = edges.clone();
        for middle in 0..n {
            let (before, rest) = reachable.split_at_mut(middle);
            let (pivot, after) = rest.split_first_mut().unwrap();
            for row in before.iter_mut().chain(after) {
                if row[middle] {
                    for (entry, via_middle) in row.iter_mut().zip(pivot.iter()) {
                        *entry |= via_middle;
                    }
                }
            }
        }
        let nonempty = std::iter::once(false)
            .chain(transactions.iter().map(|tx| !tx.events.is_empty()))
            .collect();
        Ok(Self {
            ids,
            nonempty,
            writes,
            reads,
            edges,
            reachable,
        })
    }

    fn index(&self, id: TransactionId) -> Result<usize, String> {
        self.ids
            .iter()
            .position(|candidate| *candidate == id)
            .ok_or_else(|| format!("unknown witness transaction {id:?}"))
    }

    fn repeatable(&self) -> bool {
        let mut seen = BTreeMap::new();
        for read in &self.reads {
            if seen
                .insert((read.reader, read.variable), read.source)
                .is_some_and(|previous| previous != read.source)
            {
                return false;
            }
        }
        true
    }

    fn satisfies(&self, order: &[usize], level: Consistency) -> bool {
        if !matches!(level, Consistency::CommittedRead) && !self.repeatable() {
            return false;
        }
        let n = self.ids.len();
        let mut position = vec![0; n];
        for (at, &index) in order.iter().enumerate() {
            position[index] = at;
        }
        // For an external read of x by c from a, every other writer b of x
        // satisfying the chosen premise must have b <co a (Figure 2):
        // RC/RR: b supplies an earlier read in c, of ANY variable (wr ; po).
        // RA:    (b,c) in WR union SO.
        // CC:    (b,c) in (WR union SO)+, computed independently above.
        // PC:    exists q: b <=co q and (q,c) in WR union SO.
        // SI:    PC, plus exists d sharing a write with c: b <=co d <co c.
        // SER:   b <co c.
        // RR is the project's additional repeated-external-source requirement;
        // validated local reads have already been removed per Section 2.1.
        for read in &self.reads {
            let a = read.source;
            let c = read.reader;
            for (b, writes) in self.writes.iter().enumerate() {
                if b == a || !writes.contains(&read.variable) {
                    continue;
                }
                let prefix_premise =
                    || (0..n).any(|q| position[b] <= position[q] && self.edges[q][c]);
                let premise = match level {
                    Consistency::CommittedRead | Consistency::RepeatableRead => {
                        self.reads.iter().any(|earlier| {
                            earlier.reader == c && earlier.event < read.event && earlier.source == b
                        })
                    }
                    Consistency::AtomicRead => self.edges[b][c],
                    Consistency::Causal => self.reachable[b][c],
                    Consistency::Prefix => prefix_premise(),
                    Consistency::SnapshotIsolation => {
                        prefix_premise()
                            || (0..n).any(|d| {
                                position[b] <= position[d]
                                    && position[d] < position[c]
                                    && !self.writes[c].is_disjoint(&self.writes[d])
                            })
                    }
                    Consistency::Serializable => position[b] < position[c],
                };
                if premise && position[b] >= position[a] {
                    return false;
                }
            }
        }
        true
    }

    fn find_order(&self, level: Consistency, extra: &[(usize, usize)]) -> Option<Vec<usize>> {
        let mut edges = self.edges.clone();
        for &(from, to) in extra {
            edges[from][to] = true;
        }
        // Root is fixed first, so no edge may enter it, including a self edge.
        if edges.iter().any(|row| row[0]) {
            return None;
        }
        let mut placed = vec![false; self.ids.len()];
        placed[0] = true;
        self.extend_order(level, &edges, &mut vec![0], &mut placed)
    }

    fn extend_order(
        &self,
        level: Consistency,
        edges: &[Vec<bool>],
        order: &mut Vec<usize>,
        placed: &mut [bool],
    ) -> Option<Vec<usize>> {
        if order.len() == self.ids.len() {
            return self.satisfies(order, level).then(|| order.clone());
        }
        for next in 1..self.ids.len() {
            if placed[next]
                || edges
                    .iter()
                    .enumerate()
                    .any(|(from, row)| row[next] && !placed[from])
            {
                continue;
            }
            placed[next] = true;
            order.push(next);
            if let Some(found) = self.extend_order(level, edges, order, placed) {
                return Some(found);
            }
            order.pop();
            placed[next] = false;
        }
        None
    }

    fn require_nonempty(&self, present: &[bool]) -> Result<(), String> {
        for (index, &required) in self.nonempty.iter().enumerate() {
            if required && !present[index] {
                return Err(format!(
                    "missing nonempty witness transaction {:?}",
                    self.ids[index]
                ));
            }
        }
        Ok(())
    }
}

/// Decide the raw history by enumerating commit orders, including the root.
pub fn reference_check(sessions: &[Session<u64, u64>], level: Consistency) -> ReferenceResult {
    let model = match Model::scan(sessions) {
        Ok(model) => model,
        Err(error) => return ReferenceResult::Invalid(error),
    };
    model
        .find_order(level, &[])
        .map_or(ReferenceResult::Inconsistent, |order| {
            ReferenceResult::Consistent(order.into_iter().map(|i| model.ids[i]).collect())
        })
}

/// Validate this witness's actual vertices, edges, and order against raw events.
///
/// # Errors
///
/// Returns the structural, representation, or direct-axiom violation found.
///
/// # Panics
///
/// Panics only if the scanner's internal source/phase invariants are broken.
pub fn validate_witness(
    sessions: &[Session<u64, u64>],
    level: Consistency,
    witness: &Witness,
) -> Result<(), String> {
    let model = Model::scan(sessions)?;
    let n = model.ids.len();
    let extra: Vec<(usize, usize)> = match witness {
        Witness::CommitOrder(order) => {
            let mut present = vec![false; n];
            let mut indices = Vec::new();
            for (at, &id) in order.iter().enumerate() {
                let index = model.index(id)?;
                if present[index] || (index == 0 && at != 0) {
                    return Err(format!("duplicate or misplaced commit-order vertex {id:?}"));
                }
                present[index] = true;
                indices.push(index);
            }
            model.require_nonempty(&present)?;
            // Omitted root is an implicit first initialization transaction.
            // Omitted empty transactions must admit insertion into this EXACT
            // order, not an unrelated reference solution. This also justifies
            // CommitOrder([]) for all-empty histories without phantom vertices.
            indices.windows(2).map(|pair| (pair[0], pair[1])).collect()
        }
        Witness::SaturationOrder(graph) => {
            let mut edges = Vec::new();
            for (&from, targets) in &graph.adj_map {
                let from = model.index(from)?;
                for &to in targets {
                    edges.push((from, model.index(to)?));
                }
            }
            // Total-order extension detects cycles and also checks every actual
            // SO/WR edge, even if the supplied graph omits its endpoints.
            edges
        }
        Witness::SplitCommitOrder(order) => {
            let mut read_at = vec![None; n];
            let mut write_at = vec![None; n];
            for (at, &(id, write)) in order.iter().enumerate() {
                let index = model.index(id)?;
                let slot = if write {
                    &mut write_at[index]
                } else {
                    &mut read_at[index]
                };
                if slot.replace(at + 2).is_some() {
                    return Err(format!("duplicate split phase ({id:?},{write})"));
                }
            }
            if read_at[0].is_some() || write_at[0].is_some() {
                // Production split witnesses represent both root phases or
                // omit both. A half-root is not an implicit-phase convention.
                if read_at[0] != Some(2) || write_at[0] != Some(3) {
                    return Err("explicit root phases must be the first read/write pair".into());
                }
            } else {
                read_at[0] = Some(0);
                write_at[0] = Some(1);
            }
            for index in 0..n {
                match (read_at[index], write_at[index]) {
                    (Some(read), Some(write)) if read < write => {}
                    (None, None) if !model.nonempty[index] => {}
                    _ => {
                        return Err(format!(
                            "missing or reversed phases for {:?}",
                            model.ids[index]
                        ))
                    }
                }
            }
            for (from, &source_phase) in write_at.iter().enumerate() {
                for (to, &target_phase) in read_at.iter().enumerate() {
                    if model.edges[from][to] {
                        if let (Some(write), Some(read)) = (source_phase, target_phase) {
                            if write >= read {
                                return Err(format!(
                                    "SO/WR must target read phase: {:?} -> {:?}",
                                    model.ids[from], model.ids[to]
                                ));
                            }
                        }
                    }
                }
            }
            for read in &model.reads {
                let snapshot = read_at[read.reader].unwrap();
                let source = write_at[read.source].unwrap();
                if source >= snapshot {
                    return Err(format!("source is after snapshot: {read:?}"));
                }
                for (writer, position) in write_at.iter().enumerate() {
                    if model.writes[writer].contains(&read.variable)
                        && position.is_some_and(|at| source < at && at < snapshot)
                    {
                        return Err(format!(
                            "snapshot skips preceding writer {:?}: {read:?}",
                            model.ids[writer]
                        ));
                    }
                }
            }
            for left in 1..n {
                for right in left + 1..n {
                    if !(model.writes[left].is_disjoint(&model.writes[right])
                        || write_at[left].unwrap() < read_at[right].unwrap()
                        || write_at[right].unwrap() < read_at[left].unwrap())
                    {
                        return Err(format!(
                            "conflicting writers overlap: {:?}, {:?}",
                            model.ids[left], model.ids[right]
                        ));
                    }
                }
            }
            // Empty omitted phases can be inserted between their session
            // predecessors/successors: strict SO above contains every such pair.
            // Fix the supplied write order as well, and check the direct axiom.
            let mut writes: Vec<_> = write_at
                .iter()
                .enumerate()
                .filter_map(|(index, at)| at.map(|at| (at, index)))
                .collect();
            writes.sort_unstable();
            writes
                .windows(2)
                .map(|pair| (pair[0].1, pair[1].1))
                .collect()
        }
    };
    model.find_order(level, &extra).map_or_else(
        || {
            Err(format!(
                "supplied witness has no {level:?} extension satisfying the direct axioms"
            ))
        },
        |_| Ok(()),
    )
}

/// Visit every labeled history within the published finite bounds.
// No quotienting is used: both session and variable labelings are included.
// Only write values are canonical names (positive flattened event positions).
// Enumerating sources does NOT filter cycles, invalid local reads, or external
// reads of overwritten writes; consumers count those structural assignments.
// None and Some(0) are separate initial encodings; explicit-zero writes are
// covered by directed cases because generated write positions are positive.
pub fn for_each_history(mut visit: impl FnMut(&[Session<u64, u64>])) {
    for sessions in 0..=2 {
        for total in 0..=3 {
            compositions(total, sessions, &mut Vec::new(), &mut |partition| {
                for events in 0..=4 {
                    compositions(events, total, &mut Vec::new(), &mut |lengths| {
                        event_shapes(events, &mut Vec::new(), &mut |shape| {
                            assign_sources(shape, 0, &mut Vec::new(), &mut |operations| {
                                let mut history = Vec::new();
                                let mut tx = 0;
                                let mut event = 0;
                                for &count in partition {
                                    let mut session = Vec::new();
                                    for _ in 0..count {
                                        let end = event + lengths[tx];
                                        session.push(Transaction::committed(
                                            operations[event..end].to_vec(),
                                        ));
                                        tx += 1;
                                        event = end;
                                    }
                                    history.push(session);
                                }
                                visit(&history);
                            });
                        });
                    });
                }
            });
        }
    }
}

fn compositions(
    total: usize,
    parts: usize,
    prefix: &mut Vec<usize>,
    visit: &mut impl FnMut(&[usize]),
) {
    if parts == 0 {
        if total == 0 {
            visit(prefix);
        }
        return;
    }
    for next in 0..=total {
        prefix.push(next);
        compositions(total - next, parts - 1, prefix, visit);
        prefix.pop();
    }
}

fn event_shapes(
    total: usize,
    prefix: &mut Vec<(bool, u64)>,
    visit: &mut impl FnMut(&[(bool, u64)]),
) {
    if prefix.len() == total {
        visit(prefix);
        return;
    }
    for write in [false, true] {
        for variable in 0..=1 {
            prefix.push((write, variable));
            event_shapes(total, prefix, visit);
            prefix.pop();
        }
    }
}

fn assign_sources(
    shape: &[(bool, u64)],
    at: usize,
    prefix: &mut Vec<Event<u64, u64>>,
    visit: &mut impl FnMut(&[Event<u64, u64>]),
) {
    let Some(&(write, variable)) = shape.get(at) else {
        visit(prefix);
        return;
    };
    if write {
        prefix.push(Event::write(variable, u64::try_from(at).unwrap() + 1));
        assign_sources(shape, at + 1, prefix, visit);
        prefix.pop();
    } else {
        let sources = [None, Some(0)].into_iter().chain(
            shape
                .iter()
                .enumerate()
                .filter(|(_, (is_write, var))| *is_write && *var == variable)
                .map(|(position, _)| Some(u64::try_from(position).unwrap() + 1)),
        );
        for source in sources {
            prefix.push(Event::Read {
                variable,
                version: source,
            });
            assign_sources(shape, at + 1, prefix, visit);
            prefix.pop();
        }
    }
}

pub struct DirectedCase {
    pub name: &'static str,
    pub history: History,
    pub admitted: [bool; 7],
}

pub fn directed_histories() -> Vec<DirectedCase> {
    let tx = Transaction::committed;
    let w = Event::write;
    let r = Event::read;
    let initial = Event::read_empty;
    let mut cases = vec![
        DirectedCase {
            name: "rc-not-rr-progressing-read",
            history: vec![
                vec![tx(vec![w(0, 1)])],
                vec![tx(vec![w(0, 2)])],
                vec![tx(vec![r(0, 1), r(0, 2)])],
            ],
            admitted: [true, false, false, false, false, false, false],
        },
        DirectedCase {
            name: "rr-not-ra-stale-session",
            history: vec![vec![tx(vec![w(0, 1)]), tx(vec![initial(0)])]],
            admitted: [true, true, false, false, false, false, false],
        },
        DirectedCase {
            name: "ra-not-cc-transitive-raw-dependency",
            history: vec![
                vec![tx(vec![w(0, 1)])],
                vec![tx(vec![r(0, 1), w(1, 1)])],
                vec![tx(vec![r(1, 1), initial(0)])],
            ],
            admitted: [true, true, true, false, false, false, false],
        },
        DirectedCase {
            name: "cc-not-pc-independent-observers",
            history: vec![
                vec![tx(vec![w(0, 1)])],
                vec![tx(vec![w(1, 1)])],
                vec![tx(vec![r(0, 1), initial(1)])],
                vec![tx(vec![r(1, 1), initial(0)])],
            ],
            admitted: [true, true, true, true, false, false, false],
        },
        DirectedCase {
            name: "pc-not-si-conflicting-old-snapshots",
            history: vec![
                vec![tx(vec![initial(0), w(0, 1)])],
                vec![tx(vec![initial(0), w(0, 2)])],
            ],
            admitted: [true, true, true, true, true, false, false],
        },
        DirectedCase {
            name: "si-not-ser-write-skew",
            history: vec![
                vec![tx(vec![initial(1), w(0, 1)])],
                vec![tx(vec![initial(0), w(1, 1)])],
            ],
            admitted: [true, true, true, true, true, true, false],
        },
        DirectedCase {
            name: "cross-variable-rc-earlier-read",
            history: vec![
                vec![tx(vec![w(0, 1), w(1, 1)])],
                vec![tx(vec![r(1, 1), initial(0)])],
            ],
            admitted: [false; 7],
        },
        DirectedCase {
            name: "cross-variable-rc-reversed-events",
            history: vec![
                vec![tx(vec![w(0, 1), w(1, 1)])],
                vec![tx(vec![initial(0), r(1, 1)])],
            ],
            admitted: [true, true, false, false, false, false, false],
        },
        DirectedCase {
            name: "root-after-noninitial-read",
            history: vec![vec![tx(vec![w(0, 1)])], vec![tx(vec![r(0, 1), initial(0)])]],
            admitted: [false; 7],
        },
        DirectedCase {
            name: "root-before-noninitial-read",
            history: vec![vec![tx(vec![w(0, 1)])], vec![tx(vec![initial(0), r(0, 1)])]],
            admitted: [true, false, false, false, false, false, false],
        },
        DirectedCase {
            name: "latest-preceding-local-write-not-final-write",
            history: vec![
                vec![tx(vec![
                    initial(0),
                    w(0, 1),
                    r(0, 1),
                    w(0, 2),
                    r(0, 2),
                    w(0, 3),
                ])],
                vec![tx(vec![r(0, 3)])],
            ],
            admitted: [true; 7],
        },
        DirectedCase {
            name: "writer-only-saturation",
            history: vec![
                vec![tx(vec![w(0, 1)])],
                vec![tx(vec![w(0, 2)])],
                vec![tx(vec![w(1, 1)])],
                vec![],
            ],
            admitted: [true; 7],
        },
        DirectedCase {
            // Same raw fixture as decomposition_check::articulation_chain_history.
            name: "articulation-session-overlap",
            history: vec![
                vec![tx(vec![w(0, 1)])],
                vec![tx(vec![r(0, 1)]), tx(vec![w(1, 1)])],
                vec![tx(vec![r(1, 1)])],
            ],
            admitted: [true; 7],
        },
        DirectedCase {
            // Same raw fixture as sat/tests/si_frontier_regression.rs. A serial
            // order is (2,0),(1,0),(1,1),(2,1),(3,0),(3,1), independently
            // establishing consistency rather than taking a checker result.
            name: "known-si-smaller-failed-frontier",
            history: vec![
                vec![tx(vec![w(2, 1)]), tx(vec![r(2, 1), w(0, 1)])],
                vec![tx(vec![]), tx(vec![w(0, 2)])],
                vec![tx(vec![r(2, 1), w(2, 2)]), tx(vec![r(0, 2), w(0, 3)])],
            ],
            admitted: [true; 7],
        },
        DirectedCase {
            name: "explicit-zero-is-not-initial",
            history: vec![vec![tx(vec![w(0, 0)]), tx(vec![r(0, 0)])]],
            admitted: [true; 7],
        },
        DirectedCase {
            name: "none-remains-root-despite-explicit-zero",
            history: vec![vec![tx(vec![w(0, 0)]), tx(vec![initial(0)])]],
            admitted: [true, true, false, false, false, false, false],
        },
    ];
    cases.push(DirectedCase {
        name: "cross-variable-rc-nonroot-stale-source",
        history: vec![
            vec![tx(vec![w(0, 1)]), tx(vec![w(0, 2), w(1, 3)])],
            vec![tx(vec![r(1, 3), r(0, 1)])],
        ],
        admitted: [false; 7],
    });
    cases.push(DirectedCase {
        name: "cross-variable-rc-nonroot-reversed-events",
        history: vec![
            vec![tx(vec![w(0, 1)]), tx(vec![w(0, 2), w(1, 3)])],
            vec![tx(vec![r(0, 1), r(1, 3)])],
        ],
        admitted: [true, true, false, false, false, false, false],
    });
    // The serial CLI example uses this exact raw history.
    cases.push(DirectedCase {
        name: "serial-cli-smoke",
        history: vec![vec![tx(vec![w(0, 1)])], vec![tx(vec![r(0, 1), w(1, 2)])]],
        admitted: [true; 7],
    });
    cases
}

pub fn malformed_histories() -> Vec<(&'static str, History)> {
    let tx = Transaction::committed;
    let w = Event::write;
    let r = Event::read;
    vec![
        (
            "duplicate-versions",
            vec![vec![tx(vec![w(0, 1)])], vec![tx(vec![w(0, 1)])]],
        ),
        ("missing-writer", vec![vec![tx(vec![r(0, 9)])]]),
        (
            "uncommitted-external-source",
            vec![
                vec![Transaction::uncommitted(vec![w(0, 1)])],
                vec![tx(vec![r(0, 1)])],
            ],
        ),
        (
            "overwritten-external-source",
            vec![vec![tx(vec![w(0, 1), w(0, 2)])], vec![tx(vec![r(0, 1)])]],
        ),
        (
            "old-local-source",
            vec![vec![tx(vec![w(0, 1), w(0, 2), r(0, 1)])]],
        ),
        (
            "future-local-source",
            vec![vec![tx(vec![r(0, 1), w(0, 1)])]],
        ),
        (
            "external-after-local-write",
            vec![vec![tx(vec![w(0, 1)])], vec![tx(vec![w(0, 2), r(0, 1)])]],
        ),
        (
            "none-after-zero-local-write",
            vec![vec![tx(vec![w(0, 0), Event::read_empty(0)])]],
        ),
    ]
}
