//! Serializability linearization solver.
//!
//! Serializability is the strongest consistency level. It requires a
//! total order on all transactions such that executing them sequentially
//! in that order produces the same reads and writes as the original
//! concurrent execution. Every read must be explained by the
//! immediately preceding write on the same variable in the total order.
//!
//! # Approach
//!
//! This module implements a [`ConstrainedLinearizationSolver`] that
//! searches for a valid total commit order via depth-first search with
//! backtracking. Unlike the Prefix and Snapshot Isolation solvers,
//! transactions are *not* split into read/write phases -- each vertex
//! is a plain `TransactionId`, because serializability requires reads
//! and writes to appear atomically at the same point.
//!
//! The solver tracks `active_write` -- for each variable, the set of
//! transactions that have read a value but whose writing transaction
//! has not yet been placed. A transaction is only allowed in the
//! linearization if placing it does not conflict with any outstanding
//! active writes.
//!
//! # Data flow
//!
//! ```text
//! AtomicTransactionPO (from causal check)
//!     -> SerializabilitySolver -> get_linearization() via DFS
//!     -> Some(Vec<TransactionId>) or None
//!     -> Witness::CommitOrder
//! ```
//!
//! # Witness
//!
//! On success, the linearization is directly returned as
//! `Witness::CommitOrder(Vec<TransactionId>)`.
//!
//! # Reference
//!
//! Implements the constrained linearization search described in
//! Theorem 4.8 of Biswas and Enea (2019), without the split-vertex
//! optimization (since serializability requires atomic transaction
//! placement).

use alloc::vec::Vec;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

use crate::consistency::constrained_linearization::{
    seeded_hash_u128, BranchOrdering, ConstrainedLinearizationSolver, DfsSearchOptions,
    DominancePruning, HeuristicPortfolio, NogoodLearning, TieBreaking,
};
use crate::history::atomic::types::TransactionId;
use crate::history::atomic::AtomicTransactionPO;

/// Linearization solver for Serializability.
///
/// Wraps an [`AtomicTransactionPO`] and tracks `active_write` -- a map
/// from each variable to the set of transactions that have read from a
/// write whose writer has not yet been placed in the linearization.
/// A transaction can only be placed when all of its write variables
/// have at most one active writer (itself).
#[derive(Debug)]
pub struct SerializabilitySolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    pub history: AtomicTransactionPO<Variable>,
    pub active_write: HashMap<Variable, HashSet<TransactionId>>,
}

impl<Variable> From<AtomicTransactionPO<Variable>> for SerializabilitySolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    fn from(history: AtomicTransactionPO<Variable>) -> Self {
        let mut active_write: HashMap<Variable, HashSet<TransactionId>> = HashMap::default();
        // Pre-populate active_write with root's write-read entries.
        // Root (session_id=0) is never linearized (not in history.0),
        // but transactions that read from root expect their entries in
        // active_write when forward_book_keeping runs.
        let root = TransactionId::default();
        for (var, wr_graph) in &history.write_read_relation {
            if let Some(readers) = wr_graph.adj_map.get(&root) {
                active_write.insert(var.clone(), readers.clone());
            }
        }
        Self {
            history,
            active_write,
        }
    }
}

impl<Variable> ConstrainedLinearizationSolver for SerializabilitySolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    type Vertex = TransactionId;

    fn search_options(&self) -> DfsSearchOptions {
        DfsSearchOptions {
            memoize_frontier: true,
            nogood_learning: NogoodLearning::Enabled,
            enable_killer_history: true,
            dominance_pruning: DominancePruning::Enabled,
            tie_breaking: TieBreaking::Randomized,
            restart_max_attempts: 2,
            restart_node_budget: Some(20_000),
            heuristic_portfolio: HeuristicPortfolio::Enabled,
            prefer_allowed_first: true,
            branch_ordering: BranchOrdering::HighScoreFirst,
        }
    }

    fn branch_score(&self, _linearization: &[Self::Vertex], v: &Self::Vertex) -> i64 {
        let txn_info = self.history.history.0.get(v).unwrap();
        let child_count = self.children_of(v).map_or(0, |children| children.len());
        let unresolved_readers = txn_info
            .reads
            .keys()
            .filter(|x| self.active_write.get(*x).is_some_and(|ts| ts.contains(v)))
            .count();
        let unresolved_readers_score =
            i64::try_from(unresolved_readers).expect("unresolved reader count fits i64");
        let write_set = txn_info.writes.len();
        let child_score = i64::try_from(child_count).expect("child count fits i64");
        let write_score = i64::try_from(write_set).expect("write count fits i64");
        (unresolved_readers_score * 8) + (child_score * 2) + write_score
    }

    fn frontier_signature(&self, frontier_hash: u128, _linearization: &[Self::Vertex]) -> u128 {
        let mut signature = frontier_hash;
        for (var, readers) in &self.active_write {
            let mut readers_mix = 0_u128;
            for reader in readers {
                readers_mix ^= seeded_hash_u128(0x701, reader);
            }
            let var_mix = seeded_hash_u128(0x702, var);
            let reader_count = u64::try_from(readers.len()).expect("reader count fits u64");
            let count_mix = seeded_hash_u128(0x703, &reader_count);
            signature ^= var_mix ^ readers_mix ^ count_mix;
        }
        signature
    }

    fn get_root(&self) -> Self::Vertex {
        TransactionId::default()
    }

    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        let curr_txn = linearization.last().unwrap();
        let curr_txn_info = self.history.history.0.get(curr_txn).unwrap();
        for x in curr_txn_info.reads.keys() {
            assert!(self
                .active_write
                .entry(x.clone())
                .or_default()
                .remove(curr_txn));
        }
        for x in &curr_txn_info.writes {
            let read_by = self
                .history
                .write_read_relation
                .get(x)
                .unwrap()
                .adj_map
                .get(curr_txn)
                .unwrap();
            self.active_write.insert(x.clone(), read_by.clone());
        }
        self.active_write.retain(|_, ts| !ts.is_empty());
    }

    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        let curr_txn = linearization.last().unwrap();
        let curr_txn_info = self.history.history.0.get(curr_txn).unwrap();
        for x in &curr_txn_info.writes {
            self.active_write.remove(x);
        }
        for x in curr_txn_info.reads.keys() {
            self.active_write
                .entry(x.clone())
                .or_default()
                .insert(*curr_txn);
        }
    }

    fn children_of(&self, u: &Self::Vertex) -> Option<Vec<Self::Vertex>> {
        self.history
            .visibility_relation
            .adj_map
            .get(u)
            .map(|vs| vs.iter().copied().collect())
    }

    fn allow_next(&self, _linearization: &[Self::Vertex], v: &Self::Vertex) -> bool {
        let curr_txn_info = self.history.history.0.get(v).unwrap();
        curr_txn_info
            .writes
            .iter()
            .all(|x| match self.active_write.get(x) {
                Some(ts) if ts.len() == 1 => ts.iter().next().unwrap() == v,
                None => true,
                _ => false,
            })
    }

    fn vertices(&self) -> Vec<Self::Vertex> {
        self.history.history.0.keys().copied().collect()
    }
}
