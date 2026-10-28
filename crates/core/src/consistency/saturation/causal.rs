//! Causal Consistency checker using the paper's commit-order constraints.
//!
//! Causal visibility is the fixed transitive closure of session order and
//! write-read edges. For each read of x from T1, another x-writer T2 visible
//! to that reader must precede T1 in commit order. These inferred write-write
//! edges constrain commit order; they do not create causal dependencies.
//!
//! # Algorithm
//!
//! 1. Build an [`AtomicTransactionPO`] from the raw sessions.
//! 2. Merge write-read (`wr`) edges into visibility.
//! 3. Compute the transitive closure of visibility (`vis_is_trans`).
//! 4. Compute [`causal_ww`] once from that fixed causal visibility.
//! 5. Merge the inferred edges into the commit-order graph and check acyclicity.
//!
//! The returned `visibility_relation` contains commit-order constraints for
//! downstream linearization; it must not be reused as the causal antecedent.
//!
//! # Data flow
//!
//! ```text
//! sessions -> AtomicTransactionPO -> vis_includes(wr) -> vis_is_trans()
//!     -> causal_ww() -> vis_includes(ww)
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
//! Consistency level.
//!
//! [`AtomicTransactionPO`]: crate::history::atomic::AtomicTransactionPO
//! [`causal_ww`]: crate::history::atomic::AtomicTransactionPO::causal_ww

use core::hash::Hash;

use crate::consistency::error::Error;
use crate::history::atomic::types::AtomicTransactionHistory;
use crate::history::atomic::AtomicTransactionPO;
use crate::history::raw::types::Session;
use crate::Consistency;

/// Check whether a history satisfies Causal Consistency.
///
/// Derives commit-order edges from the fixed `(so ∪ wr)+` causal relation
/// and checks the resulting commit-order graph for acyclicity.
///
/// On success, returns the [`AtomicTransactionPO`] whose
/// `visibility_relation` field contains the commit-order constraints.
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

    // Commit-order edges must never feed back into the causal premise.
    let ww_rel = atomic_history.causal_ww();
    for ww_x in ww_rel.values() {
        atomic_history.vis_includes(ww_x);
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
