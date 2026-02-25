#![allow(clippy::missing_const_for_fn)]

//! Tests for communication graph decomposition in consistency checkers.

use dbcop_core::consistency::Witness;
use dbcop_core::history::raw::types::{Event, Transaction};
use dbcop_core::{check, Consistency};

type History = Vec<Vec<Transaction<&'static str, u64>>>;

fn session(txns: Vec<Transaction<&'static str, u64>>) -> Vec<Transaction<&'static str, u64>> {
    txns
}

fn two_clusters_plus_singleton_history() -> History {
    vec![
        // Cluster 1: sessions 1,2 share "x"
        session(vec![Transaction::committed(vec![Event::write("x", 1)])]),
        session(vec![Transaction::committed(vec![Event::read("x", 1)])]),
        // Cluster 2: sessions 3,4 share "y"
        session(vec![Transaction::committed(vec![Event::write("y", 1)])]),
        session(vec![Transaction::committed(vec![Event::read("y", 1)])]),
        // Singleton session 5: accesses only "z"
        session(vec![Transaction::committed(vec![Event::write("z", 1)])]),
    ]
}

fn single_session_two_txn_history() -> History {
    vec![session(vec![
        Transaction::committed(vec![Event::write("x", 1)]),
        Transaction::committed(vec![Event::read("x", 1), Event::write("y", 1)]),
    ])]
}

#[test]
fn decomposition_two_independent_clusters_serializable_pass() {
    // Sessions {1,2} share var "x"; sessions {3,4} share var "y".
    // The two clusters are completely independent -> decomposed into 2 components.
    let history: History = vec![
        session(vec![Transaction::committed(vec![Event::write("x", 1)])]),
        session(vec![Transaction::committed(vec![Event::read("x", 1)])]),
        session(vec![Transaction::committed(vec![Event::write("y", 1)])]),
        session(vec![Transaction::committed(vec![Event::read("y", 1)])]),
    ];
    let result = check(&history, Consistency::Serializable);
    assert!(result.is_ok(), "expected pass, got: {result:?}");
    // Witness must cover all 4 sessions (all original session IDs present).
    let Witness::CommitOrder(order) = result.unwrap() else {
        panic!("expected CommitOrder witness");
    };
    // Each session has 1 transaction so we expect 4 entries total.
    assert_eq!(
        order.len(),
        4,
        "expected 4 transactions in merged CommitOrder"
    );
    // All 4 original session IDs should appear.
    let ids: std::collections::HashSet<u64> = order.iter().map(|tid| tid.session_id).collect();
    assert!(ids.contains(&1));
    assert!(ids.contains(&2));
    assert!(ids.contains(&3));
    assert!(ids.contains(&4));
}

#[test]
fn decomposition_single_cluster_fallback_serializable_pass() {
    // All sessions share var "x" -> single connected component -> fallback to direct DFS.
    let history: History = vec![
        session(vec![Transaction::committed(vec![Event::write("x", 1)])]),
        session(vec![Transaction::committed(vec![Event::read("x", 1)])]),
        session(vec![Transaction::committed(vec![Event::read("x", 1)])]),
    ];
    let result = check(&history, Consistency::Serializable);
    assert!(result.is_ok(), "expected pass, got: {result:?}");
}

#[test]
fn decomposition_one_failing_cluster_serializable_fail() {
    // Sessions {1,2} share var "x" (valid).
    // Sessions {3,4,5} share vars "a"/"b" with write-skew (not serializable).
    // After decomposition, cluster {3,4,5} fails -> overall fails.
    //
    // Write-skew pattern:
    //   s3: write(a, 1), write(b, 1)
    //   s4: read(a, 1), write(b, 2)    <- reads a from s3, overwrites b
    //   s5: read(b, 1), write(a, 2)    <- reads b from s3, overwrites a
    // Both s4 and s5 see the values written by s3 but write conflicting values.
    // No serial order exists: s4<s5 requires s5 to see b=2 (contradicts read b=1);
    // s5<s4 requires s4 to see a=2 (contradicts read a=1).
    let history: History = vec![
        // Cluster 1: sessions 1,2 (share "x")
        session(vec![Transaction::committed(vec![Event::write("x", 1)])]),
        session(vec![Transaction::committed(vec![Event::read("x", 1)])]),
        // Cluster 2: sessions 3,4,5 (share "a","b") -- write skew
        session(vec![Transaction::committed(vec![
            Event::write("a", 1),
            Event::write("b", 1),
        ])]),
        session(vec![Transaction::committed(vec![
            Event::read("a", 1),
            Event::write("b", 2),
        ])]),
        session(vec![Transaction::committed(vec![
            Event::read("b", 1),
            Event::write("a", 2),
        ])]),
    ];
    let result = check(&history, Consistency::Serializable);
    assert!(
        result.is_err(),
        "expected serializable violation, got: {result:?}"
    );
}

#[test]
fn decomposition_preserves_singleton_serializable_witness() {
    let history = two_clusters_plus_singleton_history();
    let result = check(&history, Consistency::Serializable);
    assert!(result.is_ok(), "expected pass, got: {result:?}");
    let Witness::CommitOrder(order) = result.unwrap() else {
        panic!("expected CommitOrder witness");
    };
    assert_eq!(order.len(), 5, "expected all 5 transactions in witness");
    let ids: std::collections::HashSet<u64> = order.iter().map(|tid| tid.session_id).collect();
    assert_eq!(ids, [1, 2, 3, 4, 5].into());
}

#[test]
fn decomposition_preserves_singleton_prefix_witness() {
    let history = two_clusters_plus_singleton_history();
    let result = check(&history, Consistency::Prefix);
    assert!(result.is_ok(), "expected pass, got: {result:?}");
    let Witness::CommitOrder(order) = result.unwrap() else {
        panic!("expected CommitOrder witness");
    };
    assert_eq!(order.len(), 5, "expected all 5 transactions in witness");
    let ids: std::collections::HashSet<u64> = order.iter().map(|tid| tid.session_id).collect();
    assert_eq!(ids, [1, 2, 3, 4, 5].into());
}

#[test]
fn decomposition_preserves_singleton_snapshot_isolation_witness() {
    let history = two_clusters_plus_singleton_history();
    let result = check(&history, Consistency::SnapshotIsolation);
    assert!(result.is_ok(), "expected pass, got: {result:?}");
    let Witness::SplitCommitOrder(order) = result.unwrap() else {
        panic!("expected SplitCommitOrder witness");
    };
    assert_eq!(
        order.len(),
        10,
        "expected read/write phases for all 5 transactions",
    );
    let ids: std::collections::HashSet<u64> = order.iter().map(|(tid, _)| tid.session_id).collect();
    assert_eq!(ids, [1, 2, 3, 4, 5].into());
}

#[test]
fn single_session_prefix_witness_is_trivial_chain() {
    let history = single_session_two_txn_history();
    let result = check(&history, Consistency::Prefix);
    assert!(result.is_ok(), "expected pass, got: {result:?}");
    let Witness::CommitOrder(order) = result.unwrap() else {
        panic!("expected CommitOrder witness");
    };
    assert_eq!(
        order,
        vec![
            dbcop_core::history::atomic::types::TransactionId {
                session_id: 1,
                session_height: 0,
            },
            dbcop_core::history::atomic::types::TransactionId {
                session_id: 1,
                session_height: 1,
            },
        ]
    );
}

#[test]
fn single_session_snapshot_witness_is_trivial_split_chain() {
    let history = single_session_two_txn_history();
    let result = check(&history, Consistency::SnapshotIsolation);
    assert!(result.is_ok(), "expected pass, got: {result:?}");
    let Witness::SplitCommitOrder(order) = result.unwrap() else {
        panic!("expected SplitCommitOrder witness");
    };
    assert_eq!(
        order,
        vec![
            (
                dbcop_core::history::atomic::types::TransactionId {
                    session_id: 1,
                    session_height: 0,
                },
                false
            ),
            (
                dbcop_core::history::atomic::types::TransactionId {
                    session_id: 1,
                    session_height: 0,
                },
                true
            ),
            (
                dbcop_core::history::atomic::types::TransactionId {
                    session_id: 1,
                    session_height: 1,
                },
                false
            ),
            (
                dbcop_core::history::atomic::types::TransactionId {
                    session_id: 1,
                    session_height: 1,
                },
                true
            ),
        ]
    );
}

#[test]
fn single_session_serializable_witness_is_trivial_chain() {
    let history = single_session_two_txn_history();
    let result = check(&history, Consistency::Serializable);
    assert!(result.is_ok(), "expected pass, got: {result:?}");
    let Witness::CommitOrder(order) = result.unwrap() else {
        panic!("expected CommitOrder witness");
    };
    assert_eq!(
        order,
        vec![
            dbcop_core::history::atomic::types::TransactionId {
                session_id: 1,
                session_height: 0,
            },
            dbcop_core::history::atomic::types::TransactionId {
                session_id: 1,
                session_height: 1,
            },
        ]
    );
}
