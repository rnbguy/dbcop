//! Constrained linearization framework for NP-complete consistency checks.
//!
//! This module provides the [`ConstrainedLinearizationSolver`] trait and
//! its DFS-based search engine. Consistency levels that are NP-complete
//! to verify (Prefix Consistency, Snapshot Isolation, Serializability)
//! reduce to finding a valid topological ordering of a directed graph
//! that satisfies additional domain-specific constraints.
//!
//! # How it works
//!
//! The solver performs a depth-first search over all possible topological
//! orderings of the visibility graph, pruning branches that violate the
//! consistency-specific constraints:
//!
//! 1. Compute in-degree (`active_parent` count) for every vertex.
//! 2. Seed the frontier with all vertices having zero in-degree.
//! 3. At each step, try each frontier vertex:
//!    a. Check `allow_next()` -- does this vertex satisfy the
//!    consistency constraint at this position?
//!    b. If yes, place it: update the frontier, call
//!    `forward_book_keeping()`, and recurse.
//!    c. If the recursion fails, call `backtrack_book_keeping()` to
//!    undo, restore the frontier, and try the next candidate.
//! 4. If all vertices are placed, return the linearization.
//!    If all candidates are exhausted, return `None`.
//!
//! # Memoization
//!
//! To avoid re-exploring equivalent frontier states reached via
//! different orderings, the solver can maintain a `seen` set of
//! Zobrist-hashed frontier signatures. Hash updates are incremental on
//! vertex add/remove from the frontier.
//!
//! # Trait contract
//!
//! Implementors define:
//!
//! - [`Vertex`] -- the type of graph nodes (e.g. `TransactionId` or
//!   `(TransactionId, bool)` for split-phase solvers).
//! - [`search_options`] -- DFS/memoization/ordering policy.
//! - [`get_root`] -- the root vertex (typically `TransactionId::default()`).
//! - [`children_of`] -- successors in the visibility graph.
//! - [`vertices`] -- all vertices in the graph.
//! - [`allow_next`] -- the consistency-specific filter.
//! - [`forward_book_keeping`] -- update solver state when a vertex is
//!   placed in the linearization.
//! - [`backtrack_book_keeping`] -- undo solver state when a vertex is
//!   removed during backtracking.
//! - [`zobrist_value`] -- provider-controlled Zobrist token generation.
//!
//! [`Vertex`]: ConstrainedLinearizationSolver::Vertex
//! [`search_options`]: ConstrainedLinearizationSolver::search_options
//! [`get_root`]: ConstrainedLinearizationSolver::get_root
//! [`children_of`]: ConstrainedLinearizationSolver::children_of
//! [`vertices`]: ConstrainedLinearizationSolver::vertices
//! [`allow_next`]: ConstrainedLinearizationSolver::allow_next
//! [`forward_book_keeping`]: ConstrainedLinearizationSolver::forward_book_keeping
//! [`backtrack_book_keeping`]: ConstrainedLinearizationSolver::backtrack_book_keeping
//! [`zobrist_value`]: ConstrainedLinearizationSolver::zobrist_value

use alloc::collections::vec_deque::VecDeque;
use alloc::vec::Vec;
use core::fmt::Debug;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

/// Branch-ordering mode for DFS frontier exploration.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BranchOrdering {
    /// Keep frontier order as provided.
    AsProvided,
    /// Try higher-scoring candidates first.
    HighScoreFirst,
    /// Try lower-scoring candidates first.
    LowScoreFirst,
}

/// Nogood-learning mode for DFS search.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum NogoodLearning {
    Disabled,
    Enabled,
}

/// Dominance-pruning mode for DFS search.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DominancePruning {
    Disabled,
    Enabled,
}

/// Tie-breaking policy for equally ranked candidates.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TieBreaking {
    Deterministic,
    Randomized,
}

/// Adaptive portfolio mode for attempt-level ordering policy.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum HeuristicPortfolio {
    Disabled,
    Enabled,
}

/// Principal variation ordering mode.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum PrincipalVariationOrdering {
    Disabled,
    Enabled,
}

/// DFS engine options.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct DfsSearchOptions {
    /// Enable frontier-signature memoization.
    pub memoize_frontier: bool,
    /// Enable/disable nogood learning on failed state signatures.
    pub nogood_learning: NogoodLearning,
    /// Enable killer/history move ordering augmentation.
    pub enable_killer_history: bool,
    /// Enable/disable frontier dominance pruning.
    pub dominance_pruning: DominancePruning,
    /// Tie-breaking policy among similarly ranked candidates.
    pub tie_breaking: TieBreaking,
    /// Number of additional restart attempts before a final exhaustive run.
    pub restart_max_attempts: usize,
    /// Per-attempt node budget for restart attempts.
    ///
    /// The final attempt is always exhaustive (`None`) to preserve completeness.
    pub restart_node_budget: Option<usize>,
    /// Enable/disable attempt-level adaptive heuristic portfolio.
    pub heuristic_portfolio: HeuristicPortfolio,
    /// Enable/disable chess-style principal variation ordering.
    pub principal_variation_ordering: PrincipalVariationOrdering,
    /// Prioritize currently-legal candidates before illegal ones.
    ///
    /// This keeps branch ordering focused on feasible moves and reduces
    /// `allow_next` misses in dense frontiers.
    pub prefer_allowed_first: bool,
    /// Candidate ordering strategy.
    pub branch_ordering: BranchOrdering,
}

