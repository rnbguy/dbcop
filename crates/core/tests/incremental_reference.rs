use std::collections::{BTreeMap, BTreeSet};
use std::fmt::Debug;

use dbcop_core::consistency::incremental::IncrementalChecker;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::Consistency;

#[path = "common/axiomatic_reference.rs"]
mod axiomatic_reference;

use axiomatic_reference::{for_each_history, reference_check, ReferenceResult};

type History = Vec<Session<u64, u64>>;
type Read = (u64, Option<u64>);

const LEVELS: [Consistency; 7] = [
    Consistency::CommittedRead,
    Consistency::RepeatableRead,
    Consistency::AtomicRead,
    Consistency::Causal,
    Consistency::Prefix,
    Consistency::SnapshotIsolation,
    Consistency::Serializable,
];

#[derive(Default, Debug)]
struct Counts {
    cases: usize,
    invalid_prefixes: usize,
    inconsistent_prefixes: usize,
    transaction_trials: usize,
    commits: usize,
    rejected_commits: usize,
    read_trials: usize,
    read_accepts: usize,
    rejected_reads: usize,
    successor_sets: usize,
    resets: usize,
}

// Arrival order is part of the key: equivalent histories can exercise different
// persistent witness/cache states when their committed transactions arrive apart.
#[derive(PartialEq, Eq, PartialOrd, Ord)]
struct Case {
    prefix: Vec<Vec<Vec<Event<u64, u64>>>>,
    arrival: Vec<usize>,
    session: usize,
    events: Vec<Event<u64, u64>>,
}

fn tentative(history: &[Session<u64, u64>], session: usize, events: &[Event<u64, u64>]) -> History {
    let mut history = history.to_vec();
    history.resize_with(history.len().max(session + 1), Vec::new);
    history[session].push(Transaction::committed(events.to_vec()));
    history
}

const fn is_consistent(result: &ReferenceResult) -> bool {
    matches!(result, ReferenceResult::Consistent(_))
}

fn describe(result: &ReferenceResult) -> String {
    match result {
        ReferenceResult::Invalid(error) => format!("structurally invalid: {error}"),
        ReferenceResult::Consistent(order) => format!("consistent, order={order:?}"),
        ReferenceResult::Inconsistent => "inconsistent".to_owned(),
    }
}

fn assert_verdict(
    result: &Result<(), impl Debug>,
    history: &[Session<u64, u64>],
    level: Consistency,
    arrival: &[usize],
    operation: &str,
) -> bool {
    let reference = reference_check(history, level);
    let expected = is_consistent(&reference);
    assert_eq!(
        result.is_ok(),
        expected,
        "{operation}: level={level:?}, implementation=incremental, result={result:?}, \
         reference={}, arrival (zero-based sessions)={arrival:?}, exact history={history:?}",
        describe(&reference),
    );
    expected
}

fn fresh_checker(
    history: &[Session<u64, u64>],
    arrival: &[usize],
    level: Consistency,
    session: usize,
    live: &[Event<u64, u64>],
) -> IncrementalChecker {
    let mut checker = IncrementalChecker::new(level);
    let mut heights = vec![0; history.len()];
    let mut prefix = vec![Vec::new(); history.len()];
    for &source_session in arrival {
        let transaction = &history[source_session][heights[source_session]];
        let result =
            checker.check_transaction(source_session as u64 + 1, &transaction.events, true);
        prefix[source_session].push(transaction.clone());
        assert!(
            assert_verdict(
                &result,
                &prefix,
                level,
                arrival,
                "fresh committed-prefix replay"
            ),
            "only consistent committed prefixes may be replayed",
        );
        heights[source_session] += 1;
    }
    for (position, event) in live.iter().enumerate() {
        let Event::Read { variable, version } = *event else {
            panic!("check_read accepts external reads only: {live:?}");
        };
        let result = checker.check_read(session as u64 + 1, variable, version, true);
        assert!(assert_verdict(
            &result,
            &tentative(history, session, &live[..=position]),
            level,
            arrival,
            "fresh accepted-read replay",
        ));
    }
    checker
}

