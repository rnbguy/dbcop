use std::ffi::CString;
use std::ptr;

use dbcop_ffi::{
    dbcop_check, dbcop_oracle_check, dbcop_oracle_free, dbcop_oracle_new, dbcop_oracle_read,
    DbcopOracle,
};

fn batch(history: &str, level: &str) -> i32 {
    let history = CString::new(history).unwrap();
    let level = CString::new(level).unwrap();
    // SAFETY: both C strings remain allocated throughout the call.
    unsafe { dbcop_check(history.as_ptr(), level.as_ptr()) }
}

#[test]
fn batch_boundary_distinguishes_bad_arguments_from_invalid_histories() {
    let history = CString::new("[]").unwrap();
    let level = CString::new("causal").unwrap();
    let invalid_utf8 = CString::new(vec![0xff]).unwrap();
    // SAFETY: each pointer is null or references an allocated, terminated C string.
    unsafe {
        assert_eq!(dbcop_check(ptr::null(), level.as_ptr()), -1);
        assert_eq!(dbcop_check(history.as_ptr(), ptr::null()), -1);
        assert_eq!(dbcop_check(invalid_utf8.as_ptr(), level.as_ptr()), -1);
        assert_eq!(dbcop_check(history.as_ptr(), invalid_utf8.as_ptr()), -1);
    }
    for level in ["", "unknown", "Causal", "causal "] {
        assert_eq!(batch("[]", level), -1, "{level:?}");
    }
    for malformed in [
        "",
        "[",
        "null",
        "{}",
        "[] []",
        "[[{}]]",
        r#"[[{"events":[],"committed":"yes"}]]"#,
        r#"[[{"events":[{"Read":{"variable":-1,"version":null}}],"committed":true}]]"#,
        r#"[[{"events":[{"Write":{"variable":1,"version":18446744073709551616}}],"committed":true}]]"#,
    ] {
        assert_eq!(batch(malformed, "causal"), -1, "{malformed}");
    }
    let missing_source = r#"[[{"events":[{"Read":{"variable":1,"version":9}}],"committed":true}]]"#;
    assert_eq!(batch(missing_source, "causal"), 0);
    assert_eq!(batch("[]", "causal"), 1);
}

#[test]
fn batch_checks_observable_isolation_distinctions_at_every_level() {
    let serial = r#"[
        [{"events":[{"Write":{"variable":1,"version":1}}],"committed":true}],
        [{"events":[{"Read":{"variable":1,"version":1}}],"committed":true}]
    ]"#;
    let write_skew = r#"[
        [{"events":[{"Read":{"variable":2,"version":null}},{"Write":{"variable":1,"version":1}}],"committed":true}],
        [{"events":[{"Read":{"variable":1,"version":null}},{"Write":{"variable":2,"version":2}}],"committed":true}]
    ]"#;
    let lost_update = r#"[
        [{"events":[{"Read":{"variable":1,"version":null}},{"Write":{"variable":1,"version":1}}],"committed":true}],
        [{"events":[{"Read":{"variable":1,"version":null}},{"Write":{"variable":1,"version":2}}],"committed":true}]
    ]"#;
    for (level, skew, lost) in [
        ("committed-read", 1, 1),
        ("repeatable-read", 1, 1),
        ("atomic-read", 1, 1),
        ("causal", 1, 1),
        ("prefix", 1, 1),
        ("snapshot-isolation", 1, 0),
        ("serializable", 0, 0),
    ] {
        assert_eq!(batch(serial, level), 1, "serial: {level}");
        assert_eq!(batch(write_skew, level), skew, "write skew: {level}");
        assert_eq!(batch(lost_update, level), lost, "lost update: {level}");
    }
}

struct Oracle(*mut DbcopOracle);

impl Drop for Oracle {
    fn drop(&mut self) {
        // SAFETY: this test owns the live handle and frees it exactly once.
        unsafe { dbcop_oracle_free(self.0) };
    }
}

#[test]
fn malformed_transaction_calls_preserve_commits_and_accepted_live_reads() {
    let level = CString::new("causal").unwrap();
    // SAFETY: level is a live C string; the returned handle is exclusively owned.
    let oracle = Oracle(unsafe { dbcop_oracle_new(level.as_ptr()) });
    assert!(!oracle.0.is_null());
    let writes = CString::new(
        r#"[{"Write":{"variable":1,"version":1}},{"Write":{"variable":2,"version":2}}]"#,
    )
    .unwrap();
    // SAFETY: the handle and C string are live, with exclusive handle access.
    unsafe {
        assert_eq!(dbcop_oracle_check(oracle.0, 1, writes.as_ptr(), 1), 1);
        assert_eq!(dbcop_oracle_read(oracle.0, 2, 1, 1, 0, 1), 1);
    }

    let invalid_inputs = [
        CString::new(vec![0xff]).unwrap(),
        CString::new("[").unwrap(),
        CString::new("{}").unwrap(),
        CString::new(r#"[{"Write":{"variable":2,"version":0}}]"#).unwrap(),
    ];
    let empty = CString::new("[]").unwrap();
    let mut calls = vec![(2, ptr::null(), 1), (0, empty.as_ptr(), 1)];
    calls.extend([(2, writes.as_ptr(), -1), (2, writes.as_ptr(), 2)]);
    calls.extend(invalid_inputs.iter().map(|input| (2, input.as_ptr(), 1)));
    for (session, events, commit) in calls {
        // SAFETY: all non-null strings remain allocated and the handle stays live.
        unsafe {
            assert_eq!(dbcop_oracle_check(oracle.0, session, events, commit), -1);
            // The accepted source wrote both keys, so rewinding key 2 must still fail.
            assert_eq!(dbcop_oracle_read(oracle.0, 2, 2, 0, 1, 0), 0);
            assert_eq!(dbcop_oracle_read(oracle.0, 2, 2, 2, 0, 0), 1);
        }
    }
    let reads = CString::new(
        r#"[{"Read":{"variable":1,"version":1}},{"Read":{"variable":2,"version":2}}]"#,
    )
    .unwrap();
    // SAFETY: the handle and C string remain live and exclusively accessed.
    unsafe {
        assert_eq!(dbcop_oracle_check(oracle.0, 2, reads.as_ptr(), 1), 1);
        assert_eq!(dbcop_oracle_read(oracle.0, 3, 2, 2, 0, 1), 1);
    }
}
