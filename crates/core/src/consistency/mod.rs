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

fn solve_npc_from_po<Variable, Version>(
    po: crate::history::atomic::AtomicTransactionPO<Variable>,
    level: Consistency,
) -> Result<Witness, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone + Ord,
    Version: Eq + Hash + Clone + Default,
{
    match level {
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
        _ => unreachable!("solve_npc_from_po called for non-NPC consistency level"),
    }
}

fn components_overlap(components: &[BTreeSet<u64>]) -> bool {
    let total_membership: usize = components.iter().map(BTreeSet::len).sum();
    let unique_membership: BTreeSet<u64> = components
        .iter()
        .flat_map(|component| component.iter().copied())
        .collect();
    total_membership != unique_membership.len()
}

/// Build the trivial NPC witness for a single-session history.
///
/// For one session, the transaction order is fixed by session order, so
/// Prefix/Serializable witnesses are the commit chain and Snapshot Isolation
/// is its split-phase expansion.
fn singleton_session_witness<Variable, Version>(
    session: &Session<Variable, Version>,
    level: Consistency,
) -> Witness {
    let commit_order: Vec<TransactionId> = (0..)
        .zip(session.iter())
        .map(|(session_height, _)| TransactionId {
            session_id: 1,
            session_height,
        })
        .collect();

    if matches!(level, Consistency::SnapshotIsolation) {
        Witness::SplitCommitOrder(
            commit_order
                .into_iter()
                .flat_map(|tid| [(tid, false), (tid, true)])
                .collect(),
        )
    } else {
        debug_assert!(matches!(
            level,
            Consistency::Prefix | Consistency::Serializable
        ));
        Witness::CommitOrder(commit_order)
    }
}

/// Check NP-complete consistency levels (Prefix, `SnapshotIsolation`, Serializable)
/// using biconnected-component decomposition of the communication graph
/// (Theorem 5.2 in Biswas & Enea 2019).
///
/// Decomposes the communication graph into biconnected components.
///
/// When components are disjoint, it checks and merges sub-witnesses directly.
/// When components overlap on articulation sessions, it pre-checks components
/// and then solves the full PO once to emit a non-duplicated global witness.
fn check_npc<Variable, Version>(
    sessions: &[Session<Variable, Version>],
    level: Consistency,
) -> Result<Witness, Error<Variable, Version>>
where
    Variable: Eq + Hash + Clone + Ord,
    Version: Eq + Hash + Clone + Default,
{
    let po = check_causal_read(sessions)?;

    if sessions.len() == 1 {
        return Ok(singleton_session_witness(&sessions[0], level));
    }

    let comm_graph = decomposition::communication_graph(&po);
    let all_components = decomposition::biconnected_components(&comm_graph);

    // Keep every component (including singletons) so the merged witness
    // always covers all sessions in the original history.
    let components_to_check: Vec<BTreeSet<u64>> = all_components;
    let has_overlap = components_overlap(&components_to_check);

    tracing::debug!(
        components = components_to_check.len(),
        overlap = has_overlap,
        sessions = sessions.len(),
        ?level,
        "communication graph decomposition"
    );

    // Single (or no) component: run DFS directly on the pre-built PO.
    if components_to_check.len() <= 1 {
        return solve_npc_from_po(po, level);
    }

    // Biconnected components overlap on articulation sessions. Direct
    // per-component projection can drop writer context for external reads, so
    // fall back to solving on the full PO and emit a non-duplicated witness.
    if has_overlap {
        return solve_npc_from_po(po, level);
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

        let sub_witness = if sub_sessions.len() == 1 {
            singleton_session_witness(&sub_sessions[0], level)
        } else {
            check_npc(&sub_sessions, level)?
        };
        let remapped = remap_witness(sub_witness, &session_ids);
        merged = merge_witnesses(merged, remapped);
    }

    Ok(merged)
}

#[cfg(test)]
mod tests {
    use alloc::vec;

    use super::*;
    use crate::history::raw::types::{Event, Transaction};

    type History = Vec<Vec<Transaction<&'static str, u64>>>;

