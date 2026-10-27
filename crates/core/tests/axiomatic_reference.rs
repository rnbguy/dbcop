#![allow(clippy::too_many_lines)]

#[path = "common/axiomatic_reference.rs"]
mod axiomatic_reference;

use std::collections::BTreeSet;

use axiomatic_reference::{
    directed_histories, for_each_history, malformed_histories, reference_check, validate_witness,
    History, ReferenceResult, LEVELS,
};
use dbcop_core::consistency::error::Error;
use dbcop_core::consistency::Witness;
use dbcop_core::graph::digraph::DiGraph;
use dbcop_core::history::atomic::types::TransactionId;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};

fn compare(sessions: &[Session<u64, u64>], domain: &str, ordinal: u64, counts: &mut [[u64; 3]; 7]) {
    for (index, level) in LEVELS.into_iter().enumerate() {
        let reference = reference_check(sessions, level);
        let bucket = match &reference {
            ReferenceResult::Invalid(_) => 0,
            ReferenceResult::Consistent(_) => 1,
            ReferenceResult::Inconsistent => 2,
        };
        counts[index][bucket] += 1;
        let implementation = check(sessions, level);
        let disagreement = match (&reference, &implementation) {
            (ReferenceResult::Invalid(_), Err(Error::NonAtomic(_)))
            | (ReferenceResult::Inconsistent, Err(_)) => None,
            (ReferenceResult::Consistent(_), Ok(witness)) => {
                validate_witness(sessions, level, witness).err()
            }
            _ => Some("reference/checker classification disagreement".into()),
        };
        if let Some(reason) = disagreement {
            panic!(
                "first mismatch: domain={domain}, ordinal={ordinal}, level={level:?}, implementation=core\n\
                 reason={reason}\nraw={sessions:?}\nreference={reference:?}\nactual={implementation:?}\n\
                 counts_so_far[invalid,consistent,inconsistent]={counts:?}"
            );
        }
    }
}

fn report(domain: &str, counts: &[[u64; 3]; 7]) {
    for (level, count) in LEVELS.into_iter().zip(counts) {
        eprintln!(
            "reference domain={domain} implementation=core level={level:?} invalid={} consistent={} inconsistent={} total={}",
            count[0], count[1], count[2], count.iter().sum::<u64>()
        );
    }
}

#[test]
fn core_matches_primary_axioms_on_directed_and_complete_bounded_domain() {
    let mut directed_counts = [[0; 3]; 7];
    for (ordinal, case) in directed_histories().into_iter().enumerate() {
        for (index, level) in LEVELS.into_iter().enumerate() {
            let reference = reference_check(&case.history, level);
            assert_eq!(
                matches!(reference, ReferenceResult::Consistent(_)),
                case.admitted[index],
                "independently stated expectation: case={} level={level:?} raw={:?} reference={reference:?}",
                case.name, case.history
            );
            assert!(
                !matches!(reference, ReferenceResult::Invalid(_)),
                "directed model case must be structurally valid: {} {reference:?}",
                case.name
            );
        }
        compare(
            &case.history,
            case.name,
            u64::try_from(ordinal).unwrap(),
            &mut directed_counts,
        );
    }
    report("directed", &directed_counts);

    let mut malformed_counts = [[0; 3]; 7];
    for (ordinal, (name, history)) in malformed_histories().into_iter().enumerate() {
        for level in LEVELS {
            assert!(
                matches!(
                    reference_check(&history, level),
                    ReferenceResult::Invalid(_)
                ),
                "malformed raw history was not classified separately: {name} {history:?}"
            );
        }
        compare(
            &history,
            name,
            u64::try_from(ordinal).unwrap(),
            &mut malformed_counts,
        );
    }
    report("directed-malformed", &malformed_counts);

    let mut bounded_counts = [[0; 3]; 7];
    let mut ordinal = 0;
    for_each_history(|history| {
        compare(
            history,
            "sessions<=2,transactions<=3,events<=4,variables={0,1}",
            ordinal,
            &mut bounded_counts,
        );
        ordinal += 1;
    });
    report(
        "sessions<=2,transactions<=3,events<=4,variables={0,1}",
        &bounded_counts,
    );
    assert!(
        bounded_counts[0][0] > 0,
        "structurally invalid assignments must not be filtered"
    );
    assert!(bounded_counts[0][1] > 0);
    assert!(bounded_counts[0][2] > 0);
}

