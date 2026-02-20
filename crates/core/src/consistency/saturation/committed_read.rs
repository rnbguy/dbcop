//! Checks if a valid history is a committed read history.

use core::hash::Hash;

use hashbrown::HashMap;

use crate::consistency::error::Error;
use crate::graph::digraph::DiGraph;
use crate::history::atomic::types::TransactionId;
use crate::history::raw::error::Error as NonAtomicError;
use crate::history::raw::types::{Event, EventId, Session};
use crate::history::raw::{get_all_writes, get_committed_writes, is_valid_history};
use crate::Consistency;

/// Checks if a valid history is a committed read history.
///
/// On success, returns the committed order as a [`DiGraph`] witnessing acyclicity.
///
/// # Errors
///
/// Returns `Error::CycleInCommittedRead` if the history is not a committed read history.
pub fn check_committed_read<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<DiGraph<TransactionId>, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
{
    is_valid_history(histories)?;

    let mut committed_order: DiGraph<TransactionId> = DiGraph::default();

    let init_transaction = TransactionId::root();

    for (i_node, session) in (1..).zip(histories.iter()) {
        // Add the edge from the initial transaction to the first transaction of the session
        committed_order.add_edge(
            init_transaction,
            TransactionId {
                session_id: i_node,
                session_height: 0,
            },
        );
        for i_transaction in (0..).take(session.len()).skip(1) {
            // Add the edge from the previous transaction to the current transaction
            committed_order.add_edge(
                TransactionId {
                    session_id: i_node,
                    session_height: i_transaction - 1,
                },
                TransactionId {
                    session_id: i_node,
                    session_height: i_transaction,
                },
            );
        }
    }

    let all_writes = get_all_writes(histories)?;
    let committed_writes = get_committed_writes(histories);

    for (i_node, session) in (1..).zip(histories.iter()) {
        for (i_transaction, transaction) in (0..).zip(session.iter()) {
            let mut local_reads: HashMap<Variable, EventId> = HashMap::new();
            for (i_event, event) in (0..).zip(transaction.events.iter()) {
                if let Event::Read { variable, .. } = event {
                    let current_event_id = EventId {
                        session_id: i_node,
                        session_height: i_transaction,
                        transaction_height: i_event,
                    };

                    let write_event_id =
                        all_writes
                            .get(event)
                            .ok_or_else(|| NonAtomicError::IncompleteHistory {
                                event: event.clone(),
                                id: current_event_id,
                            })?;

                    if let Some((committed_version, committed_event_id)) =
                        committed_writes.get(&(write_event_id.transaction_id(), variable.clone()))
                    {
                        if write_event_id != committed_event_id {
                            return Err(NonAtomicError::OverwrittenRead {
                                read_event: event.clone(),
                                read_event_id: current_event_id,
                                overwritten_write_event_id: *write_event_id,
                                committed_write_event: Event::write(
                                    variable.clone(),
                                    committed_version.clone(),
                                ),
                                committed_write_event_id: *committed_event_id,
                            }
                            .into());
                        }
                    } else {
                        return Err(NonAtomicError::UncommittedWrite {
                            read_event: event.clone(),
                            read_event_id: current_event_id,
                            write_event_id: *write_event_id,
                        }
                        .into());
                    }

                    // if read from another transaction
                    if write_event_id.transaction_id() != current_event_id.transaction_id() {
                        // there is a previous read
                        if let Some(&prevision_event_id) = local_reads.get(variable) {
                            // t1: prevision_event_id.transaction_id()
                            // t2: write_event_id.transaction_id()
                            //  t1─────────>r1
                            //  │    wr_x    │
                            //  │vis       po│
                            //  v    wr_x    v
                            //  t2 ────────>r2
                            committed_order.add_edge(
                                prevision_event_id.transaction_id(),
                                write_event_id.transaction_id(),
                            );
                        }

                        local_reads.insert(variable.clone(), *write_event_id);

                        // add wr_x edge
                        committed_order.add_edge(
                            write_event_id.transaction_id(),
                            current_event_id.transaction_id(),
                        );
                    }
                }
            }
        }
    }

    committed_order
        .topological_sort()
        .is_some()
        .then_some(committed_order)
        .ok_or(Error::Invalid(Consistency::CommittedRead))
}

#[cfg(test)]
mod tests {
    use alloc::vec;

    use super::*;
    use crate::history::raw::types::Transaction;

    #[test]
    fn test_invalid_committed_read_history() {
        let histories = vec![
            vec![Transaction::committed(vec![
                Event::write("x", 2),
                Event::write("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::write("x", 3),
                Event::read("y", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("x", 3),
                Event::read("x", 2),
            ])],
        ];

        let result = check_committed_read(&histories);

        assert!(
            matches!(result, Err(Error::Invalid(Consistency::CommittedRead))),
            "result: {result:?}",
        );
    }
}
