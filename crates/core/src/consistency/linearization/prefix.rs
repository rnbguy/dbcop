//! Prefix Consistency linearization solver.
//!
//! Prefix Consistency strengthens Causal Consistency by requiring a total
//! commit order on all transactions such that every transaction's visible
//! set is a prefix of this order. In other words, if T1 precedes T2 in
//! commit order, then everything visible to T1 is also visible to T2.
//!
//! # Approach
//!
//! This module implements a [`ConstrainedLinearizationSolver`] that
//! searches for a valid commit order via depth-first search with
//! backtracking. Each transaction is split into two vertices:
//!
//! - `(TransactionId, false)` -- the *read phase*, representing the point
//!   at which the transaction's snapshot is taken.
//! - `(TransactionId, true)` -- the *write phase*, representing the point
//!   at which the transaction's writes become visible.
//!
//! The solver enforces that when a write phase is placed in the
//! linearization, no other transaction has outstanding readers of the
//! same variable (the `active_write` constraint). This ensures the
//! prefix property: every reader has already observed this write or a
//! later one.
//!
//! # Data flow
//!
//! ```text
//! AtomicTransactionPO (from causal check)
//!     -> PrefixConsistencySolver -> get_linearization() via DFS
//!     -> Some(Vec<(TransactionId, bool)>) or None
//!     -> filter write-phase vertices -> Witness::CommitOrder
//! ```
//!
//! # Witness
//!
//! On success, the caller extracts only the write-phase vertices from the
//! linearization to produce a `Witness::CommitOrder(Vec<TransactionId>)`.
//!
//! # Reference
//!
//! Implements the constrained linearization search described in
//! Theorem 4.8 of Biswas and Enea (2019).

use alloc::vec::Vec;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

use crate::consistency::constrained_linearization::{
    seeded_hash_u128, BranchOrdering, ConstrainedLinearizationSolver, DfsSearchOptions,
};
use crate::history::atomic::types::TransactionId;
use crate::history::atomic::AtomicTransactionPO;

/// Linearization solver for Prefix Consistency.
///
/// Wraps an [`AtomicTransactionPO`] and tracks `active_write` -- a map
/// from each variable to the set of transactions that still have
/// unresolved readers for that variable's current write. A write phase
/// is only allowed when all of its variables have at most one active
/// writer (itself).
#[derive(Debug)]
pub struct PrefixConsistencySolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    pub history: AtomicTransactionPO<Variable>,
    pub active_write: HashMap<Variable, HashSet<TransactionId>>,
}