impl Default for DfsSearchOptions {
    fn default() -> Self {
        Self {
            memoize_frontier: true,
            nogood_learning: NogoodLearning::Enabled,
            enable_killer_history: true,
            dominance_pruning: DominancePruning::Enabled,
            tie_breaking: TieBreaking::Deterministic,
            restart_max_attempts: 0,
            restart_node_budget: None,
            heuristic_portfolio: HeuristicPortfolio::Disabled,
            principal_variation_ordering: PrincipalVariationOrdering::Enabled,
            prefer_allowed_first: true,
            branch_ordering: BranchOrdering::AsProvided,
        }
    }
}

/// Compute a Zobrist hash value for a vertex.
///
/// Produces a u128 by hashing the vertex with two different seeds and
/// combining the results. Used for O(1) incremental frontier hashing:
/// XOR-ing in a vertex when it enters the frontier and XOR-ing it out
/// when it leaves.
fn default_zobrist_value<T: Hash>(v: &T) -> u128 {
    seeded_hash_u128(0, v)
}

/// Compute a seeded `u128` hash for an arbitrary value.
///
/// Uses two independent 64-bit hashes and combines them into one `u128`.
pub(crate) fn seeded_hash_u128<T: Hash>(seed: u64, value: &T) -> u128 {
    use core::hash::{BuildHasher, Hasher};

    use hashbrown::DefaultHashBuilder;

    let builder = DefaultHashBuilder::default();
    let mut h1 = builder.build_hasher();
    seed.hash(&mut h1);
    0u64.hash(&mut h1);
    value.hash(&mut h1);
    let lo = h1.finish();

    let mut h2 = builder.build_hasher();
    seed.hash(&mut h2);
    1u64.hash(&mut h2);
    value.hash(&mut h2);
    let hi = h2.finish();

    (u128::from(hi) << 64) | u128::from(lo)
}

#[derive(Debug)]
struct SearchHeuristics<Vertex>
where
    Vertex: Eq + Hash + Clone,
{
    killer_moves: HashMap<usize, Vec<Vertex>>,
    history_scores: HashMap<Vertex, u64>,
    counter_moves: HashMap<Vertex, Vertex>,
}

impl<Vertex> SearchHeuristics<Vertex>
where
    Vertex: Eq + Hash + Clone,
{
    fn history_reward(depth: usize) -> u64 {
        let capped = core::cmp::min(depth, 16);
        let shift = u32::try_from(capped).expect("depth cap fits u32");
        1_u64 << shift
    }

    fn add_history_reward(&mut self, v: &Vertex, depth: usize) {
        let reward = Self::history_reward(depth);
        let entry = self.history_scores.entry(v.clone()).or_insert(0);
        *entry = entry.saturating_add(reward);
    }

    fn candidate_bonus(&self, depth: usize, previous: Option<&Vertex>, v: &Vertex) -> u64 {
        let history_bonus = self.history_scores.get(v).copied().unwrap_or(0);
        let killer_bonus = self
            .killer_moves
            .get(&depth)
            .and_then(|moves| moves.iter().position(|k| k == v))
            .map_or(0, |idx| if idx == 0 { 1_u64 << 20 } else { 1_u64 << 19 });
        let counter_bonus = previous
            .and_then(|parent| self.counter_moves.get(parent))
            .filter(|reply| *reply == v)
            .map_or(0, |_| 1_u64 << 22);
        history_bonus
            .saturating_add(killer_bonus)
            .saturating_add(counter_bonus)
    }

    fn record_failed_move(&mut self, depth: usize, v: &Vertex) {
        self.add_history_reward(v, depth);
        let entry = self.killer_moves.entry(depth).or_default();
        if entry.iter().any(|x| x == v) {
            return;
        }
        entry.insert(0, v.clone());
        if entry.len() > 2 {
            entry.truncate(2);
        }
    }

    fn record_success_move(&mut self, depth: usize, v: &Vertex) {
        self.add_history_reward(v, depth.saturating_add(1));
    }

    fn record_counter_move(&mut self, previous: &Vertex, response: &Vertex) {
        self.counter_moves
            .insert(previous.clone(), response.clone());
    }
}

impl<Vertex> Default for SearchHeuristics<Vertex>
where
    Vertex: Eq + Hash + Clone,
{
    fn default() -> Self {
        Self {
            killer_moves: HashMap::default(),
            history_scores: HashMap::default(),
            counter_moves: HashMap::default(),
        }
    }
}

