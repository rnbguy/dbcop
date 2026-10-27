#![allow(clippy::too_many_lines)]
#[path = "common/axiomatic_reference.rs"]
mod axiomatic_reference;
#[path = "common/tlc.rs"]
mod tlc;
#[path = "common/tlc_replica.rs"]
mod tlc_replica;

use std::collections::{BTreeMap, BTreeSet};

use axiomatic_reference::{
    directed_histories, for_each_history, malformed_histories, reference_check, validate_witness,
    DirectedCase, History, ReferenceResult, LEVELS,
};
use dbcop_core::consistency::error::Error;
use dbcop_core::consistency::incremental::IncrementalChecker;
use dbcop_core::history::raw::types::{Event, Transaction};
use dbcop_core::{check, Consistency};
use tlc::Verdict;

fn classification(history: &History, level: Consistency) -> Verdict {
    match reference_check(history, level) {
        ReferenceResult::Invalid(_) => Verdict::Invalid,
        ReferenceResult::Consistent(_) => Verdict::Consistent,
        ReferenceResult::Inconsistent => Verdict::Inconsistent,
    }
}
fn compare(histories: &[History], counts: &mut [[u64; 3]; 7]) -> Vec<[Verdict; 7]> {
    let oracle = tlc::check_histories(histories);
    for (history, verdicts) in histories.iter().zip(&oracle) {
        for (i, level) in LEVELS.into_iter().enumerate() {
            let expected = classification(history, level);
            let core = check(history, level);
            let actual = match &core {
                Ok(_) => Verdict::Consistent,
                Err(Error::NonAtomic(
                    dbcop_core::history::raw::error::Error::NonRepeatableRead { .. },
                )) => Verdict::Inconsistent,
                Err(Error::NonAtomic(_)) => Verdict::Invalid,
                Err(_) => Verdict::Inconsistent,
            };
            assert_eq!(
                verdicts[i], expected,
                "TLC/reference level={level:?} raw={history:?}"
            );
            assert_eq!(
                actual, expected,
                "core/reference level={level:?} raw={history:?} core={core:?}"
            );
            if let Ok(witness) = core {
                validate_witness(history, level, &witness).unwrap_or_else(|e| {
                    panic!("{e}; level={level:?} raw={history:?} witness={witness:?}")
                });
            }
            counts[i][match expected {
                Verdict::Invalid => 0,
                Verdict::Consistent => 1,
                Verdict::Inconsistent => 2,
            }] += 1;
        }
    }
    oracle
}
fn three_replica_cases() -> Vec<DirectedCase> {
    let tx = Transaction::committed;
    let w = Event::write;
    let r = Event::read;
    let initial = Event::read_empty;
    vec![
        DirectedCase {
            name: "three-replica-causal-chain",
            history: vec![
                vec![tx(vec![w(0, 1)])],
                vec![tx(vec![r(0, 1), w(1, 2)])],
                vec![tx(vec![r(1, 2), initial(0)])],
            ],
            admitted: [true, true, true, false, false, false, false],
        },
        // Three initial snapshots conflict pairwise on x; PC permits them, SI's
        // first-committer-wins premise and SER's latest-writer rule forbid them.
        DirectedCase {
            name: "three-replica-conflicting-writers",
            history: (1..=3)
                .map(|v| vec![tx(vec![initial(0), w(0, v)])])
                .collect(),
            admitted: [true, true, true, true, true, false, false],
        },
    ]
}
#[test]
#[ignore = "requires pinned DBCOP_TLC_JAR; exhaustive semantic gate"]
fn tlc_semantic_reference() {
    let mut counts = [[0; 3]; 7];
    let mut directed = directed_histories();
    directed.extend(three_replica_cases());
    directed.push(DirectedCase {
        name: "empty",
        history: vec![],
        admitted: [true; 7],
    });
    for case in &directed {
        for (i, level) in LEVELS.into_iter().enumerate() {
            assert_eq!(
                classification(&case.history, level),
                if case.admitted[i] {
                    Verdict::Consistent
                } else {
                    Verdict::Inconsistent
                },
                "independent expectation {} raw={:?}",
                case.name,
                case.history
            );
        }
    }
    compare(
        &directed.into_iter().map(|c| c.history).collect::<Vec<_>>(),
        &mut counts,
    );
    let malformed: Vec<_> = malformed_histories().into_iter().map(|(_, h)| h).collect();
    for h in &malformed {
        assert_eq!(classification(h, LEVELS[0]), Verdict::Invalid);
    }
    compare(&malformed, &mut counts);
    eprintln!("directed+malformed counts [invalid,consistent,inconsistent]={counts:?}");
    counts = [[0; 3]; 7];
    let mut batch = Vec::new();
    let mut total = 0;
    for_each_history(|h| {
        batch.push(h.to_vec());
        total += 1;
        if batch.len() == 256 {
            compare(&batch, &mut counts);
            batch.clear();
        }
    });
    if !batch.is_empty() {
        compare(&batch, &mut counts);
    }
    assert_eq!(total, 253_537);
    eprintln!("TLC finite domain: sessions<=2 transactions TOTAL<=3 events TOTAL<=4 keys=0/1 cases={total}; directed+malformed counted separately above");
    for (level, count) in LEVELS.into_iter().zip(counts) {
        eprintln!(
            "TLC/core/reference {level:?}: invalid={} consistent={} inconsistent={}",
            count[0], count[1], count[2]
        );
    }
}
#[test]
#[ignore = "requires pinned DBCOP_TLC_JAR; complete finite replica graphs"]
fn tlc_replica_histories() {
    tlc_replica::exhaustive_campaign();
}
#[test]
#[ignore = "requires pinned DBCOP_TLC_JAR; seeded 100-trace larger replica campaign"]
fn tlc_replica_simulation() {
    tlc_replica::simulation_campaign();
}
#[test]
#[ignore = "requires pinned DBCOP_TLC_JAR; actual runtime and parser failure boundaries"]
fn tlc_harness_error_boundaries() {
    tlc::error_boundaries();
}