fn candidates(history: &[Session<u64, u64>], variables: &BTreeSet<u64>) -> Vec<Read> {
    let mut reads = BTreeSet::new();
    for &variable in variables {
        reads.insert((variable, None));
        // The incremental API reserves write-version zero; both forms denote root.
        reads.insert((variable, Some(0)));
    }
    for transaction in history.iter().flatten() {
        for event in &transaction.events {
            if let Event::Write { variable, version } = *event {
                assert_ne!(
                    version, 0,
                    "write zero is outside the incremental API domain"
                );
                reads.insert((variable, Some(version)));
            }
        }
    }
    reads.into_iter().collect()
}

struct Probe<'a> {
    history: &'a [Session<u64, u64>],
    arrival: &'a [usize],
    variables: &'a BTreeSet<u64>,
    level: Consistency,
}

impl Probe<'_> {
    fn successors(
        &self,
        checker: &mut IncrementalChecker,
        session: usize,
        live: &[Event<u64, u64>],
        counts: &mut Counts,
        operation: &str,
    ) -> BTreeSet<Read> {
        let mut fresh = fresh_checker(self.history, self.arrival, self.level, session, live);
        let mut admitted = BTreeSet::new();
        let mut fresh_admitted = BTreeSet::new();
        let mut reference_admitted = BTreeSet::new();
        for (variable, version) in candidates(self.history, self.variables) {
            let mut events = live.to_vec();
            events.push(Event::Read { variable, version });
            let history = tentative(self.history, session, &events);
            let result = checker.check_read(session as u64 + 1, variable, version, false);
            let expected = assert_verdict(&result, &history, self.level, self.arrival, operation);
            let fresh_result = fresh.check_read(session as u64 + 1, variable, version, false);
            assert_verdict(
                &fresh_result,
                &history,
                self.level,
                self.arrival,
                "fresh successor-set trial",
            );
            counts.read_trials += 2;
            if result.is_ok() {
                admitted.insert((variable, version));
            }
            if fresh_result.is_ok() {
                fresh_admitted.insert((variable, version));
            }
            if expected {
                reference_admitted.insert((variable, version));
            }
        }
        assert_eq!(
            admitted, fresh_admitted,
            "{operation}: retained/fresh successor sets"
        );
        assert_eq!(
            admitted, reference_admitted,
            "{operation}: retained/reference successor sets"
        );
        counts.successor_sets += 1;
        admitted
    }

    fn committed_successors(&self, checker: &mut IncrementalChecker, counts: &mut Counts) {
        // Include one unused positive session to expose leaked heights/vertices.
        for session in 0..=self.history.len() {
            self.successors(
                checker,
                session,
                &[],
                counts,
                "committed-prefix successor set",
            );
        }
    }

    fn restore_live(
        &self,
        checker: &mut IncrementalChecker,
        session: usize,
        live: &[Event<u64, u64>],
        counts: &mut Counts,
    ) {
        for (position, event) in live.iter().enumerate() {
            let Event::Read { variable, version } = *event else {
                unreachable!("only external reads enter the live API");
            };
            let result = checker.check_read(session as u64 + 1, variable, version, true);
            assert!(assert_verdict(
                &result,
                &tentative(self.history, session, &live[..=position]),
                self.level,
                self.arrival,
                "restoring live reads after reset",
            ));
            counts.read_accepts += 1;
        }
    }

    fn exercise_candidates(
        &self,
        checker: &mut IncrementalChecker,
        session: usize,
        live: &[Event<u64, u64>],
        counts: &mut Counts,
    ) {
        let before = self.successors(checker, session, live, counts, "before candidate trial");
        for (variable, version) in candidates(self.history, self.variables) {
            let mut extended = live.to_vec();
            extended.push(Event::Read { variable, version });
            let history = tentative(self.history, session, &extended);
            let trial = checker.check_read(session as u64 + 1, variable, version, false);
            let expected = assert_verdict(
                &trial,
                &history,
                self.level,
                self.arrival,
                "candidate read trial",
            );
            counts.read_trials += 1;
            assert_eq!(
                self.successors(checker, session, live, counts, "after candidate trial"),
                before,
                "a successful or rejected trial must not change admitted successors",
            );
            let accepted = checker.check_read(session as u64 + 1, variable, version, true);
            assert_verdict(
                &accepted,
                &history,
                self.level,
                self.arrival,
                "candidate read acceptance",
            );
            counts.read_accepts += 1;
            if !expected {
                counts.rejected_reads += 1;
            }
            self.successors(
                checker,
                session,
                if expected { &extended } else { live },
                counts,
                "after candidate acceptance or rejection",
            );
            checker.reset_transaction();
            counts.resets += 1;
            self.committed_successors(checker, counts);
            self.restore_live(checker, session, live, counts);
            assert_eq!(
                self.successors(
                    checker,
                    session,
                    live,
                    counts,
                    "after reset and restoration"
                ),
                before,
            );
        }
    }

    fn transaction_trial(
        &self,
        checker: &mut IncrementalChecker,
        session: usize,
        events: &[Event<u64, u64>],
        live: &[Event<u64, u64>],
        counts: &mut Counts,
    ) {
        let result = checker.check_transaction(session as u64 + 1, events, false);
        assert_verdict(
            &result,
            &tentative(self.history, session, events),
            self.level,
            self.arrival,
            "tentative transaction (commit=false)",
        );
        counts.transaction_trials += 1;
        // check_transaction discards the retained live prefix even on a trial.
        self.committed_successors(checker, counts);
        self.restore_live(checker, session, live, counts);
        self.successors(
            checker,
            session,
            live,
            counts,
            "after transaction trial and live restoration",
        );
    }
}