#[test]
fn raw_uncommitted_flag_is_not_a_silent_abort_model() {
    let history = vec![vec![Transaction::uncommitted(vec![Event::write(0, 1)])]];
    for level in LEVELS {
        let ReferenceResult::Invalid(reason) = reference_check(&history, level) else {
            panic!("uncommitted transaction must be outside the declared reference domain");
        };
        assert!(reason.contains("completed-committed"), "{reason}");
    }
}

const fn id(session_id: u64, session_height: u64) -> TransactionId {
    TransactionId {
        session_id,
        session_height,
    }
}

fn saturation(edges: &[(TransactionId, TransactionId)]) -> Witness {
    let mut graph = DiGraph::default();
    // Construct public data only; the validator cannot use graph algorithms.
    for &(from, to) in edges {
        graph.adj_map.entry(from).or_default().insert(to);
        graph.adj_map.entry(to).or_default();
    }
    Witness::SaturationOrder(graph)
}

#[test]
fn witness_validation_checks_the_supplied_order_not_just_the_history() {
    let tx = Transaction::committed;
    let w = Event::write;
    let r = Event::read;
    let a = id(1, 0);
    let b = id(2, 0);
    let root = TransactionId::root();
    let history = vec![vec![tx(vec![w(0, 1)])], vec![tx(vec![r(0, 1)])]];
    let validate = |witness| validate_witness(&history, Consistency::Serializable, &witness);
    assert!(validate(Witness::CommitOrder(vec![a, b])).is_ok());
    assert!(validate(Witness::CommitOrder(vec![root, a, b])).is_ok());
    for order in [
        vec![b, a],
        vec![a],
        vec![a, b, b],
        vec![root, root, a, b],
        vec![a, root, b],
        vec![a, b, id(3, 0)],
        vec![a, b, id(0, 1)],
    ] {
        assert!(
            validate(Witness::CommitOrder(order.clone())).is_err(),
            "accepted {order:?}"
        );
    }
    assert!(validate(saturation(&[(a, b)])).is_ok());
    assert!(validate(saturation(&[(b, a)])).is_err());
    assert!(validate(saturation(&[(a, a)])).is_err());
    assert!(validate(saturation(&[(a, b), (b, a)])).is_err());
    assert!(validate(saturation(&[(b, root)])).is_err());
    assert!(validate(saturation(&[(a, id(3, 0))])).is_err());

    let writers = vec![vec![tx(vec![w(0, 1)])], vec![tx(vec![w(0, 2)])]];
    let validate_si = |order| {
        validate_witness(
            &writers,
            Consistency::SnapshotIsolation,
            &Witness::SplitCommitOrder(order),
        )
    };
    assert!(validate_si(vec![(a, false), (a, true), (b, false), (b, true)]).is_ok());
    for order in [
        vec![(a, false), (b, false), (a, true), (b, true)],
        vec![(a, true), (a, false), (b, false), (b, true)],
        vec![(a, false), (a, true), (b, true)],
        vec![(a, false), (a, false), (a, true), (b, false), (b, true)],
        vec![(root, true), (a, false), (a, true), (b, false), (b, true)],
        vec![
            (a, false),
            (a, true),
            (root, false),
            (root, true),
            (b, false),
            (b, true),
        ],
    ] {
        assert!(validate_si(order.clone()).is_err(), "accepted {order:?}");
    }
    assert!(validate_si(vec![
        (root, false),
        (root, true),
        (a, false),
        (a, true),
        (b, false),
        (b, true)
    ])
    .is_ok());

    // A valid history can nevertheless be accompanied by a stale snapshot.
    let stale = vec![
        vec![tx(vec![w(0, 1)])],
        vec![tx(vec![Event::read_empty(0)])],
    ];
    assert!(matches!(
        reference_check(&stale, Consistency::SnapshotIsolation),
        ReferenceResult::Consistent(_)
    ));
    assert!(validate_witness(
        &stale,
        Consistency::SnapshotIsolation,
        &Witness::SplitCommitOrder(vec![(a, false), (a, true), (b, false), (b, true)])
    )
    .is_err());
    assert!(validate_witness(
        &stale,
        Consistency::SnapshotIsolation,
        &Witness::SplitCommitOrder(vec![(b, false), (a, false), (a, true), (b, true)])
    )
    .is_ok());

    // SO orders a predecessor's WRITE before its successor's READ, not only
    // before the successor's write (read-only transactions still have phases).
    let session_history = vec![vec![tx(vec![w(0, 1)]), tx(vec![r(0, 1)])]];
    let next = id(1, 1);
    assert!(validate_witness(
        &session_history,
        Consistency::SnapshotIsolation,
        &Witness::SplitCommitOrder(vec![(a, false), (next, false), (a, true), (next, true)])
    )
    .is_err());

    // A graph's own edges can forbid every valid model order without cycling
    // until the independently reconstructed SO/WR/model constraints are added.
    assert!(validate_witness(&stale, Consistency::Serializable, &saturation(&[(a, b)])).is_err());
}