fn tentative(history: &History, session: usize, events: &[Event<u64, u64>]) -> History {
    let mut result = history.clone();
    result.resize_with(result.len().max(session + 1), Vec::new);
    result[session].push(Transaction::committed(events.to_vec()));
    result
}
// Every exact tentative history is retained and independently checked by TLC in
// batches after exercising the lifecycle, not converted from a production graph.
#[derive(Default)]
struct Queries {
    histories: BTreeMap<String, (History, [Verdict; 7])>,
    operations: [u64; 5],
}
impl Queries {
    fn expected(&mut self, history: History, level: Consistency) -> bool {
        let (_, verdicts) = self
            .histories
            .entry(format!("{history:?}"))
            .or_insert_with(|| {
                let verdicts = LEVELS.map(|candidate| classification(&history, candidate));
                (history, verdicts)
            });
        verdicts[LEVELS
            .iter()
            .position(|candidate| {
                std::mem::discriminant(candidate) == std::mem::discriminant(&level)
            })
            .unwrap()]
            == Verdict::Consistent
    }
}
fn fresh(history: &History, arrival: &[usize], level: Consistency) -> IncrementalChecker {
    let mut checker = IncrementalChecker::new(level);
    let mut heights = vec![0; history.len()];
    for &s in arrival {
        checker
            .check_transaction(s as u64 + 1, &history[s][heights[s]].events, true)
            .expect("fresh committed replay");
        heights[s] += 1;
    }
    checker
}
fn probes(
    checker: &mut IncrementalChecker,
    history: &History,
    arrival: &[usize],
    level: Consistency,
    queries: &mut Queries,
) {
    let mut baseline = fresh(history, arrival, level);
    let mut candidates = BTreeSet::from([(0, None), (1, None), (0, Some(0)), (1, Some(0))]);
    for event in history.iter().flatten().flat_map(|tx| &tx.events) {
        if let Event::Write { variable, version } = *event {
            candidates.insert((variable, Some(version)));
        }
    }
    for session in 0..=history.len() {
        for &(variable, version) in &candidates {
            let exact = tentative(history, session, &[Event::Read { variable, version }]);
            let expected = queries.expected(exact.clone(), level);
            let actual = checker
                .check_read(session as u64 + 1, variable, version, false)
                .is_ok();
            let new = baseline
                .check_read(session as u64 + 1, variable, version, false)
                .is_ok();
            assert_eq!((actual, new), (expected, expected), "successors after trial/reject/reset: level={level:?} arrival={arrival:?} raw={exact:?}");
            queries.operations[0] += 2;
        }
    }
}
fn live_probes(
    checker: &mut IncrementalChecker,
    history: &History,
    arrival: &[usize],
    level: Consistency,
    session: usize,
    live: &[Event<u64, u64>],
    queries: &mut Queries,
) {
    let mut baseline = fresh(history, arrival, level);
    for event in live {
        if let Event::Read { variable, version } = *event {
            baseline
                .check_read(session as u64 + 1, variable, version, true)
                .expect("fresh live replay");
        }
    }
    let mut candidates = BTreeSet::from([(0, None), (1, None), (0, Some(0)), (1, Some(0))]);
    for event in history.iter().flatten().flat_map(|tx| &tx.events) {
        if let Event::Write { variable, version } = *event {
            candidates.insert((variable, Some(version)));
        }
    }
    for (variable, version) in candidates {
        let mut events = live.to_vec();
        events.push(Event::Read { variable, version });
        let exact = tentative(history, session, &events);
        let expected = queries.expected(exact.clone(), level);
        let retained = checker
            .check_read(session as u64 + 1, variable, version, false)
            .is_ok();
        let new = baseline
            .check_read(session as u64 + 1, variable, version, false)
            .is_ok();
        assert_eq!((retained, new), (expected, expected), "live successors after trial/rejection level={level:?} arrival={arrival:?} raw={exact:?}");
        queries.operations[0] += 2;
    }
}
fn lifecycle(history: &History, arrival: &[usize], queries: &mut Queries) {
    for level in LEVELS {
        let mut checker = IncrementalChecker::new(level);
        let mut committed = vec![Vec::new(); history.len()];
        let mut accepted_arrival = Vec::new();
        for &s in arrival {
            let tx = &history[s][committed[s].len()];
            let exact = tentative(&committed, s, &tx.events);
            let expected = queries.expected(exact.clone(), level);
            let trial = checker.check_transaction(s as u64 + 1, &tx.events, false);
            assert_eq!(
                trial.is_ok(),
                expected,
                "transaction trial {level:?} raw={exact:?} arrival={arrival:?} actual={trial:?}"
            );
            queries.operations[1] += 1;
            probes(&mut checker, &committed, &accepted_arrival, level, queries);
            let mut live = Vec::new();
            let mut local = BTreeSet::new();
            for event in &tx.events {
                match *event {
                    Event::Write { variable, .. } => {
                        local.insert(variable);
                    }
                    Event::Read { variable, version } if !local.contains(&variable) => {
                        let mut candidate = live.clone();
                        candidate.push(Event::Read { variable, version });
                        let raw = tentative(&committed, s, &candidate);
                        let allowed = queries.expected(raw.clone(), level);
                        let trial = checker.check_read(s as u64 + 1, variable, version, false);
                        assert_eq!(
                            trial.is_ok(),
                            allowed,
                            "read trial {level:?} raw={raw:?} arrival={arrival:?}"
                        );
                        live_probes(
                            &mut checker,
                            &committed,
                            &accepted_arrival,
                            level,
                            s,
                            &live,
                            queries,
                        );
                        let result = checker.check_read(s as u64 + 1, variable, version, true);
                        assert_eq!(
                            result.is_ok(),
                            allowed,
                            "read accept/reject {level:?} raw={raw:?} arrival={arrival:?}"
                        );
                        queries.operations[0] += 1;
                        queries.operations[2] += 1;
                        if allowed {
                            live = candidate;
                        }
                        live_probes(
                            &mut checker,
                            &committed,
                            &accepted_arrival,
                            level,
                            s,
                            &live,
                            queries,
                        );
                        // Reset must also undo any rejected read's temporary work.
                        checker.reset_transaction();
                        queries.operations[3] += 1;
                        probes(&mut checker, &committed, &accepted_arrival, level, queries);
                        for previous in &live {
                            if let Event::Read { variable, version } = *previous {
                                checker
                                    .check_read(s as u64 + 1, variable, version, true)
                                    .expect("restore live prefix");
                            }
                        }
                    }
                    Event::Read { .. } => {}
                }
            }
            checker.reset_transaction();
            queries.operations[3] += 1;
            probes(&mut checker, &committed, &accepted_arrival, level, queries);
            let commit = checker.check_transaction(s as u64 + 1, &tx.events, true);
            assert_eq!(
                commit.is_ok(),
                expected,
                "commit/reject {level:?} raw={exact:?} arrival={arrival:?}"
            );
            queries.operations[4] += 1;
            if expected {
                committed[s].push(tx.clone());
                accepted_arrival.push(s);
            }
            probes(&mut checker, &committed, &accepted_arrival, level, queries);
            if !expected {
                break;
            }
        }
    }
}
fn arrivals(
    history: &History,
    heights: &mut [usize],
    order: &mut Vec<usize>,
    visit: &mut impl FnMut(&[usize]),
) {
    if heights.iter().sum::<usize>() == history.iter().map(Vec::len).sum::<usize>() {
        visit(order);
        return;
    }
    let mut available = BTreeSet::new();
    for (s, count) in heights.iter().enumerate() {
        for tx in &history[s][..*count] {
            for event in &tx.events {
                if let Event::Write { variable, version } = *event {
                    available.insert((variable, version));
                }
            }
        }
    }
    for s in 0..history.len() {
        let Some(tx) = history[s].get(heights[s]) else {
            continue;
        };
        let mut local = BTreeMap::new();
        let mut ready = true;
        for event in &tx.events {
            match *event {
                Event::Write { variable, version } => {
                    local.insert(variable, version);
                }
                Event::Read {
                    variable,
                    version: Some(version),
                } if !local.contains_key(&variable)
                    && version != 0
                    && !available.contains(&(variable, version)) =>
                {
                    ready = false;
                }
                Event::Read { .. } => {}
            }
        }
        if ready {
            heights[s] += 1;
            order.push(s);
            arrivals(history, heights, order, visit);
            order.pop();
            heights[s] -= 1;
        }
    }
}
#[test]
#[ignore = "requires pinned DBCOP_TLC_JAR; source-available incremental lifecycle gate"]
fn tlc_incremental_reference() {
    tlc::check_histories(&[]); // Fail before lifecycle exploration if the pinned runtime is missing.
    let mut queries = Queries::default();
    let mut cases = 0u64;
    let mut excluded = 0u64;
    let mut visit = |history: &History| {
        if classification(history, LEVELS[0]) == Verdict::Invalid
            || history
                .iter()
                .flatten()
                .flat_map(|tx| &tx.events)
                .any(|e| matches!(e, Event::Write { version: 0, .. }))
        {
            excluded += 1;
            return;
        }
        let mut found = false;
        arrivals(
            history,
            &mut vec![0; history.len()],
            &mut Vec::new(),
            &mut |order| {
                found = true;
                cases += 1;
                lifecycle(history, order, &mut queries);
            },
        );
        if !found {
            excluded += 1;
        }
    };
    for_each_history(|history| visit(&history.to_vec()));
    for case in directed_histories()
        .into_iter()
        .chain(three_replica_cases())
    {
        visit(&case.history);
    }
    let histories: Vec<_> = queries
        .histories
        .into_values()
        .map(|(history, _)| history)
        .collect();
    eprintln!("incremental lifecycle completed: cases={cases} exact tentative histories={} queued for TLC", histories.len());
    let mut counts = [[0; 3]; 7];
    compare(&histories, &mut counts);
    eprintln!("incremental source-available cases={cases} excluded invalid/future-source/write-zero={excluded} exact tentative histories={} operations[read trials,transaction trials,read accepts/rejects,resets,commits/rejects]={:?}", histories.len(), queries.operations);
}