impl<Variable> From<AtomicTransactionPO<Variable>> for PrefixConsistencySolver<Variable>
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
impl<Variable> ConstrainedLinearizationSolver for PrefixConsistencySolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    type Vertex = (TransactionId, bool);

    fn search_options(&self) -> DfsSearchOptions {
        DfsSearchOptions {
            memoize_frontier: true,
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
        let write_release_count = txn_info
            .writes
            .iter()
            .filter(|x| {
                self.active_write
                    .get(*x)
                    .is_some_and(|ts| ts.len() == 1 && ts.contains(&v.0))
            })
            .count();
        let write_release_score =
            i64::try_from(write_release_count).expect("write release count fits i64");
        let write_bias = i64::from(u8::from(v.1)) * 2;
        (unresolved_readers_score * 8) + (write_release_score * 4) + (child_score * 2) + write_bias
    }

    fn frontier_signature(&self, frontier_hash: u128, _linearization: &[Self::Vertex]) -> u128 {
        let mut signature = frontier_hash;
        for (var, readers) in &self.active_write {
            let mut readers_mix = 0_u128;
            for reader in readers {
                readers_mix ^= seeded_hash_u128(0x501, reader);
            }
            let var_mix = seeded_hash_u128(0x502, var);
            let reader_count = u64::try_from(readers.len()).expect("reader count fits u64");
            let count_mix = seeded_hash_u128(0x503, &reader_count);
            signature ^= var_mix ^ readers_mix ^ count_mix;
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
        } else {
            for x in curr_txn_info.reads.keys() {
                assert!(self
                    .active_write
                    .entry(x.clone())
                    .or_default()
                    .remove(&curr_txn.0));
            }
        }
        self.active_write.retain(|_, ts| !ts.is_empty());
    }

    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]) {
        let curr_txn = linearization.last().unwrap();
        let curr_txn_info = self.history.history.0.get(&curr_txn.0).unwrap();
        if curr_txn.1 {
            for x in &curr_txn_info.writes {
                self.active_write.remove(x);
            }
        } else {
            for x in curr_txn_info.reads.keys() {
                self.active_write
                    .entry(x.clone())
                    .or_default()
                    .insert(curr_txn.0);
            }
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
            true
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

#[cfg(test)]
mod tests {
    use alloc::vec;

    use super::*;
    use crate::consistency::saturation::causal::check_causal_read;
    use crate::history::raw::types::{Event, Session, Transaction};

    type History = Vec<Session<&'static str, u64>>;

    fn build_prefix_solver(history: &History) -> PrefixConsistencySolver<&'static str> {
        let po = check_causal_read(history).unwrap();
        PrefixConsistencySolver::from(po)
    }

    #[test]
    fn simple_prefix_consistent_history() {
        // s1: write(x, 1)
        // s2: read(x, 1)
        // This is trivially prefix-consistent.
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
        ];
        let mut solver = build_prefix_solver(&history);
        let result = solver.get_linearization();
        assert!(result.is_some(), "expected a valid linearization");
        let lin = result.unwrap();
        // Should contain both read and write phases for each transaction
        assert!(!lin.is_empty());
        // Filter to write-phase vertices only
        assert_eq!(
            lin.iter().filter(|(_, is_write)| *is_write).count(),
            2,
            "expected 2 write-phase entries",
        );
    }

    #[test]
    fn prefix_consistent_chain() {
        // s1: write(x, 1)
        // s2: read(x, 1), write(y, 1)
        // s3: read(y, 1)
        // Linear chain is prefix-consistent.
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![Event::read("y", 1)])],
        ];
        let mut solver = build_prefix_solver(&history);
        let result = solver.get_linearization();
        assert!(result.is_some(), "chain should be prefix-consistent");
    }

    #[test]
    fn concurrent_writes_pass_prefix() {
        // Concurrent writes to the same variable actually pass prefix consistency
        // (they fail SI/SER but prefix is weaker).
        // s1: write(x, 1)
        // s2: read(x, 1), write(x, 2)
        // s3: read(x, 1), write(x, 3)
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("x", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("x", 3),
            ])],
        ];
        let mut solver = build_prefix_solver(&history);
        let result = solver.get_linearization();
        assert!(
            result.is_some(),
            "concurrent writes should pass prefix consistency"
        );
        let lin = result.unwrap();
        // Write-phase vertices for all 3 transactions should be present
        assert_eq!(
            lin.iter().filter(|(_, is_write)| *is_write).count(),
            3,
            "expected 3 write-phase entries",
        );
    }

    #[test]
    fn multi_variable_prefix_consistent() {
        // Multiple variables, each with independent writer-reader pairs.
        // s1: write(x, 1), write(y, 1)
        // s2: read(x, 1), write(z, 1)
        // s3: read(y, 1), read(z, 1)
        // All reads are from valid committed writes; should be prefix-consistent.
        let history: History = vec![
            vec![Transaction::committed(vec![
                Event::write("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("z", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("y", 1),
                Event::read("z", 1),
            ])],
        ];
        let mut solver = build_prefix_solver(&history);
        let result = solver.get_linearization();
        assert!(
            result.is_some(),
            "multi-variable chain should be prefix-consistent"
        );
        let lin = result.unwrap();
        assert_eq!(
            lin.iter().filter(|(_, is_write)| *is_write).count(),
            3,
            "expected 3 write-phase entries",
        );
    }

    #[test]
    fn allow_next_permits_read_phase() {
        // allow_next should always return true for read phases (is_write=false)
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
        ];
        let solver = build_prefix_solver(&history);
        let t1 = TransactionId {
            session_id: 1,
            session_height: 0,
        };
        // Read phase should always be allowed
        assert!(solver.allow_next(&[], &(t1, false)));
    }

    #[test]
    fn allow_next_write_phase_with_no_active_writes() {
        // When active_write is empty, write phase should be allowed
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
        ];
        let mut solver = build_prefix_solver(&history);
        // Clear active_write to simulate state where no readers remain
        solver.active_write.clear();
        let t1 = TransactionId {
            session_id: 1,
            session_height: 0,
        };
        assert!(
            solver.allow_next(&[], &(t1, true)),
            "write phase should be allowed with no active writes"
        );
    }

    #[test]
    fn vertices_produces_read_and_write_phases() {
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
        ];
        let solver = build_prefix_solver(&history);
        let verts = solver.vertices();
        // Each transaction gets 2 vertices (read phase + write phase)
        // 2 transactions = 4 vertices
        assert_eq!(verts.len(), 4, "expected 4 vertices (2 per transaction)");
        // Check that both phases exist for each transaction
        let t1 = TransactionId {
            session_id: 1,
            session_height: 0,
        };
        assert!(verts.contains(&(t1, false)));
        assert!(verts.contains(&(t1, true)));
    }
}
