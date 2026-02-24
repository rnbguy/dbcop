//! Tests enforcing the strict consistency hierarchy:
//!   CommittedRead < AtomicRead < Causal < Prefix < SnapshotIsolation < Serializable
//!
//! Each test constructs a concrete history that passes the weaker level
//! but fails the stronger one, documenting the exact boundary.

use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};

// -- helpers ------------------------------------------------------------------

fn w(var: &'static str, ver: u64) -> Event<&'static str, u64> {
    Event::write(var, ver)
}

fn r(var: &'static str, ver: u64) -> Event<&'static str, u64> {
    Event::read(var, ver)
}

fn committed(events: Vec<Event<&'static str, u64>>) -> Transaction<&'static str, u64> {
    Transaction::committed(events)
}

fn uncommitted(events: Vec<Event<&'static str, u64>>) -> Transaction<&'static str, u64> {
    Transaction::uncommitted(events)
}

// -- Boundary 0: Below CommittedRead (dirty read) ---------------------------

/// Dirty read: S0 writes x=1 but does NOT commit. S1 reads x=1 from the
/// uncommitted write. CommittedRead requires all reads to come from committed
/// writes -- this violates that invariant and must fail.
///
/// S0: w(x,1)     -- UNCOMMITTED (dirty write)
/// S1: r(x,1)     -- dirty read: x=1 was never committed
///
/// RC fail: S1 reads x=1 which was only written by an uncommitted transaction.
#[test]
fn below_committed_read() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![uncommitted(vec![w("x", 1)])],
        vec![committed(vec![r("x", 1)])],
    ];

    // CommittedRead must fail on dirty reads
    assert!(
        check(&h, Consistency::CommittedRead).is_err(),
        "should fail CommittedRead (dirty read from uncommitted write)",
    );
}

// -- Boundary 1: CommittedRead pass, AtomicRead fail --------------------------

/// Fractured visibility: T1 writes both x and y atomically. T2 sees T1's
/// write to x (via y) but reads the initial value of x from T1's co-write.
/// A third session T3 reads the stale x from T1 while seeing T2's z, creating
/// a causal ww cycle visible only at the AtomicRead level.
///
/// T1: w(x,1) w(y,1)
/// T2: r(y,1) w(x,2) w(z,1)   -- sees T1 via y, overwrites x, writes z
/// T3: r(x,1) r(z,1)           -- reads stale x from T1, z from T2
///
/// RC: all reads from committed writes, committed order acyclic -- pass.
/// AR: causal_ww on x yields ww(T2,T1) but WR(y) gives vis(T1->T2) -- cycle -- fail.
#[test]
fn boundary_committed_read_to_atomic_read() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![committed(vec![w("x", 1), w("y", 1)])],
        vec![committed(vec![r("y", 1), w("x", 2), w("z", 1)])],
        vec![committed(vec![r("x", 1), r("z", 1)])],
    ];

    // Weaker level passes
    assert!(
        check(&h, Consistency::CommittedRead).is_ok(),
        "should pass CommittedRead (all reads from committed writes, acyclic CO)",
    );
    // Stronger level fails
    assert!(
        check(&h, Consistency::AtomicRead).is_err(),
        "should fail AtomicRead (causal ww cycle: T1->T2 via WR(y), T2->T1 via ww(x))",
    );
}

// -- Boundary 2: AtomicRead pass, Causal fail ---------------------------------

