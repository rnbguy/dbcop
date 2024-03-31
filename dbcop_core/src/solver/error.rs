use ::derive_more::From;

use crate::history::non_atomic::error::Error as NonAtomicError;
use crate::Consistency;

#[derive(Debug, From)]
pub enum Error<Variable, Version> {
    NonAtomic(NonAtomicError<Variable, Version>),
    Invalid(Consistency),
}
