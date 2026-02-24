use dbcop_core::consistency::Witness;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};
use dbcop_sat::{check_prefix, check_serializable, check_snapshot_isolation};
use dbcop_testgen::generator::generate_single_history;

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

/// Two independent 2-session clusters plus one singleton session.
fn two_clusters_plus_singleton_history() -> Vec<Session<&'static str, u64>> {
    vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![Event::read("x", 1)])],
        vec![Transaction::committed(vec![Event::write("y", 1)])],
        vec![Transaction::committed(vec![Event::read("y", 1)])],
        vec![Transaction::committed(vec![Event::write("z", 1)])],
    ]
}

fn single_session_two_txn_history() -> Vec<Session<&'static str, u64>> {
    vec![vec![
        Transaction::committed(vec![Event::write("x", 1)]),
        Transaction::committed(vec![Event::read("x", 1), Event::write("y", 1)]),
    ]]
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
    assert!(check_prefix(&h).is_ok(), "write skew should pass SAT-PC",);

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

#[test]
fn sat_prefix_witness_preserves_singleton_component() {
    let h = two_clusters_plus_singleton_history();
    let Witness::CommitOrder(order) = check_prefix(&h).expect("expected SAT-PC pass") else {
        panic!("expected CommitOrder witness");
    };
    assert_eq!(order.len(), 5, "expected all 5 transactions in witness");
    let ids: std::collections::HashSet<u64> = order.iter().map(|tid| tid.session_id).collect();
    assert_eq!(ids, [1, 2, 3, 4, 5].into());
}

#[test]
fn sat_snapshot_witness_preserves_singleton_component() {
    let h = two_clusters_plus_singleton_history();
    let Witness::SplitCommitOrder(order) =
        check_snapshot_isolation(&h).expect("expected SAT-SI pass")
    else {
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
fn sat_single_session_serializable_fast_path() {
    let h = single_session_two_txn_history();
    assert!(
        check_serializable(&h).is_ok(),
        "single-session histories should skip SAT search and pass after causal check",
    );
}

#[test]
fn sat_single_session_prefix_witness_is_trivial_chain() {
    let h = single_session_two_txn_history();
    let Witness::CommitOrder(order) = check_prefix(&h).expect("expected SAT-PC pass") else {
        panic!("expected CommitOrder witness");
    };
    assert_eq!(order.len(), 2, "expected both transactions in witness");
    assert_eq!(order[0].session_id, 1);
    assert_eq!(order[0].session_height, 0);
    assert_eq!(order[1].session_id, 1);
    assert_eq!(order[1].session_height, 1);
}

#[test]
fn sat_single_session_snapshot_witness_is_trivial_split_chain() {
    let h = single_session_two_txn_history();
    let Witness::SplitCommitOrder(order) =
        check_snapshot_isolation(&h).expect("expected SAT-SI pass")
    else {
        panic!("expected SplitCommitOrder witness");
    };
    assert_eq!(order.len(), 4, "expected split phases for two transactions");
    assert_eq!(order[0].0.session_id, 1);
    assert_eq!(order[0].0.session_height, 0);
    assert!(!order[0].1);
    assert_eq!(order[1].0.session_id, 1);
    assert_eq!(order[1].0.session_height, 0);
    assert!(order[1].1);
    assert_eq!(order[2].0.session_id, 1);
    assert_eq!(order[2].0.session_height, 1);
    assert!(!order[2].1);
    assert_eq!(order[3].0.session_id, 1);
    assert_eq!(order[3].0.session_height, 1);
    assert!(order[3].1);
}

#[test]
fn differential_fuzz_sat_vs_core_npc() {
    let samples = std::env::var("DBCOP_DIFF_FUZZ_SAMPLES")
        .ok()
        .and_then(|s| s.parse::<usize>().ok())
        .unwrap_or(256);

    let node_options = [1_u64, 3, 5];
    let var_options = [2_u64, 3, 4];
    let txn_options = [1_u64, 2, 3];
    let evt_options = [1_u64, 2, 3];

    for i in 0..samples {
        let n_node = node_options[i % node_options.len()];
        let n_var = var_options[i % var_options.len()];
        let n_txn = txn_options[i % txn_options.len()];
        let n_evt = evt_options[i % evt_options.len()];

        let h = generate_single_history(n_node, n_var, n_txn, n_evt);

        let core_ser = check(&h, Consistency::Serializable).is_ok();
        let sat_ser = check_serializable(&h).is_ok();
        assert_eq!(
            core_ser, sat_ser,
            "SER mismatch on sample {i} (n_node={n_node}, n_var={n_var}, n_txn={n_txn}, n_evt={n_evt})",
        );

        let core_pc = check(&h, Consistency::Prefix).is_ok();
        let sat_pc = check_prefix(&h).is_ok();
        assert_eq!(
            core_pc, sat_pc,
            "PC mismatch on sample {i} (n_node={n_node}, n_var={n_var}, n_txn={n_txn}, n_evt={n_evt})",
        );

        let core_si = check(&h, Consistency::SnapshotIsolation).is_ok();
        let sat_si = check_snapshot_isolation(&h).is_ok();
        assert_eq!(
            core_si, sat_si,
            "SI mismatch on sample {i} (n_node={n_node}, n_var={n_var}, n_txn={n_txn}, n_evt={n_evt})",
        );
    }
}
