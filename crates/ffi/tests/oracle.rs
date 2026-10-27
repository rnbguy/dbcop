use std::ffi::CString;
use std::os::raw::{c_char, c_int};
use std::ptr;

use dbcop_ffi::{
    dbcop_check, dbcop_oracle_check, dbcop_oracle_free, dbcop_oracle_new, dbcop_oracle_read,
    dbcop_oracle_reset, DbcopOracle,
};

struct Oracle(*mut DbcopOracle);

impl Oracle {
    fn new(level: &str) -> Self {
        let level = CString::new(level).unwrap();
        // SAFETY: level is a live C string; the returned handle is owned here.
        let oracle = unsafe { dbcop_oracle_new(level.as_ptr()) };
        assert!(!oracle.is_null(), "failed to create {level:?}");
        Self(oracle)
    }

    fn check(&mut self, session_id: u64, events: &str, commit: c_int) -> c_int {
        let events = CString::new(events).unwrap();
        // SAFETY: this wrapper owns the live handle exclusively and events is live.
        unsafe { dbcop_oracle_check(self.0, session_id, events.as_ptr(), commit) }
    }

    fn read(
        &mut self,
        session_id: u64,
        variable: u64,
        version: Option<u64>,
        accept: c_int,
    ) -> c_int {
        let (version, initial) = version.map_or((0, 1), |version| (version, 0));
        // SAFETY: this wrapper owns the live handle exclusively.
        unsafe { dbcop_oracle_read(self.0, session_id, variable, version, initial, accept) }
    }

    fn reset(&mut self) {
        // SAFETY: this wrapper owns the live handle exclusively.
        unsafe { dbcop_oracle_reset(self.0) };
    }
}

impl Drop for Oracle {
    fn drop(&mut self) {
        // SAFETY: this wrapper releases its exclusively owned live handle once.
        unsafe { dbcop_oracle_free(self.0) };
    }
}

const WRITE_ONE: &str = r#"[{"Write":{"variable":1,"version":1}}]"#;
const WRITE_TWO: &str = r#"[{"Write":{"variable":1,"version":2}}]"#;
const READ_ONE: &str = r#"[{"Read":{"variable":1,"version":1}}]"#;
const READ_TWO: &str = r#"[{"Read":{"variable":1,"version":2}}]"#;
const READ_EMPTY: &str = r#"[{"Read":{"variable":1,"version":null}}]"#;
const READ_ZERO: &str = r#"[{"Read":{"variable":1,"version":0}}]"#;

#[test]
fn every_level_supports_creation_check_and_free() {
    for level in [
        "committed-read",
        "repeatable-read",
        "atomic-read",
        "causal",
        "prefix",
        "snapshot-isolation",
        "serializable",
    ] {
        let mut oracle = Oracle::new(level);
        assert_eq!(oracle.check(1, READ_EMPTY, 0), 1, "{level}");
        assert_eq!(oracle.check(1, READ_ZERO, 0), 1, "{level}");
        assert_eq!(oracle.check(1, WRITE_ONE, 1), 1, "{level}");
        assert_eq!(oracle.check(2, READ_ONE, 1), 1, "{level}");
    }
    // SAFETY: freeing null is explicitly supported.
    unsafe { dbcop_oracle_free(ptr::null_mut()) };
}

#[test]
fn trials_do_not_publish_writes_or_advance_the_prefix() {
    let mut oracle = Oracle::new("causal");
    for _ in 0..3 {
        assert_eq!(oracle.check(1, WRITE_ONE, 0), 1);
        assert_eq!(oracle.check(2, READ_ONE, 0), 0);
    }
    assert_eq!(oracle.check(1, WRITE_ONE, 1), 1);
    assert_eq!(oracle.check(2, READ_ONE, 1), 1);
    assert_eq!(oracle.check(1, WRITE_TWO, 0), 1);
    assert_eq!(oracle.check(2, READ_TWO, 0), 0);
    assert_eq!(oracle.check(1, WRITE_TWO, 1), 1);
    assert_eq!(oracle.check(2, READ_TWO, 1), 1);
}

