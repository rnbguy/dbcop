use derive_more::From;

use crate::history::atomic::types::TransactionId;
use crate::history::raw::error::Error as NonAtomicError;
use crate::Consistency;

/// Error returned when a history fails a consistency check.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug, From)]
pub enum Error<Variable, Version> {
    /// The history has a structural issue (e.g. uncommitted writes read by others).
    NonAtomic(NonAtomicError<Variable, Version>),
    /// The history violates the specified consistency level.
    Invalid(Consistency),
    /// A cycle was detected in the partial order for the given consistency level.
    /// The two transaction IDs are an example of a conflicting edge in the cycle.
    Cycle {
        level: Consistency,
        a: TransactionId,
        b: TransactionId,
    },
}
