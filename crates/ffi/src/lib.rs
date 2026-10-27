//! C ABI over `dbcop_core` checkers for the Go mockserver oracle.
//!
//! The mockserver owns one persistent oracle per store. Scalar read trials reuse
//! the accepted live prefix; whole transactions use JSON `Vec<Event<u64,u64>>`:
//!
//! ```c
//! typedef struct DbcopOracle DbcopOracle;
//! DbcopOracle *dbcop_oracle_new(const char *level);
//! // Returns 1 if valid, 0 if violated, -1 on bad input.
//! int dbcop_oracle_check(DbcopOracle *, uint64_t session_id,
//!                        const char *events_json, int commit);
//! int dbcop_oracle_read(DbcopOracle *, uint64_t session_id,
//!                       uint64_t variable, uint64_t version,
//!                       int initial, int accept);
//! void dbcop_oracle_reset(DbcopOracle *);
//! void dbcop_oracle_free(DbcopOracle *);
//! // Stateless batch reference, accepting JSON Vec<Session<u64,u64>>.
//! int dbcop_check(const char *history_json, const char *level);
//! ```
//!
//! Levels: `committed-read`, `repeatable-read`, `atomic-read`, `causal`,
//! `prefix`, `snapshot-isolation`, `serializable`.

use std::ffi::CStr;
use std::os::raw::{c_char, c_int};

use dbcop_core::consistency::incremental::IncrementalChecker;
use dbcop_core::history::raw::types::{Event, Session};
use dbcop_core::Consistency;

fn parse_level(level: &str) -> Option<Consistency> {
    match level {
        "committed-read" => Some(Consistency::CommittedRead),
        "repeatable-read" => Some(Consistency::RepeatableRead),
        "atomic-read" => Some(Consistency::AtomicRead),
        "causal" => Some(Consistency::Causal),
        "prefix" => Some(Consistency::Prefix),
        "snapshot-isolation" => Some(Consistency::SnapshotIsolation),
        "serializable" => Some(Consistency::Serializable),
        _ => None,
    }
}

/// An opaque, independently owned incremental oracle.
///
/// Create with [`dbcop_oracle_new`] and release with [`dbcop_oracle_free`].
/// Callers must serialize all access, including freeing the handle.
pub struct DbcopOracle {
    checker: IncrementalChecker,
}

/// Create an empty persistent oracle, or return null for an invalid level.
///
/// The level string is borrowed only during this call. The returned handle owns
/// its state and must eventually be passed to [`dbcop_oracle_free`] exactly once.
///
/// # Safety
///
/// A non-null `level` must point to a readable NUL-terminated C string that stays
/// valid for this call. Null and non-UTF-8 strings are rejected.
#[no_mangle]
pub unsafe extern "C" fn dbcop_oracle_new(level: *const c_char) -> *mut DbcopOracle {
    if level.is_null() {
        return std::ptr::null_mut();
    }
    // SAFETY: null-checked above; caller guarantees a readable C string.
    let Ok(level) = unsafe { CStr::from_ptr(level) }.to_str() else {
        return std::ptr::null_mut();
    };
    let Some(level) = parse_level(level) else {
        return std::ptr::null_mut();
    };
    Box::into_raw(Box::new(DbcopOracle {
        checker: IncrementalChecker::new(level),
    }))
}

/// Release an oracle and all its retained state. Null is a no-op.
///
/// # Safety
///
/// A non-null `oracle` must be a live pointer returned by [`dbcop_oracle_new`].
/// It must be freed exactly once, with no concurrent or subsequent access and no
/// other outstanding references to its state.
#[no_mangle]
pub unsafe extern "C" fn dbcop_oracle_free(oracle: *mut DbcopOracle) {
    if !oracle.is_null() {
        // SAFETY: the caller transfers sole ownership of this live allocation.
        drop(unsafe { Box::from_raw(oracle) });
    }
}

