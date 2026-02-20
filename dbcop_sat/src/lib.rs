//! SAT-based consistency checkers using rustsat + rustsat-batsat.
//!
//! Provides alternative solvers for Prefix, Serializable, and Snapshot Isolation
//! that encode the linearization constraints as a SAT problem instead of DFS
//! backtracking.
//!
// TODO: explore QF_IDL (Integer Difference Logic) encoding -- ordering
//   constraints are naturally `pos(i) - pos(j) < 0`, avoiding O(n^3)
//   transitivity clauses.
// TODO: explore QF_LIA (Linear Integer Arithmetic) encoding -- assign integer
//   position variables directly with `pos(i) < pos(j)` constraints for a more
//   compact representation.

use std::collections::HashMap;
use std::hash::Hash;

use dbcop_core::consistency::error::Error;
use dbcop_core::consistency::saturation::causal::check_causal_read;
use dbcop_core::history::atomic::types::TransactionId;
use dbcop_core::history::atomic::AtomicTransactionPO;
use dbcop_core::history::raw::types::Session;
use dbcop_core::Consistency;
use rustsat::solvers::{Solve, SolverResult};
use rustsat::types::Lit;
use rustsat_batsat::BasicSolver;

/// Map from vertex pairs to SAT variable indices.
///
/// For each ordered pair `(i, j)`, `before(i, j)` is true iff vertex `i`
/// is placed before vertex `j` in the linearization.
struct OrderVars<V> {
    vars: HashMap<(V, V), u32>,
    next_var: u32,
}

impl<V: Eq + Hash + Copy> OrderVars<V> {
    fn new() -> Self {
        Self {
            vars: HashMap::new(),
            next_var: 0,
        }
    }

    fn get_or_create(&mut self, a: V, b: V) -> u32 {
        *self.vars.entry((a, b)).or_insert_with(|| {
            let v = self.next_var;
            self.next_var += 1;
            v
        })
    }

    /// `before(a, b)` as a positive literal.
    fn before_lit(&mut self, a: V, b: V) -> Lit {
        Lit::positive(self.get_or_create(a, b))
    }

    /// `NOT before(a, b)` as a negative literal.
    fn not_before_lit(&mut self, a: V, b: V) -> Lit {
        Lit::negative(self.get_or_create(a, b))
    }
}

/// Encode common ordering constraints for a set of vertices with a partial order.
///
/// Returns the solver and order variables, or None if the constraints are
/// trivially unsatisfiable.
fn encode_ordering<V: Eq + Hash + Copy + Ord>(
    vertices: &[V],
    edges: &[(V, V)],
) -> (BasicSolver, OrderVars<V>) {
    let mut solver = BasicSolver::default();
    let mut vars = OrderVars::new();

    // For each pair, create the `before` variable and enforce antisymmetry:
    //   before(i,j) XOR before(j,i) -- exactly one must hold (total order)
    //   i.e. before(i,j) OR before(j,i)    (at least one)
    //   AND  NOT before(i,j) OR NOT before(j,i)  (at most one)
    for (idx_a, &a) in vertices.iter().enumerate() {
        for &b in &vertices[idx_a + 1..] {
            let ab = vars.before_lit(a, b);
            let ba = vars.before_lit(b, a);
            let nab = vars.not_before_lit(a, b);
            let nba = vars.not_before_lit(b, a);
            // At least one
            solver.add_clause([ab, ba].into_iter().collect()).unwrap();
            // At most one
            solver.add_clause([nab, nba].into_iter().collect()).unwrap();
        }
    }

    // Transitivity: before(a,b) AND before(b,c) => before(a,c)
    //   i.e. NOT before(a,b) OR NOT before(b,c) OR before(a,c)
    for &a in vertices {
        for &b in vertices {
            if a == b {
                continue;
            }
            for &c in vertices {
                if c == a || c == b {
                    continue;
                }
                let nab = vars.not_before_lit(a, b);
                let nbc = vars.not_before_lit(b, c);
                let ac = vars.before_lit(a, c);
                solver
                    .add_clause([nab, nbc, ac].into_iter().collect())
                    .unwrap();
            }
        }
    }

    // Visibility edges: if vis(a, b), then before(a, b) must hold
    for &(a, b) in edges {
        let ab = vars.before_lit(a, b);
        solver.add_clause(std::iter::once(ab).collect()).unwrap();
    }

    (solver, vars)
}

