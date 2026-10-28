use dbcop_core::consistency::error::Error as CheckError;
use dbcop_core::history::raw::error::Error as RawError;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::history::raw::{
    committed_external_reads, consistent_local_reads, is_valid_history,
};
use dbcop_core::{check, Consistency};

const LEVELS: [Consistency; 7] = [
    Consistency::CommittedRead,
    Consistency::RepeatableRead,
    Consistency::AtomicRead,
    Consistency::Causal,
    Consistency::Prefix,
    Consistency::SnapshotIsolation,
    Consistency::Serializable,
];

fn assert_inconsistent_local_read(histories: &[Session<&str, u64>]) {
    assert!(matches!(
        consistent_local_reads(histories),
        Err(RawError::InconsistentLocalRead { .. })
    ));
    assert!(matches!(
        is_valid_history(histories),
        Err(RawError::InconsistentLocalRead { .. })
    ));
    for level in LEVELS {
        let result = check(histories, level);
        assert!(
            matches!(
                result,
                Err(CheckError::NonAtomic(
                    RawError::InconsistentLocalRead { .. }
                ))
            ),
            "expected inconsistent local read at {level:?}: {result:?}",
        );
    }
}

#[test]
fn latest_local_reads_remain_valid_when_later_overwritten() {
    for events in [
        vec![
            Event::write("x", 1),
            Event::read("x", 1),
            Event::write("x", 2),
        ],
        vec![
            Event::write("x", 1),
            Event::read("x", 1),
            Event::write("x", 2),
            Event::read("x", 2),
            Event::write("x", 3),
        ],
        vec![
            Event::write("x", 0),
            Event::read("x", 0),
            Event::write("x", 1),
        ],
        vec![
            Event::read_empty("x"),
            Event::write("x", 1),
            Event::read("x", 1),
            Event::write("x", 2),
        ],
        vec![
            Event::read("x", 0),
            Event::write("x", 1),
            Event::read("x", 1),
            Event::write("x", 2),
        ],
    ] {
        let histories: Vec<Session<&str, u64>> = vec![vec![Transaction::committed(events)]];
        assert!(consistent_local_reads(&histories).is_ok());
        assert!(committed_external_reads(&histories).is_ok());
        assert!(is_valid_history(&histories).is_ok());
        for level in LEVELS {
            let result = check(&histories, level);
            assert!(
                result.is_ok(),
                "latest preceding local write should pass {level:?}: {result:?}",
            );
        }
    }
}

#[test]
fn external_version_cannot_be_read_after_a_local_write() {
    let histories = vec![
        vec![Transaction::committed(vec![Event::write("x", 1)])],
        vec![Transaction::committed(vec![
            Event::write("x", 2),
            Event::read("x", 1),
        ])],
    ];
    assert_inconsistent_local_read(&histories);
}

#[test]
fn earlier_local_version_cannot_be_read_after_a_local_overwrite() {
    let histories = vec![vec![Transaction::committed(vec![
        Event::write("x", 1),
        Event::write("x", 2),
        Event::read("x", 1),
    ])]];
    assert_inconsistent_local_read(&histories);
}

#[test]
fn initial_version_cannot_be_read_after_a_local_write() {
    for read in [Event::read_empty("x"), Event::read("x", 0)] {
        let histories = vec![vec![Transaction::committed(vec![
            Event::write("x", 1),
            read,
        ])]];
        assert_inconsistent_local_read(&histories);
    }
}

#[test]
fn external_reads_of_overwritten_local_versions_still_fail() {
    let histories: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::read("x", 1),
            Event::write("x", 2),
        ])],
        vec![Transaction::committed(vec![Event::read("x", 1)])],
    ];
    assert!(consistent_local_reads(&histories).is_ok());
    assert!(matches!(
        committed_external_reads(&histories),
        Err(RawError::OverwrittenRead { .. })
    ));
    for level in LEVELS {
        let result = check(&histories, level);
        assert!(
            matches!(
                result,
                Err(CheckError::NonAtomic(RawError::OverwrittenRead { .. }))
            ),
            "external intermediate version should fail {level:?}: {result:?}",
        );
    }
}

#[test]
fn external_reads_of_the_final_local_version_still_pass() {
    let histories: Vec<Session<&str, u64>> = vec![
        vec![Transaction::committed(vec![
            Event::write("x", 1),
            Event::read("x", 1),
            Event::write("x", 2),
        ])],
        vec![Transaction::committed(vec![Event::read("x", 2)])],
    ];
    for level in LEVELS {
        let result = check(&histories, level);
        assert!(
            result.is_ok(),
            "external final version should pass {level:?}: {result:?}",
        );
    }
}
