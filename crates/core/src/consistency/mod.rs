use alloc::vec::Vec;
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

pub(crate) mod decomposition;
pub mod error;
pub mod linearization;
pub mod saturation;
pub mod witness;

// Re-export submodules at the consistency level for convenience.
pub use linearization::{constrained_linearization, prefix, serializable, snapshot_isolation};
pub use saturation::{atomic_read, causal, committed_read, repeatable_read};
pub use witness::Witness;

/// Consistency levels supported by dbcop, ordered from weakest to strongest.
///
/// Each level strictly includes all weaker levels:
/// Read Committed < Atomic Read < Causal < Prefix < Snapshot Isolation < Serializability.
///
/// The first three are checked in polynomial time via saturation (building a
/// visibility relation to a fixed point). The last three additionally require
/// finding a valid linearization and are NP-complete in the worst case.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug, Copy, Clone)]
pub enum Consistency {
    /// Read Committed: no dirty reads, transactions see only committed writes.
    CommittedRead,
    /// Atomic Read: reads are atomic across variables (no fractured reads).
    AtomicRead,
    /// Causal Consistency: causally related operations are ordered.
    Causal,
    /// Prefix Consistency: reads always see a consistent prefix of the write history.
    Prefix,
    /// Snapshot Isolation: transactions read from a consistent snapshot.
    SnapshotIsolation,
    /// Serializability: equivalent to some serial execution.
    Serializable,
}

/// Check whether the given history satisfies the specified consistency level.
///
/// `sessions` is the recorded history: a slice of sessions, where each
/// session is a sequence of transactions containing read/write events.
/// `level` selects which [`Consistency`] guarantee to verify.
///
/// On success, returns a [`Witness`] proving the history is consistent:
///
/// - [`Witness::SaturationOrder`] -- for Read Committed, Atomic Read, and
///   Causal. Contains the visibility relation (a [`DiGraph`]) computed by
///   the saturation algorithm.
/// - [`Witness::CommitOrder`] -- for Prefix and Serializability. Contains
///   a linearization of transactions as `Vec<TransactionId>`.
/// - [`Witness::SplitCommitOrder`] -- for Snapshot Isolation. Contains a
///   linearization where each transaction is split into a read phase and a
///   write phase: `Vec<(TransactionId, bool)>` (`true` = write phase).
///
/// An empty or all-empty-session history is trivially consistent and returns
/// `Witness::CommitOrder(vec![])`.
///
/// # Errors
///
/// Returns [`Error::NonAtomic`](error::Error::NonAtomic) if the history has
/// structural issues (e.g. a read observes an uncommitted write).
///
/// Returns [`Error::Cycle`](error::Error::Cycle) if a saturation checker
/// (Read Committed, Atomic Read, Causal) detects a cycle in the visibility
/// relation, with the two conflicting transaction IDs.
///
/// Returns [`Error::Invalid`](error::Error::Invalid) if a linearization
/// checker (Prefix, Snapshot Isolation, Serializability) cannot find a valid
/// ordering.
///
/// [`DiGraph`]: crate::graph::digraph::DiGraph
pub fn check<Variable, Version>(
    sessions: &[Session<Variable, Version>],
    level: Consistency,
) -> Result<Witness, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone + Ord,
    Version: Eq + Hash + Clone,
{
    tracing::debug!(sessions = sessions.len(), ?level, "checking consistency");

    // Trivially consistent: no sessions or all sessions empty
    #[allow(clippy::redundant_closure_for_method_calls)]
    if sessions.is_empty() || sessions.iter().all(|s| s.is_empty()) {
        tracing::debug!("trivially consistent: no sessions or all empty");
        return Ok(Witness::CommitOrder(Vec::new()));
    }

    match level {
        Consistency::CommittedRead => check_committed_read(sessions).map(Witness::SaturationOrder),
        Consistency::AtomicRead => {
            check_atomic_read(sessions).map(|po| Witness::SaturationOrder(po.visibility_relation))
        }
        Consistency::Causal => {
            check_causal_read(sessions).map(|po| Witness::SaturationOrder(po.visibility_relation))
        }
        Consistency::Prefix => {
            let po = check_causal_read(sessions)?;
            let mut solver = PrefixConsistencySolver::from(po);
            solver
                .get_linearization()
                .map(|lin| {
                    Witness::CommitOrder(
                        lin.into_iter()
                            .filter(|(_, is_write)| *is_write)
                            .map(|(tid, _)| tid)
                            .collect(),
                    )
                })
                .ok_or(Error::Invalid(Consistency::Prefix))
        }
        Consistency::SnapshotIsolation => {
            let po = check_causal_read(sessions)?;
            let mut solver = SnapshotIsolationSolver::from(po);
            solver
                .get_linearization()
                .map(Witness::SplitCommitOrder)
                .ok_or(Error::Invalid(Consistency::SnapshotIsolation))
        }
        Consistency::Serializable => {
            let po = check_causal_read(sessions)?;
            let mut solver = SerializabilitySolver::from(po);
            solver
                .get_linearization()
                .map(Witness::CommitOrder)
                .ok_or(Error::Invalid(Consistency::Serializable))
        }
    }
}
