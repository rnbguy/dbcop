//! Read Committed consistency checker using saturation.
//!
//! Read Committed requires that every read observes a value written by a
//! committed transaction (never an aborted or in-progress write) and that
//! reads within the same transaction on the same variable observe writes
//! in a consistent committed order.
//!
//! # Algorithm
//!
//! This checker builds a *committed order* graph and tests it for acyclicity:
//!
//! 1. **Session-order edges** - For each session, add edges from the root
//!    transaction to the first transaction, then chain successive transactions.
//! 2. **Write-read edges** - For each read event, locate the committed write
//!    it observes and add a `wr_x` edge from the writing transaction to the
//!    reading transaction.
//! 3. **Committed-order edges** - When two reads in the same transaction read
//!    the same variable from different transactions, the earlier write must
//!    precede the later write in committed order.
//! 4. **Acyclicity check** - Attempt a topological sort of the committed
//!    order graph. If one exists, the history satisfies Read Committed.
//!    If not, find a cycle edge and report it.
//!
//! Unlike the other saturation checkers, this module operates directly on
//! raw sessions rather than on [`AtomicTransactionPO`], because it must
//! inspect individual read/write events to validate committed writes.
//!
//! # Data flow
//!
//! ```text
//! sessions ─▶ validate ─▶ build committed order DiGraph ─▶ topological sort
//!     │                        │                                   │
//!     └── session-order edges  └── wr_x + committed edges          └── Ok(DiGraph) or Err(Cycle)
//! ```
//!
//! # Errors
//!
//! - [`Error::NonAtomic`] if the history contains uncommitted or overwritten
//!   writes.
//! - [`Error::Cycle`] if a cycle is found in the committed order graph.
//!
//! # Reference
//!
//! Corresponds to Algorithm 1 in Biswas and Enea (2019), restricted to the
//! Read Committed level.
//!
//! [`AtomicTransactionPO`]: crate::history::atomic::AtomicTransactionPO

use core::hash::Hash;

use hashbrown::HashMap;

use crate::consistency::error::Error;
use crate::graph::digraph::DiGraph;
use crate::history::atomic::types::TransactionId;
use crate::history::raw::error::Error as NonAtomicError;
use crate::history::raw::types::{Event, EventId, Session};
use crate::history::raw::{get_all_writes, get_committed_writes, is_valid_history};
use crate::Consistency;

/// Check whether a history satisfies Read Committed consistency.
///
/// Builds a committed order [`DiGraph`] from session-order, write-read, and
/// committed-order edges, then checks for acyclicity via topological sort.
///
/// On success, returns the committed order graph as a witness of acyclicity.
///
/// # Errors
///
/// - Returns [`Error::NonAtomic`] if the history contains reads from
///   uncommitted or overwritten writes.
/// - Returns [`Error::Cycle`] with the offending edge pair if the committed
///   order graph contains a cycle.
#[allow(clippy::too_many_lines)]
pub fn check_committed_read<Variable, Version>(
    histories: &[Session<Variable, Version>],
) -> Result<DiGraph<TransactionId>, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone,
{
    tracing::debug!(
        sessions = histories.len(),
        "committed read check: validating history"
    );

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

    if committed_order.topological_sort().is_some() {
        tracing::debug!("committed read check: passed");
        Ok(committed_order)
    } else if let Some((a, b)) = committed_order.find_cycle_edge() {
        tracing::debug!(?a, ?b, "committed read check: cycle detected");
        Err(Error::Cycle {
            level: Consistency::CommittedRead,
            a,
            b,
        })
    } else {
        tracing::debug!("committed read check: failed (no cycle edge found)");
        Err(Error::Invalid(Consistency::CommittedRead))
    }
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
            matches!(
                result,
                Err(Error::Cycle {
                    level: Consistency::CommittedRead,
                    ..
                })
            ),
            "result: {result:?}",
        );
    }
}