#[test]
fn rejected_commit_rolls_back_candidate_writes_and_relations() {
    let mut oracle = Oracle::new("causal");
    assert_eq!(oracle.check(1, WRITE_ONE, 1), 1);
    let rejected = r#"[
        {"Read":{"variable":1,"version":null}},
        {"Write":{"variable":2,"version":2}}
    ]"#;
    let read_candidate_write = r#"[{"Read":{"variable":2,"version":2}}]"#;
    for _ in 0..3 {
        assert_eq!(oracle.check(1, rejected, 1), 0);
        assert_eq!(oracle.check(2, read_candidate_write, 0), 0);
    }
    let accepted = r#"[
        {"Read":{"variable":1,"version":1}},
        {"Write":{"variable":2,"version":2}}
    ]"#;
    assert_eq!(oracle.check(1, accepted, 1), 1);
    assert_eq!(oracle.check(1, read_candidate_write, 1), 1);
}

#[test]
fn rejected_serializable_candidate_does_not_poison_the_next_check() {
    let mut oracle = Oracle::new("serializable");
    let first = r#"[
        {"Read":{"variable":1,"version":null}},
        {"Write":{"variable":1,"version":1}}
    ]"#;
    let lost_update = r#"[
        {"Read":{"variable":1,"version":null}},
        {"Write":{"variable":1,"version":2}}
    ]"#;
    let ordered_update = r#"[
        {"Read":{"variable":1,"version":1}},
        {"Write":{"variable":1,"version":2}}
    ]"#;
    assert_eq!(oracle.check(1, first, 1), 1);
    assert_eq!(oracle.check(2, lost_update, 0), 0);
    assert_eq!(oracle.check(2, lost_update, 1), 0);
    assert_eq!(oracle.check(2, ordered_update, 1), 1);
    assert_eq!(oracle.check(3, READ_TWO, 1), 1);
}

#[test]
fn handles_have_independent_state_and_lifetimes() {
    let mut first = Oracle::new("causal");
    let mut second = Oracle::new("causal");
    assert_eq!(first.check(1, WRITE_ONE, 1), 1);
    assert_eq!(second.check(2, READ_ONE, 0), 0);
    assert_eq!(second.check(1, WRITE_ONE, 1), 1);
    drop(first);
    assert_eq!(second.check(2, READ_ONE, 1), 1);
    let mut fresh = Oracle::new("causal");
    assert_eq!(fresh.check(2, READ_ONE, 0), 0);
}

#[test]
fn invalid_arguments_are_distinct_from_core_rejections_and_preserve_state() {
    let invalid_utf8 = [0xff_u8, 0];
    let unknown_level = CString::new("unknown").unwrap();
    // SAFETY: null is accepted; both non-null inputs are live C strings.
    unsafe {
        assert!(dbcop_oracle_new(ptr::null()).is_null());
        assert!(dbcop_oracle_new(unknown_level.as_ptr()).is_null());
        assert!(dbcop_oracle_new(invalid_utf8.as_ptr().cast::<c_char>()).is_null());
    }

    let mut oracle = Oracle::new("committed-read");
    let empty = CString::new("[]").unwrap();
    // SAFETY: oracle is live and exclusively owned; non-null strings are live.
    unsafe {
        assert_eq!(
            dbcop_oracle_check(ptr::null_mut(), 1, empty.as_ptr(), 0),
            -1
        );
        assert_eq!(dbcop_oracle_check(oracle.0, 1, ptr::null(), 0), -1);
        assert_eq!(
            dbcop_oracle_check(oracle.0, 1, invalid_utf8.as_ptr().cast::<c_char>(), 0),
            -1
        );
    }
    assert_eq!(oracle.check(0, WRITE_ONE, 1), -1);
    assert_eq!(oracle.check(1, WRITE_ONE, -1), -1);
    assert_eq!(oracle.check(1, WRITE_ONE, 2), -1);
    for invalid in [
        "not JSON",
        "{}",
        "null",
        r#"[{"Write":{"variable":1,"version":null}}]"#,
        r#"[{"Write":{"variable":-1,"version":1}}]"#,
        r#"[{"Read":{"variable":1,"version":18446744073709551616}}]"#,
        r#"[{"Write":{"variable":1,"version":0}}]"#,
    ] {
        assert_eq!(oracle.check(1, invalid, 1), -1, "{invalid}");
    }
    assert_eq!(oracle.check(2, READ_ONE, 0), 0);
    assert_eq!(oracle.check(1, WRITE_ONE, 1), 1);
    assert_eq!(oracle.check(2, READ_ONE, 1), 1);
}

