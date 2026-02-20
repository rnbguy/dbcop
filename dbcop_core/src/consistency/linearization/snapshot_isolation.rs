use alloc::vec::Vec;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

use crate::consistency::constrained_linearization::ConstrainedLinearizationSolver;
use crate::history::atomic::types::TransactionId;
use crate::history::atomic::AtomicTransactionPO;

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
        Self {
            history,
            active_write: HashMap::default(),
            active_variable: HashSet::default(),
        }
    }
}

impl<Variable> ConstrainedLinearizationSolver for SnapshotIsolationSolver<Variable>
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