fn exercise_case(
    history: &[Session<u64, u64>],
    arrival: &[usize],
    session: usize,
    events: &[Event<u64, u64>],
    level: Consistency,
    counts: &mut Counts,
) {
    match reference_check(history, level) {
        ReferenceResult::Invalid(_) => {
            counts.invalid_prefixes += 1;
            return;
        }
        ReferenceResult::Inconsistent => {
            counts.inconsistent_prefixes += 1;
            return;
        }
        ReferenceResult::Consistent(_) => {}
    }
    counts.cases += 1;
    let mut variables = BTreeSet::from([0, 1]);
    for event in history
        .iter()
        .flatten()
        .flat_map(|transaction| &transaction.events)
        .chain(events)
    {
        variables.insert(event.variable());
    }
    let probe = Probe {
        history,
        arrival,
        variables: &variables,
        level,
    };
    let mut checker = fresh_checker(history, arrival, level, session, &[]);
    let mut live = Vec::new();
    let mut local = BTreeMap::new();
    let mut live_rejected = false;
    probe.committed_successors(&mut checker, counts);
    probe.exercise_candidates(&mut checker, session, &live, counts);
    for (position, event) in events.iter().enumerate() {
        match *event {
            Event::Write { variable, version } => {
                local.insert(variable, version);
            }
            Event::Read { variable, version }
                if !local.contains_key(&variable) && !live_rejected =>
            {
                // check_read has no write API: its exact tentative history contains
                // only external reads. Ordered local writes/reads are tested below.
                let mut extended = live.clone();
                extended.push(event.clone());
                let result = checker.check_read(session as u64 + 1, variable, version, true);
                if assert_verdict(
                    &result,
                    &tentative(history, session, &extended),
                    level,
                    arrival,
                    "generated external-read acceptance",
                ) {
                    live = extended;
                    probe.exercise_candidates(&mut checker, session, &live, counts);
                } else {
                    live_rejected = true;
                    counts.rejected_reads += 1;
                    probe.successors(
                        &mut checker,
                        session,
                        &live,
                        counts,
                        "after generated rejected read",
                    );
                }
                counts.read_accepts += 1;
            }
            Event::Read { .. } => {}
        }
        probe.transaction_trial(&mut checker, session, &events[..=position], &live, counts);
    }
    if events.is_empty() {
        probe.transaction_trial(&mut checker, session, events, &live, counts);
    }
    let result = checker.check_transaction(session as u64 + 1, events, true);
    let extended = tentative(history, session, events);
    if assert_verdict(
        &result,
        &extended,
        level,
        arrival,
        "generated transaction commit",
    ) {
        counts.commits += 1;
        let mut next_arrival = arrival.to_vec();
        next_arrival.push(session);
        Probe {
            history: &extended,
            arrival: &next_arrival,
            variables: &variables,
            level,
        }
        .committed_successors(&mut checker, counts);
    } else {
        counts.rejected_commits += 1;
        probe.committed_successors(&mut checker, counts);
    }
}