#[test]
fn empty_omission_preserves_session_constraints_and_rejects_phantom_vertices() {
    let tx = Transaction::committed;
    let a = id(1, 0);
    let empty = id(1, 1);
    let c = id(1, 2);
    let history = vec![
        vec![
            tx(vec![Event::write(0, 1)]),
            tx(vec![]),
            tx(vec![Event::read(0, 1)]),
        ],
        vec![],
    ];
    for level in LEVELS {
        assert!(validate_witness(&history, level, &Witness::CommitOrder(vec![a, c])).is_ok());
        assert!(validate_witness(&history, level, &Witness::CommitOrder(vec![c, a])).is_err());
        assert!(
            validate_witness(&history, level, &Witness::CommitOrder(vec![a, c, empty])).is_err()
        );
        assert!(
            validate_witness(&history, level, &Witness::CommitOrder(vec![a, c, id(2, 0)])).is_err()
        );
    }
    assert!(validate_witness(
        &history,
        Consistency::SnapshotIsolation,
        &Witness::SplitCommitOrder(vec![(a, false), (a, true), (c, false), (c, true)])
    )
    .is_ok());
    assert!(validate_witness(
        &history,
        Consistency::SnapshotIsolation,
        &Witness::SplitCommitOrder(vec![(c, false), (a, false), (a, true), (c, true)])
    )
    .is_err());
    for all_empty in [
        vec![],
        vec![vec![]],
        vec![vec![tx(vec![])]],
        vec![vec![], vec![tx(vec![]), tx(vec![])]],
    ] {
        for level in LEVELS {
            assert!(validate_witness(&all_empty, level, &Witness::CommitOrder(vec![])).is_ok());
            assert!(
                validate_witness(&all_empty, level, &Witness::SplitCommitOrder(vec![])).is_ok()
            );
            assert!(validate_witness(&all_empty, level, &saturation(&[])).is_ok());
            let phantom = id(3, 0);
            assert!(
                validate_witness(&all_empty, level, &Witness::CommitOrder(vec![phantom])).is_err()
            );
            assert!(validate_witness(
                &all_empty,
                level,
                &saturation(&[(phantom, TransactionId::root())])
            )
            .is_err());
        }
    }
}

