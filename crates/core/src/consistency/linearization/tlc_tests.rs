//! Test-only observations of the real constrained-linearization engine.

use std::cell::RefCell;
use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::fmt::Debug;

use hashbrown::{HashMap, HashSet};

use super::constrained_linearization::{
    BranchOrdering, ConstrainedLinearizationSolver, DfsSearchOptions, DominancePruning,
    HeuristicPortfolio, NogoodLearning, PrincipalVariationOrdering, TieBreaking,
};
use super::prefix::PrefixConsistencySolver;
use super::serializable::SerializabilitySolver;
use super::snapshot_isolation::SnapshotIsolationSolver;
use crate::consistency::saturation::causal::check_causal_read;
use crate::consistency::{check, Witness};
use crate::history::atomic::types::TransactionId;
use crate::history::raw::types::{Event, Transaction};

#[path = "../../../tests/common/axiomatic_reference.rs"]
mod axiomatic_reference;
#[path = "../../../tests/common/tlc.rs"]
mod tlc;
#[path = "tlc_trace_validation.rs"]
mod validation;

use axiomatic_reference::{History, ReferenceResult, LEVELS};

#[derive(Clone, Debug)]
pub(super) struct Capture {
    pub(super) level: usize,
    pub(super) history: History,
    pub(super) vertices: Vec<VertexInfo>,
    pub(super) edges: Vec<(usize, usize)>,
    pub(super) observations: Vec<Observation>,
}

#[derive(Clone, Debug)]
pub(super) struct VertexInfo {
    pub(super) txn: usize,
    pub(super) phase: usize,
}

#[derive(Clone, Debug)]
pub(super) struct Observation {
    pub(super) tag: usize,
    pub(super) path: Vec<usize>,
    pub(super) frontier: Vec<usize>,
    pub(super) parents: Vec<usize>,
    pub(super) active: Vec<(u64, usize)>,
    pub(super) active_keys: Vec<u64>,
    pub(super) open: Vec<u64>,
    pub(super) candidates: Vec<(usize, bool)>,
    pub(super) selected: i64,
    pub(super) reason: usize,
    pub(super) attempt: usize,
    pub(super) final_attempt: bool,
    pub(super) budget_hit: bool,
    pub(super) found: bool,
    pub(super) jump_depth: usize,
}

impl Default for Observation {
    fn default() -> Self {
        Self {
            tag: 0,
            path: Vec::new(),
            frontier: Vec::new(),
            parents: Vec::new(),
            active: Vec::new(),
            active_keys: Vec::new(),
            open: Vec::new(),
            candidates: Vec::new(),
            selected: -1,
            reason: 0,
            attempt: 0,
            final_attempt: false,
            budget_hit: false,
            found: false,
            jump_depth: 0,
        }
    }
}

struct CaptureState {
    capture: Capture,
    registry: BTreeMap<String, usize>,
    transactions: BTreeMap<TransactionId, usize>,
    active: Vec<(u64, usize)>,
    active_keys: Vec<u64>,
    open: Vec<u64>,
    attempt: usize,
    final_attempt: bool,
    budget_hit: bool,
}

thread_local! {
    static CAPTURE: RefCell<Option<CaptureState>> = const { RefCell::new(None) };
}

struct CaptureGuard;

impl CaptureGuard {
    fn start<S: ConcreteSolver>(history: &History, level: usize, solver: &S) -> Self {
        CAPTURE.with(|slot| {
            let mut slot = slot.borrow_mut();
            assert!(slot.is_none(), "nested TLC trace capture");
            let transactions = transaction_ids(history);
            let mut vertices = solver.vertices();
            vertices.sort();
            vertices.dedup();
            let mut registry = BTreeMap::new();
            let mut infos = Vec::new();
            for (index, vertex) in vertices.iter().enumerate() {
                assert!(
                    registry.insert(format!("{vertex:?}"), index).is_none(),
                    "Debug identity is not unique in this registered vertex domain"
                );
                let (txn, phase) = S::identity(vertex);
                infos.push(VertexInfo {
                    txn: transactions[&txn],
                    phase,
                });
            }
            let mut edges = Vec::new();
            for (from, vertex) in vertices.iter().enumerate() {
                for child in solver.children_of(vertex).unwrap_or_default() {
                    let to = *registry
                        .get(&format!("{child:?}"))
                        .expect("unregistered graph child");
                    edges.push((from, to));
                }
            }
            edges.sort_unstable();
            edges.dedup();
            *slot = Some(CaptureState {
                capture: Capture {
                    level,
                    history: history.clone(),
                    vertices: infos,
                    edges,
                    observations: Vec::new(),
                },
                registry,
                transactions,
                active: Vec::new(),
                active_keys: Vec::new(),
                open: Vec::new(),
                attempt: 0,
                final_attempt: false,
                budget_hit: false,
            });
        });
        let guard = Self;
        refresh(solver);
        guard
    }