// A source must exist now, not merely somewhere in the final raw history. Local
// sources must precede the read; local mismatch/overwritten-source errors remain
// representable and are compared rather than filtered out.
fn sources_available(history: &[Session<u64, u64>], events: &[Event<u64, u64>]) -> bool {
    let mut available = BTreeSet::new();
    for event in history
        .iter()
        .flatten()
        .flat_map(|transaction| &transaction.events)
    {
        if let Event::Write { variable, version } = *event {
            available.insert((variable, version));
        }
    }
    for event in events {
        match *event {
            Event::Write { variable, version } => {
                assert_ne!(version, 0);
                available.insert((variable, version));
            }
            Event::Read {
                variable,
                version: Some(version),
            } if version != 0 && !available.contains(&(variable, version)) => return false,
            Event::Read { .. } => {}
        }
    }
    true
}

fn for_each_arrival(
    history: &[Session<u64, u64>],
    prefix: &mut History,
    arrival: &mut Vec<usize>,
    visit: &mut impl FnMut(&[Session<u64, u64>], &[usize], usize, &[Event<u64, u64>]),
) -> (usize, usize) {
    if prefix.iter().map(Vec::len).sum::<usize>() == history.iter().map(Vec::len).sum::<usize>() {
        return (1, 0);
    }
    let mut completed = 0;
    let mut stalled = 0;
    let mut ready = false;
    for session in 0..history.len() {
        let Some(transaction) = history[session].get(prefix[session].len()) else {
            continue;
        };
        assert!(
            transaction.committed,
            "the generated domain is committed transactions only"
        );
        if !sources_available(prefix, &transaction.events) {
            continue;
        }
        ready = true;
        visit(prefix, arrival, session, &transaction.events);
        prefix[session].push(transaction.clone());
        arrival.push(session);
        let (child_completed, child_stalled) = for_each_arrival(history, prefix, arrival, visit);
        completed += child_completed;
        stalled += child_stalled;
        arrival.pop();
        prefix[session].pop();
    }
    (completed, stalled + usize::from(!ready))
}

#[test]
fn exhaustive_incremental_admission_matches_exact_tentative_histories() {
    let mut generated = 0;
    let mut complete_arrivals = 0;
    let mut stalled_arrivals = 0;
    let mut no_complete_arrival = 0;
    let mut seen = BTreeSet::new();
    let mut counts = LEVELS.map(|_| Counts::default());
    for_each_history(|history| {
        generated += 1;
        let (complete, stalled) = for_each_arrival(
            history,
            &mut vec![Vec::new(); history.len()],
            &mut Vec::new(),
            &mut |prefix, arrival, session, events| {
                let case = Case {
                    prefix: prefix
                        .iter()
                        .map(|session| {
                            session
                                .iter()
                                .map(|transaction| transaction.events.clone())
                                .collect()
                        })
                        .collect(),
                    arrival: arrival.to_vec(),
                    session,
                    events: events.to_vec(),
                };
                if seen.insert(case) {
                    for (index, level) in LEVELS.into_iter().enumerate() {
                        exercise_case(prefix, arrival, session, events, level, &mut counts[index]);
                    }
                }
            },
        );
        complete_arrivals += complete;
        stalled_arrivals += stalled;
        no_complete_arrival += usize::from(complete == 0);
    });
    println!(
        "incremental generated domain: sessions<=2, transactions<=3 TOTAL, events<=4 TOTAL, \
         variables={{0,1}}; generated={generated}, complete_source_available_arrivals={complete_arrivals}, \
         stalled_source_unavailable_arrivals={stalled_arrivals}, histories_without_complete_arrival={no_complete_arrival}, \
         distinct_arrival_prefix_candidates={}",
        seen.len(),
    );
    println!("successor probes add one tentative read-only transaction; no future source/session-zero/write-zero probes");
    assert!(generated > 0 && complete_arrivals > 0 && !seen.is_empty());
    for (level, count) in LEVELS.into_iter().zip(counts) {
        println!("incremental level={level:?}, implementation=incremental, {count:?}");
        assert!(count.cases > 0 && count.commits > 0 && count.rejected_commits > 0);
        assert!(count.read_trials > 0 && count.read_accepts > 0 && count.rejected_reads > 0);
        assert!(count.transaction_trials > 0 && count.resets > 0 && count.successor_sets > 0);
    }
}

