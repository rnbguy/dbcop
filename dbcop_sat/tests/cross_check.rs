use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};
use dbcop_sat::{check_prefix, check_serializable, check_snapshot_isolation};

// ---------------------------------------------------------------------------
// Agreement helpers
// ---------------------------------------------------------------------------

/// Assert that the DFS (core) and SAT solvers agree on serializability.
fn assert_agree_ser(sessions: &[Session<&str, u64>], label: &str) {
    let core = check(sessions, Consistency::Serializable);
    let sat = check_serializable(sessions);
    assert_eq!(
        core.is_ok(),
        sat.is_ok(),
        "SAT and DFS disagree on SER for '{label}': core={core:?} sat={sat:?}",
    );
}

/// Assert that the DFS (core) and SAT solvers agree on prefix consistency.
fn assert_agree_pc(sessions: &[Session<&str, u64>], label: &str) {
    let core = check(sessions, Consistency::Prefix);
    let sat = check_prefix(sessions);
    assert_eq!(
        core.is_ok(),
        sat.is_ok(),
        "SAT and DFS disagree on PC for '{label}': core={core:?} sat={sat:?}",
    );
}

/// Assert that the DFS (core) and SAT solvers agree on snapshot isolation.
fn assert_agree_si(sessions: &[Session<&str, u64>], label: &str) {
    let core = check(sessions, Consistency::SnapshotIsolation);
    let sat = check_snapshot_isolation(sessions);
    assert_eq!(
        core.is_ok(),
        sat.is_ok(),
        "SAT and DFS disagree on SI for '{label}': core={core:?} sat={sat:?}",
    );
}

// ---------------------------------------------------------------------------
// Common histories
// ---------------------------------------------------------------------------

/// Simple serial history: T0 writes x,y then T1 reads them.
fn serial_history() -> Vec<Session<&'static str, u64>> {
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

/// Write-skew history: disjoint write sets, anti-dependency cycle.
/// SI allows, SER forbids.
fn write_skew_history() -> Vec<Session<&'static str, u64>> {
    vec![
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
    ]
}

/// Concurrent-writes history: two transactions both write x.
/// Overlapping write sets → SI and SER both forbid.
fn concurrent_writes_history() -> Vec<Session<&'static str, u64>> {
    vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 3),
        ])],
    ]
}

/// A clearly serializable chain: T0->T1->T2 with no concurrency.
fn chain_history() -> Vec<Session<&'static str, u64>> {
    vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::read("y", 1),
        ])],
    ]
}

/// A history that violates prefix consistency.
///
/// T0: w(a,1)
/// T1: r(a,1), w(b,1)
/// T2: r(b,1) r(a, init)   ← sees T1's write on b but misses T0's write on a
///
/// T2 must see T1 (reads b from T1), and must therefore also see T0 (T1 saw T0).
/// But T2 reads a as uninitialized → prefix violation.
fn pc_violation_history() -> Vec<Session<&'static str, u64>> {
    vec![
        vec![Transaction::committed(vec![Event::write("a", 1)])],
        vec![Transaction::committed(vec![
            Event::read("a", 1),
            Event::write("b", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("b", 1),
            Event::read_empty("a"), // sees T1 but misses T0
        ])],
    ]
}

// ---------------------------------------------------------------------------
// Serializability cross-checks
// ---------------------------------------------------------------------------

#[test]
fn cross_check_ser_pass() {
    let h = serial_history();
    assert_agree_ser(&h, "serial");
    assert!(
        check(&h, Consistency::Serializable).is_ok(),
        "serial history must pass SER",
    );
    assert!(
        check_serializable(&h).is_ok(),
        "serial history must pass SAT-SER",
    );
}

#[test]
fn cross_check_ser_violation() {
    let h = write_skew_history();
    assert_agree_ser(&h, "write-skew");
    assert!(
        check(&h, Consistency::Serializable).is_err(),
        "write skew must fail SER",
    );
    assert!(
        check_serializable(&h).is_err(),
        "write skew must fail SAT-SER",
    );
}