/// Extract all visibility edges from the PO.
fn visibility_edges<Variable: Eq + Hash + Clone>(
    po: &AtomicTransactionPO<Variable>,
) -> Vec<(TransactionId, TransactionId)> {
    let mut edges = Vec::new();
    for (src, dsts) in &po.visibility_relation.adj_map {
        for dst in dsts {
            edges.push((*src, *dst));
        }
    }
    edges
}

/// Check serializability using SAT.
///
/// # Errors
///
/// Returns an error if the history violates serializability.
pub fn check_serializable<Variable, Version>(
    sessions: &[Session<Variable, Version>],
) -> Result<(), Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone + Ord,
    Version: Eq + Hash + Clone,
{
    let po = check_causal_read(sessions)?;
    check_serializable_from_po(&po)
        .then_some(())
        .ok_or(Error::Invalid(Consistency::Serializable))
}

/// Check serializability from an already-computed partial order.
fn check_serializable_from_po<Variable: Eq + Hash + Clone + Ord>(
    po: &AtomicTransactionPO<Variable>,
) -> bool {
    let vertices: Vec<TransactionId> = po.history.0.keys().copied().collect();
    let edges = visibility_edges(po);

    let (mut solver, mut vars) = encode_ordering(&vertices, &edges);

    // Serializable constraint: for each variable x written by t_w1 and t_w2,
    // if t_w1 comes before t_w2, then all readers of t_w1's write on x
    // must come between t_w1 and t_w2 (i.e., after t_w1 and before t_w2).
    //
    // Formally: before(t_w1, t_w2) => (before(t_w1, t_r) AND before(t_r, t_w2))
    //   for each t_r that reads x from t_w1.
    //
    // As clauses: NOT before(t_w1, t_w2) OR before(t_w1, t_r)
    //             NOT before(t_w1, t_w2) OR before(t_r, t_w2)
    for wr_x in po.write_read_relation.values() {
        // Writers of x are all vertices in wr_x.adj_map
        let writers: Vec<TransactionId> = wr_x.adj_map.keys().copied().collect();

        for &t_w1 in &writers {
            let readers: Vec<TransactionId> = wr_x
                .adj_map
                .get(&t_w1)
                .map(|s| s.iter().copied().collect())
                .unwrap_or_default();

            for &t_w2 in &writers {
                if t_w1 == t_w2 {
                    continue;
                }

                for &t_r in &readers {
                    if t_r == t_w2 {
                        // t_r == t_w2 means t_w2 reads from t_w1, so t_w1 must come before t_w2.
                        // This is already handled by visibility edges.
                        continue;
                    }

                    // NOT before(t_w1, t_w2) OR before(t_w1, t_r)
                    let nw1w2 = vars.not_before_lit(t_w1, t_w2);
                    let w1r = vars.before_lit(t_w1, t_r);
                    solver
                        .add_clause([nw1w2, w1r].into_iter().collect())
                        .unwrap();

                    // NOT before(t_w1, t_w2) OR before(t_r, t_w2)
                    let nw1w2 = vars.not_before_lit(t_w1, t_w2);
                    let rw2 = vars.before_lit(t_r, t_w2);
                    solver
                        .add_clause([nw1w2, rw2].into_iter().collect())
                        .unwrap();
                }
            }
        }
    }

    matches!(solver.solve().unwrap(), SolverResult::Sat)
}

/// Check prefix consistency using SAT.
///
/// # Errors
///
/// Returns an error if the history violates prefix consistency.
pub fn check_prefix<Variable, Version>(
    sessions: &[Session<Variable, Version>],
) -> Result<(), Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone + Ord,
    Version: Eq + Hash + Clone,
{
    let po = check_causal_read(sessions)?;
    check_prefix_from_po(&po)
        .then_some(())
        .ok_or(Error::Invalid(Consistency::Prefix))
}

