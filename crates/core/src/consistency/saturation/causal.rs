//! Causal Consistency checker using iterated saturation.
//!
//! Causal Consistency strengthens Atomic Read by requiring the visibility
//! relation to be transitively closed. If transaction T1 is visible to T2,
//! and T2 is visible to T3, then T1 must also be visible to T3. This
//! captures the notion that causal dependencies propagate through the
//! system.
//!
//! # Algorithm
//!
//! This checker runs a saturation loop that alternates between computing
//! new write-write (`ww`) edges and incrementally closing the visibility
//! relation:
//!
//! 1. Build an [`AtomicTransactionPO`] from the raw sessions.
//! 2. Merge write-read (`wr`) edges into visibility.
//! 3. Compute the transitive closure of visibility (`vis_is_trans`).
//! 4. **Saturation loop**:
//!    a. Compute `ww` edges via [`causal_ww`] using current visibility.
//!    b. Collect any `ww` edges not yet in the visibility relation.
//!    c. If no new edges, the fixpoint is reached -- break.
//!    d. Otherwise, add new edges with [`incremental_closure`] (which
//!    extends the transitively-closed graph without a full recompute).
//!    e. Repeat from (a).
//! 5. Check that the final visibility relation is acyclic.
//!
//! The incremental closure in step 4d avoids the O(V*(V+E)) cost of a
//! full transitive closure on each iteration, using BFS-based
//! ancestor/descendant cross-product instead.
//!
//! # Data flow
//!
//! ```text
//! sessions -> AtomicTransactionPO -> vis_includes(wr) -> vis_is_trans()
//!     -> loop { causal_ww() -> new edges? -> incremental_closure() }
//!     -> acyclicity check -> Ok(PO) or Err(Cycle)
//! ```
//!
//! # Errors
//!
//! - [`Error::NonAtomic`] if the history is structurally invalid.
//! - [`Error::Cycle`] if the visibility relation contains a cycle after
//!   saturation.
//!
//! # Reference
//!
//! Corresponds to Algorithm 1 in Biswas and Enea (2019) at the Causal
//! Consistency level, with incremental closure as a performance
//! optimization.
//!
//! [`AtomicTransactionPO`]: crate::history::atomic::AtomicTransactionPO
//! [`causal_ww`]: crate::history::atomic::AtomicTransactionPO::causal_ww
//! [`incremental_closure`]: crate::graph::digraph::DiGraph::incremental_closure

use alloc::vec::Vec;
use core::hash::Hash;

use crate::consistency::error::Error;
use crate::history::atomic::types::AtomicTransactionHistory;
use crate::history::atomic::AtomicTransactionPO;
use crate::history::raw::types::Session;
use crate::Consistency;

/// Check whether a history satisfies Causal Consistency.
///
/// Runs a saturation loop that alternates between computing write-write
/// edges and incrementally closing the visibility relation until a
/// fixpoint is reached. Then checks the result for acyclicity.
///
/// On success, returns the full [`AtomicTransactionPO`] whose
/// `visibility_relation` field is the transitively-closed, acyclic
/// witness graph.
///
/// # Errors
///
/// - Returns [`Error::NonAtomic`] for structurally invalid histories.
/// - Returns [`Error::Cycle`] with the offending edge pair if the
///   visibility relation contains a cycle.
pub fn check_causal_read<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<AtomicTransactionPO<Variable>, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone + Default,
{
    tracing::debug!(
        sessions = histories.len(),
        "causal check: building atomic partial order"
    );

    let mut atomic_history =
        AtomicTransactionPO::from(AtomicTransactionHistory::try_from(histories)?);

    atomic_history.vis_includes(&atomic_history.get_wr());
    atomic_history.vis_is_trans();

    let mut iteration = 0u32;
    loop {
        let ww_rel = atomic_history.causal_ww();
        let mut new_edges = Vec::new();

        for ww_x in ww_rel.values() {
            for (src, dsts) in &ww_x.adj_map {
                for dst in dsts {
                    if !atomic_history.visibility_relation.has_edge(src, dst) {
                        new_edges.push((*src, *dst));
                    }
                }
            }
        }

        if new_edges.is_empty() {
            tracing::debug!(
                iterations = iteration,
                "causal check: saturation fixpoint reached"
            );
            break;
        }

        tracing::trace!(
            iteration,
            new_edges = new_edges.len(),
            "causal check: saturation iteration"
        );

        atomic_history
            .visibility_relation
            .incremental_closure(new_edges);

        iteration += 1;
    }

    if atomic_history.has_valid_visibility() {
        tracing::debug!("causal check: passed");
        Ok(atomic_history)
    } else if let Some((a, b)) = atomic_history.visibility_relation.find_cycle_edge() {
        tracing::debug!(?a, ?b, "causal check: cycle detected");
        Err(Error::Cycle {
            level: Consistency::Causal,
            a,
            b,
        })
    } else {
        tracing::debug!("causal check: failed (no cycle edge found)");
        Err(Error::Invalid(Consistency::Causal))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::consistency::atomic_read::check_atomic_read;
    use crate::history::raw::types::{Event, Transaction};

    #[test]
    fn test_atomic_read() {
        let histories = vec![
            vec![Transaction::committed(vec![
                Event::write("x", 1),
                Event::write("a", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("y", 1),
                Event::write("z", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("z", 1),
                Event::write("a", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("a", 2),
                Event::write("p", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("p", 1),
                Event::write("q", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("q", 1),
                Event::read("a", 1),
            ])],
        ];

        assert!(check_atomic_read(&histories).is_ok());

        assert!(matches!(
            check_causal_read(&histories),
            Err(Error::Cycle {
                level: Consistency::Causal,
                ..
            })
        ));
    }
}
