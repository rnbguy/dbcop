pub mod types;

use alloc::vec::Vec;
use core::default::Default;
use core::hash::Hash;

use hashbrown::HashMap;

use crate::graph::digraph::DiGraph;
use crate::history::atomic::types::{AtomicTransactionHistory, TransactionId};

#[derive(Debug)]
pub struct AtomicTransactionPO<Variable>
where
    Variable: Clone + Eq + Hash,
{
    pub root: TransactionId,
    pub history: AtomicTransactionHistory<Variable>,
    pub session_order: DiGraph<TransactionId>,
    pub write_read_relation: HashMap<Variable, DiGraph<TransactionId>>,
    pub visibility_relation: DiGraph<TransactionId>,
}

impl<Variable> From<AtomicTransactionHistory<Variable>> for AtomicTransactionPO<Variable>
where
    Variable: Clone + Eq + Hash,
{
    fn from(history: AtomicTransactionHistory<Variable>) -> Self {
        let mut session_order: DiGraph<TransactionId> = DiGraph::default();

        {
            let mut transactions: Vec<_> = history.0.keys().copied().collect();
            transactions.sort_unstable();

            // TODO(rano): this is bit hacky
            // here we try to create the session order without knowing the number of sessions
            //             ┌────────┐  ┌────────┐
            //       ┌────>│ (1, 0) ├─>│ (1, 1) ├─>...
            //       │     └────────┘  └────────┘
            // ┌─────┴──┐  ┌────────┐  ┌────────┐
            // │ (0, 0) ├─>│ (2, 0) ├─>│ (2, 1) ├─>...
            // └─────┬──┘  └────────┘  └────────┘
            //       │     ┌────────┐  ┌────────┐
            //       └────>│ (3, 0) ├─>│ (3, 1) ├─>...
            //             └────────┘  └────────┘
            for pair in transactions.windows(2) {
                let [t1, t2] = pair else {
                    unreachable!("windows should have at least 2 elements")
                };
                if t1.session_id == t2.session_id {
                    session_order.add_edge(*t1, *t2);
                } else {
                    session_order.add_edge(TransactionId::default(), *t2);
                };
            }
        }

        // takes closure of the session order
        // if SO(A, B) and SO(B, C) then SO*(A, C)
        session_order = session_order.closure();

        // This creates a wr_x relation for each variable x
        // This is also used as a transaction history indexed by variable
        let mut write_read_relation: HashMap<Variable, DiGraph<TransactionId>> = HashMap::default();

        for (txn_id, txn_info) in &history.0 {
            for variable in &txn_info.writes {
                write_read_relation
                    .entry(variable.clone())
                    .or_default()
                    .add_vertex(*txn_id);
            }
            for (variable, txn_id2) in &txn_info.reads {
                write_read_relation
                    .entry(variable.clone())
                    .or_default()
                    .add_edge(*txn_id2, *txn_id);
            }
        }

        Self {
            root: TransactionId::default(),
            history,
            write_read_relation,
            visibility_relation: session_order.clone(),
            session_order,
        }
    }
}

impl<Variable> AtomicTransactionPO<Variable>
where
    Variable: Clone + Eq + Hash,
{
    /// Returns the union of the write-read relation of all variables
    #[must_use]
    pub fn get_wr(&self) -> DiGraph<TransactionId> {
        let mut wr: DiGraph<TransactionId> = DiGraph::default();

        for (_, wr_x) in &self.write_read_relation {
            wr.union(wr_x);
        }

        wr
    }

    /// Takes the union of the visibility relation and the given graph
    /// and returns true if the relation has changed
    pub fn vis_includes(&mut self, g: &DiGraph<TransactionId>) -> bool {
        self.visibility_relation.union(g)
    }

    /// Takes the transitive closure of the visibility relation
    /// and returns true if the relation has changed
    ///
    /// # Panics
    ///
    /// The `expect` never panics
    pub fn vis_is_trans(&mut self) -> bool {
        let closure = self.visibility_relation.closure();
        let change = self.visibility_relation.adj_map.iter().any(|(k, v)| {
            closure
                .adj_map
                .get(k)
                .expect("closure map should have it")
                .difference(v)
                .count()
                > 0
        });
        self.visibility_relation = closure;
        change
    }

    pub fn causal_ww(&mut self) -> HashMap<Variable, DiGraph<TransactionId>> {
        let mut ww: HashMap<Variable, DiGraph<TransactionId>> = HashMap::default();

        for (x, wr_x) in &self.write_read_relation {
            let mut ww_x: DiGraph<TransactionId> = DiGraph::default();
            for (t1, t3s) in &wr_x.adj_map {
                // t3s reads x from t1
                // !t3s.contains(t1) - otherwise, it's a cycle in wr_x
                for (t2, _) in &wr_x.adj_map {
                    // t1 and t2 both writes on x
                    if t1 != t2
                        && (self.visibility_relation.has_edge(t2, t1)
                            || t3s
                                .iter()
                                .any(|t3| t3 != t2 && self.visibility_relation.has_edge(t2, t3)))
                    {
                        // it is obvious that vis(t2, t1) implies ww(t2, t1),
                        // in other case, if vis(t2, t3), then t2 overwrites t1's write, read by t3
                        // ┌──── t2 ─────┐
                        // │ww_x      vis│
                        // V             V
                        // t1───────────>t3
                        //      wr_x
                        // t3 != t2 check is skipped, as acyclic vis(t3, t2) implies t3 != t2
                        ww_x.add_edge(*t2, *t1);
                    }
                }
            }
            ww.insert(x.clone(), ww_x);
        }

        ww
    }

    pub fn causal_rw(&mut self) -> HashMap<Variable, DiGraph<TransactionId>> {
        let mut rw: HashMap<Variable, DiGraph<TransactionId>> = HashMap::default();

        for (x, wr_x) in &self.write_read_relation {
            let mut rw_x: DiGraph<TransactionId> = DiGraph::default();
            for (t1, t3s) in &wr_x.adj_map {
                // t3s reads x from t1
                // !t3s.contains(t1) - otherwise, it's a cycle in wr_x
                for (t2, _) in &wr_x.adj_map {
                    // t1 and t2 both writes on x
                    if t1 != t2 {
                        if self.visibility_relation.has_edge(t1, t2) {
                            for t3 in t3s {
                                if t3 != t2 {
                                    // if vis(t1, t2) and t2 != t3, then t2 overwrites the version read by t3
                                    // ┌───> t2 <────┐
                                    // │vis      rw_x│
                                    // │             │
                                    // t1───────────>t3
                                    //      wr_x
                                    rw_x.add_edge(*t3, *t2);
                                }
                            }
                        } else {
                            for t3 in t3s {
                                if self.visibility_relation.has_edge(t3, t2) {
                                    // it is obvious that vis(t3, t2) implies rw(t3, t2)
                                    // t3 != t2 check is skipped, as acyclic vis(t3, t2) implies t3 != t2
                                    rw_x.add_edge(*t3, *t2);
                                }
                            }
                        }
                    }
                }
            }
            rw.insert(x.clone(), rw_x);
        }

        rw
    }

    #[must_use] pub fn has_valid_visibility(&self) -> bool {
        self.visibility_relation.is_acyclic()
    }
}