// ---------------------------------------------------------------------------
// Prefix consistency cross-checks
// ---------------------------------------------------------------------------

#[test]
fn cross_check_pc_pass() {
    let h = serial_history();
    assert_agree_pc(&h, "serial");
    assert!(
        check(&h, Consistency::Prefix).is_ok(),
        "serial history must pass PC",
    );
    assert!(check_prefix(&h).is_ok(), "serial history must pass SAT-PC");
}

#[test]
fn cross_check_pc_violation() {
    let h = pc_violation_history();
    assert_agree_pc(&h, "pc-violation");
    assert!(
        check(&h, Consistency::Prefix).is_err(),
        "prefix violation history must fail PC",
    );
    assert!(
        check_prefix(&h).is_err(),
        "prefix violation history must fail SAT-PC",
    );
}

// ---------------------------------------------------------------------------
// Snapshot isolation cross-checks
// ---------------------------------------------------------------------------

#[test]
fn cross_check_si_pass() {
    let h = serial_history();
    assert_agree_si(&h, "serial");
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_ok(),
        "serial history must pass SI",
    );
    assert!(
        check_snapshot_isolation(&h).is_ok(),
        "serial history must pass SAT-SI",
    );
}

#[test]
fn cross_check_si_violation() {
    let h = concurrent_writes_history();
    assert_agree_si(&h, "concurrent-writes");
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_err(),
        "concurrent writes must fail SI",
    );
    assert!(
        check_snapshot_isolation(&h).is_err(),
        "concurrent writes must fail SAT-SI",
    );
}

// ---------------------------------------------------------------------------
// Semantic boundary: write skew passes PC+SI but fails SER (both agree)
// ---------------------------------------------------------------------------

#[test]
fn cross_check_write_skew() {
    let h = write_skew_history();

    // Both solvers must agree that write skew passes PC
    assert_agree_pc(&h, "write-skew-pc");
    assert!(
        check(&h, Consistency::Prefix).is_ok(),
        "write skew should pass PC",
    );
    assert!(
        check_prefix(&h).is_ok(),
        "write skew should pass SAT-PC",
    );

    // Both solvers must agree that write skew passes SI
    assert_agree_si(&h, "write-skew-si");
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_ok(),
        "write skew should pass SI (disjoint write sets)",
    );
    assert!(
        check_snapshot_isolation(&h).is_ok(),
        "write skew should pass SAT-SI",
    );

    // Both solvers must agree that write skew fails SER
    assert_agree_ser(&h, "write-skew-ser");
    assert!(
        check(&h, Consistency::Serializable).is_err(),
        "write skew should fail SER",
    );
    assert!(
        check_serializable(&h).is_err(),
        "write skew should fail SAT-SER",
    );
}

// ---------------------------------------------------------------------------
// Semantic boundary: concurrent writes fail SI+SER (both agree)
// ---------------------------------------------------------------------------

#[test]
fn cross_check_concurrent_writes() {
    let h = concurrent_writes_history();

    // Both solvers must agree that concurrent writes fail SI
    assert_agree_si(&h, "concurrent-writes-si");
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_err(),
        "concurrent writes should fail SI",
    );
    assert!(
        check_snapshot_isolation(&h).is_err(),
        "concurrent writes should fail SAT-SI",
    );

    // Both solvers must agree that concurrent writes fail SER
    assert_agree_ser(&h, "concurrent-writes-ser");
    assert!(
        check(&h, Consistency::Serializable).is_err(),
        "concurrent writes should fail SER",
    );
    assert!(
        check_serializable(&h).is_err(),
        "concurrent writes should fail SAT-SER",
    );
}

// ---------------------------------------------------------------------------
// Chain history cross-checks (extra coverage)
// ---------------------------------------------------------------------------

#[test]
fn cross_check_chain_all_levels() {
    let h = chain_history();
    assert_agree_ser(&h, "chain");
    assert_agree_pc(&h, "chain");
    assert_agree_si(&h, "chain");
}
