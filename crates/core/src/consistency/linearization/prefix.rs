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

use crate::consistency::constrained_linearization::ConstrainedLinearizationSolver;
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
        Self {
            history,
            active_write: HashMap::default(),
        }
    }
}

impl<Variable> ConstrainedLinearizationSolver for PrefixConsistencySolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    type Vertex = (TransactionId, bool);
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