#[test]
fn stateless_batch_reference_remains_available() {
    let history = CString::new("[]").unwrap();
    let level = CString::new("serializable").unwrap();
    // SAFETY: both inputs are live NUL-terminated C strings.
    assert_eq!(unsafe { dbcop_check(history.as_ptr(), level.as_ptr()) }, 1);
}

#[test]
fn accepted_read_prefix_enforces_atomicity_and_reset_preserves_commits() {
    for level in [
        "committed-read",
        "repeatable-read",
        "atomic-read",
        "causal",
        "prefix",
        "snapshot-isolation",
        "serializable",
    ] {
        let mut oracle = Oracle::new(level);
        let writes =
            r#"[{"Write":{"variable":1,"version":1}},{"Write":{"variable":2,"version":2}}]"#;
        assert_eq!(oracle.check(1, writes, 1), 1, "{level}");
        assert_eq!(oracle.read(2, 1, Some(1), 0), 1, "{level}");
        assert_eq!(oracle.read(2, 2, None, 0), 1, "trial leaked: {level}");
        assert_eq!(oracle.read(2, 1, Some(1), 1), 1, "{level}");
        // The earlier source writes both variables: RC already forbids this rewind.
        assert_eq!(oracle.read(2, 2, None, 0), 0, "{level}");
        assert_eq!(
            oracle.read(2, 2, Some(2), 1),
            1,
            "rejected trial poisoned {level}"
        );
        oracle.reset();
        assert_eq!(oracle.read(2, 2, None, 1), 1, "reset leaked: {level}");
        let reads = r#"[{"Read":{"variable":1,"version":1}},{"Read":{"variable":2,"version":2}}]"#;
        assert_eq!(
            oracle.check(2, reads, 1),
            1,
            "whole commit did not reset: {level}"
        );
        oracle.reset();
        assert_eq!(oracle.read(3, 2, Some(2), 0), 1, "commit lost: {level}");
        assert_eq!(
            oracle.read(3, 2, Some(3), 0),
            0,
            "unknown version accepted: {level}"
        );
    }
}

#[test]
fn invalid_scalar_read_arguments_preserve_the_accepted_prefix() {
    let mut oracle = Oracle::new("causal");
    assert_eq!(oracle.check(1, WRITE_ONE, 1), 1);
    assert_eq!(oracle.read(2, 1, Some(1), 1), 1);
    for (session, version, initial, accept) in [
        (0, 1, 0, 0),
        (2, 1, 2, 0),
        (2, 0, 0, 0),
        (2, 1, 1, 0),
        (2, 1, 0, -1),
        (2, 1, 0, 2),
    ] {
        // SAFETY: the handle is live and exclusively owned; invalid scalar arguments are supported.
        assert_eq!(
            unsafe { dbcop_oracle_read(oracle.0, session, 1, version, initial, accept) },
            -1
        );
        assert_eq!(oracle.read(2, 1, None, 0), 0);
    }
    // SAFETY: null handles are rejected without dereferencing.
    assert_eq!(
        unsafe { dbcop_oracle_read(ptr::null_mut(), 1, 1, 1, 0, 0) },
        -1
    );
    oracle.reset();
    assert_eq!(oracle.read(2, 1, None, 0), 1);
}