/// A 7-session causal visibility cycle that only manifests after transitive
/// closure of ww edges -- invisible to AtomicRead's one-shot ww check.
///
/// S1: w(x,1) w(a,1)
/// S2: r(x,1) w(y,1)        -- WR: S1->S2
/// S3: r(y,1) w(z,1)        -- WR: S2->S3
/// S4: r(z,1) w(a,2)        -- WR: S3->S4; overwrites S1's a
/// S5: r(a,2) w(p,1)        -- WR: S4->S5
/// S6: r(p,1) w(q,1)        -- WR: S5->S6
/// S7: r(q,1) r(a,1)        -- WR: S6->S7 (q); reads stale a=1 from S1
///
/// AR: the ww(S4,S1) edge on variable a is NOT cyclic without transitive
///     closure of visibility -- pass.
/// CC: transitive vis S4->S5->S6->S7; S7 reads a from S1; S4 writes a;
///     causal_ww derives ww(S4,S1). But S1 <vis S4 (S1->S2->S3->S4) -- cycle -- fail.
#[test]
fn boundary_atomic_read_to_causal() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![committed(vec![w("x", 1), w("a", 1)])],
        vec![committed(vec![r("x", 1), w("y", 1)])],
        vec![committed(vec![r("y", 1), w("z", 1)])],
        vec![committed(vec![r("z", 1), w("a", 2)])],
        vec![committed(vec![r("a", 2), w("p", 1)])],
        vec![committed(vec![r("p", 1), w("q", 1)])],
        vec![committed(vec![r("q", 1), r("a", 1)])],
    ];

    // Weaker level passes
    assert!(
        check(&h, Consistency::AtomicRead).is_ok(),
        "should pass AtomicRead (ww cycle needs transitive closure)",
    );
    // Stronger level fails
    assert!(
        check(&h, Consistency::Causal).is_err(),
        "should fail Causal (visibility cycle via transitive ww on variable a)",
    );
}

// -- Boundary 3: Causal pass, Prefix fail ------------------------------------

/// Two-variable stale-read crossover: each reader sees one updated variable
/// and one stale variable, creating contradictory prefix ordering constraints.
///
/// S0: w(x,1) w(y,1)        -- initial writes
/// S1: r(x,1) w(x,2)        -- reads x from S0, updates x
/// S2: r(y,1) w(y,2)        -- reads y from S0, updates y
/// S3: r(x,2) r(y,1)        -- sees S1's x but S0's stale y
/// S4: r(y,2) r(x,1)        -- sees S2's y but S0's stale x
///
/// CC pass:
///   WR edges: S0->S1(x), S0->S2(y), S1->S3(x), S0->S3(y), S2->S4(y), S0->S4(x).
///   After transitive closure: S0->all, S1->S3, S2->S4.
///   causal_ww on x (writers S0,S1): S0 already precedes S1, no new edge.
///   causal_ww on y (writers S0,S2): S0 already precedes S2, no new edge.
///   Crucially, S1 and S2 are on different variables, so no cross-ww.
///   No S1<->S2 causal path exists -- no cycle.
///
/// PC fail:
///   S3 reads x=2 from S1, so S1 < S3 in commit order.
///   S3 reads y=1 (stale), so S2 must NOT be in S3's prefix: S3 < S2.
///   S4 reads y=2 from S2, so S2 < S4 in commit order.
///   S4 reads x=1 (stale), so S1 must NOT be in S4's prefix: S4 < S1.
///   Chain: S1 < S3 < S2 < S4 < S1 -- cycle, no valid total order.
#[test]
fn boundary_causal_to_prefix() {
    let h: Vec<Session<&str, u64>> = vec![
        // S0: initial writes for both variables
        vec![committed(vec![w("x", 1), w("y", 1)])],
        // S1: reads x from S0, updates x to 2
        vec![committed(vec![r("x", 1), w("x", 2)])],
        // S2: reads y from S0, updates y to 2
        vec![committed(vec![r("y", 1), w("y", 2)])],
        // S3: sees updated x from S1, stale y from S0
        vec![committed(vec![r("x", 2), r("y", 1)])],
        // S4: sees updated y from S2, stale x from S0
        vec![committed(vec![r("y", 2), r("x", 1)])],
    ];

    // Weaker level passes
    assert!(
        check(&h, Consistency::Causal).is_ok(),
        "should pass Causal (S1 and S2 update different variables, no ww cycle)",
    );
    // Stronger level fails
    assert!(
        check(&h, Consistency::Prefix).is_err(),
        "should fail Prefix (stale-read crossover forces cyclic ordering: S1<S3<S2<S4<S1)",
    );
}

// -- Boundary 4: Prefix pass, SnapshotIsolation fail --------------------------

