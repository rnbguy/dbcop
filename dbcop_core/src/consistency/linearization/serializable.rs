use alloc::vec::Vec;
use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

use crate::history::atomic::types::TransactionId;
use crate::history::atomic::AtomicTransactionPO;
use crate::consistency::constrained_linearization::ConstrainedLinearizationSolver;

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
        Self {
            history,
            active_write: HashMap::default(),
        }
    }
}

impl<Variable> ConstrainedLinearizationSolver for SerializabilitySolver<Variable>
where
    Variable: Clone + Eq + Ord + Hash,
{
    type Vertex = TransactionId;
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