#[test]
fn directed_larger_histories_exercise_ordering_and_live_recovery() {
    let histories = [
        // The RC premise comes from the earlier read of y, not just reads of x.
        vec![
            vec![
                Transaction::committed(vec![Event::write(0, 1)]),
                Transaction::committed(vec![Event::write(0, 2), Event::write(1, 3)]),
            ],
            vec![Transaction::committed(vec![
                Event::read(1, 3),
                Event::read(0, 1),
            ])],
        ],
        // SI admits write skew; SER rejects it. Blind writers also survive reset.
        vec![
            vec![Transaction::committed(vec![
                Event::read_empty(1),
                Event::write(0, 1),
            ])],
            vec![Transaction::committed(vec![
                Event::read_empty(0),
                Event::write(1, 2),
            ])],
            vec![Transaction::committed(vec![Event::write(2, 3)])],
        ],
        // Read-your-writes is about the latest preceding write, not the final one.
        vec![
            vec![Transaction::committed(vec![
                Event::write(0, 1),
                Event::read(0, 1),
                Event::write(0, 2),
                Event::read(0, 2),
            ])],
            vec![Transaction::committed(vec![Event::read(0, 1)])],
        ],
        vec![vec![Transaction::committed(vec![
            Event::write(0, 1),
            Event::write(0, 2),
            Event::read(0, 1),
            Event::write(1, 3),
            Event::read(1, 3),
        ])]],
        // Inferred write order must not become causal visibility after reset.
        vec![
            vec![Transaction::committed(vec![
                Event::write(1, 46),
                Event::read_empty(2),
            ])],
            vec![
                Transaction::committed(vec![
                    Event::write(2, 48),
                    Event::read_empty(1),
                    Event::write(1, 49),
                ]),
                Transaction::committed(vec![Event::write(3, 56)]),
            ],
            vec![Transaction::committed(vec![
                Event::read(3, 56),
                Event::read(1, 46),
            ])],
        ],
    ];
    let mut counts = LEVELS.map(|_| Counts::default());
    let mut arrivals = 0;
    for history in &histories {
        let (complete, _) = for_each_arrival(
            history,
            &mut vec![Vec::new(); history.len()],
            &mut Vec::new(),
            &mut |prefix, arrival, session, events| {
                for (index, level) in LEVELS.into_iter().enumerate() {
                    exercise_case(prefix, arrival, session, events, level, &mut counts[index]);
                }
            },
        );
        assert!(
            complete > 0,
            "directed history has no representable arrival: {history:?}"
        );
        arrivals += complete;
    }
    println!(
        "incremental directed larger histories={}, complete_arrivals={arrivals}",
        histories.len()
    );
    for (level, count) in LEVELS.into_iter().zip(counts) {
        println!("incremental directed level={level:?}, {count:?}");
    }
}

