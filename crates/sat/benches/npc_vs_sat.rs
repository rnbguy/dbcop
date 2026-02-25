use std::cell::Cell;
use std::hint::black_box;
use std::time::{Duration, Instant};

use criterion::{criterion_group, criterion_main, Criterion};
use dbcop_core::history::raw::types::Session;
use dbcop_core::{check, Consistency};
use dbcop_sat::{check_prefix, check_serializable, check_snapshot_isolation};
use dbcop_testgen::generator::generate_single_history;

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

    fn run_core(self, history: &History) -> bool {
        let level = match self {
            Self::Prefix => Consistency::Prefix,
            Self::SnapshotIsolation => Consistency::SnapshotIsolation,
            Self::Serializable => Consistency::Serializable,
        };
        check(history, level).is_ok()
    }

    fn run_sat(self, history: &History) -> bool {
        match self {
            Self::Prefix => check_prefix(history).is_ok(),
            Self::SnapshotIsolation => check_snapshot_isolation(history).is_ok(),
            Self::Serializable => check_serializable(history).is_ok(),
        }
    }
}

fn sample_random_histories(level: NpcLevel, target_count: usize) -> Vec<History> {
    let mut histories = Vec::with_capacity(target_count);
    let mut attempts = 0usize;
    let max_attempts = target_count * 200;

    while histories.len() < target_count && attempts < max_attempts {
        attempts += 1;

        #[allow(clippy::cast_possible_truncation)]
        let n_node = 3 + (attempts % 5) as u64; // 3..=7 sessions
        #[allow(clippy::cast_possible_truncation)]
        let n_variable = 3 + (attempts % 6) as u64; // 3..=8 variables
        #[allow(clippy::cast_possible_truncation)]
        let n_transaction = 2 + (attempts % 3) as u64; // 2..=4 txns/session
        #[allow(clippy::cast_possible_truncation)]
        let n_event = 2 + (attempts % 3) as u64; // 2..=4 events/txn

        let history = generate_single_history(n_node, n_variable, n_transaction, n_event);

        let core_ok = level.run_core(&history);
        let sat_ok = level.run_sat(&history);
        assert_eq!(
            core_ok,
            sat_ok,
            "SAT/Core disagreement while sampling {} benchmark input",
            level.name(),
        );

        if core_ok {
            histories.push(history);
        }
    }

    assert_eq!(
        histories.len(),
        target_count,
        "could not sample enough {} histories for benchmark in {max_attempts} attempts",
        level.name(),
    );

    histories
}

fn print_prebench_stats(level: NpcLevel, histories: &[History]) {
    let rounds = 8usize;
    let mut core_total = Duration::ZERO;
    let mut sat_total = Duration::ZERO;

    for _ in 0..rounds {
        let core_start = Instant::now();
        for history in histories {
            black_box(level.run_core(black_box(history)));
        }
        core_total += core_start.elapsed();

        let sat_start = Instant::now();
        for history in histories {
            black_box(level.run_sat(black_box(history)));
        }
        sat_total += sat_start.elapsed();
    }

    let samples = u32::try_from(rounds * histories.len()).expect("sample count fits u32");
    let core_avg = core_total / samples;
    let sat_avg = sat_total / samples;
    let ratio = sat_avg.as_secs_f64() / core_avg.as_secs_f64();

    eprintln!(
        "[npc_vs_sat:{}] prebench avg/core={}ns avg/sat={}ns sat/core={ratio:.3}",
        level.name(),
        core_avg.as_nanos(),
        sat_avg.as_nanos(),
    );
}

fn bench_level(c: &mut Criterion, level: NpcLevel) {
    let histories = sample_random_histories(level, 12);
    print_prebench_stats(level, &histories);

    let mut group = c.benchmark_group(format!("npc_vs_sat_{}", level.name()));
    group.sample_size(80);
    group.measurement_time(Duration::from_secs(8));

    let core_idx = Cell::new(0usize);
    group.bench_function("core_npc_search", |b| {
        b.iter(|| {
            let i = core_idx.get();
            core_idx.set((i + 1) % histories.len());
            black_box(level.run_core(black_box(&histories[i])));
        });
    });

    let sat_idx = Cell::new(0usize);
    group.bench_function("sat_solver", |b| {
        b.iter(|| {
            let i = sat_idx.get();
            sat_idx.set((i + 1) % histories.len());
            black_box(level.run_sat(black_box(&histories[i])));
        });
    });

    group.finish();
}

fn bench_npc_vs_sat(c: &mut Criterion) {
    for level in [
        NpcLevel::Prefix,
        NpcLevel::SnapshotIsolation,
        NpcLevel::Serializable,
    ] {
        bench_level(c, level);
    }
}

criterion_group!(benches, bench_npc_vs_sat);
criterion_main!(benches);
