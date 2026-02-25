//! Snapshot Isolation linearization solver.
//!
//! Snapshot Isolation strengthens Prefix Consistency by additionally
//! requiring write-write conflict freedom: two concurrent transactions
//! must not write to the same variable. Each transaction reads from a
//! consistent snapshot and commits atomically, but concurrent writers
//! on the same variable are forbidden.
//!
//! # Approach
//!
//! This module implements a [`ConstrainedLinearizationSolver`] that
//! searches for a valid split commit order via depth-first search with
//! backtracking. Like the Prefix solver, each transaction is split into
//! two vertices:
//!
//! - `(TransactionId, false)` -- the *read phase*.
//! - `(TransactionId, true)` -- the *write phase*.
//!
//! In addition to the `active_write` constraint from Prefix Consistency,
//! the solver tracks `active_variable` -- the set of variables that have
//! been written by a transaction whose write phase has been placed but
//! whose readers have not all been placed yet. A read phase is only
//! allowed if none of its write variables overlap with `active_variable`,
//! enforcing the no-write-write-conflict property.
//!
//! # Data flow
//!
//! ```text
//! AtomicTransactionPO (from causal check)
//!     -> SnapshotIsolationSolver -> get_linearization() via DFS
//!     -> Some(Vec<(TransactionId, bool)>) or None
//!     -> Witness::SplitCommitOrder
//! ```
//!
//! # Witness
//!
//! On success, the full split linearization is returned as
//! `Witness::SplitCommitOrder(Vec<(TransactionId, bool)>)`, preserving
//! the read/write phase distinction.
//!
//! # Reference
//!
//! Implements the constrained linearization search described in
//! Theorem 4.10 of Biswas and Enea (2019).

use alloc::vec::Vec;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

use crate::consistency::constrained_linearization::{
    seeded_hash_u128, BranchOrdering, ConstrainedLinearizationSolver, DfsSearchOptions,
    DominancePruning, NogoodLearning,
};
use crate::history::atomic::types::TransactionId;
use crate::history::atomic::AtomicTransactionPO;

/// Linearization solver for Snapshot Isolation.
///
/// Wraps an [`AtomicTransactionPO`] and tracks two constraints:
///
/// - `active_write` -- same as [`PrefixConsistencySolver`]: maps each
///   variable to the set of transactions with unresolved readers.
/// - `active_variable` -- the set of variables currently "locked" by a
///   write phase that has been committed but whose readers are still
///   outstanding. A new read phase is blocked if its write set overlaps
///   with `active_variable`.
///
/// [`PrefixConsistencySolver`]: super::prefix::PrefixConsistencySolver
#[derive(Debug)]
pub struct SnapshotIsolationSolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    pub history: AtomicTransactionPO<Variable>,
    pub active_write: HashMap<Variable, HashSet<TransactionId>>,
    pub active_variable: HashSet<Variable>,
}

impl<Variable> From<AtomicTransactionPO<Variable>> for SnapshotIsolationSolver<Variable>
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
            active_variable: HashSet::default(),
        }
    }
}