/// Lost update / concurrent writes to the same variable.
///
/// T0: w(x,1)               -- initial writer
/// T1: r(x,1) w(x,2)        -- reads T0's x, overwrites with x=2
/// T2: r(x,1) w(x,3)        -- concurrent with T1, also overwrites x
///
/// PC: causal order is acyclic (both T1 and T2 depend on T0 only).
///     Prefix linearization finds a valid order, e.g. T0 < T1 < T2 or T0 < T2 < T1.
/// SI: T1 and T2 are concurrent (no visibility between them) and both write x.
///     SI forbids concurrent write-write conflicts -- fail.
#[test]
fn boundary_prefix_to_snapshot_isolation() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![committed(vec![w("x", 1)])],
        vec![committed(vec![r("x", 1), w("x", 2)])],
        vec![committed(vec![r("x", 1), w("x", 3)])],
    ];

    // Weaker level passes
    assert!(
        check(&h, Consistency::Prefix).is_ok(),
        "should pass Prefix (valid linearization exists)",
    );
    // Stronger level fails
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_err(),
        "should fail SnapshotIsolation (concurrent writes to x)",
    );
}

// -- Boundary 5: SnapshotIsolation pass, Serializable fail --------------------

/// Classic write skew: two sessions each read a variable written by the other
/// (from initial state) and write to a different variable.
///
/// T0: w(x,1) w(y,1)        -- initial state
/// T1: r(x,1) w(y,2)        -- reads x from T0, writes y
/// T2: r(y,1) w(x,2)        -- reads y from T0, writes x
///
/// SI: write sets are disjoint ({y} vs {x}) -- no write-write conflict.
///     Each sees a consistent snapshot of the initial state -- pass.
/// SER: T1 must come after T0 (reads x from T0). T2 must come after T0.
///      T1 reads x=1 (old), but T2 writes x=2 -- anti-dep T1->T2 on x.
///      T2 reads y=1 (old), but T1 writes y=2 -- anti-dep T2->T1 on y.
///      Cycle: T1 -> T2 -> T1 -- fail.
#[test]
fn boundary_snapshot_isolation_to_serializable() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![committed(vec![w("x", 1), w("y", 1)])],
        vec![committed(vec![r("x", 1), w("y", 2)])],
        vec![committed(vec![r("y", 1), w("x", 2)])],
    ];

    // Weaker level passes
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_ok(),
        "should pass SnapshotIsolation (disjoint write sets, consistent snapshots)",
    );
    // Stronger level fails
    assert!(
        check(&h, Consistency::Serializable).is_err(),
        "should fail Serializable (anti-dependency cycle: T1->T2->T1)",
    );
}

// -- Boundary 6: Serializable pass (max interleaved reads and writes) --------

/// A serializable history with maximum interleaving of reads and writes.
/// Four sessions, each a single transaction that alternates reads and writes
/// across three variables. The unique valid serial order is S0 < S1 < S2 < S3.
///
/// S0: w(x,1) w(y,1) w(z,1)
/// S1: r(x,1) w(y,2) r(z,1) w(x,2)   -- reads x,z from S0; writes y=2, x=2
/// S2: r(y,2) w(z,2) r(x,2) w(y,3)   -- reads y,x from S1; writes z=2, y=3
/// S3: r(z,2) r(x,2) r(y,3)           -- reads z from S2, x from S1, y from S2
///
/// Serial order S0 < S1 < S2 < S3 satisfies all reads:
///   S1: r(x,1) from S0, r(z,1) from S0.
///   S2: r(y,2) from S1, r(x,2) from S1.
///   S3: r(z,2) from S2, r(x,2) from S1, r(y,3) from S2.
/// No cycles, no anti-dependencies -- passes Serializable.
#[test]
fn passes_serializable() {
    let h: Vec<Session<&str, u64>> = vec![
        // S0: initial state for all three variables
        vec![committed(vec![w("x", 1), w("y", 1), w("z", 1)])],
        // S1: interleaved r/w -- reads x,z from S0; writes y=2, x=2
        vec![committed(vec![r("x", 1), w("y", 2), r("z", 1), w("x", 2)])],
        // S2: interleaved r/w -- reads y,x from S1; writes z=2, y=3
        vec![committed(vec![r("y", 2), w("z", 2), r("x", 2), w("y", 3)])],
        // S3: reads z from S2, x from S1, y from S2
        vec![committed(vec![r("z", 2), r("x", 2), r("y", 3)])],
    ];

    // Serializable must pass: unique valid serial order is S0 < S1 < S2 < S3
    assert!(
        check(&h, Consistency::Serializable).is_ok(),
        "should pass Serializable (interleaved r/w, serial order S0 < S1 < S2 < S3)",
    );
}
