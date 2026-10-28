//! Test-only adapter: observations constrain the model, never supply its answers.

use std::any::Any;
use std::fmt::Write;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::string::{String, ToString};
use std::vec::Vec;

use super::{tlc, Capture, Observation};

fn sequence(items: impl IntoIterator<Item = String>) -> String {
    std::format!("<<{}>>", items.into_iter().collect::<Vec<_>>().join(","))
}

fn numbers(items: &[usize]) -> String {
    sequence(items.iter().map(|value| std::format!("{value}")))
}

fn boolean(value: bool) -> &'static str {
    if value {
        "TRUE"
    } else {
        "FALSE"
    }
}

fn observation(o: &Observation) -> String {
    std::format!(
        "[tag |-> {}, path |-> {}, frontier |-> {}, parents |-> {}, active |-> {}, active_keys |-> {}, open |-> {}, candidates |-> {}, selected |-> {}, reason |-> {}, attempt |-> {}, final_attempt |-> {}, budget_hit |-> {}, found |-> {}, jump_depth |-> {}]",
        o.tag, numbers(&o.path), numbers(&o.frontier), numbers(&o.parents),
        sequence(o.active.iter().map(|(key, reader)| std::format!("<<{key},{reader}>>"))),
        sequence(o.active_keys.iter().map(|value| std::format!("{value}"))),
        sequence(o.open.iter().map(|value| std::format!("{value}"))),
        sequence(o.candidates.iter().map(|(vertex, legal)| std::format!("<<{vertex},{}>>", boolean(*legal)))),
        o.selected, o.reason, o.attempt, boolean(o.final_attempt),
        boolean(o.budget_hit), boolean(o.found), o.jump_depth,
    )
}

fn data(capture: &Capture) -> Result<String, String> {
    // TLC 1.7.4's integer values are signed 32-bit. Reject an encoding failure
    // rather than allowing it to look like an incompatible search execution.
    let unsigned = |n: u64| -> Result<(), String> {
        if i32::try_from(n).is_ok() {
            Ok(())
        } else {
            Err(std::format!(
                "trace integer {n} exceeds pinned TLC's integer range"
            ))
        }
    };
    let index = |n: usize| unsigned(u64::try_from(n).map_err(|_| "trace index overflow")?);
    index(capture.level)?;
    let transaction_count = capture.history.iter().try_fold(0usize, |total, session| {
        total
            .checked_add(session.len())
            .ok_or("trace transaction count overflow")
    })?;
    index(transaction_count)?;
    index(capture.history.len())?;
    for session in &capture.history {
        for transaction in session {
            index(transaction.events.len())?;
            for event in &transaction.events {
                use crate::history::raw::types::Event;
                match *event {
                    Event::Read { variable, version } => {
                        unsigned(variable)?;
                        if let Some(version) = version {
                            unsigned(version)?;
                        }
                    }
                    Event::Write { variable, version } => {
                        unsigned(variable)?;
                        unsigned(version)?;
                    }
                }
            }
        }
    }
    index(capture.vertices.len())?;
    for vertex in &capture.vertices {
        index(vertex.txn)?;
        index(vertex.phase)?;
    }
    for &(from, to) in &capture.edges {
        index(from)?;
        index(to)?;
    }
    index(capture.observations.len())?;
    for o in &capture.observations {
        for n in [o.tag, o.reason, o.attempt, o.jump_depth] {
            index(n)?;
        }
        for n in o.path.iter().chain(&o.frontier).chain(&o.parents) {
            index(*n)?;
        }
        for &(key, reader) in &o.active {
            unsigned(key)?;
            index(reader)?;
        }
        for &key in o.active_keys.iter().chain(&o.open) {
            unsigned(key)?;
        }
        for &(vertex, _) in &o.candidates {
            index(vertex)?;
        }
        if i32::try_from(o.selected).is_err() {
            return Err("selected vertex exceeds pinned TLC's integer range".into());
        }
    }
    let mut text = std::format!(
        "---- MODULE CaseData ----\nEXTENDS Isolation\nHistory == {}\nLevel == {}\nVertices == {}\nGraph == {{{}}}\nObservations == {}\nR0 == Edges(History)\n",
        tlc::literal(&capture.history), capture.level,
        sequence(capture.vertices.iter().map(|v| std::format!("[txn |-> {}, phase |-> {}]", v.txn, v.phase))),
        capture.edges.iter().map(|(from, to)| std::format!("<<{from},{to}>>")).collect::<Vec<_>>().join(","),
        sequence(capture.observations.iter().map(observation)),
    );
    // The client renders operator applications only; all semantic edges and
    // causal reachability are calculated by canonical Isolation.tla.
    for middle in 0..=transaction_count {
        writeln!(
            text,
            "R{} == ReachStep(Ids(History), R{middle}, {middle})",
            middle + 1
        )
        .map_err(|e| e.to_string())?;
    }
    writeln!(text, "Reach == R{}\n====", transaction_count + 1).map_err(|e| e.to_string())?;
    Ok(text)
}

fn panic_message(value: &(dyn Any + Send)) -> String {
    value
        .downcast_ref::<String>()
        .cloned()
        .or_else(|| {
            value
                .downcast_ref::<&str>()
                .map(|message| (*message).to_string())
        })
        .unwrap_or_else(|| "TLC harness panicked without a diagnostic string".into())
}

fn matched(output: &str, level: usize) -> Result<(bool, bool), String> {
    let mut input = false;
    let mut found = false;
    let mut malformed = false;
    let expected = std::format!("DBCOP_TLC_MATCH|1|{level}");
    for line in output.lines().filter(|line| line.contains("DBCOP_TLC_")) {
        let marker = line
            .strip_prefix('"')
            .and_then(|line| line.strip_suffix('"'))
            .ok_or_else(|| std::format!("unquoted TLC trace marker: {line}"))?;
        if marker == "DBCOP_TLC_INPUT|0|1" {
            input = true;
        } else if marker == "DBCOP_TLC_INVALID|1" {
            malformed = true;
        } else if marker == expected {
            found = true;
        } else {
            return Err(std::format!("unexpected TLC trace marker: {marker}"));
        }
    }
    if !input {
        return Err("missing TLC trace input marker".into());
    }
    if found && malformed {
        return Err("contradictory malformed/matched trace markers".into());
    }
    Ok((found, malformed))
}

/// Only a normally completed exhaustive TLC run certifies either verdict.
/// Malformed/truncated captures are model rejection (Ok(false)); runtime,
/// encoding and protocol failures are Err, never successful negative controls.
pub(super) fn validate(capture: &Capture) -> Result<bool, String> {
    let source = data(capture)?;
    let run = catch_unwind(AssertUnwindSafe(|| {
        tlc::run_model("SearchTrace", &source, tlc::RunMode::Exhaustive)
    }))
    .map_err(|panic| panic_message(panic.as_ref()))?;
    let (result, malformed) = matched(&run.stdout, capture.level)
        .map_err(|error| std::format!("{error}; artifacts {}", run.directory.display()))?;
    std::eprintln!(
        "TLC search trace: level={} observations={} malformed={} terminal/full-consumption={} artifacts={}",
        capture.level, capture.observations.len(), malformed, result, run.directory.display(),
    );
    Ok(result)
}