#[test]
#[ignore = "requires pinned DBCOP_TLC_JAR; cross-batch verdict ordering"]
fn tlc_parallel_batch_order() {
    let tx = Transaction::committed;
    let mut histories = vec![vec![]; 256];
    histories.extend(vec![
        vec![
            vec![tx(vec![Event::write(0, 1)])],
            vec![tx(vec![Event::read(0, 1), Event::read_empty(0)])]
        ];
        256
    ]);
    histories.extend(vec![vec![vec![tx(vec![Event::read(0, 9)])]]; 256]);
    histories.extend(vec![
        vec![vec![
            tx(vec![Event::write(0, 1)]),
            tx(vec![Event::read_empty(0)])
        ]];
        256
    ]);
    histories.push(vec![]);
    let verdicts = tlc::check_histories(&histories);
    for (index, actual) in verdicts.into_iter().enumerate() {
        let expected = match index {
            0..256 | 1024 => [Verdict::Consistent; 7],
            256..512 => [Verdict::Inconsistent; 7],
            512..768 => [Verdict::Invalid; 7],
            _ => [
                Verdict::Consistent,
                Verdict::Consistent,
                Verdict::Inconsistent,
                Verdict::Inconsistent,
                Verdict::Inconsistent,
                Verdict::Inconsistent,
                Verdict::Inconsistent,
            ],
        };
        assert_eq!(actual, expected, "parallel batch boundary at case {index}");
    }
}

