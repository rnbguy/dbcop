//! Checks if a valid history maintains causal consistency.

use crate::history::atomic::types::AtomicTransactionHistory;
use crate::history::atomic::AtomicTransactionPO;
use crate::history::raw::types::Session;
use crate::consistency::error::Error;
use crate::Consistency;

use ::core::hash::Hash;

/// # Errors
///
/// Returns [`Error::Invalid`] if the history does not maintain causal consistency.
pub fn check_causal_read<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<AtomicTransactionPO<Variable>, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
{
    let mut atomic_history =
        AtomicTransactionPO::from(AtomicTransactionHistory::try_from(histories)?);

    atomic_history.vis_includes(&atomic_history.get_wr());

    loop {
        atomic_history.vis_is_trans();

        let ww_rel = atomic_history.causal_ww();
        let mut changed = false;

        for ww_x in ww_rel.values() {
            changed |= atomic_history.vis_includes(ww_x);
        }

        if !changed {
            break;
        }
    }

    atomic_history
        .has_valid_visibility()
        .then_some(atomic_history)
        .ok_or(Error::Invalid(Consistency::Causal))
}

#[cfg(test)]
mod tests {
    use crate::{
        history::raw::types::{Event, Transaction},
        consistency::atomic_read::check_atomic_read,
    };

    use super::*;

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
            Err(Error::Invalid(Consistency::Causal))
        ));
    }
}