    /// Build a two-cluster history: sessions {1,2} share var "x",
    /// sessions {3,4} share var "y". Completely independent clusters.
    fn two_cluster_history() -> History {
        vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
            vec![Transaction::committed(vec![Event::write("y", 1)])],
            vec![Transaction::committed(vec![Event::read("y", 1)])],
        ]
    }

    #[test]
    fn multi_component_prefix_pass() {
        let history = two_cluster_history();
        let result = check(&history, Consistency::Prefix);
        assert!(result.is_ok(), "expected pass, got: {result:?}");
        let Witness::CommitOrder(order) = result.unwrap() else {
            panic!("expected CommitOrder witness for Prefix");
        };
        // 4 transactions total across 2 independent clusters
        assert_eq!(
            order.len(),
            4,
            "expected 4 transactions in merged CommitOrder"
        );
        let ids: BTreeSet<u64> = order.iter().map(|tid| tid.session_id).collect();
        assert!(ids.contains(&1));
        assert!(ids.contains(&2));
        assert!(ids.contains(&3));
        assert!(ids.contains(&4));
    }

    #[test]
    fn multi_component_snapshot_isolation_pass() {
        let history = two_cluster_history();
        let result = check(&history, Consistency::SnapshotIsolation);
        assert!(result.is_ok(), "expected pass, got: {result:?}");
        let Witness::SplitCommitOrder(order) = result.unwrap() else {
            panic!("expected SplitCommitOrder witness for SnapshotIsolation");
        };
        // Each transaction has a read phase + write phase = 2 entries per txn,
        // 4 transactions = 8 entries total
        assert_eq!(
            order.len(),
            8,
            "expected 8 entries in merged SplitCommitOrder"
        );
        let ids: BTreeSet<u64> = order.iter().map(|(tid, _)| tid.session_id).collect();
        assert!(ids.contains(&1));
        assert!(ids.contains(&2));
        assert!(ids.contains(&3));
        assert!(ids.contains(&4));
    }

    #[test]
    fn multi_component_serializable_pass() {
        let history = two_cluster_history();
        let result = check(&history, Consistency::Serializable);
        assert!(result.is_ok(), "expected pass, got: {result:?}");
        let Witness::CommitOrder(order) = result.unwrap() else {
            panic!("expected CommitOrder witness for Serializable");
        };
        assert_eq!(
            order.len(),
            4,
            "expected 4 transactions in merged CommitOrder"
        );
        let ids: BTreeSet<u64> = order.iter().map(|tid| tid.session_id).collect();
        assert!(ids.contains(&1));
        assert!(ids.contains(&2));
        assert!(ids.contains(&3));
        assert!(ids.contains(&4));
    }

    #[test]
    fn multi_component_one_cluster_fails_serializable() {
        // Cluster 1: {1,2} share "x" (valid)
        // Cluster 2: {3,4,5} share "a","b" with write-skew (violates serializable)
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
            vec![Transaction::committed(vec![
                Event::write("a", 1),
                Event::write("b", 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read("a", 1),
                Event::write("b", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("b", 1),
                Event::write("a", 2),
            ])],
        ];
        let result = check(&history, Consistency::Serializable);
        assert!(
            result.is_err(),
            "expected serializable violation, got: {result:?}"
        );
    }

    #[test]
    fn multi_component_one_cluster_fails_snapshot_isolation() {
        // Cluster 1: {1,2} share "x" (valid)
        // Cluster 2: {3,4,5} share "a" with concurrent writes (violates SI)
        let history: History = vec![
            vec![Transaction::committed(vec![Event::write("x", 1)])],
            vec![Transaction::committed(vec![Event::read("x", 1)])],
            vec![Transaction::committed(vec![Event::write("a", 1)])],
            vec![Transaction::committed(vec![
                Event::read("a", 1),
                Event::write("a", 2),
            ])],
            vec![Transaction::committed(vec![
                Event::read("a", 1),
                Event::write("a", 3),
            ])],
        ];
        let result = check(&history, Consistency::SnapshotIsolation);
        assert!(result.is_err(), "expected SI violation, got: {result:?}");
    }

    #[test]
    fn singleton_session_witness_commit_order_for_prefix() {
        let session = vec![
            Transaction::committed(vec![Event::write("x", 1)]),
            Transaction::committed(vec![Event::read("x", 1), Event::write("y", 1)]),
        ];
        let witness = singleton_session_witness(&session, Consistency::Prefix);
        let Witness::CommitOrder(order) = witness else {
            panic!("expected CommitOrder");
        };
        assert_eq!(order.len(), 2);
        assert_eq!(order[0].session_id, 1);
        assert_eq!(order[0].session_height, 0);
        assert_eq!(order[1].session_id, 1);
        assert_eq!(order[1].session_height, 1);
    }

    #[test]
    fn singleton_session_witness_split_order_for_snapshot_isolation() {
        let session = vec![
            Transaction::committed(vec![Event::write("x", 1)]),
            Transaction::committed(vec![Event::read("x", 1), Event::write("y", 1)]),
        ];
        let witness = singleton_session_witness(&session, Consistency::SnapshotIsolation);
        let Witness::SplitCommitOrder(order) = witness else {
            panic!("expected SplitCommitOrder");
        };
        assert_eq!(order.len(), 4);
        assert!(!order[0].1);
        assert!(order[1].1);
        assert!(!order[2].1);
        assert!(order[3].1);
    }

    #[test]
    fn remap_witness_commit_order() {
        let witness = Witness::CommitOrder(vec![
            TransactionId {
                session_id: 1,
                session_height: 0,
            },
            TransactionId {
                session_id: 2,
                session_height: 0,
            },
        ]);
        // Remap: sub-session 1 -> original 3, sub-session 2 -> original 5
        let remapped = remap_witness(witness, &[3, 5]);
        let Witness::CommitOrder(order) = remapped else {
            panic!("expected CommitOrder");
        };
        assert_eq!(order[0].session_id, 3);
        assert_eq!(order[1].session_id, 5);
    }

    #[test]
    fn remap_witness_split_commit_order() {
        let witness = Witness::SplitCommitOrder(vec![
            (
                TransactionId {
                    session_id: 1,
                    session_height: 0,
                },
                false,
            ),
            (
                TransactionId {
                    session_id: 1,
                    session_height: 0,
                },
                true,
            ),
        ]);
        let remapped = remap_witness(witness, &[7]);
        let Witness::SplitCommitOrder(order) = remapped else {
            panic!("expected SplitCommitOrder");
        };
        assert_eq!(order[0].0.session_id, 7);
        assert!(!order[0].1);
        assert_eq!(order[1].0.session_id, 7);
        assert!(order[1].1);
    }

    #[test]
    fn remap_witness_preserves_root() {
        let witness = Witness::CommitOrder(vec![
            TransactionId {
                session_id: 0,
                session_height: 0,
            },
            TransactionId {
                session_id: 1,
                session_height: 0,
            },
        ]);
        let remapped = remap_witness(witness, &[42]);
        let Witness::CommitOrder(order) = remapped else {
            panic!("expected CommitOrder");
        };
        // Root (session_id=0) should be preserved
        assert_eq!(order[0].session_id, 0);
        assert_eq!(order[1].session_id, 42);
    }

    #[test]
    fn merge_witnesses_commit_order() {
        let a = Witness::CommitOrder(vec![TransactionId {
            session_id: 1,
            session_height: 0,
        }]);
        let b = Witness::CommitOrder(vec![TransactionId {
            session_id: 2,
            session_height: 0,
        }]);
        let merged = merge_witnesses(a, b);
        let Witness::CommitOrder(order) = merged else {
            panic!("expected CommitOrder");
        };
        assert_eq!(order.len(), 2);
    }

    #[test]
    fn merge_witnesses_split_commit_order() {
        let a = Witness::SplitCommitOrder(vec![(
            TransactionId {
                session_id: 1,
                session_height: 0,
            },
            false,
        )]);
        let b = Witness::SplitCommitOrder(vec![(
            TransactionId {
                session_id: 2,
                session_height: 0,
            },
            true,
        )]);
        let merged = merge_witnesses(a, b);
        let Witness::SplitCommitOrder(order) = merged else {
            panic!("expected SplitCommitOrder");
        };
        assert_eq!(order.len(), 2);
    }

    #[test]
    fn trivially_consistent_empty_sessions() {
        let empty: History = vec![];
        let result = check(&empty, Consistency::Serializable);
        assert!(result.is_ok());
        let Witness::CommitOrder(order) = result.unwrap() else {
            panic!("expected CommitOrder");
        };
        assert!(order.is_empty());
    }

    #[test]
    fn trivially_consistent_all_empty_sessions() {
        let history: History = vec![vec![], vec![]];
        let result = check(&history, Consistency::Prefix);
        assert!(result.is_ok());
        let Witness::CommitOrder(order) = result.unwrap() else {
            panic!("expected CommitOrder");
        };
        assert!(order.is_empty());
    }
}
