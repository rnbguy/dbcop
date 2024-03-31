pub mod error;
pub mod types;

use ::core::hash::Hash;
use ::hashbrown::HashMap;

use super::atomic::types::TransactionId;
use crate::history::non_atomic::error::Error;
use crate::history::non_atomic::types::{Event, EventId, Session};

// Raw history
// sanity checks --
// gather all the writes
// complete history check --
// reads must be from some write or initialization -- IncompleteHistory
// fork checks --
// successful writes must be unique -- NonUniqueVersion
// Unsuccessful write checks
// Reads can't happen from unsuccessful writes -- UnsuccessfulEventRead
// Reads can't happen from unsuccessful transactions -- UnsuccessfulTransactionRead
// Uncommitted writes checks --
// Reads must be from committed writes (groups the write set to unique version) -- UncommittedRead
// Repeatable reads checks --
// Reads must be from one committed writes (groups the read set to unique version) -- NonrepeatableRead
// Now we have AtomicTransactions

/// All writes
/// # Errors
///
/// Will return [`Error`] if there are two writes with the same version.
pub fn get_all_writes<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<HashMap<Event<Variable, Version>, EventId>, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
{
    let mut write_map = HashMap::new();

    // 0 session_id is reserved for variable initization
    for (session_id, session) in (1..).zip(histories.iter()) {
        // (0..).zip() is used for u64 index
        for (session_height, transaction) in (0..).zip(session.iter()) {
            for (transaction_height, event) in (0..).zip(transaction.events.iter()) {
                match event {
                    Event::Read { version, .. } => {
                        if version.is_none() {
                            let init_event_id = EventId {
                                session_id: 0,
                                session_height: 0,
                                transaction_height: 0,
                            };
                            let _ = write_map.insert(event.clone(), init_event_id);
                        }
                    }
                    Event::Write { variable, version } => {
                        let current_event_id = EventId {
                            session_id,
                            session_height,
                            transaction_height,
                        };

                        // store uncommitted writes too

                        let read_event = Event::Read {
                            variable: variable.clone(),
                            version: Some(version.clone()),
                        };

                        if let Some(other_event_id) =
                            write_map.insert(read_event.clone(), current_event_id)
                        {
                            return Err(Error::SameVersionWrite {
                                event: event.clone(),
                                ids: [current_event_id, other_event_id],
                            });
                        }
                    }
                }
            }
        }
    }

    Ok(write_map)
}

/// Get committed writes
#[must_use]
pub fn get_committed_writes<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> HashMap<(TransactionId, Variable), (Version, EventId)>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
{
    let mut write_map = HashMap::new();

    // 0 session_id is reserved for variable initization
    for (session_id, session) in (1..).zip(histories.iter()) {
        // (0..).zip() is used for u64 index
        for (session_height, transaction) in (0..).zip(session.iter()) {
            if transaction.committed {
                for (transaction_height, event) in (0..).zip(transaction.events.iter()) {
                    if let Event::Write { variable, version } = event {
                        let current_event_id = EventId {
                            session_id,
                            session_height,
                            transaction_height,
                        };

                        write_map.insert(
                            (current_event_id.transaction_id(), variable.clone()),
                            (version.clone(), current_event_id),
                        );
                    }
                }
            }
        }
    }

    write_map
}

/// Checks if the local reads are consistent
/// # Errors
///
/// Will return [`Error`] if there are two writes with the same version.
pub fn consistent_local_reads<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<(), Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
{
    let all_write_map = get_all_writes(histories)?;

    for (i_node, session) in (1..).zip(histories.iter()) {
        for (i_transaction, transaction) in (0..).zip(session.iter()) {
            for (i_event, event) in (0..).zip(transaction.events.iter()) {
                let current_event_id = EventId {
                    session_id: i_node,
                    session_height: i_transaction,
                    transaction_height: i_event,
                };
                let mut local_writes = HashMap::new();
                match event {
                    Event::Write { variable, version } => {
                        local_writes.insert(variable.clone(), version.clone());
                    }
                    Event::Read { variable, version } => {
                        if let Some(version) = version {
                            let write_event_id = all_write_map.get(event).ok_or_else(|| {
                                Error::IncompleteHistory {
                                    event: event.clone(),
                                    id: current_event_id,
                                }
                            })?;

                            if write_event_id.transaction_id() == current_event_id.transaction_id()
                            {
                                local_writes
                                    .get(variable)
                                    .filter(|local_latest_version| local_latest_version == &version)
                                    .ok_or_else(|| Error::InconsistentLocalRead {
                                        read_event_id: current_event_id,
                                        write_event_id: *write_event_id,
                                        read_event: event.clone(),
                                    })?;
                            }
                        }
                    }
                }
            }
        }
    }

    Ok(())
}