#[derive(Debug)]
struct DfsRuntime<Vertex>
where
    Vertex: Eq + Hash + Clone,
{
    options: DfsSearchOptions,
    heuristics: SearchHeuristics<Vertex>,
    nogood_signatures: HashSet<u128>,
    conflict_jump_depth: HashMap<u128, usize>,
    failed_frontiers_by_state: HashMap<u128, Vec<HashSet<Vertex>>>,
    rng: XorShift64,
    nodes_expanded: usize,
    node_budget: Option<usize>,
    budget_hit: bool,
    portfolio_mode: PortfolioMode,
    pv_hint: Vec<Vertex>,
    best_path: Vec<Vertex>,
    best_depth: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum DfsStepResult {
    Found,
    Fail { jump_depth: usize },
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum PortfolioMode {
    SolverBiased,
    FrontierHeavy,
    Diverse,
}

#[derive(Debug, Clone, Copy, Default)]
struct PortfolioStats {
    attempts: u64,
    successes: u64,
    total_nodes: u64,
}

impl<Vertex> DfsRuntime<Vertex>
where
    Vertex: Eq + Hash + Clone,
{
    fn next_random(&mut self) -> u64 {
        self.rng.next_u64()
    }

    fn consume_node_budget(&mut self) -> bool {
        self.nodes_expanded = self.nodes_expanded.saturating_add(1);
        if self
            .node_budget
            .is_some_and(|budget| self.nodes_expanded > budget)
        {
            self.budget_hit = true;
            return false;
        }
        true
    }

    fn maybe_record_best_path(&mut self, linearization: &[Vertex]) {
        let depth = linearization.len();
        if depth > self.best_depth {
            self.best_depth = depth;
            self.best_path = linearization.to_vec();
        }
    }

    fn is_dominated(&self, state_signature: u128, frontier: &HashSet<Vertex>) -> bool {
        self.failed_frontiers_by_state
            .get(&state_signature)
            .is_some_and(|failed_frontiers| {
                failed_frontiers
                    .iter()
                    .any(|failed| frontier.is_subset(failed))
            })
    }

    fn record_failed_frontier(&mut self, state_signature: u128, frontier: HashSet<Vertex>) {
        let entry = self
            .failed_frontiers_by_state
            .entry(state_signature)
            .or_default();
        if entry.iter().any(|existing| existing.is_superset(&frontier)) {
            return;
        }
        entry.retain(|existing| !existing.is_subset(&frontier));
        entry.push(frontier);
        if entry.len() > 32 {
            entry.remove(0);
        }
    }
}

#[derive(Debug, Clone, Copy)]
struct XorShift64 {
    state: u64,
}

impl XorShift64 {
    const fn new(seed: u64) -> Self {
        let initial = if seed == 0 {
            0x9E37_79B9_7F4A_7C15
        } else {
            seed
        };
        Self { state: initial }
    }

    fn next_u64(&mut self) -> u64 {
        let mut x = self.state;
        x ^= x << 13;
        x ^= x >> 7;
        x ^= x << 17;
        self.state = x;
        x
    }
}

const PORTFOLIO_MODES: [PortfolioMode; 3] = [
    PortfolioMode::SolverBiased,
    PortfolioMode::FrontierHeavy,
    PortfolioMode::Diverse,
];

const fn portfolio_index(mode: PortfolioMode) -> usize {
    match mode {
        PortfolioMode::SolverBiased => 0,
        PortfolioMode::FrontierHeavy => 1,
        PortfolioMode::Diverse => 2,
    }
}

fn choose_portfolio_mode(stats: &[PortfolioStats; 3]) -> PortfolioMode {
    for mode in PORTFOLIO_MODES {
        if stats[portfolio_index(mode)].attempts == 0 {
            return mode;
        }
    }
    let mut best_mode = PortfolioMode::SolverBiased;
    let mut best_score = 0_u128;
    for mode in PORTFOLIO_MODES {
        let stat = stats[portfolio_index(mode)];
        let attempts = u128::from(stat.attempts).saturating_add(1);
        let success_term = u128::from(stat.successes).saturating_mul(1_000_000) / attempts;
        let explore_term = 100_000 / attempts;
        let avg_nodes = u128::from(stat.total_nodes) / attempts;
        let cost_term = 10_000 / avg_nodes.saturating_add(1);
        let score = success_term
            .saturating_add(explore_term)
            .saturating_add(cost_term);
        if score > best_score {
            best_score = score;
            best_mode = mode;
        }
    }
    best_mode
}

fn order_frontier_with_heuristics<S: ConstrainedLinearizationSolver + ?Sized>(
    solver: &S,
    non_det_choices: &VecDeque<S::Vertex>,
    linearization: &[S::Vertex],
    runtime: &mut DfsRuntime<S::Vertex>,
) -> Vec<(S::Vertex, bool)> {
    let options = runtime.options;
    let base = solver.ordered_frontier_candidates(non_det_choices, linearization, options);
    if !options.enable_killer_history {
        return base;
    }

    let depth = linearization.len();
    let mut decorated: Vec<(usize, S::Vertex, bool, u64, u64)> = base
        .into_iter()
        .enumerate()
        .map(|(idx, (v, allow_next))| {
            let previous = linearization.last();
            let base_bonus = runtime.heuristics.candidate_bonus(depth, previous, &v);
            let portfolio_bonus =
                portfolio_bonus(solver, linearization, &v, runtime.portfolio_mode);
            let pv_bonus = if matches!(
                options.principal_variation_ordering,
                PrincipalVariationOrdering::Enabled
            ) && runtime
                .pv_hint
                .get(depth)
                .is_some_and(|candidate| candidate == &v)
            {
                1_u64 << 30
            } else {
                0
            };
            let bonus = base_bonus
                .saturating_add(portfolio_bonus)
                .saturating_add(pv_bonus);
            let random_tie = if matches!(options.tie_breaking, TieBreaking::Randomized) {
                runtime.next_random()
            } else {
                0
            };
            (idx, v, allow_next, bonus, random_tie)
        })
        .collect();

    decorated.sort_by(|a, b| {
        if options.prefer_allowed_first {
            b.2.cmp(&a.2)
                .then_with(|| b.3.cmp(&a.3))
                .then_with(|| b.4.cmp(&a.4))
                .then_with(|| a.0.cmp(&b.0))
        } else {
            b.3.cmp(&a.3)
                .then_with(|| b.4.cmp(&a.4))
                .then_with(|| a.0.cmp(&b.0))
        }
    });

    decorated
        .into_iter()
        .map(|(_, v, allow_next, _, _)| (v, allow_next))
        .collect()
}

fn portfolio_bonus<S: ConstrainedLinearizationSolver + ?Sized>(
    solver: &S,
    linearization: &[S::Vertex],
    v: &S::Vertex,
    mode: PortfolioMode,
) -> u64 {
    match mode {
        PortfolioMode::SolverBiased => 0,
        PortfolioMode::FrontierHeavy => {
            let children = solver.children_of(v).map_or(0, |vs| vs.len());
            let child_score = u64::try_from(children).expect("child count fits u64");
            let solver_score = solver.branch_score(linearization, v).unsigned_abs();
            child_score
                .saturating_mul(512)
                .saturating_add(solver_score.saturating_mul(64))
        }
        PortfolioMode::Diverse => {
            let mixed = seeded_hash_u128(0x0D15_EA5E, v);
            let low = mixed & u128::from(u64::MAX);
            u64::try_from(low).expect("masked hash fits u64")
        }
    }
}

#[allow(clippy::too_many_lines)]
fn do_dfs_impl<S: ConstrainedLinearizationSolver + ?Sized>(
    solver: &mut S,
    non_det_choices: &mut VecDeque<S::Vertex>,
    active_parent: &mut HashMap<S::Vertex, usize>,
    linearization: &mut Vec<S::Vertex>,
    seen: &mut HashSet<u128>,
    frontier_hash: &mut u128,
    runtime: &mut DfsRuntime<S::Vertex>,
) -> DfsStepResult {
    let depth = linearization.len();
    let options = runtime.options;
    runtime.maybe_record_best_path(linearization);
    let state_signature = solver.frontier_signature(0, linearization);
    let mut frontier_set_for_dominance: Option<HashSet<S::Vertex>> = None;
    if !runtime.consume_node_budget() {
        return DfsStepResult::Fail {
            jump_depth: depth.saturating_sub(1),
        };
    }
    if solver.should_prune(linearization, non_det_choices.len()) {
        return DfsStepResult::Fail {
            jump_depth: depth.saturating_sub(1),
        };
    }
    if matches!(options.dominance_pruning, DominancePruning::Enabled) {
        let frontier_set: HashSet<S::Vertex> = non_det_choices.iter().cloned().collect();
        if runtime.is_dominated(state_signature, &frontier_set) {
            return DfsStepResult::Fail {
                jump_depth: runtime
                    .conflict_jump_depth
                    .get(&state_signature)
                    .copied()
                    .unwrap_or_else(|| depth.saturating_sub(1)),
            };
        }
        frontier_set_for_dominance = Some(frontier_set);
    }
    let signature = solver.frontier_signature(*frontier_hash, linearization);
    if matches!(options.nogood_learning, NogoodLearning::Enabled)
        && runtime.nogood_signatures.contains(&signature)
    {
        return DfsStepResult::Fail {
            jump_depth: runtime
                .conflict_jump_depth
                .get(&signature)
                .copied()
                .unwrap_or_else(|| depth.saturating_sub(1)),
        };
    }
    if options.memoize_frontier && !seen.insert(signature) {
        return DfsStepResult::Fail {
            jump_depth: runtime
                .conflict_jump_depth
                .get(&signature)
                .copied()
                .unwrap_or_else(|| depth.saturating_sub(1)),
        };
    }
    if non_det_choices.is_empty() {
        return DfsStepResult::Found;
    }
    let candidates =
        order_frontier_with_heuristics(solver, non_det_choices, linearization, runtime);
    for (candidate, allow_next) in candidates {
        let Some(pos) = non_det_choices.iter().position(|v| v == &candidate) else {
            continue;
        };
        let Some(u) = non_det_choices.remove(pos) else {
            continue;
        };
        if allow_next {
            let mut newly_activated: Vec<S::Vertex> = Vec::new();
            if let Some(vs) = solver.children_of(&u) {
                for v in vs {
                    let entry = active_parent
                        .get_mut(&v)
                        .expect("all vertices are expected in active parent");
                    *entry -= 1;
                    if *entry == 0 {
                        non_det_choices.push_back(v.clone());
                        *frontier_hash ^= solver.zobrist_value(&v);
                        newly_activated.push(v);
                    }
                }
            }
            linearization.push(u.clone());
            *frontier_hash ^= solver.zobrist_value(&u);
            solver.forward_book_keeping(linearization);
            let recurse = do_dfs_impl(
                solver,
                non_det_choices,
                active_parent,
                linearization,
                seen,
                frontier_hash,
                runtime,
            );
            if matches!(recurse, DfsStepResult::Found) {
                runtime.heuristics.record_success_move(depth, &u);
                if let Some(previous) = linearization.get(depth.saturating_sub(1)) {
                    runtime.heuristics.record_counter_move(previous, &u);
                }
                return DfsStepResult::Found;
            }
            runtime.heuristics.record_failed_move(depth, &u);
            solver.backtrack_book_keeping(linearization);
            linearization.pop();
            *frontier_hash ^= solver.zobrist_value(&u);
            if let Some(vs) = solver.children_of(&u) {
                for v in vs {
                    let entry = active_parent
                        .get_mut(&v)
                        .expect("all vertices are expected in active parent");
                    *entry += 1;
                }
            }
            for v in newly_activated {
                if let Some(activated_pos) = non_det_choices.iter().position(|x| x == &v) {
                    let removed = non_det_choices
                        .remove(activated_pos)
                        .expect("frontier vertex should exist");
                    *frontier_hash ^= solver.zobrist_value(&removed);
                }
            }
            if let DfsStepResult::Fail { jump_depth } = recurse {
                if jump_depth < depth {
                    runtime.conflict_jump_depth.insert(signature, jump_depth);
                    return DfsStepResult::Fail { jump_depth };
                }
            }
        }
        non_det_choices.push_back(u);
    }
    if matches!(options.nogood_learning, NogoodLearning::Enabled) {
        runtime.nogood_signatures.insert(signature);
    }
    if matches!(options.dominance_pruning, DominancePruning::Enabled) {
        let frontier_set =
            frontier_set_for_dominance.unwrap_or_else(|| non_det_choices.iter().cloned().collect());
        runtime.record_failed_frontier(state_signature, frontier_set);
    }
    let jump_depth = depth.saturating_sub(1);
    runtime.conflict_jump_depth.insert(signature, jump_depth);
    DfsStepResult::Fail { jump_depth }
}

fn attempt_seed(attempt: usize) -> u64 {
    let attempt64 = u64::try_from(attempt).expect("attempt index fits u64");
    0xA076_1D64_78BD_642F_u64 ^ attempt64.wrapping_mul(0xE703_7ED1_A0B4_28DB_u64)
}

/// Trait for consistency-specific linearization solvers.
///
/// Implementors define the graph structure and constraints for a
/// particular consistency level. The default [`do_dfs`] and
/// [`get_linearization`] methods provide the DFS search engine with
/// Zobrist-hash memoization.
///
/// [`do_dfs`]: ConstrainedLinearizationSolver::do_dfs
/// [`get_linearization`]: ConstrainedLinearizationSolver::get_linearization
pub trait ConstrainedLinearizationSolver {
    /// The type of vertices in the linearization graph.
    ///
    /// For Serializability this is `TransactionId`. For Prefix
    /// Consistency and Snapshot Isolation this is
    /// `(TransactionId, bool)` where the bool distinguishes the read
    /// phase (`false`) from the write phase (`true`).
    type Vertex: Hash + Ord + Eq + Clone + Debug;

    /// Return DFS engine options for this solver.
    fn search_options(&self) -> DfsSearchOptions {
        DfsSearchOptions::default()
    }

    /// Rank a frontier candidate for optional branch ordering.
    ///
    /// Default score prefers nodes with larger out-degree (more
    /// constraining successors).
    fn branch_score(&self, _linearization: &[Self::Vertex], v: &Self::Vertex) -> i64 {
        #[allow(clippy::cast_possible_wrap)]
        self.children_of(v)
            .map_or(0, |children| children.len() as i64)
    }

    /// Return the per-vertex Zobrist token used by frontier hashing.
    ///
    /// Implementors may override this to provide custom/randomized keys.
    fn zobrist_value(&self, v: &Self::Vertex) -> u128 {
        default_zobrist_value(v)
    }

    /// Build the memoization signature for the current DFS state.
    ///
    /// Default uses frontier hash only. Implementors may include
    /// solver-specific state fingerprints for stronger pruning.
    fn frontier_signature(&self, frontier_hash: u128, _linearization: &[Self::Vertex]) -> u128 {
        frontier_hash
    }

    /// Optional solver-defined branch pruning hook.
    ///
    /// Returning `true` prunes the current DFS branch immediately.
    fn should_prune(&self, _linearization: &[Self::Vertex], _frontier_len: usize) -> bool {
        false
    }

    /// Build the frontier candidate list with legality and ordering metadata.
    fn ordered_frontier_candidates(
        &self,
        non_det_choices: &VecDeque<Self::Vertex>,
        linearization: &[Self::Vertex],
        options: DfsSearchOptions,
    ) -> Vec<(Self::Vertex, bool)> {
        let mut candidates: Vec<(Self::Vertex, bool, i64)> = non_det_choices
            .iter()
            .map(|v| {
                (
                    v.clone(),
                    self.allow_next(linearization, v),
                    self.branch_score(linearization, v),
                )
            })
            .collect();

        match options.branch_ordering {
            BranchOrdering::AsProvided => {}
            BranchOrdering::HighScoreFirst => {
                candidates.sort_by(|a, b| {
                    if options.prefer_allowed_first {
                        b.1.cmp(&a.1).then_with(|| b.2.cmp(&a.2))
                    } else {
                        b.2.cmp(&a.2)
                    }
                });
            }
            BranchOrdering::LowScoreFirst => {
                candidates.sort_by(|a, b| {
                    if options.prefer_allowed_first {
                        b.1.cmp(&a.1).then_with(|| a.2.cmp(&b.2))
                    } else {
                        a.2.cmp(&b.2)
                    }
                });
            }
        }

        if matches!(options.branch_ordering, BranchOrdering::AsProvided)
            && options.prefer_allowed_first
        {
            candidates.sort_by(|a, b| b.1.cmp(&a.1));
        }

        candidates
            .into_iter()
            .map(|(v, allow_next, _)| (v, allow_next))
            .collect()
    }

    /// Return the root vertex of the visibility graph.
    ///
    /// This is the starting point of the linearization, typically
    /// `TransactionId::default()` (the implicit initial transaction).
    fn get_root(&self) -> Self::Vertex;

    /// Return the successors of `source` in the visibility graph.
    ///
    /// Returns `None` if `source` has no outgoing edges in the
    /// adjacency map, or `Some(vec)` with the list of successors.
    /// These successors become candidates for the frontier once all
    /// their parents have been placed.
    fn children_of(&self, source: &Self::Vertex) -> Option<Vec<Self::Vertex>>;

    /// Test whether vertex `v` may be placed next in the linearization.
    ///
    /// This is the consistency-specific constraint filter. For example,
    /// the Serializability solver checks that placing `v` does not
    /// conflict with any outstanding active writes.
    fn allow_next(&self, linearization: &[Self::Vertex], v: &Self::Vertex) -> bool;

    /// Return all vertices in the graph.
    ///
    /// Used during initialization to compute in-degrees and seed the
    /// frontier.
    fn vertices(&self) -> Vec<Self::Vertex>;

    /// Update solver state after placing a vertex in the linearization.
    ///
    /// Called immediately after a vertex is appended to the
    /// linearization during forward exploration. Implementations
    /// typically update `active_write` and `active_variable` tracking
    /// maps.
    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]);

    /// Undo solver state after removing a vertex during backtracking.
    ///
    /// Called when the DFS backtracks past a vertex. Must exactly
    /// reverse the effects of [`forward_book_keeping`] to restore the
    /// solver to its previous state.
    ///
    /// [`forward_book_keeping`]: ConstrainedLinearizationSolver::forward_book_keeping
    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]);

    /// Recursive DFS step with Zobrist memoization.
    ///
    /// Tries each vertex in `non_det_choices` (the current frontier).
    /// For each candidate that passes `allow_next`, places it, updates
    /// the frontier, and recurses. On failure, backtracks and restores
    /// state. Returns `true` if a complete linearization is found.
    ///
    /// The `seen` set and `frontier_hash` implement Zobrist memoization:
    /// if the current frontier hash has been seen before, this branch is
    /// pruned immediately.
    fn do_dfs(
        &mut self,
        non_det_choices: &mut VecDeque<Self::Vertex>,
        active_parent: &mut HashMap<Self::Vertex, usize>,
        linearization: &mut Vec<Self::Vertex>,
        seen: &mut HashSet<u128>,
        frontier_hash: &mut u128,
    ) -> bool {
        let mut runtime = DfsRuntime {
            options: self.search_options(),
            heuristics: SearchHeuristics::default(),
            nogood_signatures: HashSet::default(),
            conflict_jump_depth: HashMap::default(),
            failed_frontiers_by_state: HashMap::default(),
            rng: XorShift64::new(attempt_seed(0)),
            nodes_expanded: 0,
            node_budget: None,
            budget_hit: false,
            portfolio_mode: PortfolioMode::SolverBiased,
            pv_hint: Vec::default(),
            best_path: Vec::default(),
            best_depth: 0,
        };
        matches!(
            do_dfs_impl(
                self,
                non_det_choices,
                active_parent,
                linearization,
                seen,
                frontier_hash,
                &mut runtime,
            ),
            DfsStepResult::Found
        )
    }

    /// Search for a valid constrained linearization.
    ///
    /// Initializes the in-degree map and frontier, then runs [`do_dfs`]
    /// to find a topological ordering that satisfies all constraints.
    ///
    /// Returns `Some(linearization)` if a valid ordering exists, or
    /// `None` if no valid linearization can be found (indicating the
    /// history violates the target consistency level).
    ///
    /// [`do_dfs`]: ConstrainedLinearizationSolver::do_dfs
    fn get_linearization(&mut self) -> Option<Vec<Self::Vertex>> {
        let options = self.search_options();
        let attempts = options.restart_max_attempts.saturating_add(1);
        let mut portfolio_stats = [PortfolioStats::default(); 3];
        let mut pv_hint: Vec<Self::Vertex> = Vec::default();
        for attempt in 0..attempts {
            let is_final_attempt = attempt.saturating_add(1) == attempts;
            let portfolio_mode =
                if matches!(options.heuristic_portfolio, HeuristicPortfolio::Enabled) {
                    choose_portfolio_mode(&portfolio_stats)
                } else {
                    PortfolioMode::SolverBiased
                };
            let mut non_det_choices: VecDeque<Self::Vertex> = VecDeque::default();
            let mut active_parent: HashMap<Self::Vertex, usize> = HashMap::default();
            let mut linearization: Vec<Self::Vertex> = Vec::default();
            let mut seen: HashSet<u128> = HashSet::default();
            let mut frontier_hash: u128 = 0;

            for u in self.vertices() {
                active_parent.entry(u.clone()).or_insert(0);
                if let Some(vs) = self.children_of(&u) {
                    for v in vs {
                        let entry = active_parent.entry(v).or_insert(0);
                        *entry += 1;
                    }
                }
            }

            active_parent.iter().for_each(|(n, v)| {
                if *v == 0 {
                    non_det_choices.push_back(n.clone());
                    frontier_hash ^= self.zobrist_value(n);
                }
            });

            let mut runtime = DfsRuntime {
                options,
                heuristics: SearchHeuristics::default(),
                nogood_signatures: HashSet::default(),
                conflict_jump_depth: HashMap::default(),
                failed_frontiers_by_state: HashMap::default(),
                rng: XorShift64::new(attempt_seed(attempt)),
                nodes_expanded: 0,
                node_budget: if is_final_attempt {
                    None
                } else {
                    options.restart_node_budget
                },
                budget_hit: false,
                portfolio_mode,
                pv_hint: if matches!(
                    options.principal_variation_ordering,
                    PrincipalVariationOrdering::Enabled
                ) {
                    pv_hint.clone()
                } else {
                    Vec::default()
                },
                best_path: Vec::default(),
                best_depth: 0,
            };

            let found = matches!(
                do_dfs_impl(
                    self,
                    &mut non_det_choices,
                    &mut active_parent,
                    &mut linearization,
                    &mut seen,
                    &mut frontier_hash,
                    &mut runtime,
                ),
                DfsStepResult::Found
            );
            let stat = &mut portfolio_stats[portfolio_index(portfolio_mode)];
            stat.attempts = stat.attempts.saturating_add(1);
            stat.total_nodes = stat.total_nodes.saturating_add(
                u64::try_from(runtime.nodes_expanded).expect("node count fits u64"),
            );
            if found {
                stat.successes = stat.successes.saturating_add(1);
                return Some(linearization);
            }
            if matches!(
                options.principal_variation_ordering,
                PrincipalVariationOrdering::Enabled
            ) {
                pv_hint = runtime.best_path;
            }
            if !runtime.budget_hit {
                break;
            }
        }
        None
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Debug)]
    struct ToySolver {
        scores: HashMap<u64, i64>,
        use_custom_zobrist: bool,
    }

    impl ConstrainedLinearizationSolver for ToySolver {
        type Vertex = u64;

        fn search_options(&self) -> DfsSearchOptions {
            DfsSearchOptions {
                memoize_frontier: true,
                nogood_learning: NogoodLearning::Enabled,
                enable_killer_history: true,
                dominance_pruning: DominancePruning::Enabled,
                tie_breaking: TieBreaking::Deterministic,
                restart_max_attempts: 0,
                restart_node_budget: None,
                heuristic_portfolio: HeuristicPortfolio::Disabled,
                principal_variation_ordering: PrincipalVariationOrdering::Enabled,
                prefer_allowed_first: true,
                branch_ordering: BranchOrdering::HighScoreFirst,
            }
        }

        fn branch_score(&self, _linearization: &[Self::Vertex], v: &Self::Vertex) -> i64 {
            *self.scores.get(v).unwrap_or(&0)
        }

        fn zobrist_value(&self, v: &Self::Vertex) -> u128 {
            if self.use_custom_zobrist {
                u128::from(*v) << 1
            } else {
                default_zobrist_value(v)
            }
        }

        fn get_root(&self) -> Self::Vertex {
            0
        }

        fn children_of(&self, _source: &Self::Vertex) -> Option<Vec<Self::Vertex>> {
            None
        }

        fn allow_next(&self, _linearization: &[Self::Vertex], _v: &Self::Vertex) -> bool {
            true
        }

        fn vertices(&self) -> Vec<Self::Vertex> {
            vec![1, 2]
        }

        fn forward_book_keeping(&mut self, _linearization: &[Self::Vertex]) {}

        fn backtrack_book_keeping(&mut self, _linearization: &[Self::Vertex]) {}
    }

    #[test]
    fn high_score_branching_picks_high_score_first() {
        let mut solver = ToySolver {
            scores: [(1, 1), (2, 10)].into(),
            use_custom_zobrist: false,
        };
        let lin = solver.get_linearization().expect("expected linearization");
        assert_eq!(lin[0], 2);
    }

    #[test]
    fn custom_zobrist_provider_still_finds_linearization() {
        let mut solver = ToySolver {
            scores: HashMap::default(),
            use_custom_zobrist: true,
        };
        let lin = solver.get_linearization().expect("expected linearization");
        assert_eq!(lin.len(), 2);
    }

    #[test]
    fn principal_variation_hint_is_prioritized() {
        let solver = ToySolver {
            scores: HashMap::default(),
            use_custom_zobrist: false,
        };
        let frontier = VecDeque::from([2_u64, 1_u64]);
        let mut runtime = DfsRuntime {
            options: DfsSearchOptions {
                memoize_frontier: true,
                nogood_learning: NogoodLearning::Enabled,
                enable_killer_history: true,
                dominance_pruning: DominancePruning::Enabled,
                tie_breaking: TieBreaking::Deterministic,
                restart_max_attempts: 0,
                restart_node_budget: None,
                heuristic_portfolio: HeuristicPortfolio::Disabled,
                principal_variation_ordering: PrincipalVariationOrdering::Enabled,
                prefer_allowed_first: true,
                branch_ordering: BranchOrdering::AsProvided,
            },
            heuristics: SearchHeuristics::default(),
            nogood_signatures: HashSet::default(),
            conflict_jump_depth: HashMap::default(),
            failed_frontiers_by_state: HashMap::default(),
            rng: XorShift64::new(1),
            nodes_expanded: 0,
            node_budget: None,
            budget_hit: false,
            portfolio_mode: PortfolioMode::SolverBiased,
            pv_hint: vec![1],
            best_path: Vec::default(),
            best_depth: 0,
        };

        let ordered = order_frontier_with_heuristics(&solver, &frontier, &[], &mut runtime);
        assert_eq!(ordered[0].0, 1);
    }

    #[test]
    fn counter_move_hint_is_prioritized() {
        let solver = ToySolver {
            scores: HashMap::default(),
            use_custom_zobrist: false,
        };
        let frontier = VecDeque::from([2_u64, 1_u64]);
        let mut heuristics = SearchHeuristics::default();
        heuristics.record_counter_move(&9_u64, &1_u64);
        let mut runtime = DfsRuntime {
            options: DfsSearchOptions {
                memoize_frontier: true,
                nogood_learning: NogoodLearning::Enabled,
                enable_killer_history: true,
                dominance_pruning: DominancePruning::Enabled,
                tie_breaking: TieBreaking::Deterministic,
                restart_max_attempts: 0,
                restart_node_budget: None,
                heuristic_portfolio: HeuristicPortfolio::Disabled,
                principal_variation_ordering: PrincipalVariationOrdering::Disabled,
                prefer_allowed_first: true,
                branch_ordering: BranchOrdering::AsProvided,
            },
            heuristics,
            nogood_signatures: HashSet::default(),
            conflict_jump_depth: HashMap::default(),
            failed_frontiers_by_state: HashMap::default(),
            rng: XorShift64::new(1),
            nodes_expanded: 0,
            node_budget: None,
            budget_hit: false,
            portfolio_mode: PortfolioMode::SolverBiased,
            pv_hint: Vec::default(),
            best_path: Vec::default(),
            best_depth: 0,
        };

        let ordered = order_frontier_with_heuristics(&solver, &frontier, &[9], &mut runtime);
        assert_eq!(ordered[0].0, 1);
    }
}