#[allow(clippy::too_many_lines)]
#[test]
fn rejected_commits_and_trials_allow_version_reuse_and_a_later_valid_commit() {
    for level in LEVELS {
        let mut history = vec![
            vec![Transaction::committed(vec![
                Event::write(0, 11),
                Event::write(1, 12),
            ])],
            Vec::new(),
            Vec::new(),
        ];
        let mut arrival = vec![0];
        let variables = BTreeSet::from([0, 1, 2]);
        let mut counts = Counts::default();
        let mut checker = fresh_checker(&history, &arrival, level, 2, &[]);
        {
            let probe = Probe {
                history: &history,
                arrival: &arrival,
                variables: &variables,
                level,
            };
            let live = [Event::read(0, 11)];
            probe.restore_live(&mut checker, 2, &live, &mut counts);
            probe.exercise_candidates(&mut checker, 2, &live, &mut counts);
            let rejected = [
                Event::write(2, 13),
                Event::read(0, 11),
                Event::read_empty(0),
            ];
            let result = checker.check_transaction(3, &rejected, true);
            assert!(!assert_verdict(
                &result,
                &tentative(&history, 2, &rejected),
                level,
                &arrival,
                "root-after-noninitial rejected commit must discard staged version and live reads",
            ));
            probe.committed_successors(&mut checker, &mut counts);
            let valid = [Event::write(2, 13), Event::read(0, 11)];
            probe.transaction_trial(&mut checker, 2, &valid, &[], &mut counts);
            probe.restore_live(&mut checker, 2, &live, &mut counts);
            checker.reset_transaction();
            counts.resets += 1;
            probe.committed_successors(&mut checker, &mut counts);
            // A reset must allow a different existing session to start reading.
            probe.restore_live(&mut checker, 0, &[Event::read(1, 12)], &mut counts);
            checker.reset_transaction();
            counts.resets += 1;
            probe.committed_successors(&mut checker, &mut counts);
            let result = checker.check_transaction(3, &valid, true);
            assert!(assert_verdict(
                &result,
                &tentative(&history, 2, &valid),
                level,
                &arrival,
                "valid commit reuses the rejected and trial write version",
            ));
            history[2].push(Transaction::committed(valid.to_vec()));
            arrival.push(2);
        }
        let probe = Probe {
            history: &history,
            arrival: &arrival,
            variables: &variables,
            level,
        };
        probe.committed_successors(&mut checker, &mut counts);
        let repeated = [
            Event::write(2, 14),
            Event::read(2, 14),
            Event::write(2, 15),
            Event::read(2, 15),
        ];
        probe.transaction_trial(&mut checker, 2, &repeated, &[], &mut counts);
        let result = checker.check_transaction(3, &repeated, true);
        let extended = tentative(&history, 2, &repeated);
        assert!(assert_verdict(
            &result,
            &extended,
            level,
            &arrival,
            "ordered repeated local writes commit after rejection/trial/reset",
        ));
        arrival.push(2);
        let probe = Probe {
            history: &extended,
            arrival: &arrival,
            variables: &variables,
            level,
        };
        probe.committed_successors(&mut checker, &mut counts);
        // Version 14 now exists but is externally overwritten, so rejecting it
        // tests provenance validation, not an unrepresentable future-source API.
        let overwritten = checker.check_read(2, 2, Some(14), true);
        assert!(!assert_verdict(
            &overwritten,
            &tentative(&extended, 1, &[Event::read(2, 14)]),
            level,
            &arrival,
            "overwritten committed local version is not externally visible",
        ));
        probe.committed_successors(&mut checker, &mut counts);
        let final_events = [Event::read(2, 15), Event::write(1, 16)];
        let final_result = checker.check_transaction(2, &final_events, true);
        let final_history = tentative(&extended, 1, &final_events);
        assert!(assert_verdict(
            &final_result,
            &final_history,
            level,
            &arrival,
            "consumer-visible valid read and commit after overwritten-source rejection",
        ));
        arrival.push(1);
        Probe {
            history: &final_history,
            arrival: &arrival,
            variables: &variables,
            level,
        }
        .committed_successors(&mut checker, &mut counts);
        println!("incremental directed rejection/reset/reuse level={level:?}, {counts:?}");
    }
}
