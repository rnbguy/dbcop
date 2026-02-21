pub mod types;

use alloc::vec::Vec;
use core::default::Default;
use core::hash::Hash;

use hashbrown::HashMap;

use crate::graph::digraph::DiGraph;
use crate::history::atomic::types::{AtomicTransactionHistory, TransactionId};

/// Partial order over transactions derived from an atomic history.
///
/// Built from an [`AtomicTransactionHistory`] via `From`, this struct holds
/// every relation needed by the saturation and linearization checkers.
/// The session order and write-read relations are fixed at construction;
/// the visibility relation is grown by checkers during saturation.
#[derive(Debug)]
pub struct AtomicTransactionPO<Variable>
where
    Variable: Clone + Eq + Hash,
{
    /// The synthetic root transaction `(0, 0)`.
    pub root: TransactionId,
    /// Per-transaction read-set and write-set.
    pub history: AtomicTransactionHistory<Variable>,
    /// Transitive closure of the per-session chain order.
    /// Includes edges from the root to every transaction.
    pub session_order: DiGraph<TransactionId>,
    /// Per-variable write-read graphs: an edge `(w, r)` in `write_read_relation[x]`
    /// means transaction `r` read variable `x` from transaction `w`.
    pub write_read_relation: HashMap<Variable, DiGraph<TransactionId>>,
    /// Union of all per-variable write-read graphs.
    pub wr_union: DiGraph<TransactionId>,
    /// Visibility relation, initialized to the session order and extended
    /// by saturation checkers. An edge `(a, b)` means transaction `a` is
    /// visible to transaction `b`.
    pub visibility_relation: DiGraph<TransactionId>,
}

impl<Variable> From<AtomicTransactionHistory<Variable>> for AtomicTransactionPO<Variable>
where
    Variable: Clone + Eq + Hash,
{
    fn from(history: AtomicTransactionHistory<Variable>) -> Self {
        let root = TransactionId::default();
        let mut session_order: DiGraph<TransactionId> = DiGraph::default();

        {
            // Compute the session-order transitive closure specialized for chain
            // topology. Each session is a chain: root -> t_0 -> t_1 -> ... -> t_k.
            //
            //              ┌────────┐  ┌────────┐
            //        ┌────▶│ (1, 0) │─▶│ (1, 1) │─▶...
            //        │     └────────┘  └────────┘
            // ┌──────┴─┐   ┌────────┐  ┌────────┐
            // │ (0, 0) ├──▶│ (2, 0) │─▶│ (2, 1) │─▶...
            // └──────┬─┘   └────────┘  └────────┘
            //        │     ┌────────┐  ┌────────┐
            //        └────▶│ (3, 0) │─▶│ (3, 1) │─▶...
            //              └────────┘  └────────┘
            //
            // The transitive closure of a chain is all pairs (i, j) where i < j,
            // computed in O(S * T^2) instead of the general O(V * (V + E)) closure.
            let mut by_session: HashMap<u64, Vec<TransactionId>> = HashMap::default();
            for &txn_id in history.0.keys() {
                by_session
                    .entry(txn_id.session_id)
                    .or_default()
                    .push(txn_id);
            }

            for txns in by_session.values_mut() {
                txns.sort_unstable_by_key(|t| t.session_height);
                for (i, &txn) in txns.iter().enumerate() {
                    // root connects to every transaction in the session
                    session_order.add_edge(root, txn);
                    // every earlier transaction in the session connects to this one
                    for &earlier in &txns[..i] {
                        session_order.add_edge(earlier, txn);
                    }
                }
            }
        }

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

        let mut wr_union = DiGraph::default();
        for g in write_read_relation.values() {
            wr_union.union(g);
        }

        Self {
            root,
            history,
            write_read_relation,
            wr_union,
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
        self.wr_union.clone()
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
                    if t1 != t2 {
                        // Pre-fetch visibility neighbors of t2 to avoid repeated HashMap lookups
                        let vis_neighbors_t2 = self.visibility_relation.adj_map.get(t2);
                        if vis_neighbors_t2.is_some_and(|neighbors| neighbors.contains(t1))
                            || t3s.iter().any(|t3| {
                                t3 != t2
                                    && vis_neighbors_t2
                                        .is_some_and(|neighbors| neighbors.contains(t3))
                            })
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
                // Pre-fetch visibility neighbors of t1 to avoid repeated HashMap lookups
                let vis_neighbors_t1 = self.visibility_relation.adj_map.get(t1);
                for (t2, _) in &wr_x.adj_map {
                    // t1 and t2 both writes on x
                    if t1 != t2 {
                        if vis_neighbors_t1.is_some_and(|neighbors| neighbors.contains(t2)) {
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
                                // Pre-fetch visibility neighbors of t3 to avoid repeated HashMap lookups
                                let vis_neighbors_t3 = self.visibility_relation.adj_map.get(t3);
                                if vis_neighbors_t3.is_some_and(|neighbors| neighbors.contains(t2))
                                {
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

    #[must_use]
    pub fn has_valid_visibility(&self) -> bool {
        self.visibility_relation.is_acyclic()
    }
}