impl<Variable> ConstrainedLinearizationSolver for SnapshotIsolationSolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    type Vertex = (TransactionId, bool);

    fn search_options(&self) -> DfsSearchOptions {
        DfsSearchOptions {
            memoize_frontier: true,
            nogood_learning: NogoodLearning::Enabled,
            enable_killer_history: true,
            dominance_pruning: DominancePruning::Enabled,
            prefer_allowed_first: true,
            branch_ordering: BranchOrdering::HighScoreFirst,
        }
    }

    fn branch_score(&self, _linearization: &[Self::Vertex], v: &Self::Vertex) -> i64 {
        let txn_info = self.history.history.0.get(&v.0).unwrap();
        let child_count = self.children_of(v).map_or(0, |children| children.len());
        let child_score = i64::try_from(child_count).expect("child count fits i64");
        let unresolved_readers = txn_info
            .reads
            .keys()
            .filter(|x| {
                self.active_write
                    .get(*x)
                    .is_some_and(|ts| ts.contains(&v.0))
            })
            .count();
        let unresolved_readers_score =
            i64::try_from(unresolved_readers).expect("unresolved reader count fits i64");
        let write_release_count = txn_info.writes.intersection(&self.active_variable).count();
        let write_release_score =
            i64::try_from(write_release_count).expect("write release count fits i64");
        let write_set_count = txn_info.writes.len();
        let write_set_score = i64::try_from(write_set_count).expect("write set count fits i64");

        if v.1 {
            (write_release_score * 8) + (child_score * 2) + write_set_score + 2
        } else {
            (unresolved_readers_score * 8) + (child_score * 2) + (write_set_score * 2)
        }
    }

    fn frontier_signature(&self, frontier_hash: u128, _linearization: &[Self::Vertex]) -> u128 {
        let mut signature = frontier_hash;
        for (var, readers) in &self.active_write {
            let mut readers_mix = 0_u128;
            for reader in readers {
                readers_mix ^= seeded_hash_u128(0x601, reader);
            }
            let var_mix = seeded_hash_u128(0x602, var);
            let reader_count = u64::try_from(readers.len()).expect("reader count fits u64");
            let count_mix = seeded_hash_u128(0x603, &reader_count);
            signature ^= var_mix ^ readers_mix ^ count_mix;
        }
        for var in &self.active_variable {
            signature ^= seeded_hash_u128(0x604, var);
        }
        signature
    }

    fn get_root(&self) -> Self::Vertex {
        // Transaction is partitioned into read and write section
        // (TransactionId, false): read section
        // (TransactionId, true): write section
        (TransactionId::default(), false)
    }

    fn children_of(&self, u: &Self::Vertex) -> Option<Vec<Self::Vertex>> {
        if u.1 {
            self.history
                .visibility_relation
                .adj_map
                .get(&u.0)
                .map(|vs| vs.iter().copied().map(|v| (v, false)).collect())
        } else {
            Some([(u.0, true)].into())
        }
    }

    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        let curr_txn = linearization.last().unwrap();
        let curr_txn_info = self.history.history.0.get(&curr_txn.0).unwrap();
        if curr_txn.1 {
            for x in &curr_txn_info.writes {
                let read_by = self
                    .history
                    .write_read_relation
                    .get(x)
                    .unwrap()
                    .adj_map
                    .get(&curr_txn.0)
                    .unwrap();
                self.active_write.insert(x.clone(), read_by.clone());
            }

            self.active_variable = self
                .active_variable
                .difference(&curr_txn_info.writes)
                .cloned()
                .collect();
        } else {
            for x in curr_txn_info.reads.keys() {
                assert!(self
                    .active_write
                    .entry(x.clone())
                    .or_default()
                    .remove(&curr_txn.0));
            }
            self.active_write.retain(|_, ts| !ts.is_empty());

            self.active_variable = self
                .active_variable
                .union(&curr_txn_info.writes)
                .cloned()
                .collect();
        }
    }

    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        let curr_txn = linearization.last().unwrap();
        let curr_txn_info = self.history.history.0.get(&curr_txn.0).unwrap();
        if curr_txn.1 {
            for x in &curr_txn_info.writes {
                self.active_write.remove(x);
            }
            self.active_variable = self
                .active_variable
                .union(&curr_txn_info.writes)
                .cloned()
                .collect();
        } else {
            for x in curr_txn_info.reads.keys() {
                self.active_write
                    .entry(x.clone())
                    .or_default()
                    .insert(curr_txn.0);
            }
            self.active_variable = self
                .active_variable
                .difference(&curr_txn_info.writes)
                .cloned()
                .collect();
        }
    }

    fn allow_next(&self, _linearization: &[Self::Vertex], v: &Self::Vertex) -> bool {
        if v.1 {
            let curr_txn_info = self.history.history.0.get(&v.0).unwrap();
            curr_txn_info
                .writes
                .iter()
                .all(|x| match self.active_write.get(x) {
                    Some(ts) if ts.len() == 1 => ts.iter().next().unwrap() == &v.0,
                    None => true,
                    _ => false,
                })
        } else {
            self.active_variable
                .intersection(&self.history.history.0.get(&v.0).unwrap().writes)
                .next()
                .is_none()
        }
    }

    fn vertices(&self) -> Vec<Self::Vertex> {
        self.history
            .history
            .0
            .keys()
            .flat_map(|u| [(*u, false), (*u, true)])
            .collect()
    }
}