    fn finish(self) -> Capture {
        let capture = CAPTURE.with(|slot| {
            slot.borrow_mut()
                .take()
                .expect("capture scope missing")
                .capture
        });
        drop(self);
        capture
    }
}

impl Drop for CaptureGuard {
    fn drop(&mut self) {
        CAPTURE.with(|slot| {
            slot.borrow_mut().take();
        });
    }
}

fn transaction_ids(history: &History) -> BTreeMap<TransactionId, usize> {
    let mut ids = BTreeMap::from([(TransactionId::root(), 0)]);
    let mut next = 1;
    for (session, txs) in history.iter().enumerate() {
        for height in 0..txs.len() {
            ids.insert(
                TransactionId {
                    session_id: u64::try_from(session).unwrap() + 1,
                    session_height: u64::try_from(height).unwrap(),
                },
                next,
            );
            next += 1;
        }
    }
    ids
}

impl CaptureState {
    fn id<V: Debug>(&self, vertex: &V) -> usize {
        // Debug is only an exact lookup into registered actual values, never parsed.
        *self
            .registry
            .get(&format!("{vertex:?}"))
            .expect("unregistered observed vertex")
    }

    fn observation<V: Debug>(&self, tag: usize, path: &[V]) -> Observation {
        Observation {
            tag,
            path: path.iter().map(|v| self.id(v)).collect(),
            active: self.active.clone(),
            active_keys: self.active_keys.clone(),
            open: self.open.clone(),
            attempt: self.attempt,
            final_attempt: self.final_attempt,
            budget_hit: self.budget_hit,
            ..Observation::default()
        }
    }

    fn graph<V: Debug>(
        &self,
        observation: &mut Observation,
        frontier: &VecDeque<V>,
        parents: &HashMap<V, usize>,
    ) {
        observation.frontier = frontier.iter().map(|v| self.id(v)).collect();
        observation.frontier.sort_unstable();
        observation.parents = vec![0; self.capture.vertices.len()];
        assert_eq!(parents.len(), observation.parents.len());
        for (vertex, count) in parents {
            observation.parents[self.id(vertex)] = *count;
        }
    }
}

fn recording(f: impl FnOnce(&mut CaptureState)) {
    CAPTURE.with(|slot| {
        if let Some(state) = slot.borrow_mut().as_mut() {
            f(state);
        }
    });
}

pub(super) fn attempt_start(attempt: usize, final_attempt: bool) {
    recording(|state| {
        state.attempt = attempt;
        state.final_attempt = final_attempt;
        state.budget_hit = false;
        let event = state.observation::<usize>(0, &[]);
        state.capture.observations.push(event);
    });
}

pub(super) fn node<V: Debug>(path: &[V], frontier: &VecDeque<V>, parents: &HashMap<V, usize>) {
    recording(|state| {
        let mut event = state.observation(1, path);
        state.graph(&mut event, frontier, parents);
        state.capture.observations.push(event);
    });
}

pub(super) fn candidates<V: Debug>(
    path: &[V],
    frontier: &VecDeque<V>,
    parents: &HashMap<V, usize>,
    candidates: &[(V, bool)],
) {
    recording(|state| {
        let mut event = state.observation(2, path);
        state.graph(&mut event, frontier, parents);
        event.candidates = candidates
            .iter()
            .map(|(v, allowed)| (state.id(v), *allowed))
            .collect();
        state.capture.observations.push(event);
    });
}

