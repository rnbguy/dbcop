use alloc::collections::BTreeSet;
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
use crate::history::atomic::types::TransactionId;
use crate::history::raw::types::Session;

pub mod decomposition;
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
    Version: Eq + Hash + Clone + Default,
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
        Consistency::Prefix | Consistency::SnapshotIsolation | Consistency::Serializable => {
            check_npc(sessions, level)
        }
    }
}

/// Remap witness `TransactionId`s from sub-history session IDs back to the
/// original session IDs.
///
/// `session_ids[i]` is the original session ID corresponding to the
/// sub-history session ID `i + 1`.
fn remap_witness(witness: Witness, session_ids: &[u64]) -> Witness {
    let remap = |tid: TransactionId| -> TransactionId {
        if tid.session_id == 0 {
            return tid;
        }
        #[allow(clippy::cast_possible_truncation)]
        TransactionId {
            session_id: session_ids[tid.session_id as usize - 1],
            session_height: tid.session_height,
        }
    };
    match witness {
        Witness::CommitOrder(v) => Witness::CommitOrder(v.into_iter().map(remap).collect()),
        Witness::SplitCommitOrder(v) => {
            Witness::SplitCommitOrder(v.into_iter().map(|(tid, b)| (remap(tid), b)).collect())
        }
        Witness::SaturationOrder(_) => {
            unreachable!("SaturationOrder is not produced by NPC linearization checkers")
        }
    }
}

/// Merge two witnesses of the same variant by concatenation.
fn merge_witnesses(base: Witness, other: Witness) -> Witness {
    match (base, other) {
        (Witness::CommitOrder(mut a), Witness::CommitOrder(b)) => {
            a.extend(b);
            Witness::CommitOrder(a)
        }
        (Witness::SplitCommitOrder(mut a), Witness::SplitCommitOrder(b)) => {
            a.extend(b);
            Witness::SplitCommitOrder(a)
        }
        _ => unreachable!("mismatched witness variants during merge"),
    }
}

/// Check NP-complete consistency levels (Prefix, `SnapshotIsolation`, Serializable)
/// using connected-component decomposition of the communication graph
/// (Theorem 5.2 in Biswas & Enea 2019).
///
/// Decomposes the communication graph into connected components and checks
/// each independently, then remaps and merges the witnesses.
fn check_npc<Variable, Version>(
    sessions: &[Session<Variable, Version>],
    level: Consistency,
) -> Result<Witness, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone + Ord,
    Version: Eq + Hash + Clone + Default,
{
    let po = check_causal_read(sessions)?;

    let comm_graph = decomposition::communication_graph(&po);
    let all_components = decomposition::connected_components(&comm_graph);

    // Only non-trivial components (>= 2 sessions) require a consistency check.
    // Singleton sessions are trivially consistent after the causal check.
    let components_to_check: Vec<BTreeSet<u64>> = all_components
        .into_iter()
        .filter(|c| c.len() >= 2)
        .collect();

    tracing::debug!(
        components = components_to_check.len(),
        sessions = sessions.len(),
        ?level,
        "communication graph decomposition"
    );

    // Single (or no) non-trivial component: run DFS directly on the pre-built PO.
    if components_to_check.len() <= 1 {
        return match level {
            Consistency::Prefix => {
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
                let mut solver = SnapshotIsolationSolver::from(po);
                solver
                    .get_linearization()
                    .map(Witness::SplitCommitOrder)
                    .ok_or(Error::Invalid(Consistency::SnapshotIsolation))
            }
            Consistency::Serializable => {
                let mut solver = SerializabilitySolver::from(po);
                solver
                    .get_linearization()
                    .map(Witness::CommitOrder)
                    .ok_or(Error::Invalid(Consistency::Serializable))
            }
            _ => unreachable!("check_npc called for non-NPC consistency level"),
        };
    }

    // Initial empty witness of the correct variant for this level.
    let mut merged: Witness = match level {
        Consistency::SnapshotIsolation => Witness::SplitCommitOrder(Vec::new()),
        _ => Witness::CommitOrder(Vec::new()),
    };

    // Check each connected component independently and accumulate witnesses.
    for component in components_to_check {
        let session_ids: Vec<u64> = component.iter().copied().collect();
        #[allow(clippy::cast_possible_truncation)]
        let sub_sessions: Vec<Session<Variable, Version>> = session_ids
            .iter()
            .map(|&sid| sessions[sid as usize - 1].clone())
            .collect();

        let sub_witness = check_npc(&sub_sessions, level)?;
        let remapped = remap_witness(sub_witness, &session_ids);
        merged = merge_witnesses(merged, remapped);
    }

    Ok(merged)
}
