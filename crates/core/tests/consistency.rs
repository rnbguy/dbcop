#![allow(clippy::doc_markdown)]

use dbcop_core::consistency::atomic_read::check_atomic_read;
use dbcop_core::consistency::causal::check_causal_read;
use dbcop_core::consistency::committed_read::check_committed_read;
use dbcop_core::consistency::constrained_linearization::ConstrainedLinearizationSolver;
use dbcop_core::consistency::error::Error;
use dbcop_core::consistency::prefix::PrefixConsistencySolver;
use dbcop_core::consistency::repeatable_read::check_repeatable_read;
use dbcop_core::consistency::serializable::SerializabilitySolver;
use dbcop_core::consistency::snapshot_isolation::SnapshotIsolationSolver;
use dbcop_core::history::atomic::types::AtomicTransactionHistory;
use dbcop_core::history::atomic::AtomicTransactionPO;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};

/// A trivially valid history: one writer, one reader.
/// Should pass all consistency levels.
fn simple_valid_history() -> Vec<Session<&'static str, u64>> {
    vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::read("y", 1),
        ])],
    ]
}

/// Helper: build AtomicTransactionPO from sessions that are known to pass repeatable read.
fn build_atomic_po<V, W>(histories: &[Session<V, W>]) -> Result<AtomicTransactionPO<V>, Error<V, W>>
where
    V: Eq + std::hash::Hash + Clone,
    W: Eq + std::hash::Hash + Clone + Default,
{
    let atomic_history = AtomicTransactionHistory::try_from(histories)?;
    let mut po = AtomicTransactionPO::from(atomic_history);
    // Reproduce causal's visibility fixpoint so the PO is fully resolved
    po.vis_includes(&po.get_wr());
    loop {
        po.vis_is_trans();
        let ww_rel = po.causal_ww();
        let mut changed = false;
        for ww_x in ww_rel.values() {
            changed |= po.vis_includes(ww_x);
        }
        if !changed {
            break;
        }
    }
    Ok(po)
}

// -- Committed Read ------------------------------------------------------

#[test]
fn committed_read_pass() {
    let h = simple_valid_history();
    assert!(check_committed_read(&h).is_ok());
}

#[test]
fn committed_read_violation() {
    // Cycle in committed order: S3 reads x=3 (from S2) then x=2 (from S1),
    // but S2 reads y=1 (from S1), creating S1->S2 and S2->S1 via S3.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 2),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::write("x", 3),
            Event::read("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 3),
            Event::read("x", 2),
        ])],
    ];

    let result = check_committed_read(&h);
    assert!(
        matches!(
            result,
            Err(Error::Cycle {
                level: Consistency::CommittedRead,
                ..
            })
        ),
        "expected committed read violation, got {result:?}",
    );
}

// -- Repeatable Read -----------------------------------------------------

#[test]
fn repeatable_read_pass() {
    let h = simple_valid_history();
    assert!(check_repeatable_read(&h).is_ok());
}

#[test]
fn repeatable_read_violation() {
    // Within one transaction, reads x=2 then x=3 -- two different versions.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![Event::write("x", 2)])],
        vec![Transaction::committed(vec![Event::write("x", 3)])],
        vec![Transaction::committed(vec![
            Event::read("x", 2),
            Event::read("x", 3),
        ])],
    ];

    let result = check_repeatable_read(&h);
    assert!(result.is_err(), "expected repeatable read violation");
}

// -- Atomic Read ---------------------------------------------------------

#[test]
fn atomic_read_pass() {
    let h = simple_valid_history();
    assert!(check_atomic_read(&h).is_ok());
}

#[test]
fn atomic_read_violation() {
    // Fractured visibility history:
    // s1: write x=1,y=1
    // s2: read y=1, write x=2,z=1
    // s3: read x=1, read z=1
    // AtomicRead should fail due a visibility cycle after ww saturation.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("y", 1),
            Event::write("x", 2),
            Event::write("z", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::read("z", 1),
        ])],
    ];

    let result = check_atomic_read(&h);
    assert!(
        matches!(
            result,
            Err(Error::Cycle {
                level: Consistency::AtomicRead,
                ..
            })
        ),
        "expected atomic read violation, got {result:?}",
    );
}

// -- Causal --------------------------------------------------------------

#[test]
fn causal_pass() {
    let h = simple_valid_history();
    assert!(check_causal_read(&h).is_ok());
}

