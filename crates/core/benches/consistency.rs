use std::hint::black_box;

use criterion::{criterion_group, criterion_main, Criterion};
use dbcop_core::consistency::Consistency;
use dbcop_core::history::raw::types::{Event, Session, Transaction};

const VARIABLES: [&str; 10] = ["x", "y", "z", "a", "b", "c", "d", "e", "f", "g"];

/// Build a history with given dimensions.
/// sessions: number of sessions
/// `txns_per_session`: transactions per session
/// `events_per_txn`: events per transaction
fn build_history(
    sessions: usize,
    txns_per_session: usize,
    events_per_txn: usize,
) -> Vec<Session<&'static str, u64>> {
    let mut result = Vec::new();
    let mut latest_version = [0u64; VARIABLES.len()];
    let mut next_version: u64 = 1;
    let mut txn_index: usize = 0;

    for _ in 0..sessions {
        let mut session = Vec::new();
        for _ in 0..txns_per_session {
            let mut events = Vec::new();
            for e in 0..events_per_txn {
                let var_idx = (txn_index + e) % VARIABLES.len();
                let var_name = VARIABLES[var_idx];

                if e % 2 == 0 {
                    // Reads always target an existing committed version
                    // (or 0, which maps to the root initial version).
                    events.push(Event::read(var_name, latest_version[var_idx]));
                } else {
                    events.push(Event::write(var_name, next_version));
                    latest_version[var_idx] = next_version;
                    next_version += 1;
                }
            }
            session.push(Transaction::committed(events));
            txn_index += 1;
        }
        result.push(session);
    }

    result
}

#[allow(clippy::too_many_lines)]
fn bench_consistency(c: &mut Criterion) {
    // Small: 2 sessions, 3 txns each, 3 events per txn
    let history_small = build_history(2, 3, 3);

    // Medium: 4 sessions, 6 txns each, 4 events per txn
    let history_medium = build_history(4, 6, 4);

    // Large: 8 sessions, 10 txns each, 5 events per txn
    let history_large = build_history(8, 10, 5);

    for history in [&history_small, &history_medium, &history_large] {
        assert!(
            dbcop_core::consistency::check(history, Consistency::CommittedRead).is_ok(),
            "benchmark history generation must produce valid committed-read histories",
        );
    }

    let mut group = c.benchmark_group("consistency_check");

    // CommittedRead benchmarks
    group.bench_function("committed_read_small", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_small),
                black_box(Consistency::CommittedRead),
            );
        });
    });

    group.bench_function("committed_read_medium", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_medium),
                black_box(Consistency::CommittedRead),
            );
        });
    });

    group.bench_function("committed_read_large", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_large),
                black_box(Consistency::CommittedRead),
            );
        });
    });

    // RepeatableRead benchmarks
    group.bench_function("repeatable_read_small", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_small),
                black_box(Consistency::RepeatableRead),
            );
        });
    });

    group.bench_function("repeatable_read_medium", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_medium),
                black_box(Consistency::RepeatableRead),
            );
        });
    });

    group.bench_function("repeatable_read_large", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_large),
                black_box(Consistency::RepeatableRead),
            );
        });
    });

    // AtomicRead benchmarks
    group.bench_function("atomic_read_small", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_small),
                black_box(Consistency::AtomicRead),
            );
        });
    });

    group.bench_function("atomic_read_medium", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_medium),
                black_box(Consistency::AtomicRead),
            );
        });
    });

    group.bench_function("atomic_read_large", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_large),
                black_box(Consistency::AtomicRead),
            );
        });
    });

    // Causal benchmarks
    group.bench_function("causal_small", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_small),
                black_box(Consistency::Causal),
            );
        });
    });

    group.bench_function("causal_medium", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_medium),
                black_box(Consistency::Causal),
            );
        });
    });

    group.bench_function("causal_large", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_large),
                black_box(Consistency::Causal),
            );
        });
    });

    // Prefix benchmarks
    group.bench_function("prefix_small", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_small),
                black_box(Consistency::Prefix),
            );
        });
    });

    group.bench_function("prefix_medium", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_medium),
                black_box(Consistency::Prefix),
            );
        });
    });

    group.bench_function("prefix_large", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_large),
                black_box(Consistency::Prefix),
            );
        });
    });

    // SnapshotIsolation benchmarks
    group.bench_function("snapshot_isolation_small", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_small),
                black_box(Consistency::SnapshotIsolation),
            );
        });
    });

    group.bench_function("snapshot_isolation_medium", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_medium),
                black_box(Consistency::SnapshotIsolation),
            );
        });
    });

    group.bench_function("snapshot_isolation_large", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_large),
                black_box(Consistency::SnapshotIsolation),
            );
        });
    });

    // Serializable benchmarks
    group.bench_function("serializable_small", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_small),
                black_box(Consistency::Serializable),
            );
        });
    });

    group.bench_function("serializable_medium", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_medium),
                black_box(Consistency::Serializable),
            );
        });
    });

    group.bench_function("serializable_large", |b| {
        b.iter(|| {
            let _ = dbcop_core::consistency::check(
                black_box(&history_large),
                black_box(Consistency::Serializable),
            );
        });
    });

    group.finish();
}

criterion_group!(benches, bench_consistency);
criterion_main!(benches);