#[test]
fn finite_generator_includes_empty_readonly_and_structurally_invalid_assignments() {
    let mut count = 0_u64;
    let mut empty_shapes = BTreeSet::new();
    let mut readonly = false;
    let mut invalid = false;
    let mut future_source = false;
    let mut event_distribution = [[0_u64; 5]; 4];
    for_each_history(|history| {
        count += 1;
        let transactions = history.iter().map(Vec::len).sum::<usize>();
        let events = history
            .iter()
            .flatten()
            .map(|tx| tx.events.len())
            .sum::<usize>();
        assert!(history.len() <= 2 && transactions <= 3 && events <= 4);
        event_distribution[transactions][events] += 1;
        if events == 0 {
            empty_shapes.insert(history.iter().map(Vec::len).collect::<Vec<_>>());
        }
        readonly |= history.iter().flatten().any(|tx| {
            !tx.events.is_empty()
                && tx
                    .events
                    .iter()
                    .all(|event| matches!(event, Event::Read { .. }))
        });
        if let ReferenceResult::Invalid(reason) =
            reference_check(history, Consistency::CommittedRead)
        {
            invalid = true;
            future_source |= reason.contains("non-preceding local");
        }
        for event in history.iter().flatten().flat_map(|tx| &tx.events) {
            let variable = match event {
                Event::Read { variable, .. } | Event::Write { variable, .. } => variable,
            };
            assert!(*variable <= 1);
        }
    });
    // Each event is a write (two variable choices) or a read (two
    // initial encodings per variable, plus one source per write):
    // F(e) = sum_k C(e,k) * 2^k * (4+k)^(e-k).
    // Session/event weak compositions then give these independent totals.
    assert_eq!(
        event_distribution,
        [
            [3, 0, 0, 0, 0],
            [3, 18, 120, 882, 7_080],
            [4, 48, 480, 4_704, 47_200],
            [5, 90, 1_200, 14_700, 177_000],
        ]
    );
    assert_eq!(count, 253_537);
    // For s sessions and t transactions, weak compositions enumerate all
    // ordered partitions: C(t+s-1,s-1), plus the zero-session history.
    assert_eq!(empty_shapes.len(), 1 + 4 + (1 + 2 + 3 + 4));
    assert!(empty_shapes.contains(&vec![]));
    assert!(empty_shapes.contains(&vec![0, 0]));
    assert!(empty_shapes.contains(&vec![0, 3]));
    assert!(empty_shapes.contains(&vec![3, 0]));
    assert!(readonly && invalid && future_source);
    eprintln!("generator total={count} by_transactions_then_events={event_distribution:?} empty_partition_shapes={}", empty_shapes.len());
}

fn fixture_history(text: &str) -> History {
    // Core's default features deliberately omit serde for its public types.
    // Decode the raw payload of the CLI's History wrapper without enabling it.
    let wrapper: serde_json::Value = serde_json::from_str(text).unwrap();
    wrapper["data"]
        .as_array()
        .unwrap()
        .iter()
        .map(|session| {
            session
                .as_array()
                .unwrap()
                .iter()
                .map(|tx| {
                    assert_eq!(tx["committed"], true);
                    Transaction::committed(
                        tx["events"]
                            .as_array()
                            .unwrap()
                            .iter()
                            .map(|event| {
                                let event = event.as_array().unwrap();
                                let variable = event[1].as_u64().unwrap();
                                match event[0].as_str().unwrap() {
                                    "w" => Event::write(variable, event[2].as_u64().unwrap()),
                                    "r" => Event::Read {
                                        variable,
                                        version: if event[2].is_null() {
                                            None
                                        } else {
                                            Some(event[2].as_u64().unwrap())
                                        },
                                    },
                                    other => panic!("unexpected fixture operation {other}"),
                                }
                            })
                            .collect(),
                    )
                })
                .collect()
        })
        .collect()
}

#[test]
fn cli_json_smoke_payloads_have_independent_reference_expectations() {
    let mut counts = [[0; 3]; 7];
    for (name, text, serializable) in [
        (
            "serial",
            include_str!("fixtures/axiomatic/serial.json"),
            true,
        ),
        (
            "write-skew",
            include_str!("fixtures/axiomatic/write-skew.json"),
            false,
        ),
    ] {
        let history = fixture_history(text);
        assert!(
            matches!(
                reference_check(&history, Consistency::SnapshotIsolation),
                ReferenceResult::Consistent(_)
            ),
            "{name}"
        );
        assert_eq!(
            matches!(
                reference_check(&history, Consistency::Serializable),
                ReferenceResult::Consistent(_)
            ),
            serializable,
            "{name}"
        );
        compare(&history, name, 0, &mut counts);
    }
    report("cli-json-fixtures", &counts);
}
