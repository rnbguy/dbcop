use ::derive_more::From;

use crate::history::raw::error::Error as NonAtomicError;
use crate::Consistency;

/// Error returned when a history fails a consistency check.
#[derive(Debug, From)]
pub enum Error<Variable, Version> {
    /// The history has a structural issue (e.g. uncommitted writes read by others).
    NonAtomic(NonAtomicError<Variable, Version>),
    /// The history violates the specified consistency level.
    Invalid(Consistency),
}
