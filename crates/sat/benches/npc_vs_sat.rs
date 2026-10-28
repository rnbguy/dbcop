//! Deterministic SAT/UNSAT decisions, with construction and validation untimed.
//!
//! Smoke: `cargo bench -p dbcop_sat --bench npc_vs_sat -- --test`
//! Timings: `DBCOP_BENCH_SEED=42 cargo bench -p dbcop_sat --bench npc_vs_sat`
//! The seed fixes inputs, not production hash iteration or DFS branch ordering.

use std::hint::black_box;
use std::time::Duration;

use criterion::{criterion_group, criterion_main, BenchmarkId, Criterion};
use dbcop_core::consistency::Witness;
use dbcop_core::history::atomic::types::TransactionId;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::{check, Consistency};
use dbcop_sat::{check_prefix, check_serializable, check_snapshot_isolation};
use rand::rngs::StdRng;
use rand::seq::SliceRandom;
use rand::{RngExt, SeedableRng};

#[path = "../../core/tests/common/axiomatic_reference.rs"]
mod axiomatic_reference;

use axiomatic_reference::{reference_check, validate_witness, ReferenceResult};

type History = Vec<Session<u64, u64>>;

#[derive(Clone, Copy)]
enum NpcLevel {
    Prefix,
    SnapshotIsolation,
    Serializable,
}

impl NpcLevel {
    const fn name(self) -> &'static str {
        match self {
            Self::Prefix => "prefix",
            Self::SnapshotIsolation => "snapshot_isolation",
            Self::Serializable => "serializable",
        }
    }

    const fn consistency(self) -> Consistency {
        match self {
            Self::Prefix => Consistency::Prefix,
            Self::SnapshotIsolation => Consistency::SnapshotIsolation,
            Self::Serializable => Consistency::Serializable,
        }
    }

    fn run_core(self, history: &History) -> bool {
        check(history, self.consistency()).is_ok()
    }

    fn run_sat(self, history: &History) -> bool {
        match self {
            Self::Prefix => check_prefix(history).is_ok(),
            Self::SnapshotIsolation => check_snapshot_isolation(history).is_ok(),
            Self::Serializable => check_serializable(history).is_ok(),
        }
    }
}

fn seed() -> u64 {
    let seed = match std::env::var("DBCOP_BENCH_SEED") {
        Ok(value) => value
            .parse()
            .expect("DBCOP_BENCH_SEED must be a u64 decimal integer"),
        Err(std::env::VarError::NotPresent) => 42,
        Err(error) => panic!("invalid DBCOP_BENCH_SEED: {error}"),
    };
    eprintln!("[npc_vs_sat] seed={seed}");
    seed
}

fn append(
    history: &mut History,
    order: &mut Vec<TransactionId>,
    session: usize,
    events: Vec<Event<u64, u64>>,
) {
    order.push(TransactionId {
        session_id: u64::try_from(session).unwrap() + 1,
        session_height: u64::try_from(history[session].len()).unwrap(),
    });
    history[session].push(Transaction::committed(events));
}

// A serial execution with seeded session interleavings and data choices. Every
// session reads the shared marker, making the communication graph a clique.
fn serial_history(
    seed: u64,
    sessions: usize,
    rounds: usize,
) -> (History, Vec<TransactionId>, Vec<u64>) {
    let mut rng = StdRng::seed_from_u64(seed);
    let mut history = vec![Vec::new(); sessions];
    let mut order = Vec::new();
    let mut latest = vec![None; sessions];
    let mut done = vec![0; sessions];
    let mut schedule: Vec<_> = (0..sessions).collect();
    let shared = u64::try_from(sessions).unwrap();
    for _ in 0..rounds {
        schedule.shuffle(&mut rng);
        for &session in &schedule {
            let variable = rng.random_range(0..sessions);
            let version = u64::try_from(order.len()).unwrap() + 1;
            let mut events = vec![Event::Read {
                variable: u64::try_from(variable).unwrap(),
                version: latest[variable],
            }];
            if order.is_empty() {
                events.push(Event::write(shared, 1));
            } else {
                events.push(Event::read(shared, 1));
            }
            events.push(Event::write(u64::try_from(variable).unwrap(), version));
            events.push(Event::write(
                shared + 1 + u64::try_from(session).unwrap(),
                version,
            ));
            append(&mut history, &mut order, session, events);
            latest[variable] = Some(version);
            done[session] = version;
        }
    }
    (history, order, done)
}