pub(super) fn trial<V: Debug>(
    path: &[V],
    frontier: &VecDeque<V>,
    parents: &HashMap<V, usize>,
    candidate: &V,
    allowed: bool,
) {
    recording(|state| {
        let mut event = state.observation(3, path);
        state.graph(&mut event, frontier, parents);
        event.selected = i64::try_from(state.id(candidate)).unwrap();
        event.candidates = vec![(state.id(candidate), allowed)];
        state.capture.observations.push(event);
    });
}

pub(super) fn phase<V: Debug>(tag: usize, path: &[V]) {
    recording(|state| {
        let mut event = state.observation(tag, path);
        event.selected = i64::try_from(state.id(path.last().unwrap())).unwrap();
        state.capture.observations.push(event);
    });
}

pub(super) fn prune<V: Debug>(path: &[V], reason: usize, budget_hit: bool, jump_depth: usize) {
    recording(|state| {
        state.budget_hit = budget_hit;
        let mut event = state.observation(8, path);
        event.reason = reason;
        event.jump_depth = jump_depth;
        state.capture.observations.push(event);
    });
}

pub(super) fn learn<V: Debug>(path: &[V], budget_hit: bool, jump_depth: usize) {
    recording(|state| {
        state.budget_hit = budget_hit;
        let mut event = state.observation(9, path);
        event.reason = if budget_hit { 1 } else { 6 };
        event.jump_depth = jump_depth;
        state.capture.observations.push(event);
    });
}

pub(super) fn backjump<V: Debug>(path: &[V], budget_hit: bool, jump_depth: usize) {
    recording(|state| {
        state.budget_hit = budget_hit;
        let mut event = state.observation(10, path);
        event.jump_depth = jump_depth;
        state.capture.observations.push(event);
    });
}

pub(super) fn attempt_end<V: Debug>(path: &[V], budget_hit: bool, found: bool) {
    recording(|state| {
        state.budget_hit = budget_hit;
        let mut event = state.observation(11, path);
        event.found = found;
        state.capture.observations.push(event);
    });
}

pub(super) fn terminal(found: bool) {
    recording(|state| {
        let mut event = state.observation::<usize>(12, &[]);
        event.path = state
            .capture
            .observations
            .last()
            .expect("terminal without attempt")
            .path
            .clone();
        event.found = found;
        state.capture.observations.push(event);
    });
}

trait ConcreteSolver: ConstrainedLinearizationSolver {
    fn identity(vertex: &Self::Vertex) -> (TransactionId, usize);
    fn active(&self) -> &HashMap<u64, HashSet<TransactionId>>;
    fn open(&self) -> Vec<u64> {
        Vec::new()
    }
    fn witness(path: Vec<Self::Vertex>) -> Witness;
}

impl ConcreteSolver for PrefixConsistencySolver<u64> {
    fn identity(vertex: &Self::Vertex) -> (TransactionId, usize) {
        (vertex.0, usize::from(vertex.1))
    }
    fn active(&self) -> &HashMap<u64, HashSet<TransactionId>> {
        &self.active_write
    }
    fn witness(path: Vec<Self::Vertex>) -> Witness {
        Witness::CommitOrder(
            path.into_iter()
                .filter_map(|(txn, write)| write.then_some(txn))
                .collect(),
        )
    }
}

impl ConcreteSolver for SnapshotIsolationSolver<u64> {
    fn identity(vertex: &Self::Vertex) -> (TransactionId, usize) {
        (vertex.0, usize::from(vertex.1))
    }
    fn active(&self) -> &HashMap<u64, HashSet<TransactionId>> {
        &self.active_write
    }
    fn open(&self) -> Vec<u64> {
        self.active_variable.iter().copied().collect()
    }
    fn witness(path: Vec<Self::Vertex>) -> Witness {
        Witness::SplitCommitOrder(path)
    }
}

