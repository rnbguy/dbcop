use derive_more::From;

use crate::history::atomic::types::TransactionId;
use crate::history::raw::error::Error as NonAtomicError;
use crate::Consistency;

/// Error returned when a history fails a consistency check.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug, From, thiserror::Error)]
pub enum Error<Variable, Version> {
    /// The history has a structural issue (e.g. uncommitted writes read by others).
    #[from(skip)]
    #[error("history is not atomic: {0}")]
    NonAtomic(#[from] NonAtomicError<Variable, Version>),
    /// The history violates the specified consistency level.
    #[error("history violates {0:?} consistency")]
    Invalid(Consistency),
    /// A cycle was detected in the partial order for the given consistency level.
    /// The two transaction IDs are an example of a conflicting edge in the cycle.
    #[error("cycle in {level:?} order between transactions {a:?} and {b:?}")]
    Cycle {
        level: Consistency,
        a: TransactionId,
        b: TransactionId,
    },
}

#[cfg(test)]
mod tests {
    use alloc::string::String;
    use core::error::Error as _;

    use super::{Error, NonAtomicError};
    use crate::history::raw::types::{Event, EventId};

    #[test]
    fn non_atomic_conversion_retains_source_and_accepts_borrowed_variables() {
        let variable = String::from("x");
        let id = EventId {
            session_id: 1,
            session_height: 0,
            transaction_height: 0,
        };
        let borrowed = NonAtomicError::IncompleteHistory {
            event: Event::Read {
                variable: variable.as_str(),
                version: None::<u64>,
            },
            id,
        };
        assert!(borrowed.source().is_none());
        let _: Error<&str, u64> = borrowed.into();

        let raw = NonAtomicError::IncompleteHistory {
            event: Event::Read {
                variable,
                version: None::<u64>,
            },
            id,
        };
        let error: Error<String, u64> = raw.into();
        let source = error
            .source()
            .expect("raw history error remains the source");
        assert!(source.is::<NonAtomicError<String, u64>>());
        assert!(source.source().is_none());
    }
}
