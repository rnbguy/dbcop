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

/// DFS engine options.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct DfsSearchOptions {
    /// Enable frontier-signature memoization.
    pub memoize_frontier: bool,
    /// Candidate ordering strategy.
    pub branch_ordering: BranchOrdering,
}

impl Default for DfsSearchOptions {
    fn default() -> Self {
        Self {
            memoize_frontier: true,
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
    use core::hash::{BuildHasher, Hasher};

    use hashbrown::DefaultHashBuilder;

    let builder = DefaultHashBuilder::default();
    let mut h1 = builder.build_hasher();
    0u64.hash(&mut h1);
    v.hash(&mut h1);
    let lo = h1.finish();

    let mut h2 = builder.build_hasher();
    1u64.hash(&mut h2);
    v.hash(&mut h2);
    let hi = h2.finish();

    (u128::from(hi) << 64) | u128::from(lo)
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
        let options = self.search_options();

        if self.should_prune(linearization, non_det_choices.len()) {
            return false;
        }

        if options.memoize_frontier {
            let signature = self.frontier_signature(*frontier_hash, linearization);
            if !seen.insert(signature) {
                return false;
            }
        }

        if non_det_choices.is_empty() {
            true
        } else {
            let curr_non_det_choices = non_det_choices.len();
            let mut candidates: Vec<Self::Vertex> = non_det_choices
                .iter()
                .take(curr_non_det_choices)
                .cloned()
                .collect();

            match options.branch_ordering {
                BranchOrdering::AsProvided => {}
                BranchOrdering::HighScoreFirst => {
                    use core::cmp::Reverse;
                    candidates.sort_by_key(|v| Reverse(self.branch_score(linearization, v)));
                }
                BranchOrdering::LowScoreFirst => {
                    candidates.sort_by_key(|v| self.branch_score(linearization, v));
                }
            }

            for candidate in candidates {
                let Some(pos) = non_det_choices.iter().position(|v| v == &candidate) else {
                    continue;
                };
                let Some(u) = non_det_choices.remove(pos) else {
                    continue;
                };

                if self.allow_next(linearization, &u) {
                    let mut newly_activated: Vec<Self::Vertex> = Vec::new();
                    if let Some(vs) = self.children_of(&u) {
                        for v in vs {
                            let entry = active_parent
                                .get_mut(&v)
                                .expect("all vertices are expected in active parent");
                            *entry -= 1;
                            if *entry == 0 {
                                non_det_choices.push_back(v.clone());
                                *frontier_hash ^= self.zobrist_value(&v);
                                newly_activated.push(v);
                            }
                        }
                    }

                    linearization.push(u.clone());
                    *frontier_hash ^= self.zobrist_value(&u);

                    self.forward_book_keeping(linearization);

                    if self.do_dfs(
                        non_det_choices,
                        active_parent,
                        linearization,
                        seen,
                        frontier_hash,
                    ) {
                        return true;
                    }

                    self.backtrack_book_keeping(linearization);
                    linearization.pop();
                    *frontier_hash ^= self.zobrist_value(&u);

                    if let Some(vs) = self.children_of(&u) {
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
                            *frontier_hash ^= self.zobrist_value(&removed);
                        }
                    }
                }

                non_det_choices.push_back(u);
            }
            false
        }
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
        let mut non_det_choices: VecDeque<Self::Vertex> = VecDeque::default();
        let mut active_parent: HashMap<Self::Vertex, usize> = HashMap::default();
        let mut linearization: Vec<Self::Vertex> = Vec::default();
        let mut seen: HashSet<u128> = HashSet::default();
        let mut frontier_hash: u128 = 0;

        // do active_parent counting
        for u in self.vertices() {
            {
                active_parent.entry(u.clone()).or_insert(0);
            }
            if let Some(vs) = self.children_of(&u) {
                for v in vs {
                    let entry = active_parent.entry(v).or_insert(0);
                    *entry += 1;
                }
            }
        }

        // take vertices with zero active_parent as non-det choices
        active_parent.iter().for_each(|(n, v)| {
            if *v == 0 {
                non_det_choices.push_back(n.clone());
                frontier_hash ^= self.zobrist_value(n);
            }
        });

        self.do_dfs(
            &mut non_det_choices,
            &mut active_parent,
            &mut linearization,
            &mut seen,
            &mut frontier_hash,
        );

        if linearization.is_empty() {
            None
        } else {
            Some(linearization)
        }
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
}
