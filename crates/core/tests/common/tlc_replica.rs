//! Operational-model coverage is distinct from the axiomatic bounded domain.

use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::Path;

use dbcop_core::check;
use dbcop_core::consistency::error::Error;
use dbcop_core::history::raw::types::Event;

use super::axiomatic_reference::{
    reference_check, validate_witness, History, ReferenceResult, LEVELS,
};
use super::tlc::{self, RunMode, Verdict};

type HistoryKey = Vec<Vec<Vec<Event<u64, u64>>>>;

fn key(history: &History) -> HistoryKey {
    history
        .iter()
        .map(|session| {
            session
                .iter()
                .map(|transaction| transaction.events.clone())
                .collect()
        })
        .collect()
}

fn case_data(replicas: u64, transactions: u64, events: u64) -> String {
    format!(
        "---------------- MODULE CaseData ----------------\n\
         ReplicaCount == {replicas}\nMaxTransactions == {transactions}\nMaxEvents == {events}\n\
         =================================================\n"
    )
}

fn number(text: &str) -> u64 {
    text.replace(',', "")
        .parse()
        .unwrap_or_else(|error| panic!("malformed pinned TLC statistics integer {text:?}: {error}"))
}

fn exhaustive_statistics(output: &str) -> (u64, u64) {
    output
        .lines()
        .rev()
        .find_map(|line| {
            let (generated, rest) = line.split_once(" states generated, ")?;
            let (distinct, rest) = rest.split_once(" distinct states found, ")?;
            if !rest.ends_with(" states left on queue.") {
                return None;
            }
            // Progress lines include a timestamp; only accept the final summary.
            if !generated.chars().all(|c| c.is_ascii_digit() || c == ',') {
                return None;
            }
            Some((number(generated), number(distinct)))
        })
        .expect("complete exhaustive TLC run omitted state statistics")
}

fn simulation_states(output: &str) -> u64 {
    output
        .lines()
        .rev()
        .find_map(|line| {
            let (prefix, stats) = line.rsplit_once(": ")?;
            if !prefix.starts_with("Progress(-1) at ") {
                return None;
            }
            let (generated, rest) = stats.split_once(" states generated, ")?;
            if rest != "-1 distinct states found, -1 states left on queue." {
                return None;
            }
            Some(number(generated))
        })
        .expect("complete simulation omitted pinned TLC state statistics")
}

fn trace_length(contents: &str, depth: u64) -> Result<u64, String> {
    let mut length = 0;
    for line in contents.lines() {
        if let Some(header) = line.strip_prefix("STATE_") {
            let ordinal = header
                .strip_suffix(" == ")
                .or_else(|| header.strip_suffix(" =="))
                .ok_or_else(|| format!("malformed trace state header {line:?}"))?
                .parse::<u64>()
                .map_err(|error| error.to_string())?;
            length += 1;
            if ordinal != length || length > depth {
                return Err(format!("noncontiguous/out-of-bounds trace state {line:?}"));
            }
        }
    }
    if length == 0 {
        return Err("simulation trace contains no state headers".into());
    }
    Ok(length)
}

// v1.7.4 tool mode prints 2209 progress, not the non-tool 2210 summary.
// Certify completed traces using the actual per-worker trace files.
fn simulation_lengths(directory: &Path, traces: u64, depth: u64) -> Vec<u64> {
    let files: BTreeMap<_, _> = fs::read_dir(directory)
        .unwrap()
        .map(|entry| {
            let entry = entry.unwrap();
            (
                entry.file_name().to_string_lossy().into_owned(),
                entry.path(),
            )
        })
        .filter(|(name, _)| name.starts_with("simulation_"))
        .collect();
    assert_eq!(
        u64::try_from(files.len()).unwrap(),
        traces,
        "wrong simulation trace-file count; artifacts={}",
        directory.display()
    );
    (0..traces)
        .map(|index| {
            let name = format!("simulation_0_{index}");
            let path = files.get(&name).unwrap_or_else(|| {
                panic!(
                    "missing simulation trace {name}; artifacts={}",
                    directory.display()
                )
            });
            let contents = fs::read_to_string(path).unwrap();
            trace_length(&contents, depth)
                .unwrap_or_else(|error| panic!("{error}; trace={}", path.display()))
        })
        .collect()
}

