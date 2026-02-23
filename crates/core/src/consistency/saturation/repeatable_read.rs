//! Checks if a valid history is a atomic read history.

use core::hash::Hash;

use hashbrown::HashMap;

use super::committed_read::check_committed_read;
use crate::consistency::error::Error;
use crate::history::raw::error::Error as NonAtomicError;
use crate::history::raw::types::{Event, EventId, Session};
use crate::history::raw::{get_all_writes, is_valid_history};

/// Checks if a valid history maintains repeatable read.
///
/// # Errors
///
/// Returns [`NonAtomicError::NonRepeatableRead`] if the history does not maintain repeatable read.
pub fn check_repeatable_read<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<(), Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone + Default,
{
    is_valid_history(histories)?;

    check_committed_read(histories)?;

    let all_writes = get_all_writes(histories)?;

    for (i_node, session) in (1..).zip(histories.iter()) {
        for (i_transaction, transaction) in (0..).zip(session.iter()) {
            let mut latest_writes: HashMap<Variable, EventId> = HashMap::new();
            for (i_event, event) in (0..).zip(transaction.events.iter()) {
                let event_id = EventId {
                    session_id: i_node,
                    session_height: i_transaction,
                    transaction_height: i_event,
                };
                match event {
                    Event::Write { variable, .. } => {
                        // if transactions writes a variable, the following reads should read from this write
                        latest_writes.insert(variable.clone(), event_id);
                    }
                    Event::Read { variable, .. } => {
                        let write_event_id = all_writes.get(event).ok_or_else(|| {
                            NonAtomicError::IncompleteHistory {
                                event: event.clone(),
                                id: event_id,
                            }
                        })?;

                        if let Some(local_write_event_id) = latest_writes.get(variable) {
                            // latest write should match with the current write
                            if local_write_event_id != write_event_id {
                                Err(NonAtomicError::NonRepeatableRead {
                                    read_event: event.clone(),
                                    read_event_id: event_id,
                                    write_event_ids: [*local_write_event_id, *write_event_id],
                                })?;
                            }
                        } else {
                            // no latest writes, first external read
                            latest_writes.insert(variable.clone(), *write_event_id);
                        }
                    }
                }
            }
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::history::raw::types::Transaction;

    #[test]
    fn test_non_repeatable_read() {
        let histories = vec![
            vec![Transaction::committed(vec![Event::write("x", 2)])],
            vec![Transaction::committed(vec![Event::write("x", 3)])],
            vec![Transaction::committed(vec![
                Event::read("x", 2),
                Event::read("x", 3),
            ])],
        ];

        let result = check_repeatable_read(&histories);

        assert!(
            matches!(
                result,
                Err(Error::NonAtomic(NonAtomicError::NonRepeatableRead {
                    read_event: Event::Read {
                        variable: "x",
                        version: Some(3)
                    },
                    read_event_id: EventId {
                        session_id: 3,
                        session_height: 0,
                        transaction_height: 1
                    },
                    write_event_ids: [
                        EventId {
                            session_id: 1,
                            session_height: 0,
                            transaction_height: 0
                        },
                        EventId {
                            session_id: 2,
                            session_height: 0,
                            transaction_height: 0
                        },
                    ]
                }))
            ),
            "result: {result:?}",
        );
    }
}
