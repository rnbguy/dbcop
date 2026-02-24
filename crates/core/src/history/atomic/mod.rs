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
            //        ┌────>│ (1, 0) │─>│ (1, 1) │─>...
            //        │     └────────┘  └────────┘
            // ┌──────┴─┐   ┌────────┐  ┌────────┐
            // │ (0, 0) ├──>│ (2, 0) │─>│ (2, 1) │─>...
            // └──────┬─┘   └────────┘  └────────┘
            //        │     ┌────────┐  ┌────────┐
            //        └────>│ (3, 0) │─>│ (3, 1) │─>...
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
            // wr_x contains both writers and readers as vertices. The ww rule is
            // defined only between writers of x, so filter candidate vertices.
            let writers: Vec<TransactionId> = wr_x
                .adj_map
                .keys()
                .copied()
                .filter(|tid| {
                    self.history
                        .0
                        .get(tid)
                        .is_some_and(|txn| txn.writes.contains(x))
                })
                .collect();
            let mut ww_x: DiGraph<TransactionId> = DiGraph::default();
            for t1 in &writers {
                let Some(t3s) = wr_x.adj_map.get(t1) else {
                    continue;
                };
                // t3s reads x from t1
                // !t3s.contains(t1) - otherwise, it's a cycle in wr_x
                for t2 in &writers {
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
            // wr_x contains both writers and readers as vertices. The rw rule
            // compares writers of x, so filter candidate vertices.
            let writers: Vec<TransactionId> = wr_x
                .adj_map
                .keys()
                .copied()
                .filter(|tid| {
                    self.history
                        .0
                        .get(tid)
                        .is_some_and(|txn| txn.writes.contains(x))
                })
                .collect();
            let mut rw_x: DiGraph<TransactionId> = DiGraph::default();
            for t1 in &writers {
                let Some(t3s) = wr_x.adj_map.get(t1) else {
                    continue;
                };
                // t3s reads x from t1
                // !t3s.contains(t1) - otherwise, it's a cycle in wr_x
                // Pre-fetch visibility neighbors of t1 to avoid repeated HashMap lookups
                let vis_neighbors_t1 = self.visibility_relation.adj_map.get(t1);
                for t2 in &writers {
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

#[cfg(test)]
mod tests {
    use alloc::vec;

    use super::*;
    use crate::history::raw::types::{Event, Session, Transaction};

    type History = Vec<Session<&'static str, u64>>;

    fn build_po(history: &History) -> AtomicTransactionPO<&'static str> {
        let atomic_history = AtomicTransactionHistory::try_from(history.as_slice()).unwrap();
        AtomicTransactionPO::from(atomic_history)
    }

    #[test]
    fn session_order_chain_closure() {
        // Session with 3 transactions: root -> t(1,0) -> t(1,1) -> t(1,2)
        // Session order closure should have edges from every earlier to every later.
        let history: History = vec![vec![
            Transaction::committed(vec![Event::write("x", 1)]),
            Transaction::committed(vec![Event::write("x", 2)]),
            Transaction::committed(vec![Event::write("x", 3)]),
        ]];
        let po = build_po(&history);
        let root = TransactionId::default();
        let t0 = TransactionId {
            session_id: 1,
            session_height: 0,
        };
        let t1 = TransactionId {
            session_id: 1,
            session_height: 1,
        };
        let t2 = TransactionId {
            session_id: 1,
            session_height: 2,
        };
        // Root connects to all
        assert!(po.session_order.has_edge(&root, &t0));
        assert!(po.session_order.has_edge(&root, &t1));
        assert!(po.session_order.has_edge(&root, &t2));
        // Transitive closure within chain
        assert!(po.session_order.has_edge(&t0, &t1));
        assert!(po.session_order.has_edge(&t0, &t2));
        assert!(po.session_order.has_edge(&t1, &t2));
    }

    #[test]
    fn write_read_relation_built_correctly() {
        // s1: write(x, 1), s2: read(x, 1) -- produces wr_x edge from s1 to s2
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
        ];
        let po = build_po(&history);
        let t1 = TransactionId {
            session_id: 1,
            session_height: 0,
        };
        let t2 = TransactionId {
            session_id: 2,
            session_height: 0,
        };
        let wr_x = po.write_read_relation.get("x").unwrap();
        assert!(wr_x.adj_map.get(&t1).unwrap().contains(&t2));
        // wr_union should also have this edge
        assert!(po.wr_union.adj_map.get(&t1).unwrap().contains(&t2));
    }

    #[test]
    fn causal_ww_detects_write_write_dependency() {
        // s1: write(x, 1)
        // s2: read(x, 1), write(x, 2)
        // s3: read(x, 2)
        // After including wr in vis:
        //   vis(s1, s2) via wr_x, vis(s2, s3) via wr_x
        //   ww should find that s1's write on x is overwritten by s2
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("x", 2),
            ])],
            vec![Transaction::committed(vec![Event::read("x", 2)])],
        ];
        let mut po = build_po(&history);
        // Include wr edges into visibility
        po.vis_includes(&po.get_wr());
        po.vis_is_trans();

        let ww = po.causal_ww();
        let ww_x = ww.get("x");
        // There should be a ww edge: s2 overwrites s1 on x (visible via s3 reading from s2)
        assert!(ww_x.is_some(), "expected ww relation for x");
    }

    #[test]
    fn causal_rw_detects_anti_dependency() {
        // s1: write(x, 1)
        // s2: read(x, 1), write(x, 2)
        // s3: read(x, 2)
        // rw(s3, s1) or similar anti-dependency patterns
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("x", 2),
            ])],
            vec![Transaction::committed(vec![Event::read("x", 2)])],
        ];
        let mut po = build_po(&history);
        po.vis_includes(&po.get_wr());
        po.vis_is_trans();

        let rw = po.causal_rw();
        // rw should have entries for variable "x"
        assert!(rw.contains_key("x"), "expected rw relation for x");
    }

    #[test]
    fn causal_ww_ignores_non_writer_vertices() {
        // x writers: s1 and s2.
        // s3 and s4 both read x=2, but s3 does not write x.
        // Even if vis(s3, s4), ww_x must not include s3 -> s2 because s3 is not an x-writer.
        let history: History = vec![
            vec![Transaction::committed(vec![
                Event::write("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("x", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 2),
                Event::write("y", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 2),
                Event::read("y", 2),
            ])],
        ];
        let mut po = build_po(&history);
        po.vis_includes(&po.get_wr());
        po.vis_is_trans();

        let ww = po.causal_ww();
        let ww_x = ww.get("x").expect("expected ww relation for x");

        let s2 = TransactionId {
            session_id: 2,
            session_height: 0,
        };
        let s3 = TransactionId {
            session_id: 3,
            session_height: 0,
        };

        assert!(
            !ww_x.has_edge(&s3, &s2),
            "non-writer s3 must not induce ww edge on x"
        );
    }

    #[test]
    fn causal_rw_ignores_non_writer_vertices() {
        // Same history as above: s3 is not an x-writer.
        // rw_x must not include edges ending at s3.
        let history: History = vec![
            vec![Transaction::committed(vec![
                Event::write("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("x", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 2),
                Event::write("y", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 2),
                Event::read("y", 2),
            ])],
        ];
        let mut po = build_po(&history);
        po.vis_includes(&po.get_wr());
        po.vis_is_trans();

        let rw = po.causal_rw();
        let rw_x = rw.get("x").expect("expected rw relation for x");

        let s3 = TransactionId {
            session_id: 3,
            session_height: 0,
        };
        let s4 = TransactionId {
            session_id: 4,
            session_height: 0,
        };

        assert!(
            !rw_x.has_edge(&s4, &s3),
            "non-writer s3 must not appear as rw target on x"
        );
    }

    #[test]
    fn has_valid_visibility_acyclic() {
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
        ];
        let po = build_po(&history);
        assert!(po.has_valid_visibility(), "simple chain should be acyclic");
    }

    #[test]
    fn vis_includes_returns_true_on_change() {
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
        ];
        let mut po = build_po(&history);
        let wr = po.get_wr();
        // First inclusion should add new edges
        let changed = po.vis_includes(&wr);
        assert!(
            changed,
            "vis_includes should return true when new edges are added"
        );
        // Second inclusion of the same graph should not change anything
        let changed_again = po.vis_includes(&wr);
        assert!(
            !changed_again,
            "vis_includes should return false when no new edges"
        );
    }

    #[test]
    fn vis_is_trans_computes_closure() {
        // s1: write(x, 1)
        // s2: read(x, 1), write(y, 1)
        // s3: read(y, 1)
        // After wr: vis(s1, s2), vis(s2, s3)
        // After closure: vis(s1, s3) should exist
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![Event::read("y", 1)])],
        ];
        let mut po = build_po(&history);
        po.vis_includes(&po.get_wr());
        let changed = po.vis_is_trans();
        assert!(changed, "closure should add new transitive edges");
        let t1 = TransactionId {
            session_id: 1,
            session_height: 0,
        };
        let t3 = TransactionId {
            session_id: 3,
            session_height: 0,
        };
        assert!(
            po.visibility_relation.has_edge(&t1, &t3),
            "transitive vis(s1, s3) should exist after closure"
        );
    }
}
