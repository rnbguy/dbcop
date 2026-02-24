/// Tests for polynomial-time consistency checkers: RC, RR, RA.
/// Uses the `history!` DSL macro defined in tests/common/mod.rs.
mod common;

use dbcop_core::consistency::atomic_read::check_atomic_read;
use dbcop_core::consistency::committed_read::check_committed_read;
use dbcop_core::consistency::error::Error;
use dbcop_core::consistency::repeatable_read::check_repeatable_read;
use dbcop_core::{check, Consistency};

// ── Committed Read (RC) ─────────────────────────────────────────────────────

/// All reads observe only committed writes → RC pass.
#[test]
fn rc_pass_all_committed() {
    let h = history! {
        [
            { w(x, 1), w(y, 1) },
        ],
        [
            { r(x, 1), r(y, 1) },
        ],
    };
    assert!(check_committed_read(&h).is_ok(), "expected RC pass");
}

/// A session reads from an uncommitted transaction → RC violation (dirty read).
#[test]
fn rc_violation_dirty_read() {
    use dbcop_core::history::raw::types::{Event, Transaction};
    // Build manually: the macro only supports committed transactions.
    let h = vec![
        // S1: one uncommitted transaction that writes x=42
        vec![Transaction::uncommitted(vec![Event::<&str, u64>::write(
            "x", 42,
        )])],
        // S2: reads x=42 (from the uncommitted write)
        vec![Transaction::committed(vec![Event::<&str, u64>::read(
            "x", 42,
        )])],
    ];
    let result = check_committed_read(&h);
    assert!(
        result.is_err(),
        "expected RC violation (dirty read), got Ok"
    );
}

/// RC via the unified check() API -- pass case.
#[test]
fn rc_check_api_pass() {
    let h = history! {
        [
            { w(x, 1) },
        ],
        [
            { r(x, 1) },
        ],
    };
    assert!(
        check(&h, Consistency::CommittedRead).is_ok(),
        "expected RC pass via check()",
    );
}

/// RC cycle: two sessions whose write-read order creates a cycle → RC violation.
#[test]
fn rc_violation_committed_order_cycle() {
    // S1: write x=2, write y=1
    // S2: write x=3, read y=1   (so S1 →wr S2 via y)
    // S3: read x=3, read x=2   (so S2 →wr S3 via x=3, but S3 also reads x=2 from S1;
    //                            S1 must come before S2 (via y) and S2 before S1 (via x) → cycle)
    let h = history! {
        [
            { w(x, 2), w(y, 1) },
        ],
        [
            { w(x, 3), r(y, 1) },
        ],
        [
            { r(x, 3), r(x, 2) },
        ],
    };
    let result = check_committed_read(&h);
    assert!(
        matches!(
            result,
            Err(Error::Cycle {
                level: Consistency::CommittedRead,
                ..
            })
        ),
        "expected RC violation, got {result:?}",
    );
}

// ── Repeatable Read (RR) ────────────────────────────────────────────────────

/// Single read of each variable → trivially RR-consistent.
#[test]
fn rr_pass_single_reads() {
    let h = history! {
        [
            { w(x, 1), w(y, 2) },
        ],
        [
            { r(x, 1), r(y, 2) },
        ],
    };
    assert!(check_repeatable_read(&h).is_ok(), "expected RR pass");
}

/// Same variable read twice across two separate transactions in same session → RR pass.
/// (RR only requires within-transaction consistency; across transactions is fine.)
#[test]
fn rr_pass_reads_in_separate_txns() {
    let h = history! {
        [
            { w(x, 5) },
        ],
        [
            { r(x, 5) },
            { r(x, 5) },
        ],
    };
    assert!(
        check_repeatable_read(&h).is_ok(),
        "expected RR pass (same value in separate transactions)",
    );
}

/// Fractured read: same transaction reads x=1, then x=2 → RR violation.
#[test]
fn rr_violation_fractured_read() {
    let h = history! {
        [
            { w(x, 1) },
        ],
        [
            { w(x, 2) },
        ],
        [
            { r(x, 1), r(x, 2) },
        ],
    };
    let result = check_repeatable_read(&h);
    assert!(result.is_err(), "expected RR violation (fractured read)");
}

