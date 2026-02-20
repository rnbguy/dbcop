//! Checks if a valid history maintains atomic read.

use crate::history::atomic::types::AtomicTransactionHistory;
use crate::history::atomic::AtomicTransactionPO;
use crate::history::raw::types::Session;
use crate::solver::error::Error;
use crate::Consistency;

use ::core::hash::Hash;

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
    let mut atomic_history =
        AtomicTransactionPO::from(AtomicTransactionHistory::try_from(histories)?);

    atomic_history.vis_includes(&atomic_history.get_wr());

    let ww_rel = atomic_history.causal_ww();

    for ww_x in ww_rel.values() {
        atomic_history.vis_includes(ww_x);
    }

    atomic_history
        .has_valid_visibility()
        .then_some(atomic_history)
        .ok_or(Error::Invalid(Consistency::AtomicRead))
}

#[cfg(test)]
mod tests {
    use crate::history::raw::types::{Event, Transaction};

    use super::*;

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
            Err(Error::Invalid(Consistency::AtomicRead))
        ));
    }
}