// These rings are causal-compatible obstructions from the isolation axioms
// (Biswas/Enea Fig. 2/Table 1), not malformed inputs or causal-cycle negatives.
// They exercise the NP-complete stage; no worst-case runtime hardness is claimed.
fn contradiction(level: NpcLevel, sessions: usize) -> History {
    let mut history = vec![Vec::new(); sessions];
    for (session, transactions) in history.iter_mut().enumerate() {
        let own = u64::try_from(session).unwrap();
        let next = u64::try_from((session + 1) % sessions).unwrap();
        let events = match level {
            NpcLevel::Prefix => {
                transactions.push(Transaction::committed(vec![Event::write(own, 1)]));
                // Seeing Wi but not Wnext requires Wi <co Wnext: a ring.
                vec![Event::read(own, 1), Event::read_empty(next)]
            }
            NpcLevel::SnapshotIsolation => {
                // Tnext also writes own. Old snapshots plus conflicting-write
                // exclusion require Ti before Tnext, cyclically. PC permits it.
                let previous = u64::try_from((session + sessions - 1) % sessions).unwrap();
                vec![
                    Event::read_empty(own),
                    Event::write(own, 1),
                    Event::write(previous, 2),
                ]
            }
            NpcLevel::Serializable => {
                // Disjoint writes permit SI; initial reads force Ti < Tnext
                // in every serial execution, cyclically.
                vec![Event::read_empty(next), Event::write(own, 1)]
            }
        };
        transactions.push(Transaction::committed(events));
    }
    history
}

fn negative_history(
    mut prelude: History,
    mut order: Vec<TransactionId>,
    done: &[u64],
    level: NpcLevel,
) -> (History, Vec<TransactionId>) {
    let sessions = prelude.len();
    let shared = u64::try_from(sessions).unwrap();
    let offset = shared * 2 + 1;
    let gadget = contradiction(level, sessions);
    // Finish every prelude before the obstruction, while keeping its variables
    // disjoint. Projecting away the prelude preserves the impossible ring.
    let barrier: Vec<_> = done
        .iter()
        .enumerate()
        .map(|(session, &version)| {
            Event::read(shared + 1 + u64::try_from(session).unwrap(), version)
        })
        .collect();
    for phase in 0..gadget[0].len() {
        for (session, transactions) in gadget.iter().enumerate() {
            let mut events = barrier.clone();
            events.extend(transactions[phase].events.iter().map(|event| match event {
                Event::Read { variable, version } => Event::Read {
                    variable: *variable + offset,
                    version: *version,
                },
                Event::Write { variable, version } => Event::Write {
                    variable: *variable + offset,
                    version: *version,
                },
            }));
            append(&mut prelude, &mut order, session, events);
        }
    }
    (prelude, order)
}