/// Check prefix consistency from an already-computed partial order.
///
/// Prefix uses split-phase vertices (read then write) like SI, but the
/// read phase has no additional constraint (`allow_next` always true for reads).
fn check_prefix_from_po<Variable: Eq + Hash + Clone + Ord>(
    po: &AtomicTransactionPO<Variable>,
) -> bool {
    let txn_ids: Vec<TransactionId> = po.history.0.keys().copied().collect();

    // Split-phase vertices: (txn_id, false=read, true=write)
    let mut vertices: Vec<(TransactionId, bool)> = Vec::new();
    for &t in &txn_ids {
        vertices.push((t, false));
        vertices.push((t, true));
    }

    // Edges: read phase -> write phase of same txn
    // write phase of t -> read phase of visibility successors
    let mut edges: Vec<((TransactionId, bool), (TransactionId, bool))> = Vec::new();

    for &t in &txn_ids {
        edges.push(((t, false), (t, true)));
    }

    for (src, dsts) in &po.visibility_relation.adj_map {
        for dst in dsts {
            edges.push(((*src, true), (*dst, false)));
        }
    }

    let (mut solver, mut vars) = encode_ordering(&vertices, &edges);

    // Write-phase constraint (same as serializable):
    // For each variable x written by t_w1 and t_w2,
    // if t_w1's write phase comes before t_w2's write phase,
    // then all readers of t_w1's write on x must have their
    // read phase between t_w1's write phase and t_w2's write phase.
    for wr_x in po.write_read_relation.values() {
        let writers: Vec<TransactionId> = wr_x.adj_map.keys().copied().collect();

        for &t_w1 in &writers {
            let readers: Vec<TransactionId> = wr_x
                .adj_map
                .get(&t_w1)
                .map(|s| s.iter().copied().collect())
                .unwrap_or_default();

            for &t_w2 in &writers {
                if t_w1 == t_w2 {
                    continue;
                }

                for &t_r in &readers {
                    if t_r == t_w2 {
                        continue;
                    }

                    // NOT before((t_w1,W), (t_w2,W)) OR before((t_w1,W), (t_r,R))
                    let nw1w2 = vars.not_before_lit((t_w1, true), (t_w2, true));
                    let w1r = vars.before_lit((t_w1, true), (t_r, false));
                    solver
                        .add_clause([nw1w2, w1r].into_iter().collect())
                        .unwrap();

                    // NOT before((t_w1,W), (t_w2,W)) OR before((t_r,R), (t_w2,W))
                    let nw1w2 = vars.not_before_lit((t_w1, true), (t_w2, true));
                    let rw2 = vars.before_lit((t_r, false), (t_w2, true));
                    solver
                        .add_clause([nw1w2, rw2].into_iter().collect())
                        .unwrap();
                }
            }
        }
    }

    matches!(solver.solve().unwrap(), SolverResult::Sat)
}

/// Check snapshot isolation using SAT.
///
/// # Errors
///
/// Returns an error if the history violates snapshot isolation.
pub fn check_snapshot_isolation<Variable, Version>(
    sessions: &[Session<Variable, Version>],
) -> Result<(), Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone + Ord,
    Version: Eq + Hash + Clone,
{
    let po = check_causal_read(sessions)?;
    check_si_from_po(&po)
        .then_some(())
        .ok_or(Error::Invalid(Consistency::SnapshotIsolation))
}

/// Check snapshot isolation from an already-computed partial order.
///
/// SI adds the `active_variable` constraint on top of prefix:
/// When placing the read phase of t, the write set of t must not overlap
/// with the write set of any transaction whose read phase has been placed
/// but whose write phase has not yet been placed.
///
/// Encoded as: for any two transactions t1, t2 with overlapping write sets
/// that are NOT ordered by visibility, their read-write intervals cannot
/// interleave. That is, either t1's write phase comes before t2's read phase,
/// or t2's write phase comes before t1's read phase.
fn check_si_from_po<Variable: Eq + Hash + Clone + Ord>(po: &AtomicTransactionPO<Variable>) -> bool {
    let txn_ids: Vec<TransactionId> = po.history.0.keys().copied().collect();

    let mut vertices: Vec<(TransactionId, bool)> = Vec::new();
    for &t in &txn_ids {
        vertices.push((t, false));
        vertices.push((t, true));
    }

    let mut edges: Vec<((TransactionId, bool), (TransactionId, bool))> = Vec::new();
    for &t in &txn_ids {
        edges.push(((t, false), (t, true)));
    }
    for (src, dsts) in &po.visibility_relation.adj_map {
        for dst in dsts {
            edges.push(((*src, true), (*dst, false)));
        }
    }

    let (mut solver, mut vars) = encode_ordering(&vertices, &edges);

    // Write-phase constraint (same as prefix/serializable)
    for wr_x in po.write_read_relation.values() {
        let writers: Vec<TransactionId> = wr_x.adj_map.keys().copied().collect();

        for &t_w1 in &writers {
            let readers: Vec<TransactionId> = wr_x
                .adj_map
                .get(&t_w1)
                .map(|s| s.iter().copied().collect())
                .unwrap_or_default();

            for &t_w2 in &writers {
                if t_w1 == t_w2 {
                    continue;
                }

                for &t_r in &readers {
                    if t_r == t_w2 {
                        continue;
                    }

                    let nw1w2 = vars.not_before_lit((t_w1, true), (t_w2, true));
                    let w1r = vars.before_lit((t_w1, true), (t_r, false));
                    solver
                        .add_clause([nw1w2, w1r].into_iter().collect())
                        .unwrap();

                    let nw1w2 = vars.not_before_lit((t_w1, true), (t_w2, true));
                    let rw2 = vars.before_lit((t_r, false), (t_w2, true));
                    solver
                        .add_clause([nw1w2, rw2].into_iter().collect())
                        .unwrap();
                }
            }
        }
    }

    // SI additional constraint: for any two transactions t1, t2 with
    // overlapping write sets, their [read, write) intervals must not
    // interleave.
    //
    // If t1 and t2 both write variable x, then either:
    //   before((t1,W), (t2,R))  -- t1 finishes writing before t2 starts reading
    //   OR before((t2,W), (t1,R))  -- t2 finishes writing before t1 starts reading
    //
    // As a clause: before((t1,W), (t2,R)) OR before((t2,W), (t1,R))
    //
    // We collect pairs of transactions with overlapping write sets.
    let mut write_conflict_pairs: Vec<(TransactionId, TransactionId)> = Vec::new();
    for (idx_a, &t1) in txn_ids.iter().enumerate() {
        let info1 = po.history.0.get(&t1).unwrap();
        for &t2 in &txn_ids[idx_a + 1..] {
            let info2 = po.history.0.get(&t2).unwrap();
            if info1.writes.intersection(&info2.writes).next().is_some() {
                write_conflict_pairs.push((t1, t2));
            }
        }
    }

    for (t1, t2) in write_conflict_pairs {
        let t1w_before_t2r = vars.before_lit((t1, true), (t2, false));
        let t2w_before_t1r = vars.before_lit((t2, true), (t1, false));
        solver
            .add_clause([t1w_before_t2r, t2w_before_t1r].into_iter().collect())
            .unwrap();
    }

    matches!(solver.solve().unwrap(), SolverResult::Sat)
}