#[test]
fn causal_violation() {
    // 7-session chain: passes atomic read but violates causal.
    // The chain creates a transitive dependency cycle when visibility is closed.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::write("a", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("y", 1),
            Event::write("z", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("z", 1),
            Event::write("a", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("a", 2),
            Event::write("p", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("p", 1),
            Event::write("q", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("q", 1),
            Event::read("a", 1),
        ])],
    ];

    assert!(check_atomic_read(&h).is_ok(), "should pass atomic read");
    assert!(
        matches!(
            check_causal_read(&h),
            Err(Error::Cycle {
                level: Consistency::Causal,
                ..
            })
        ),
        "should violate causal",
    );
}

// -- Serializable (linearization) ----------------------------------------

#[test]
fn serializable_pass() {
    let h = simple_valid_history();
    let po = build_atomic_po(&h).expect("should build atomic PO");
    let mut solver = SerializabilitySolver::from(po);
    assert!(
        solver.get_linearization().is_some(),
        "simple history should be serializable",
    );
}

#[test]
fn serializable_violation() {
    // Write skew: T1 writes x,y. T2 reads x from T1, writes y. T3 reads y from T1, writes x.
    // T2 and T3 are concurrent and both read stale data -- not serializable.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("y", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("y", 1),
            Event::write("x", 2),
        ])],
    ];

    let po = build_atomic_po(&h).expect("should build atomic PO (passes causal)");
    let mut solver = SerializabilitySolver::from(po);
    assert!(
        solver.get_linearization().is_none(),
        "write skew should not be serializable",
    );
}

// -- Snapshot Isolation (linearization) -----------------------------------

#[test]
fn snapshot_isolation_pass() {
    let h = simple_valid_history();
    let po = build_atomic_po(&h).expect("should build atomic PO");
    let mut solver = SnapshotIsolationSolver::from(po);
    assert!(
        solver.get_linearization().is_some(),
        "simple history should satisfy SI",
    );
}

// -- Prefix (linearization) ----------------------------------------------

#[test]
fn prefix_pass() {
    let h = simple_valid_history();
    let po = build_atomic_po(&h).expect("should build atomic PO");
    let mut solver = PrefixConsistencySolver::from(po);
    assert!(
        solver.get_linearization().is_some(),
        "simple history should satisfy prefix consistency",
    );
}

// -- Unified check() API ------------------------------------------------

#[test]
fn check_dispatch_all_pass() {
    let h = simple_valid_history();
    for level in [
        Consistency::CommittedRead,
        Consistency::AtomicRead,
        Consistency::Causal,
        Consistency::Prefix,
        Consistency::SnapshotIsolation,
        Consistency::Serializable,
    ] {
        assert!(
            check(&h, level).is_ok(),
            "simple history should pass {level:?}",
        );
    }
}

#[test]
fn check_dispatch_serializable_violation() {
    // Write skew via check() dispatch
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("y", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("y", 1),
            Event::write("x", 2),
        ])],
    ];

    assert!(
        check(&h, Consistency::Causal).is_ok(),
        "write skew should pass causal",
    );
    assert!(
        check(&h, Consistency::Serializable).is_err(),
        "write skew should fail serializable",
    );
}

#[test]
fn check_dispatch_causal_violation() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::write("a", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("y", 1),
            Event::write("z", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("z", 1),
            Event::write("a", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("a", 2),
            Event::write("p", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("p", 1),
            Event::write("q", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("q", 1),
            Event::read("a", 1),
        ])],
    ];

    assert!(
        check(&h, Consistency::AtomicRead).is_ok(),
        "should pass atomic read via check()",
    );
    assert!(
        check(&h, Consistency::Causal).is_err(),
        "should fail causal via check()",
    );
}

#[test]
fn test_empty_history_is_valid() {
    let empty: Vec<Session<&str, u64>> = vec![];
    for level in [
        Consistency::CommittedRead,
        Consistency::AtomicRead,
        Consistency::Causal,
        Consistency::Prefix,
        Consistency::SnapshotIsolation,
        Consistency::Serializable,
    ] {
        assert!(
            check(&empty, level).is_ok(),
            "empty history should pass {level:?}",
        );
    }
}

#[test]
fn test_all_empty_sessions_is_valid() {
    let all_empty: Vec<Session<&str, u64>> = vec![vec![], vec![], vec![]];
    for level in [
        Consistency::CommittedRead,
        Consistency::AtomicRead,
        Consistency::Causal,
        Consistency::Prefix,
        Consistency::SnapshotIsolation,
        Consistency::Serializable,
    ] {
        assert!(
            check(&all_empty, level).is_ok(),
            "all-empty sessions should pass {level:?}",
        );
    }
}