fn validate(
    level: NpcLevel,
    history: &History,
    expected: bool,
    known_order: Vec<TransactionId>,
    label: &str,
) {
    // This raw-event reference checks structural validity and the complete causal
    // witness independently of production conversion/saturation, for BOTH signs.
    validate_witness(
        history,
        Consistency::Causal,
        &Witness::CommitOrder(known_order.clone()),
    )
    .unwrap_or_else(|error| panic!("{label}: independent causal witness: {error}"));
    let causal = check(history, Consistency::Causal)
        .unwrap_or_else(|error| panic!("{label}: early causal rejection: {error:?}"));
    validate_witness(history, Consistency::Causal, &causal)
        .unwrap_or_else(|error| panic!("{label}: production causal witness: {error}"));
    if expected {
        validate_witness(
            history,
            level.consistency(),
            &Witness::CommitOrder(known_order),
        )
        .unwrap_or_else(|error| panic!("{label}: known serial execution: {error}"));
    }
    let core = check(history, level.consistency());
    assert_eq!(core.is_ok(), expected, "{label}: core={core:?}");
    if let Ok(witness) = core {
        validate_witness(history, level.consistency(), &witness)
            .unwrap_or_else(|error| panic!("{label}: core witness: {error}"));
    }
    // SER's SAT API returns (), not a witness.
    if matches!(level, NpcLevel::Serializable) {
        let sat = check_serializable(history);
        assert_eq!(sat.is_ok(), expected, "{label}: SAT={sat:?}");
    } else {
        let sat = match level {
            NpcLevel::Prefix => check_prefix(history),
            NpcLevel::SnapshotIsolation => check_snapshot_isolation(history),
            NpcLevel::Serializable => unreachable!(),
        };
        assert_eq!(sat.is_ok(), expected, "{label}: SAT={sat:?}");
        if let Ok(witness) = sat {
            validate_witness(history, level.consistency(), &witness)
                .unwrap_or_else(|error| panic!("{label}: SAT witness: {error}"));
        }
    }
}

fn bench_level(c: &mut Criterion, level: NpcLevel, seed: u64) {
    let mut group = c.benchmark_group(format!("npc_vs_sat_{}", level.name()));
    group.sample_size(40);
    group.measurement_time(Duration::from_secs(3));
    for (sessions, rounds) in [(2, 2), (4, 4)] {
        // Exhaust only the small obstruction, not factorially many placements of
        // the larger prelude. Full negatives follow by projection onto this ring.
        let gadget = contradiction(level, sessions);
        assert!(
            matches!(
                reference_check(&gadget, level.consistency()),
                ReferenceResult::Inconsistent
            ),
            "{}: independent obstruction verdict",
            level.name()
        );
        let weaker = match level {
            NpcLevel::Prefix => Consistency::Causal,
            NpcLevel::SnapshotIsolation => Consistency::Prefix,
            NpcLevel::Serializable => Consistency::SnapshotIsolation,
        };
        assert!(
            matches!(
                reference_check(&gadget, weaker),
                ReferenceResult::Consistent(_)
            ),
            "{}: obstruction must pass its weaker model",
            level.name()
        );
        let (positive, serial_order, done) = serial_history(seed, sessions, rounds);
        let (negative, causal_order) =
            negative_history(positive.clone(), serial_order.clone(), &done, level);
        for (expected, family, history, known_order) in [
            (true, "sat/seeded_serial", positive, serial_order),
            (false, "unsat/late_ring", negative, causal_order),
        ] {
            let transactions: usize = history.iter().map(Vec::len).sum();
            let events: usize = history.iter().flatten().map(|tx| tx.events.len()).sum();
            let label =
                format!("{family}_seed{seed}_s{sessions}_r{rounds}_t{transactions}_e{events}");
            eprintln!("[npc_vs_sat:{}] case={label} expected={expected} sessions={sessions} transactions={transactions} events={events}", level.name());
            validate(level, &history, expected, known_order, &label);
            group.bench_with_input(
                BenchmarkId::new("core_npc_search", &label),
                &history,
                |b, history| b.iter(|| black_box(level.run_core(black_box(history)))),
            );
            group.bench_with_input(
                BenchmarkId::new("sat_solver", &label),
                &history,
                |b, history| b.iter(|| black_box(level.run_sat(black_box(history)))),
            );
        }
    }
    group.finish();
}

fn bench_npc_vs_sat(c: &mut Criterion) {
    let seed = seed();
    for level in [
        NpcLevel::Prefix,
        NpcLevel::SnapshotIsolation,
        NpcLevel::Serializable,
    ] {
        bench_level(c, level, seed);
    }
}

criterion_group!(benches, bench_npc_vs_sat);
criterion_main!(benches);
