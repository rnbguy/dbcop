#![allow(clippy::doc_markdown)]

/// Integration tests for version-0 reads mapping to the initial state (Bug 3 fix).
///
/// `x==0` (version Some(0) where 0 == Default::default()) is treated as reading
/// the initial (pre-history) value when no explicit `x:=0` write exists -- same
/// as `x==?` (version None).
mod common;

use dbcop_core::{check, Consistency};

/// [x==0 x:=1] / [x==0 x:=2]: two sessions independently read initial x
/// and each write a different version. No causal dependency between them -> PASS.
#[test]
fn version_zero_independent_sessions_pass_causal() {
    let h = history! {
        [{ r(x, 0), w(x, 1) }],
        [{ r(x, 0), w(x, 2) }],
    };
    let result = check(&h, Consistency::Causal);
    assert!(
        result.is_ok(),
        "two sessions reading initial x==0 independently should pass causal: {result:?}",
    );
}

/// Same lost-update history fails SI: both sessions snapshot at initial x=0
/// and write, so one update is lost.
#[test]
fn version_zero_independent_sessions_fail_snapshot_isolation() {
    let h = history! {
        [{ r(x, 0), w(x, 1) }],
        [{ r(x, 0), w(x, 2) }],
    };
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_err(),
        "lost update (two sessions write x from initial state) should fail SI",
    );
}

/// Same lost-update history also fails serializable.
#[test]
fn version_zero_independent_sessions_fail_serializable() {
    let h = history! {
        [{ r(x, 0), w(x, 1) }],
        [{ r(x, 0), w(x, 2) }],
    };
    assert!(
        check(&h, Consistency::Serializable).is_err(),
        "lost update should also fail serializable",
    );
}

/// Single session reading initial x==0 passes all consistency levels.
#[test]
fn version_zero_single_session_passes_all_levels() {
    let h = history! {
        [{ r(x, 0), w(x, 1) }],
    };
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
            "single session reading initial x==0 should pass {level:?}",
        );
    }
}

/// When an explicit x:=0 write exists (committed), x==0 reads use that
/// writer -- not the root. Normal write-read chain.
#[test]
fn version_zero_with_explicit_writer_passes_causal() {
    let h = history! {
        [{ w(x, 0) }],           // explicit x:=0 (committed)
        [{ r(x, 0), w(x, 1) }],  // reads from explicit write
    };
    assert!(
        check(&h, Consistency::Causal).is_ok(),
        "x==0 with explicit committed x:=0 writer should pass causal",
    );
}

/// `x==0` (Some(0)) and `x==?` (None) behave identically for initial-state reads.
#[test]
fn version_zero_and_none_are_equivalent_for_initial_state() {
    use dbcop_core::history::raw::types::{Event, Transaction};

    // Two histories: same structure but one uses x==0, the other x==?.
    let h_zero = vec![vec![Transaction::committed(vec![
        Event::<&str, u64>::read("x", 0),
        Event::<&str, u64>::write("x", 1),
    ])]];
    let h_none = vec![vec![Transaction::committed(vec![
        Event::<&str, u64>::read_empty("x"),
        Event::<&str, u64>::write("x", 1),
    ])]];

    for level in [
        Consistency::CommittedRead,
        Consistency::AtomicRead,
        Consistency::Causal,
    ] {
        let r_zero = check(&h_zero, level);
        let r_none = check(&h_none, level);
        assert!(
            r_zero.is_ok() == r_none.is_ok(),
            "x==0 and x==? should behave identically at {level:?}: zero={r_zero:?}, none={r_none:?}",
        );
    }
}