impl ConcreteSolver for SerializabilitySolver<u64> {
    fn identity(vertex: &Self::Vertex) -> (TransactionId, usize) {
        (*vertex, 2)
    }
    fn active(&self) -> &HashMap<u64, HashSet<TransactionId>> {
        &self.active_write
    }
    fn witness(path: Vec<Self::Vertex>) -> Witness {
        Witness::CommitOrder(path)
    }
}

fn refresh<S: ConcreteSolver>(solver: &S) {
    recording(|state| {
        state.active_keys = solver.active().keys().copied().collect();
        state.active_keys.sort_unstable();
        state.active = solver
            .active()
            .iter()
            .flat_map(|(key, readers)| {
                readers
                    .iter()
                    .map(|txn| (*key, state.transactions[txn]))
                    .collect::<Vec<_>>()
            })
            .collect();
        state.active.sort_unstable();
        state.open = solver.open();
        state.open.sort_unstable();
    });
}

#[derive(Clone, Copy, Debug)]
enum Profile {
    Default,
    Exhaustive,
    Restart,
}

struct CapturedSolver<S> {
    inner: S,
    profile: Profile,
}

impl<S: ConcreteSolver> ConstrainedLinearizationSolver for CapturedSolver<S> {
    type Vertex = S::Vertex;

    fn search_options(&self) -> DfsSearchOptions {
        let options = self.inner.search_options();
        match self.profile {
            Profile::Default => options,
            Profile::Restart => DfsSearchOptions {
                restart_node_budget: Some(2),
                restart_max_attempts: 2,
                ..options
            },
            Profile::Exhaustive => DfsSearchOptions {
                memoize_frontier: false,
                nogood_learning: NogoodLearning::Disabled,
                dominance_pruning: DominancePruning::Disabled,
                enable_killer_history: false,
                tie_breaking: TieBreaking::Deterministic,
                restart_max_attempts: 0,
                restart_node_budget: None,
                heuristic_portfolio: HeuristicPortfolio::Disabled,
                principal_variation_ordering: PrincipalVariationOrdering::Disabled,
                prefer_allowed_first: false,
                branch_ordering: BranchOrdering::AsProvided,
            },
        }
    }

    fn branch_score(&self, path: &[Self::Vertex], vertex: &Self::Vertex) -> i64 {
        self.inner.branch_score(path, vertex)
    }
    fn zobrist_value(&self, vertex: &Self::Vertex) -> u128 {
        self.inner.zobrist_value(vertex)
    }
    fn frontier_signature(&self, hash: u128, path: &[Self::Vertex]) -> u128 {
        self.inner.frontier_signature(hash, path)
    }
    fn should_prune(&self, path: &[Self::Vertex], frontier: usize) -> bool {
        self.inner.should_prune(path, frontier)
    }
    fn get_root(&self) -> Self::Vertex {
        self.inner.get_root()
    }
    fn allow_next(&self, path: &[Self::Vertex], vertex: &Self::Vertex) -> bool {
        self.inner.allow_next(path, vertex)
    }

    fn vertices(&self) -> Vec<Self::Vertex> {
        let mut vertices = self.inner.vertices();
        if matches!(self.profile, Profile::Exhaustive) {
            vertices.sort();
        }
        vertices
    }

    fn children_of(&self, vertex: &Self::Vertex) -> Option<Vec<Self::Vertex>> {
        self.inner.children_of(vertex).map(|mut children| {
            if matches!(self.profile, Profile::Exhaustive) {
                children.sort();
            }
            children
        })
    }

    fn ordered_frontier_candidates(
        &self,
        frontier: &VecDeque<Self::Vertex>,
        path: &[Self::Vertex],
        options: DfsSearchOptions,
    ) -> Vec<(Self::Vertex, bool)> {
        let mut candidates = self
            .inner
            .ordered_frontier_candidates(frontier, path, options);
        // Reverse numeric order makes the matcher's first speculative ID wrong.
        if matches!(self.profile, Profile::Exhaustive) {
            candidates.sort_by(|a, b| b.0.cmp(&a.0));
        }
        candidates
    }

    fn forward_book_keeping(&mut self, path: &[Self::Vertex]) {
        self.inner.forward_book_keeping(path);
        refresh(&self.inner);
    }

