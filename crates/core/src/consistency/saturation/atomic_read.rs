//! Atomic Read consistency checker using saturation.
//!
//! Atomic Read strengthens Repeatable Read by requiring that all reads within a
//! single transaction observe a consistent snapshot across variables -- no
//! fractured reads are allowed. If
//! transaction T reads variable x from T1 and variable y from T2, then the
//! visibility relation must be consistent with a single point-in-time view.
//!
//! # Algorithm
//!
//! This is a single-pass saturation checker (no fixpoint loop needed):
//!
//! 1. Build an [`AtomicTransactionPO`] from the raw sessions, which computes
//!    the session order, write-read relations, and initial visibility.
//! 2. Merge write-read (`wr`) edges into the visibility relation.
//! 3. Compute write-write (`ww`) edges via [`causal_ww`] -- for each
//!    variable x, if transaction T2 is visible to a reader of T1's write
//!    on x, then T2 must precede T1 in write order.
//! 4. Merge all `ww` edges into visibility.
//! 5. Check that the resulting visibility relation is acyclic.
//!
//! Because `ww` edges are derived from the current visibility and no
//! transitive closure is taken, a single round of edge insertion suffices.
//!
//! # Data flow
//!
//! ```text
//! sessions -> AtomicTransactionPO -> vis_includes(wr) -> causal_ww()
//!     -> vis_includes(ww) -> acyclicity check -> Ok(PO) or Err(Cycle)
//! ```
//!
//! # Errors
//!
//! - [`Error::NonAtomic`] if the history is structurally invalid (e.g.
//!   uncommitted writes).
//! - [`Error::Cycle`] if the visibility relation contains a cycle after
//!   adding `ww` edges.
//!
//! # Reference
//!
//! Corresponds to Algorithm 1 in Biswas and Enea (2019) at the Atomic Read
//! level.
//!
//! [`AtomicTransactionPO`]: crate::history::atomic::AtomicTransactionPO
//! [`causal_ww`]: crate::history::atomic::AtomicTransactionPO::causal_ww

use core::hash::Hash;

use crate::consistency::error::Error;
use crate::history::atomic::types::AtomicTransactionHistory;
use crate::history::atomic::AtomicTransactionPO;
use crate::history::raw::types::Session;
use crate::Consistency;

/// Check whether a history satisfies Atomic Read consistency.
///
/// Builds an [`AtomicTransactionPO`], saturates the visibility relation with
/// write-read and write-write edges, then checks for acyclicity.
///
/// On success, returns the full [`AtomicTransactionPO`] whose
/// `visibility_relation` field is the acyclic witness graph.
///
/// # Errors
///
/// - Returns [`Error::NonAtomic`] for structurally invalid histories.
/// - Returns [`Error::Cycle`] with the offending edge pair if the visibility
///   relation contains a cycle.
pub fn check_atomic_read<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<AtomicTransactionPO<Variable>, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone + Default,
{
    tracing::debug!(
        sessions = histories.len(),
        "atomic read check: building partial order"
    );

    let mut atomic_history =
        AtomicTransactionPO::from(AtomicTransactionHistory::try_from(histories)?);

    atomic_history.vis_includes(&atomic_history.get_wr());

    let ww_rel = atomic_history.causal_ww();

    tracing::trace!(
        variables = ww_rel.len(),
        "atomic read check: applying write-write edges"
    );

    for ww_x in ww_rel.values() {
        atomic_history.vis_includes(ww_x);
    }

    if atomic_history.has_valid_visibility() {
        tracing::debug!("atomic read check: passed");
        Ok(atomic_history)
    } else if let Some((a, b)) = atomic_history.visibility_relation.find_cycle_edge() {
        tracing::debug!(?a, ?b, "atomic read check: cycle detected");
        Err(Error::Cycle {
            level: Consistency::AtomicRead,
            a,
            b,
        })
    } else {
        tracing::debug!("atomic read check: failed (no cycle edge found)");
        Err(Error::Invalid(Consistency::AtomicRead))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::history::raw::types::{Event, Transaction};

    #[test]
    fn test_atomic_read() {
        // Fractured visibility history:
        // s1: write x=1,y=1
        // s2: read y=1, write x=2,z=1
        // s3: read x=1, read z=1
        // AtomicRead should detect a cycle after adding ww edges.
        let histories = vec![
            vec![Transaction::committed(vec![
                Event::write("x", 1),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("y", 1),
                Event::write("x", 2),
                Event::write("z", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 1),
                Event::read("z", 1),
            ])],
        ];

        let result = check_atomic_read(&histories);

        assert!(matches!(
            result,
            Err(Error::Cycle {
                level: Consistency::AtomicRead,
                ..
            })
        ));
    }
}
