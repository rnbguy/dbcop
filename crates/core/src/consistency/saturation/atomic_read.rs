//! Checks if a valid history maintains atomic read.

use core::hash::Hash;

use crate::consistency::error::Error;
use crate::history::atomic::types::AtomicTransactionHistory;
use crate::history::atomic::AtomicTransactionPO;
use crate::history::raw::types::Session;
use crate::Consistency;

/// checks if a valid history maintains atomic read
/// # Errors
///
/// Returns [`Error::Invalid`] if the history does not maintain atomic read.
pub fn check_atomic_read<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<AtomicTransactionPO<Variable>, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
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
        let histories = vec![
            vec![
                Transaction::committed(vec![Event::write("x", 1)]),
                Transaction::committed(vec![Event::write("x", 2)]),
            ],
            vec![
                Transaction::committed(vec![Event::read("x", 2)]),
                Transaction::committed(vec![Event::read("x", 1)]),
            ],
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
