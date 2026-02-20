use core::hash::Hash;

use crate::history::raw::types::Session;

use self::error::Error;
use self::linearization::constrained_linearization::ConstrainedLinearizationSolver;
use self::linearization::prefix::PrefixConsistencySolver;
use self::linearization::serializable::SerializabilitySolver;
use self::linearization::snapshot_isolation::SnapshotIsolationSolver;
use self::saturation::atomic_read::check_atomic_read;
use self::saturation::causal::check_causal_read;
use self::saturation::committed_read::check_committed_read;

pub mod error;
pub mod linearization;
pub mod saturation;

// Re-export submodules at the consistency level for convenience.
pub use linearization::{constrained_linearization, prefix, serializable, snapshot_isolation};
pub use saturation::{atomic_read, causal, committed_read, repeatable_read};

/// Consistency levels supported by dbcop, ordered from weakest to strongest.
#[derive(Debug, Copy, Clone)]
pub enum Consistency {
    CommittedRead,
    AtomicRead,
    Causal,
    Prefix,
    SnapshotIsolation,
    Serializable,
}

/// Check whether the given history satisfies the specified consistency level.
///
/// # Errors
///
/// Returns an error if the history violates the consistency level.
pub fn check<Variable, Version>(
    sessions: &[Session<Variable, Version>],
    level: Consistency,
) -> Result<(), Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone + Ord,
    Version: Eq + Hash + Clone,
{
    match level {
        Consistency::CommittedRead => check_committed_read(sessions).map(|()| ()),
        Consistency::AtomicRead => check_atomic_read(sessions).map(|_| ()),
        Consistency::Causal => check_causal_read(sessions).map(|_| ()),
        Consistency::Prefix => {
            let po = check_causal_read(sessions)?;
            let mut solver = PrefixConsistencySolver::from(po);
            solver
                .get_linearization()
                .map(|_| ())
                .ok_or(Error::Invalid(Consistency::Prefix))
        }
        Consistency::SnapshotIsolation => {
            let po = check_causal_read(sessions)?;
            let mut solver = SnapshotIsolationSolver::from(po);
            solver
                .get_linearization()
                .map(|_| ())
                .ok_or(Error::Invalid(Consistency::SnapshotIsolation))
        }
        Consistency::Serializable => {
            let po = check_causal_read(sessions)?;
            let mut solver = SerializabilitySolver::from(po);
            solver
                .get_linearization()
                .map(|_| ())
                .ok_or(Error::Invalid(Consistency::Serializable))
        }
    }
}