fn classify(histories: &[History], domain: &str, directory: &Path) {
    let mut counts = [[0_u64; 3]; 7];
    for batch in histories.chunks(256) {
        let oracle = tlc::check_histories(batch);
        assert_eq!(oracle.len(), batch.len());
        for (history, actual) in batch.iter().zip(oracle) {
            for (index, level) in LEVELS.into_iter().enumerate() {
                let reference = reference_check(history, level);
                let expected = match &reference {
                    ReferenceResult::Invalid(_) => Verdict::Invalid,
                    ReferenceResult::Consistent(_) => Verdict::Consistent,
                    ReferenceResult::Inconsistent => Verdict::Inconsistent,
                };
                let core = check(history, level);
                let core_verdict = match &core {
                    Ok(witness) => {
                        validate_witness(history, level, witness).unwrap_or_else(|error| {
                            panic!(
                                "invalid core witness: {error}; domain={domain}; level={level:?}; \
                                raw={history:?}; witness={witness:?}; artifacts={}",
                                directory.display()
                            )
                        });
                        Verdict::Consistent
                    }
                    Err(Error::NonAtomic(
                        dbcop_core::history::raw::error::Error::NonRepeatableRead { .. },
                    )) => Verdict::Inconsistent,
                    Err(Error::NonAtomic(_)) => Verdict::Invalid,
                    Err(_) => Verdict::Inconsistent,
                };
                assert_eq!(
                    actual[index],
                    expected,
                    "replica TLC/reference mismatch: domain={domain}; level={level:?}; \
                     raw={history:?}; reference={reference:?}; artifacts={}",
                    directory.display()
                );
                assert_eq!(
                    core_verdict,
                    expected,
                    "replica core/reference mismatch: domain={domain}; level={level:?}; \
                     raw={history:?}; core={core:?}; reference={reference:?}; artifacts={}",
                    directory.display()
                );
                let bucket = match expected {
                    Verdict::Invalid => 0,
                    Verdict::Consistent => 1,
                    Verdict::Inconsistent => 2,
                };
                counts[index][bucket] += 1;
            }
        }
    }
    eprintln!(
        "replica domain={domain} distinct_projected_histories={} invalid_histories={}",
        histories.len(),
        counts[0][0]
    );
    for (level, count) in LEVELS.into_iter().zip(counts) {
        eprintln!("replica domain={domain} level={level:?} invalid={} consistent={} inconsistent={} total={}",
            count[0], count[1], count[2], count.iter().sum::<u64>());
    }
}

fn project(output: &str, directory: &Path) -> Vec<History> {
    let projected = std::panic::catch_unwind(|| tlc::histories(output)).unwrap_or_else(|_| {
        panic!(
            "malformed replica history output; artifacts={}",
            directory.display()
        )
    });
    let mut histories = BTreeMap::new();
    for history in projected {
        histories.entry(key(&history)).or_insert(history);
    }
    assert!(
        !histories.is_empty(),
        "operational model emitted no completed histories"
    );
    histories.into_values().collect()
}

/// Exhaust the operational graphs and classify every distinct projection.
/// This is not every axiomatic history, nor a count of delivery schedules.
pub fn exhaustive_campaign() {
    for replicas in 1..=3 {
        let domain = format!("exhaustive,k={replicas},transactions<=3,events<=4,keys=0/1");
        let run = tlc::run_model(
            "ReplicaHistories",
            &case_data(replicas, 3, 4),
            RunMode::Exhaustive,
        );
        eprintln!(
            "replica domain={domain} artifacts={}",
            run.directory.display()
        );
        let (generated, distinct) = exhaustive_statistics(&run.stdout);
        // Exactly one Init state. TLC's generated count includes it and all
        // successor occurrences, including transitions into already seen states.
        let transitions = generated.checked_sub(1).expect("missing initial state");
        eprintln!(
            "replica domain={domain} explored_states={distinct} generated_states={generated} \
            generated_transitions={transitions} artifacts={}",
            run.directory.display()
        );
        let histories = project(&run.stdout, &run.directory);
        classify(&histories, &domain, &run.directory);
    }
}

/// Separately sample larger behaviors; classify every emitted exact history.
pub fn simulation_campaign() {
    let domain = "sampled,k=3,transactions<=6,events<=12,keys=0/1,traces=100,depth=60,seed=42";
    let run = tlc::run_model(
        "ReplicaHistories",
        &case_data(3, 6, 12),
        RunMode::Simulation {
            traces: 100,
            depth: 60,
            seed: 42,
        },
    );
    eprintln!(
        "replica domain={domain} artifacts={}",
        run.directory.display()
    );
    let lengths = simulation_lengths(&run.directory, 100, 60);
    let generated = simulation_states(&run.stdout);
    let transitions = generated
        .checked_sub(1)
        .expect("missing simulation initial state");
    eprintln!("replica domain={domain} generated_states={generated} generated_successor_transitions={transitions} \
        actual_trace_state_lengths={lengths:?} selected_trace_steps={} artifacts={}",
        lengths.iter().map(|length| length - 1).sum::<u64>(), run.directory.display());
    // Simulation invariants inspect all generated successors, not only the
    // successor selected into a trace. This classification includes both.
    let histories = project(&run.stdout, &run.directory);
    classify(&histories, domain, &run.directory);
}

#[test]
fn pinned_trace_lengths_preserve_short_terminated_traces() {
    assert_eq!(
        trace_length("STATE_1 == \nx = 0\nSTATE_2 == \nx = 1\n", 60),
        Ok(2)
    );
    assert_eq!(trace_length("STATE_1 == \nx = 0\n", 60), Ok(1));
    assert!(trace_length("STATE_2 == \nx = 0\n", 60).is_err());
    assert!(trace_length("STATE_1 == \nSTATE_2 == \n", 1).is_err());
    assert!(trace_length("", 60).is_err());
}

#[test]
fn pinned_state_statistics_do_not_count_schedules() {
    assert_eq!(exhaustive_statistics(
        "Progress(2) at time: 6 states generated, 3 distinct states found, 1 states left on queue.\n\
         1,234 states generated, 321 distinct states found, 0 states left on queue.\n"), (1234, 321));
    assert_eq!(simulation_states("Progress(-1) at 2026-10-20 22:47:58: 456 states generated, -1 distinct states found, -1 states left on queue.\n"), 456);
    let unique: BTreeSet<_> = [key(&vec![]), key(&vec![vec![]])].into_iter().collect();
    assert_eq!(unique.len(), 2, "empty sessions must remain observable");
}