    fn backtrack_book_keeping(&mut self, path: &[Self::Vertex]) {
        self.inner.backtrack_book_keeping(path);
        refresh(&self.inner);
    }
}

fn capture_solver<S: ConcreteSolver>(
    history: &History,
    level: usize,
    inner: S,
    profile: Profile,
) -> (Capture, Option<Witness>) {
    let guard = CaptureGuard::start(history, level, &inner);
    let mut solver = CapturedSolver { inner, profile };
    // Deliberately use the trait's actual shared engine, not a wrapper DFS.
    let witness = solver.get_linearization().map(S::witness);
    (guard.finish(), witness)
}

fn capture_history(
    history: &History,
    level: usize,
    profile: Profile,
) -> (Capture, Option<Witness>) {
    let po = check_causal_read(history).unwrap();
    match level {
        4 => capture_solver(history, level, PrefixConsistencySolver::from(po), profile),
        5 => capture_solver(history, level, SnapshotIsolationSolver::from(po), profile),
        6 => capture_solver(history, level, SerializabilitySolver::from(po), profile),
        _ => unreachable!(),
    }
}

fn compatible(capture: &Capture) -> bool {
    validation::validate(capture).unwrap_or_else(|error| {
        panic!("trace validator execution failed: {error}; capture={capture:#?}")
    })
}

fn reject_mutation(capture: &Capture, name: &str, mutate: impl FnOnce(&mut Capture)) {
    let mut mutation = capture.clone();
    mutate(&mut mutation);
    assert!(
        !compatible(&mutation),
        "accepted {name}; capture={mutation:#?}"
    );
}

#[test]
fn scoped_capture_rejects_nesting_and_restores_after_panic() {
    let history = vec![vec![Transaction::committed(vec![Event::read_empty(0)])]];
    let make = || PrefixConsistencySolver::from(check_causal_read(&history).unwrap());
    let first = CaptureGuard::start(&history, 4, &make());
    assert!(std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let _nested = CaptureGuard::start(&history, 4, &make());
    }))
    .is_err());
    drop(first);
    assert!(std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let _guard = CaptureGuard::start(&history, 4, &make());
        panic!("exercise capture unwind");
    }))
    .is_err());
    let (capture, witness) = capture_history(&history, 4, Profile::Exhaustive);
    assert!(witness.is_some());
    assert_eq!(capture.observations.last().unwrap().tag, 12);
    std::thread::scope(|scope| {
        let _guard = CaptureGuard::start(&history, 4, &make());
        scope
            .spawn(|| {
                let (capture, witness) = capture_history(&history, 4, Profile::Exhaustive);
                assert!(witness.is_some());
                assert_eq!(capture.observations.last().unwrap().tag, 12);
            })
            .join()
            .unwrap();
    });
}

