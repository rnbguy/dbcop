use core::hash::Hash;

use self::error::Error;
use self::linearization::constrained_linearization::ConstrainedLinearizationSolver;
use self::linearization::prefix::PrefixConsistencySolver;
use self::linearization::serializable::SerializabilitySolver;
use self::linearization::snapshot_isolation::SnapshotIsolationSolver;
use self::saturation::atomic_read::check_atomic_read;
use self::saturation::causal::check_causal_read;
use self::saturation::committed_read::check_committed_read;
use crate::history::raw::types::Session;

pub mod error;
pub mod linearization;
pub mod saturation;
pub mod witness;

// Re-export submodules at the consistency level for convenience.
pub use linearization::{constrained_linearization, prefix, serializable, snapshot_isolation};
pub use saturation::{atomic_read, causal, committed_read, repeatable_read};
pub use witness::Witness;

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
    Prefix,
    /// Prefix plus write-write conflict freedom (disjoint write sets for concurrent transactions).
    SnapshotIsolation,
    /// A total order on all transactions that explains every read.
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
    // Trivially consistent: no sessions or all sessions empty
    #[allow(clippy::redundant_closure_for_method_calls)]
    if sessions.is_empty() || sessions.iter().all(|s| s.is_empty()) {
        return Ok(());
    }

    match level {
        Consistency::CommittedRead => check_committed_read(sessions),
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