/// Reads can't happen from uncommitted writes
/// # Errors
///
/// Will return [`Error`] if there is a read from an uncommitted write.
pub fn committed_external_reads<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<(), Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
{
    let all_writes = get_all_writes(histories)?;
    let committed_writes = get_committed_writes(histories);

    for (i_node, session) in (1..).zip(histories.iter()) {
        for (i_transaction, transaction) in (0..).zip(session.iter()) {
            for (i_event, event) in (0..).zip(transaction.events.iter()) {
                if let Event::Read { variable, .. } = event {
                    let current_event_id = EventId {
                        session_id: i_node,
                        session_height: i_transaction,
                        transaction_height: i_event,
                    };

                    let &write_event_id =
                        all_writes
                            .get(event)
                            .ok_or_else(|| Error::IncompleteHistory {
                                event: event.clone(),
                                id: current_event_id,
                            })?;
                    if let Some(&(ref committed_version, committed_event_id)) =
                        committed_writes.get(&(write_event_id.transaction_id(), variable.clone()))
                    {
                        if write_event_id != committed_event_id {
                            return Err(Error::OverwrittenRead {
                                read_event: event.clone(),
                                read_event_id: current_event_id,
                                overwritten_write_event_id: write_event_id,
                                committed_write_event: Event::write(
                                    variable.clone(),
                                    committed_version.clone(),
                                ),
                                committed_write_event_id: committed_event_id,
                            });
                        }
                    } else {
                        return Err(Error::UncommittedWrite {
                            read_event: event.clone(),
                            read_event_id: current_event_id,
                            write_event_id,
                        });
                    }
                }
            }
        }
    }

    Ok(())
}

/// Reads are from a write and no writes have the same version
/// # Errors
///
/// Will return [`Error`] if otherwise.
pub fn is_valid_history<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<(), Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
{
    // checks consistency of within transactions
    consistent_local_reads(histories)?;
    // checks external reads are from committed writes
    committed_external_reads(histories)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use tests::types::Transaction;

    use super::*;

    #[test]
    fn test_incomplete_history() {
        let histories = vec![
            vec![Transaction::committed(vec![Event::read_empty("a")])],
            vec![Transaction::committed(vec![Event::write("a", 0)])],
            vec![Transaction::committed(vec![Event::read("a", 1)])],
        ];

        let result = is_valid_history(&histories);

        assert!(
            matches!(
                result,
                Err(Error::IncompleteHistory {
                    event: Event::Read {
                        variable: "a",
                        version: Some(1)
                    },
                    id: EventId {
                        session_id: 3,
                        session_height: 0,
                        transaction_height: 0,
                    }
                })
            ),
            "complete history check failed: {result:?}"
        );
    }

    #[test]
    fn test_uncommitted_reads() {
        let histories = vec![
            vec![Transaction::uncommitted(vec![Event::write("a", 0)])],
            vec![Transaction::committed(vec![Event::read("a", 0)])],
        ];

        let result = is_valid_history(&histories);

        assert!(
            matches!(
                result,
                Err(Error::UncommittedWrite {
                    read_event: Event::Read {
                        variable: "a",
                        version: Some(0)
                    },
                    read_event_id: EventId {
                        session_id: 2,
                        session_height: 0,
                        transaction_height: 0,
                    },
                    write_event_id: EventId {
                        session_id: 1,
                        session_height: 0,
                        transaction_height: 0,
                    }
                })
            ),
            "committed reads check failed: {result:?}"
        );
    }

    #[test]
    fn test_overwritten_reads() {
        let histories = vec![
            vec![Transaction::committed(vec![
                Event::write("a", 0),
                Event::write("a", 1),
            ])],
            vec![Transaction::committed(vec![Event::read("a", 0)])],
        ];

        let result = is_valid_history(&histories);

        assert!(
            matches!(
                result,
                Err(Error::OverwrittenRead {
                    read_event: Event::Read {
                        variable: "a",
                        version: Some(0)
                    },
                    read_event_id: EventId {
                        session_id: 2,
                        session_height: 0,
                        transaction_height: 0,
                    },
                    overwritten_write_event_id: EventId {
                        session_id: 1,
                        session_height: 0,
                        transaction_height: 0,
                    },
                    committed_write_event: Event::Write {
                        variable: "a",
                        version: 1
                    },
                    committed_write_event_id: EventId {
                        session_id: 1,
                        session_height: 0,
                        transaction_height: 1,
                    }
                })
            ),
            "non-overwritten reads check failed: {result:?}"
        );
    }

    #[test]
    fn test_inconsistent_local_reads() {
        let histories = vec![vec![Transaction::committed(vec![
            Event::write("a", 0),
            Event::read("a", 1),
            Event::write("a", 1),
        ])]];

        let result = is_valid_history(&histories);

        assert!(
            matches!(
                result,
                Err(Error::InconsistentLocalRead {
                    read_event_id: EventId {
                        session_id: 1,
                        session_height: 0,
                        transaction_height: 1,
                    },
                    write_event_id: EventId {
                        session_id: 1,
                        session_height: 0,
                        transaction_height: 2,
                    },
                    read_event: Event::Read {
                        variable: "a",
                        version: Some(1)
                    }
                })
            ),
            "consistent local reads check failed: {result:?}"
        );
    }
}
