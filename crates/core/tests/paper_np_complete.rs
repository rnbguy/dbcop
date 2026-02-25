use dbcop_core::consistency::error::Error;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};

// ---------------------------------------------------------------------------
// Snapshot Isolation tests
// ---------------------------------------------------------------------------

/// Two sessions writing different variables -- no conflict, SI should pass.
#[test]
fn si_pass_non_conflicting() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![Event::write("y", 1)])],
    ];
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_ok(),
        "non-conflicting writes should pass SI",
    );
}

/// Write skew: T1 reads x (init), writes y=1; T2 reads y (init), writes x=1.
/// Disjoint write sets -- SI ALLOWS write skew.
#[test]
fn si_pass_write_skew() {
    // T0 initialises x and y.
    // T1: r(x,1) w(y,2)
    // T2: r(y,1) w(x,2)
    // Write sets are disjoint ({y} vs {x}), so SI permits this.
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
        check(&h, Consistency::SnapshotIsolation).is_ok(),
        "write skew has disjoint write sets -- SI should allow it",
    );
}

/// Two concurrent transactions both write x -- SI forbids overlapping write sets.
#[test]
fn si_violation_concurrent_writes() {
    // T0: w(x,1)
    // T1: r(x,1), w(x,2)
    // T2: r(x,1), w(x,3)
    // T1 and T2 are concurrent and both write x → SI violation.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 3),
        ])],
    ];
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_err(),
        "concurrent writes to same variable should violate SI",
    );
}

/// Lost update: T1 and T2 both read x=init then write x.
/// Overlapping write sets on the same variable → SI violation.
#[test]
fn si_violation_lost_update() {
    // T0: w(x,1)
    // T1: r(x,1), w(x,2)
    // T2: r(x,1), w(x,3)
    // Identical structure to concurrent_writes, modelling a lost-update scenario.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 3),
        ])],
    ];
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_err(),
        "lost update (both read then write x) should violate SI",
    );
}

// ---------------------------------------------------------------------------
// Serializability tests
// ---------------------------------------------------------------------------

/// Simple sequential history -- one writer then one reader.
#[test]
fn ser_pass_serial() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::read("y", 1),
        ])],
    ];
    assert!(
        check(&h, Consistency::Serializable).is_ok(),
        "simple sequential history should be serializable",
    );
}

/// Write skew: T1 r(x,init) w(y,1); T2 r(y,init) w(x,1).
/// Both read each other's pre-image -- creates an anti-dependency cycle → SER violation.
#[test]
fn ser_violation_write_skew() {
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
        check(&h, Consistency::Serializable).is_err(),
        "write skew should violate serializability",
    );
}

/// Three sessions with a valid serial order (each variable read by one txn).
#[test]
fn ser_pass_multi_session() {
    // Serial order: T0 -> T1 -> T2
    // T0: w(x,1)
    // T1: r(x,1), w(y,1)
    // T2: r(y,1)
    // Each variable has exactly one writer and one reader -- clean chain.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![Event::read("y", 1)])],
    ];
    assert!(
        check(&h, Consistency::Serializable).is_ok(),
        "history with clear serial order T0->T1->T2 should be serializable",
    );
}

/// Three transactions forming an anti-dependency cycle → SER violation.
///
/// T0: w(a,1), w(b,1)
/// T1: r(a,1), w(b,2)   [reads T0's a, overwrites T0's b]
/// T2: r(b,1), w(a,2)   [reads T0's b (stale!), overwrites T0's a]
///
/// T1 must come after T0 (wr: T0->T1 on a).
/// T2 must come after T0 (wr: T0->T2 on b).
/// T2 reads T0's b, but T1 wrote b=2 → anti-dep T1->T2 on b.
/// T2 wrote a=2 → anti-dep T2->T1 on a (T1 reads a=1 from T0, not T2).
/// Cycle: T1 → T2 → T1.
#[test]
fn ser_violation_cycle() {
    let h: Vec<Session<&str, u64>> = vec![
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
    assert!(
        check(&h, Consistency::Serializable).is_err(),
        "anti-dependency cycle should violate serializability",
    );
}

// ---------------------------------------------------------------------------
// Consistency hierarchy tests
// ---------------------------------------------------------------------------

/// A serializable history must pass ALL weaker levels.
#[test]
fn hierarchy_ser_implies_all() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::write("y", 1),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::read("y", 1),
        ])],
    ];
    for level in [
        Consistency::CommittedRead,
        Consistency::RepeatableRead,
        Consistency::AtomicRead,
        Consistency::Causal,
        Consistency::Prefix,
        Consistency::SnapshotIsolation,
        Consistency::Serializable,
    ] {
        assert!(
            check(&h, level).is_ok(),
            "SER-valid history should pass {level:?}",
        );
    }
}

/// Write skew passes CC, PC, SI but fails SER.
#[test]
fn hierarchy_write_skew_passes_si_fails_ser() {
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
        check(&h, Consistency::CommittedRead).is_ok(),
        "write skew should pass CommittedRead",
    );
    assert!(
        check(&h, Consistency::Causal).is_ok(),
        "write skew should pass Causal",
    );
    assert!(
        check(&h, Consistency::Prefix).is_ok(),
        "write skew should pass Prefix",
    );
    assert!(
        check(&h, Consistency::SnapshotIsolation).is_ok(),
        "write skew should pass SI (disjoint write sets)",
    );
    assert!(
        check(&h, Consistency::Serializable).is_err(),
        "write skew should fail Serializable",
    );
}

/// If SI fails, SER must also fail (SI is weaker than SER).
#[test]
fn hierarchy_si_violation_fails_ser_too() {
    // Concurrent writes to x violates SI; must also violate SER.
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 3),
        ])],
    ];

    let si_result = check(&h, Consistency::SnapshotIsolation);
    let ser_result = check(&h, Consistency::Serializable);

    assert!(si_result.is_err(), "concurrent writes should violate SI");
    assert!(
        ser_result.is_err(),
        "if SI fails, SER must also fail (SER => SI)",
    );
}

// ---------------------------------------------------------------------------
// Error type smoke-tests
// ---------------------------------------------------------------------------

/// Verify that a SI violation produces the expected error variant.
#[test]
fn si_violation_error_variant() {
    let h: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 2),
        ])],
        vec![Transaction::committed(vec![
            Event::read("x", 1),
            Event::write("x", 3),
        ])],
    ];
    assert!(
        matches!(
            check(&h, Consistency::SnapshotIsolation),
            Err(Error::Invalid(Consistency::SnapshotIsolation))
        ),
        "expected Invalid(SnapshotIsolation) error",
    );
}

/// Verify that a SER violation produces the expected error variant.
#[test]
fn ser_violation_error_variant() {
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
        matches!(
            check(&h, Consistency::Serializable),
            Err(Error::Invalid(Consistency::Serializable))
        ),
        "expected Invalid(Serializable) error",
    );
}