#[cfg(feature = "serde")]
#[test]
#[ignore = "Go integration supplies DBCOP_GO_HISTORIES; requires pinned TLC"]
fn tlc_go_histories() {
    let path = std::env::var("DBCOP_GO_HISTORIES").expect("Go history input path");
    let input = std::fs::read(path).expect("read Go history input");
    let cases: Vec<(History, [Option<bool>; 7])> =
        serde_json::from_slice(&input).expect("decode primitive Go histories");
    assert!(!cases.is_empty(), "no Go observations supplied");
    let (histories, expected): (Vec<_>, Vec<_>) = cases.into_iter().unzip();
    let verdicts = compare(&histories, &mut [[0; 3]; 7]);
    for ((history, expected), verdicts) in histories.iter().zip(expected).zip(verdicts) {
        assert!(
            expected.iter().any(Option::is_some),
            "unconstrained Go observation"
        );
        for (level, expected) in expected.into_iter().enumerate() {
            if let Some(expected) = expected {
                assert_eq!(
                    verdicts[level] == Verdict::Consistent,
                    expected,
                    "Go/TLC level={:?} raw={history:?}",
                    LEVELS[level]
                );
            }
        }
    }
    println!("DBCOP_GO_TLC_VERIFIED|{}", histories.len());
}