#[allow(clippy::too_many_lines)]
#[test]
#[ignore = "requires pinned DBCOP_TLC_JAR; executes real search trace compatibility"]
fn tlc_search_trace_compatibility() {
    let mut histories: Vec<_> = axiomatic_reference::directed_histories()
        .into_iter()
        .map(|case| (case.name, case.history))
        .collect();
    histories.extend([
        ("no-sessions", vec![]),
        ("empty-sessions", vec![vec![], vec![]]),
        (
            "empty-transactions",
            vec![
                vec![Transaction::committed(vec![])],
                vec![Transaction::committed(vec![])],
            ],
        ),
        (
            "empty-prefix-height-mapping",
            vec![
                vec![
                    Transaction::committed(vec![]),
                    Transaction::committed(vec![Event::write(0, 1)]),
                ],
                vec![
                    Transaction::committed(vec![]),
                    Transaction::committed(vec![Event::read(0, 1)]),
                ],
            ],
        ),
    ]);
    let mut generated = 0;
    let mut eligible = 0_usize;
    axiomatic_reference::for_each_history(|history| {
        if generated == 8 || history.len() != 2 || history.iter().any(Vec::is_empty) {
            return;
        }
        let mut transactions = 0;
        let mut reads = 0;
        let mut writes = 0;
        for transaction in history.iter().flatten() {
            transactions += 1;
            for event in &transaction.events {
                match event {
                    Event::Read { .. } => reads += 1,
                    Event::Write { .. } => writes += 1,
                }
            }
        }
        if transactions != 3
            || reads + writes != 4
            || reads == 0
            || writes == 0
            || check_causal_read(history).is_err()
        {
            return;
        }
        if eligible.is_multiple_of(1024) {
            histories.push(("generated-bounded", history.to_vec()));
            generated += 1;
        }
        eligible += 1;
    });
    let raw: Vec<_> = histories.iter().map(|(_, h)| h.clone()).collect();
    let oracle = tlc::check_histories(&raw);
    let mut captures = Vec::new();
    let mut preprocessing_rejections = 0;
    for ((name, history), verdicts) in histories.iter().zip(&oracle) {
        for level in 4..=6 {
            let expected = match axiomatic_reference::reference_check(history, LEVELS[level]) {
                ReferenceResult::Consistent(_) => true,
                ReferenceResult::Inconsistent => false,
                ReferenceResult::Invalid(error) => {
                    panic!("invalid trace fixture {name}: {error}; {history:#?}")
                }
            };
            assert_eq!(
                verdicts[level],
                if expected {
                    tlc::Verdict::Consistent
                } else {
                    tlc::Verdict::Inconsistent
                },
                "TLC/reference {name} {level}; {history:#?}"
            );
            // Keep decomposition and singleton fast paths covered independently.
            let public = check(history, LEVELS[level]);
            assert_eq!(
                public.is_ok(),
                expected,
                "public check {name} {level}; {history:#?}"
            );
            if let Ok(witness) = public {
                axiomatic_reference::validate_witness(history, LEVELS[level], &witness).unwrap();
            }
            if check_causal_read(history).is_err() {
                preprocessing_rejections += 1;
                continue;
            }
            for profile in [Profile::Default, Profile::Exhaustive, Profile::Restart] {
                let (capture, witness) = capture_history(history, level, profile);
                assert_eq!(
                    witness.is_some(),
                    expected,
                    "solver {name} {level} {profile:?}; {capture:#?}"
                );
                if let Some(witness) = witness {
                    axiomatic_reference::validate_witness(history, LEVELS[level], &witness)
                        .unwrap_or_else(|error| {
                            panic!("invalid actual witness: {error}; {capture:#?}")
                        });
                }
                assert!(
                    compatible(&capture),
                    "incompatible actual trace {name} {level} {profile:?}; {capture:#?}"
                );
                captures.push(capture);
            }
        }
    }

    assert!(
        captures
            .iter()
            .any(|c| c.observations.iter().any(|o| o.tag == 7)),
        "no actual rollback exercised"
    );
    assert!(
        captures
            .iter()
            .any(|c| c.observations.iter().any(|o| o.tag == 0 && o.attempt == 2)),
        "unbounded restart attempt not reached"
    );
    assert!(
        captures
            .iter()
            .any(|c| c.observations.iter().any(|o| o.tag == 8 && o.reason == 1)),
        "no actual budget abandonment"
    );
    assert!(
        captures.iter().any(|c| c
            .observations
            .last()
            .is_some_and(|o| !o.found && !o.budget_hit && !o.final_attempt)),
        "earlier exhausted failure not exercised"
    );
    assert!(
        captures.iter().any(|c| c.level != 6
            && c.observations
                .iter()
                .any(|o| o.tag == 2 && o.candidates.len() > 1)),
        "no competing split-phase choices"
    );

    let first_guess_wrong = |pair: &[Observation]| {
        pair[0].tag == 2
            && pair[1].tag == 3
            && pair[0]
                .candidates
                .iter()
                .map(|(v, _)| i64::try_from(*v).unwrap())
                .min()
                .is_some_and(|first| first != pair[1].selected)
    };
    let ambiguous = captures
        .iter()
        .find(|c| c.observations.windows(2).any(first_guess_wrong))
        .expect("no actual trace whose first numeric matching branch dead-ends");
    // The model guesses numeric IDs before consuming CandidateTrial: the first
    // guess dead-ends; another must still consume the entire terminal trace.
    assert!(
        compatible(ambiguous),
        "existential matcher rejected alternate matching branch"
    );
    let choice = ambiguous
        .observations
        .windows(2)
        .position(first_guess_wrong)
        .unwrap();
    reject_mutation(ambiguous, "wrong selected vertex", |c| {
        let selected = c.observations[choice + 1].selected;
        let &(other, legal) = c.observations[choice]
            .candidates
            .iter()
            .find(|(v, _)| i64::try_from(*v).unwrap() != selected)
            .unwrap();
        c.observations[choice + 1].selected = i64::try_from(other).unwrap();
        c.observations[choice + 1].candidates[0] = (other, legal);
    });
    reject_mutation(ambiguous, "wrong cached legality", |c| {
        c.observations[choice].candidates[0].1 = !c.observations[choice].candidates[0].1;
    });
    let restored = captures
        .iter()
        .find_map(|c| {
            c.observations
                .iter()
                .position(|o| o.tag == 7 && !o.active.is_empty())
                .map(|i| (c, i))
        })
        .expect("no actual restored reader");
    reject_mutation(restored.0, "missing restored reader", |c| {
        c.observations[restored.1].active.pop();
    });
    let (si, si_event, closed_key) = captures
        .iter()
        .filter(|c| c.level == 5)
        .find_map(|capture| {
            let keys: BTreeSet<_> = capture
                .history
                .iter()
                .flatten()
                .flat_map(|t| &t.events)
                .map(|event| match event {
                    Event::Read { variable, .. } | Event::Write { variable, .. } => *variable,
                })
                .collect();
            capture
                .observations
                .iter()
                .enumerate()
                .filter(|(_, o)| o.tag == 5)
                .find_map(|(index, event)| {
                    keys.iter()
                        .find(|key| !event.open.contains(key))
                        .map(|key| (capture, index, *key))
                })
        })
        .expect("no actual SI state with a closed existing variable");
    reject_mutation(si, "spurious SI open variable", |c| {
        c.observations[si_event].open.push(closed_key);
    });
    let failed = captures
        .iter()
        .find(|c| !c.observations.last().unwrap().found)
        .unwrap();
    reject_mutation(failed, "premature success", |c| {
        c.observations.last_mut().unwrap().found = true;
    });
    let complete_budget = captures
        .iter()
        .find_map(|c| {
            c.observations
                .iter()
                .position(|o| {
                    o.tag == 8
                        && o.reason == 1
                        && !o.path.is_empty()
                        && o.path.len() == c.vertices.len()
                })
                .map(|i| (c, i))
        })
        .expect("no real budget abandonment at a semantic completion");
    reject_mutation(complete_budget.0, "unjustified semantic prune", |c| {
        let event = &mut c.observations[complete_budget.1];
        event.reason = 2;
        event.budget_hit = false;
    });
    reject_mutation(ambiguous, "truncated terminal", |c| {
        c.observations.pop();
    });
    reject_mutation(ambiguous, "post-terminal observation", |c| {
        c.observations.push(c.observations.last().unwrap().clone());
    });

    let mut grouped = 0;
    let mut distinct_active = 0;
    let mut nodes = 0;
    for capture in &captures {
        let mut by_frontier = BTreeMap::<_, BTreeSet<_>>::new();
        for event in capture.observations.iter().filter(|o| o.tag == 1) {
            nodes += 1;
            by_frontier
                .entry(event.frontier.clone())
                .or_default()
                .insert((
                    event.active_keys.clone(),
                    event.active.clone(),
                    event.open.clone(),
                ));
        }
        grouped += by_frontier.len();
        distinct_active += by_frontier
            .values()
            .filter(|states| states.len() > 1)
            .count();
    }
    eprintln!("actual TLC search traces: histories={} captures={} stable_nodes={nodes} frontier_groups={grouped} groups_with_distinct_active={distinct_active} preprocessing_rejected_level_cases={preprocessing_rejections}; profiles=default,deterministic-exhaustive,budget2-plus2; all compatible traces consumed terminal; finite bounds only, probabilistic hash collisions not ruled out", histories.len(), captures.len());
}