/// Check one whole tentative transaction against the committed prefix.
///
/// `events_json` is a JSON `Vec<Event<u64,u64>>`, not a full history. Session IDs
/// must be nonzero. Write versions must be nonzero; null and zero read versions
/// denote the initial state. `commit` must be 0 (trial) or 1 (validate and commit).
/// Returns 1 on success, 0 for a core history/consistency error, or -1 for an
/// invalid argument. Trials and all rejected calls leave committed state intact.
/// Successful commits advance only the specified session's transaction height.
/// A valid whole-transaction call first discards any retained live read prefix.
/// The events string is borrowed only during this call.
///
/// # Safety
///
/// A non-null `oracle` must be a live pointer returned by [`dbcop_oracle_new`].
/// Access to it must be exclusive for this call, with no concurrent checks or
/// free. A non-null `events_json` must point to a readable NUL-terminated C string
/// valid for this call. Null pointers and non-UTF-8 strings are rejected.
#[no_mangle]
pub unsafe extern "C" fn dbcop_oracle_check(
    oracle: *mut DbcopOracle,
    session_id: u64,
    events_json: *const c_char,
    commit: c_int,
) -> c_int {
    if oracle.is_null() || events_json.is_null() || session_id == 0 || !matches!(commit, 0 | 1) {
        return -1;
    }
    // SAFETY: null-checked above; caller guarantees a readable C string.
    let Ok(events) = unsafe { CStr::from_ptr(events_json) }.to_str() else {
        return -1;
    };
    let Ok(events) = serde_json::from_str::<Vec<Event<u64, u64>>>(events) else {
        return -1;
    };
    if events
        .iter()
        .any(|event| matches!(event, Event::Write { version: 0, .. }))
    {
        return -1;
    }
    // SAFETY: the caller guarantees a live handle and exclusive access.
    let oracle = unsafe { &mut *oracle };
    c_int::from(
        oracle
            .checker
            .check_transaction(session_id, &events, commit == 1)
            .is_ok(),
    )
}

/// Check one additional external read against the retained live transaction.
///
/// `initial` and `accept` must be 0 or 1. Initial reads require version zero;
/// other reads require a nonzero version. Session IDs must be nonzero.
/// Returns 1 on success, 0 for a consistency error, or -1 for invalid arguments.
/// A successful `accept = 1` retains the read for subsequent trials without
/// committing a transaction. Trials and rejected reads leave that prefix intact.
/// Reset the live prefix before switching to another session.
///
/// # Safety
///
/// A non-null `oracle` must be a live pointer returned by [`dbcop_oracle_new`].
/// Access must be exclusive, with no concurrent checks, resets, or free.
#[no_mangle]
pub unsafe extern "C" fn dbcop_oracle_read(
    oracle: *mut DbcopOracle,
    session_id: u64,
    variable: u64,
    version: u64,
    initial: c_int,
    accept: c_int,
) -> c_int {
    if oracle.is_null()
        || session_id == 0
        || !matches!(initial, 0 | 1)
        || !matches!(accept, 0 | 1)
        || (initial == 1) != (version == 0)
    {
        return -1;
    }
    // SAFETY: the caller guarantees a live handle and exclusive access.
    let oracle = unsafe { &mut *oracle };
    c_int::from(
        oracle
            .checker
            .check_read(
                session_id,
                variable,
                (initial == 0).then_some(version),
                accept == 1,
            )
            .is_ok(),
    )
}

/// Discard the live read prefix without changing committed state. Null is a no-op.
///
/// # Safety
///
/// A non-null `oracle` must be a live pointer returned by [`dbcop_oracle_new`].
/// Access must be exclusive, with no concurrent checks, resets, or free.
#[no_mangle]
pub unsafe extern "C" fn dbcop_oracle_reset(oracle: *mut DbcopOracle) {
    if !oracle.is_null() {
        // SAFETY: the caller guarantees a live handle and exclusive access.
        unsafe { &mut *oracle }.checker.reset_transaction();
    }
}

/// Check a JSON history against a level.
///
/// Returns 1 (satisfies), 0 (core history/consistency error), or -1 (bad input).
/// The strings are borrowed only during this call.
///
/// # Safety
///
/// Non-null pointers must reference readable NUL-terminated C strings that stay
/// valid for this call. Null pointers and non-UTF-8 strings return -1.
#[no_mangle]
pub unsafe extern "C" fn dbcop_check(history_json: *const c_char, level: *const c_char) -> c_int {
    if history_json.is_null() || level.is_null() {
        return -1;
    }
    // SAFETY: null-checked above; caller guarantees NUL-terminated C strings.
    let (Ok(history), Ok(level)) = (
        unsafe { CStr::from_ptr(history_json) }.to_str(),
        unsafe { CStr::from_ptr(level) }.to_str(),
    ) else {
        return -1;
    };
    let Some(consistency) = parse_level(level) else {
        return -1;
    };
    let Ok(sessions) = serde_json::from_str::<Vec<Session<u64, u64>>>(history) else {
        return -1;
    };
    c_int::from(dbcop_core::check(&sessions, consistency).is_ok())
}
