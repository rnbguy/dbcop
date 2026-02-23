//! The atomic history is assumed to be read-committed and repeatable-read. That is,
//!   - The external reads of a variable is always from committed write i.e. last write of in a committed transaction.
//!   - The external reads of a variable is always from a unique transaction.
//!
//! Hence, if a transaction has a `wr_x` parent for a variable `x`, it is unique.
//! Also, if a transaction has a `wr_x` child, then it commits a write on variable `x`.
//!
//! So it suffices to maintain the _write-read_ relation per variable across the transactions and the _write-set_ of each transaction.

use core::hash::Hash;

use hashbrown::{HashMap, HashSet};

use crate::consistency::error::Error;
use crate::consistency::repeatable_read::check_repeatable_read;
use crate::history::raw::error::Error as NonAtomicError;
use crate::history::raw::get_all_writes;
use crate::history::raw::types::{Event, EventId, Session};

/// Information about a transaction.
/// `reads` is the read-set of the current transaction, mapping each variable to the transaction that it read from.
/// `writes` is the write-set of the current transaction.
#[derive(Debug)]
pub struct AtomicTransactionInfo<Variable> {
    pub reads: HashMap<Variable, TransactionId>,
    pub writes: HashSet<Variable>,
}

/// Unique identifier for a transaction within a history.
///
/// A transaction is identified by the session it belongs to (`session_id`)
/// and its position within that session (`session_height`). Ordering is
/// lexicographic: first by `session_id`, then by `session_height`.
///
/// The default value `(0, 0)` serves as the **root node** in session-order
/// and visibility graphs. Every real transaction in every session has the
/// root as a predecessor.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug, Default, Clone, Copy, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct TransactionId {
    /// 1-based session index. Session 0 is reserved for the root.
    pub session_id: u64,
    /// 0-based position of the transaction within its session.
    pub session_height: u64,
}

impl TransactionId {
    /// Returns the root transaction `(0, 0)`.
    ///
    /// The root is a synthetic predecessor of every transaction in the history.
    #[must_use]
    pub const fn root() -> Self {
        Self {
            session_id: 0,
            session_height: 0,
        }
    }
}

/// Maps every transaction in a history to its read-set and write-set.
///
/// Constructed from raw sessions via `TryFrom<&[Session]>`, which validates
/// that the history is repeatable-read (no fractured reads) and resolves
/// every read to its source write transaction.
#[derive(Debug)]
pub struct AtomicTransactionHistory<Variable>(
    pub HashMap<TransactionId, AtomicTransactionInfo<Variable>>,
);

impl<Variable, Version> TryFrom<&[Session<Variable, Version>]>
    for AtomicTransactionHistory<Variable>
where
    Variable: Eq + Hash + Clone,
    Version: Eq + Hash + Clone + Default,
{
    type Error = Error<Variable, Version>;

    fn try_from(histories: &[Session<Variable, Version>]) -> Result<Self, Self::Error> {
        check_repeatable_read(histories)?;

        let all_writes = get_all_writes(histories)?;

        let mut atomic_history = HashMap::new();

        for (i_node, session) in (1..).zip(histories.iter()) {
            for (i_transaction, transaction) in (0..).zip(session.iter()) {
                let current_transaction_id = TransactionId {
                    session_id: i_node,
                    session_height: i_transaction,
                };

                let mut current_transaction_info = AtomicTransactionInfo {
                    reads: HashMap::new(),
                    writes: HashSet::new(),
                };

                for (i_event, event) in (0..).zip(transaction.events.iter()) {
                    let event_id = EventId {
                        session_id: i_node,
                        session_height: i_transaction,
                        transaction_height: i_event,
                    };
                    match event {
                        Event::Write { variable, .. } => {
                            current_transaction_info.writes.insert(variable.clone());
                        }
                        Event::Read { variable, .. } => {
                            let write_event_id = all_writes.get(event).ok_or_else(|| {
                                NonAtomicError::IncompleteHistory {
                                    event: event.clone(),
                                    id: event_id,
                                }
                            })?;

                            if write_event_id.transaction_id() != current_transaction_id {
                                current_transaction_info
                                    .reads
                                    .insert(variable.clone(), write_event_id.transaction_id());
                            }
                        }
                    }
                }

                atomic_history.insert(current_transaction_id, current_transaction_info);
            }
        }

        Ok(Self(atomic_history))
    }
}