#[cfg(test)]
mod tests {
    use dbcop_core::history::raw::types::{Event, Transaction};

    use super::*;

    fn serializable_history() -> Vec<Session<&'static str, u64>> {
        vec![
            vec![Transaction::committed(vec![
                Event::write("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("y", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("y", 2),
                Event::write("x", 2),
            ])],
        ]
    }

    fn non_serializable_history() -> Vec<Session<&'static str, u64>> {
        // write skew: t1 reads x, writes y; t2 reads y, writes x
        // both read from t0, but their writes conflict
        vec![
            vec![Transaction::committed(vec![
                Event::write("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("y", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("y", 1),
                Event::write("x", 2),
            ])],
        ]
    }

    #[test]
    fn test_serializable_sat() {
        assert!(check_serializable(&serializable_history()).is_ok());
    }

    #[test]
    fn test_serializable_violation_sat() {
        assert!(check_serializable(&non_serializable_history()).is_err());
    }

    #[test]
    fn test_prefix_sat() {
        assert!(check_prefix(&serializable_history()).is_ok());
    }

    #[test]
    fn test_prefix_allows_non_serializable() {
        // Prefix is weaker than serializable -- write skew is allowed
        assert!(check_prefix(&non_serializable_history()).is_ok());
    }

    #[test]
    fn test_si_sat() {
        assert!(check_snapshot_isolation(&serializable_history()).is_ok());
    }

    #[test]
    fn test_si_allows_write_skew() {
        // Write skew has disjoint write sets, so SI allows it
        assert!(check_snapshot_isolation(&non_serializable_history()).is_ok());
    }

    #[test]
    fn test_si_rejects_overlapping_writes() {
        // Two concurrent transactions both write x (overlapping write sets)
        // t0: write(x,1)
        // t1: read(x,1), write(x,2)
        // t2: read(x,1), write(x,3)
        // SI should reject: t1 and t2 both write x and are concurrent
        let history: Vec<Session<&str, u64>> = vec![
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
        assert!(check_snapshot_isolation(&history).is_err());
    }

    #[test]
    fn test_agreement_with_core_serializable() {
        // Compare SAT solver with DFS solver on a known-serializable history
        let history = serializable_history();
        let core_result = dbcop_core::check(&history, Consistency::Serializable);
        let sat_result = check_serializable(&history);
        assert_eq!(core_result.is_ok(), sat_result.is_ok());
    }

    #[test]
    fn test_agreement_with_core_non_serializable() {
        let history = non_serializable_history();
        let core_result = dbcop_core::check(&history, Consistency::Serializable);
        let sat_result = check_serializable(&history);
        assert_eq!(core_result.is_ok(), sat_result.is_ok());
    }
}