/// RR via the unified check() API -- violation case.
#[test]
fn rr_check_api_violation() {
    let h = history! {
        [
            { w(x, 10) },
        ],
        [
            { w(x, 20) },
        ],
        [
            { r(x, 10), r(x, 20) },
        ],
    };
    // AtomicRead subsumes RR, so AtomicRead should also catch this.
    let result = check(&h, Consistency::AtomicRead);
    assert!(result.is_err(), "expected AtomicRead violation via check()");
}

// ── Atomic Read / Read Atomicity (RA) ───────────────────────────────────────

/// Single writer, single reader -- all writes visible atomically → RA pass.
#[test]
fn ra_pass_atomic_visibility() {
    let h = history! {
        [
            { w(x, 1), w(y, 1) },
        ],
        [
            { r(x, 1), r(y, 1) },
        ],
    };
    assert!(check_atomic_read(&h).is_ok(), "expected RA pass");
}

/// RA violation via causal write-write cycle.
///
/// T1: w(x,1), w(y,1) -- writes both x and y.
/// T2: r(y,1), w(x,2), w(z,1) -- reads y from T1 (vis T1→T2), overwrites x, writes z.
/// T3: r(x,1), r(z,1) -- reads x from T1 (stale!) and z from T2 (vis T2→T3).
///
/// causal_ww on x: T1 and T2 both write x.  T3 reads x from T1.
/// T2 is visible to T3 (via z), so ww(T2, T1) = edge T2→T1.
/// But vis(T1→T2) via WR(y).  Cycle: T1→T2→T1.  RA violated.
#[test]
fn ra_violation_non_atomic_visibility() {
    let h = history! {
        [
            { w(x, 1), w(y, 1) },
        ],
        [
            { r(y, 1), w(x, 2), w(z, 1) },
        ],
        [
            { r(x, 1), r(z, 1) },
        ],
    };
    let result = check_atomic_read(&h);
    assert!(
        result.is_err(),
        "expected RA violation (causal ww cycle), got {result:?}",
    );
}

/// RA violation via the unified check() API -- same causal ww cycle pattern.
#[test]
fn ra_check_api_violation() {
    let h = history! {
        [
            { w(x, 1), w(y, 1) },
        ],
        [
            { r(y, 1), w(x, 2), w(z, 1) },
        ],
        [
            { r(x, 1), r(z, 1) },
        ],
    };
    let result = check(&h, Consistency::AtomicRead);
    assert!(result.is_err(), "expected RA violation via check(), got Ok",);
}

/// Sees newer then older version of x across *different transactions*.
/// This passes AtomicRead (single-step ww saturation) but can fail at Causal.
#[test]
fn ra_pass_stale_read_after_newer_across_transactions() {
    // S1: txn1 write(x,1), txn2 write(x,2)  -- two separate committed txns
    // S2: txn1 read(x,2),  txn2 read(x,1)   -- reads newer then older → violates AR
    let h = history! {
        [
            { w(x, 1) },
            { w(x, 2) },
        ],
        [
            { r(x, 2) },
            { r(x, 1) },
        ],
    };
    let result = check_atomic_read(&h);
    assert!(
        result.is_ok(),
        "expected AtomicRead pass (cycle requires transitive closure), got {result:?}",
    );
}

/// RC pass but RA violation: the causal ww cycle only appears at the RA level.
///
/// T1: w(x,1), w(y,1)
/// T2: r(y,1), w(x,2), w(z,1)  -- reads y from T1, overwrites x, writes z
/// T3: r(x,1), r(z,1)          -- reads stale x from T1, z from T2
///
/// RC: all reads from committed writes, committed order is acyclic → pass.
/// RA: causal_ww on x yields ww(T2→T1), but WR(y) gives vis(T1→T2) → cycle → fail.
#[test]
fn rc_pass_ra_violation() {
    let h = history! {
        [
            { w(x, 1), w(y, 1) },
        ],
        [
            { r(y, 1), w(x, 2), w(z, 1) },
        ],
        [
            { r(x, 1), r(z, 1) },
        ],
    };
    assert!(
        check_committed_read(&h).is_ok(),
        "should pass RC (all reads from committed writes, acyclic committed order)",
    );
    assert!(
        check_atomic_read(&h).is_err(),
        "should fail RA (causal ww cycle: T1→T2 via WR(y), T2→T1 via ww(x))",
    );
}
