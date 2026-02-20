use core::hash::Hash;

use self::error::Error;
#[cfg(feature = "partial-order")]
use self::linearization::constrained_linearization::ConstrainedLinearizationSolver;
#[cfg(feature = "partial-order")]
use self::linearization::prefix::PrefixConsistencySolver;
#[cfg(feature = "partial-order")]
use self::linearization::serializable::SerializabilitySolver;
#[cfg(feature = "partial-order")]
use self::linearization::snapshot_isolation::SnapshotIsolationSolver;
use self::saturation::atomic_read::check_atomic_read;
use self::saturation::causal::check_causal_read;
use self::saturation::committed_read::check_committed_read;
use crate::history::raw::types::Session;

pub mod error;
#[cfg(feature = "partial-order")]
pub mod linearization;
pub mod saturation;

// Re-export submodules at the consistency level for convenience.
#[cfg(feature = "partial-order")]
pub use linearization::{constrained_linearization, prefix, serializable, snapshot_isolation};
pub use saturation::{atomic_read, causal, committed_read, repeatable_read};

/// Consistency levels supported by dbcop, ordered from weakest to strongest.
#[derive(Debug, Copy, Clone)]
pub enum Consistency {
    /// No transaction reads from an aborted or uncommitted write.
    CommittedRead,
    /// All reads in a transaction observe a consistent snapshot (no fractured reads).
    AtomicRead,
    /// Visibility is transitively closed and respects the write-write order.
    Causal,
    /// Causal plus a total order on transactions consistent with visibility.
    #[cfg(feature = "partial-order")]
    Prefix,
    /// Prefix plus write-write conflict freedom (disjoint write sets for concurrent transactions).
    #[cfg(feature = "partial-order")]
    SnapshotIsolation,
    /// A total order on all transactions that explains every read.
    #[cfg(feature = "partial-order")]
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
        Consistency::CommittedRead => check_committed_read(sessions),
        Consistency::AtomicRead => check_atomic_read(sessions).map(|_| ()),
        Consistency::Causal => check_causal_read(sessions).map(|_| ()),
        #[cfg(feature = "partial-order")]
        Consistency::Prefix => {
            let po = check_causal_read(sessions)?;
            let mut solver = PrefixConsistencySolver::from(po);
            solver
                .get_linearization()
                .map(|_| ())
                .ok_or(Error::Invalid(Consistency::Prefix))
        }
        #[cfg(feature = "partial-order")]
        Consistency::SnapshotIsolation => {
            let po = check_causal_read(sessions)?;
            let mut solver = SnapshotIsolationSolver::from(po);
            solver
                .get_linearization()
                .map(|_| ())
                .ok_or(Error::Invalid(Consistency::SnapshotIsolation))
        }
        #[cfg(feature = "partial-order")]
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
