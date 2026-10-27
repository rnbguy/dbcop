#![allow(dead_code)]

use std::collections::BTreeSet;
use std::fmt::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};
use std::{fs, thread};

use dbcop_core::history::raw::types::{Event, Transaction};

use super::axiomatic_reference::History;

const SHA256: &str = "936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88";
static RUN: AtomicU64 = AtomicU64::new(0);
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Verdict {
    Invalid,
    Consistent,
    Inconsistent,
}
#[derive(Clone, Copy, Debug)]
pub enum RunMode {
    Exhaustive,
    Simulation { traces: u64, depth: u64, seed: u64 },
}
pub struct RunOutput {
    pub stdout: String,
    pub directory: PathBuf,
}
#[derive(Debug)]
struct Failure {
    directory: PathBuf,
    message: String,
    timeout: bool,
}

fn runtime() -> Result<PathBuf, String> {
    let jar = std::env::var_os("DBCOP_TLC_JAR")
        .map(PathBuf::from)
        .ok_or("DBCOP_TLC_JAR must name the pinned v1.7.4 tla2tools.jar")?;
    verify_jar(&jar)
}

fn verify_jar(jar: &Path) -> Result<PathBuf, String> {
    let result = Command::new("sha256sum")
        .arg(jar)
        .output()
        .map_err(|e| format!("sha256sum unavailable: {e}"))?;
    if !result.status.success() {
        return Err(format!(
            "sha256sum failed: {}",
            String::from_utf8_lossy(&result.stderr)
        ));
    }
    if String::from_utf8_lossy(&result.stdout)
        .split_whitespace()
        .next()
        != Some(SHA256)
    {
        return Err(format!("TLC jar hash mismatch: {}", jar.display()));
    }
    fs::canonicalize(jar).map_err(|e| format!("resolve TLC jar: {e}"))
}

fn private_directory(path: &Path) {
    let mut builder = fs::DirBuilder::new();
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    builder
        .create(path)
        .unwrap_or_else(|e| panic!("create private TLC directory {}: {e}", path.display()));
}

#[allow(clippy::too_many_lines)]
fn execute(
    module: &str,
    case_data: &str,
    mode: RunMode,
    jar: &Path,
    deadline: Duration,
) -> Result<RunOutput, Failure> {
    let directory = std::env::temp_dir().join(format!(
        "dbcop-tlc-{}-{}",
        std::process::id(),
        RUN.fetch_add(1, Ordering::Relaxed)
    ));
    private_directory(&directory);
    let work = || -> Result<String, String> {
        let root = Path::new(env!("CARGO_MANIFEST_DIR"));
        fs::write(
            directory.join("Isolation.tla"),
            include_str!("../../../../mockserver/apalachebackend/spec/Isolation.tla"),
        )
        .map_err(|e| e.to_string())?;
        for name in ["HistoryOracle", "ReplicaHistories", "SearchTrace"] {
            let source = root.join("tests/tla").join(format!("{name}.tla"));
            if source.exists() {
                fs::copy(source, directory.join(format!("{name}.tla")))
                    .map_err(|e| e.to_string())?;
            }
        }
        fs::write(directory.join("CaseData.tla"), case_data).map_err(|e| e.to_string())?;
        let invariant = match module {
            "HistoryOracle" | "SearchTrace" => "INVARIANT Report\n",
            "ReplicaHistories" => "INVARIANT EmitHistories\n",
            _ => "",
        };
        fs::write(
            directory.join("Model.cfg"),
            format!("INIT Init\nNEXT Next\nCHECK_DEADLOCK FALSE\n{invariant}"),
        )
        .map_err(|e| e.to_string())?;
        let stdout = fs::File::create(directory.join("stdout.log")).map_err(|e| e.to_string())?;
        let stderr = fs::File::create(directory.join("stderr.log")).map_err(|e| e.to_string())?;
        let mut command = Command::new("java");
        // Pinned SANY extracts standard modules by name into java.io.tmpdir.
        let mut java_tmp = std::ffi::OsString::from("-Djava.io.tmpdir=");
        java_tmp.push(&directory);
        command
            .current_dir(&directory)
            .arg(java_tmp)
            .arg("-cp")
            .arg(jar)
            .args([
                "tlc2.TLC",
                "-tool",
                "-workers",
                "1",
                "-config",
                "Model.cfg",
                "-metadir",
                "states",
            ]);
        if let RunMode::Simulation {
            traces,
            depth,
            seed,
        } = mode
        {
            command
                .arg("-simulate")
                .arg(format!("num={traces},file=simulation"))
                .arg("-depth")
                .arg(depth.to_string())
                .arg("-seed")
                .arg(seed.to_string());
        }
        let mut child = command
            .arg(module)
            .stdout(Stdio::from(stdout))
            .stderr(Stdio::from(stderr))
            .spawn()
            .map_err(|e| format!("launch java: {e}"))?;
        let start = Instant::now();
        let status = loop {
            match child.try_wait() {
                Ok(Some(status)) => break status,
                Ok(None) if start.elapsed() < deadline => thread::sleep(Duration::from_millis(20)),
                Ok(None) => {
                    let _ = child.kill();
                    let _ = child.wait();
                    return Err("TIMEOUT".into());
                }
                Err(e) => {
                    let _ = child.kill();
                    let _ = child.wait();
                    return Err(e.to_string());
                }
            }
        };
        let output = fs::read_to_string(directory.join("stdout.log")).map_err(|e| e.to_string())?;
        let errors = fs::read_to_string(directory.join("stderr.log")).map_err(|e| e.to_string())?;
        if !status.success() {
            return Err(format!("TLC exited {status}: {errors}\n{output}"));
        }
        completion(&output, mode)?;
        if let RunMode::Simulation { traces, .. } = mode {
            // Tool mode emits 2209, not 2210; only files certify trace count.
            let names: BTreeSet<_> = fs::read_dir(&directory)
                .map_err(|e| e.to_string())?
                .map(|entry| entry.map(|e| e.file_name().to_string_lossy().into_owned()))
                .collect::<Result<_, _>>()
                .map_err(|e| e.to_string())?;
            let actual: BTreeSet<_> = names
                .into_iter()
                .filter(|name| name.starts_with("simulation_"))
                .collect();
            let expected: BTreeSet<_> = (0..traces).map(|i| format!("simulation_0_{i}")).collect();
            if actual != expected {
                return Err(format!(
                    "incomplete simulation trace files: expected {traces}, found {}",
                    actual.len()
                ));
            }
        }
        Ok(output)
    };
    work()
        .map(|stdout| RunOutput {
            stdout,
            directory: directory.clone(),
        })
        .map_err(|message| Failure {
            timeout: message == "TIMEOUT",
            message,
            directory,
        })
}

fn completion(output: &str, mode: RunMode) -> Result<(), String> {
    let code = |number| output.contains(&format!("@!@!@STARTMSG {number}:"));
    if !code(2186) {
        return Err("missing TLC_FINISHED=2186".into());
    }
    match mode {
        RunMode::Exhaustive if !code(2193) => Err("missing TLC_SUCCESS=2193".into()),
        RunMode::Simulation { .. } if !code(2209) => Err("missing TLC_PROGRESS_SIMU=2209".into()),
        _ => Ok(()),
    }
}

pub fn run_model(module: &str, case_data: &str, mode: RunMode) -> RunOutput {
    let jar = runtime().unwrap_or_else(|e| panic!("{e}"));
    // A whole replica graph is not a 256-history oracle batch.
    let deadline = Duration::from_secs(
        if module == "ReplicaHistories" && matches!(mode, RunMode::Exhaustive) {
            1_200
        } else {
            120
        },
    );
    execute(module, case_data, mode, &jar, deadline).unwrap_or_else(|e| {
        panic!(
            "TLC execution failure; artifacts {}: {}",
            e.directory.display(),
            e.message
        )
    })
}

pub fn literal(history: &History) -> String {
    let mut transactions = Vec::new();
    for (s, session) in history.iter().enumerate() {
        for (height, tx) in session.iter().enumerate() {
            let events: Vec<_> = tx
                .events
                .iter()
                .map(|event| {
                    let (kind, key, initial, version) = match *event {
                        Event::Read { variable, version } => {
                            (0, variable, version.is_none(), version.unwrap_or(0))
                        }
                        Event::Write { variable, version } => (1, variable, false, version),
                    };
                    format!(
                        "[kind |-> {kind}, key |-> {key}, initial |-> {}, version |-> {version}]",
                        if initial { "TRUE" } else { "FALSE" }
                    )
                })
                .collect();
            transactions.push(format!(
                "[session |-> {}, height |-> {height}, committed |-> {}, events |-> <<{}>>]",
                s + 1,
                if tx.committed { "TRUE" } else { "FALSE" },
                events.join(",")
            ));
        }
    }
    format!("<<{}>>", transactions.join(","))
}

fn data(histories: &[History]) -> String {
    let mut text = format!(
        "---- MODULE CaseData ----\nEXTENDS Isolation\nCases == <<{}>>\n",
        histories.iter().map(literal).collect::<Vec<_>>().join(",")
    );
    let max = histories
        .iter()
        .map(|h| h.iter().map(Vec::len).sum::<usize>())
        .max()
        .unwrap_or(0);
    text.push_str("R0(h) == Edges(h)\n");
    for middle in 0..=max {
        writeln!(text, "R{}(h) == IF {middle} <= Len(h) THEN ReachStep(Ids(h), R{middle}(h), {middle}) ELSE R{middle}(h)", middle + 1).expect("write closure operator");
    }
    writeln!(text, "Reach(caseId) == R{}(Cases[caseId])\n====", max + 1)
        .expect("write reachability operator");
    text
}

fn markers(output: &str) -> Result<Vec<&str>, String> {
    output
        .lines()
        .filter(|line| line.contains("DBCOP_TLC_"))
        .map(|line| {
            line.strip_prefix('"')
                .and_then(|s| s.strip_suffix('"'))
                .ok_or_else(|| format!("unquoted protocol marker: {line}"))
        })
        .collect()
}
fn number(text: &str) -> Result<usize, String> {
    if text.is_empty() || !text.bytes().all(|b| b.is_ascii_digit()) {
        return Err(format!("invalid numeric field {text:?}"));
    }
    text.parse()
        .map_err(|_| format!("numeric overflow {text:?}"))
}
fn verdicts(output: &str, count: usize) -> Result<Vec<[Verdict; 7]>, String> {
    let mut result = vec![[Verdict::Inconsistent; 7]; count];
    let mut input = false;
    for marker in markers(output)? {
        let fields: Vec<_> = marker.split('|').collect();
        match fields.as_slice() {
            ["DBCOP_TLC_INPUT", "0", n] if number(n)? == count => input = true,
            ["DBCOP_TLC_INVALID", id] => {
                let i = number(id)?
                    .checked_sub(1)
                    .filter(|&i| i < count)
                    .ok_or("unknown case ID")?;
                if result[i].contains(&Verdict::Consistent) {
                    return Err("contradictory invalid/match".into());
                }
                result[i] = [Verdict::Invalid; 7];
            }
            ["DBCOP_TLC_MATCH", id, level] => {
                let i = number(id)?
                    .checked_sub(1)
                    .filter(|&i| i < count)
                    .ok_or("unknown case ID")?;
                let l = number(level)?;
                if l >= 7 || result[i][0] == Verdict::Invalid {
                    return Err("contradictory/unknown match".into());
                }
                result[i][l] = Verdict::Consistent;
            }
            _ => return Err(format!("unexpected marker {marker}")),
        }
    }
    if !input {
        return Err("missing input marker".into());
    }
    Ok(result)
}

fn check_batch(batch: &[History], jar: &Path) -> Vec<[Verdict; 7]> {
    let mut result = Vec::new();
    let output = execute(
        "HistoryOracle",
        &data(batch),
        RunMode::Exhaustive,
        jar,
        Duration::from_secs(120),
    );
    match output {
        Ok(run) => result.extend(
            verdicts(&run.stdout, batch.len())
                .unwrap_or_else(|e| panic!("{e}; artifacts {}", run.directory.display())),
        ),
        Err(error) if error.timeout && batch.len() > 16 => {
            eprintln!(
                "TLC timeout; preserving {} and retrying same cases in batches of 16",
                error.directory.display()
            );
            for small in batch.chunks(16) {
                let run = execute(
                    "HistoryOracle",
                    &data(small),
                    RunMode::Exhaustive,
                    jar,
                    Duration::from_secs(120),
                )
                .unwrap_or_else(|e| {
                    panic!(
                        "TLC failure: {}; artifacts {}",
                        e.message,
                        e.directory.display()
                    )
                });
                result.extend(
                    verdicts(&run.stdout, small.len())
                        .unwrap_or_else(|e| panic!("{e}; artifacts {}", run.directory.display())),
                );
            }
        }
        Err(error) => panic!(
            "TLC failure: {}; artifacts {}",
            error.message,
            error.directory.display()
        ),
    }
    result
}

pub fn check_histories(histories: &[History]) -> Vec<[Verdict; 7]> {
    let jar = runtime().unwrap_or_else(|e| panic!("{e}"));
    let mut result = Vec::new();
    // Bound JVM concurrency while preserving input order and each batch's proof.
    for group in histories.chunks(4 * 256) {
        thread::scope(|scope| {
            let jobs: Vec<_> = group
                .chunks(256)
                .map(|batch| {
                    let jar = &jar;
                    scope.spawn(move || check_batch(batch, jar))
                })
                .collect();
            for job in jobs {
                result.extend(
                    job.join()
                        .unwrap_or_else(|panic| std::panic::resume_unwind(panic)),
                );
            }
        });
    }
    result
}

fn vector(text: &str) -> Result<Vec<usize>, String> {
    let (length, body) = text.split_once(':').ok_or("missing vector length")?;
    let values = if body.is_empty() {
        Vec::new()
    } else {
        body.split(',').map(number).collect::<Result<Vec<_>, _>>()?
    };
    if number(length)? != values.len() {
        return Err("vector length mismatch".into());
    }
    Ok(values)
}
fn decode_history(marker: &str) -> Result<History, String> {
    let fields: Vec<_> = marker.split('|').collect();
    let ["DBCOP_TLC_HISTORY", k, sessions, transactions, events] = fields.as_slice() else {
        return Err("invalid history marker".into());
    };
    let counts = vector(sessions)?;
    if counts.len() != number(k)? {
        return Err("session count mismatch".into());
    }
    let event_counts = vector(transactions)?;
    let total = counts
        .iter()
        .try_fold(0usize, |a, b| a.checked_add(*b))
        .ok_or("transaction overflow")?;
    if total != event_counts.len() {
        return Err("transaction count mismatch".into());
    }
    let values = vector(events)?;
    let event_total = event_counts
        .iter()
        .try_fold(0usize, |a, b| a.checked_add(*b))
        .ok_or("event overflow")?;
    if event_total.checked_mul(7) != Some(values.len()) {
        return Err("event count mismatch".into());
    }
    let mut history = Vec::new();
    let mut tx_index = 0;
    let mut chunks = values.as_chunks::<7>().0.iter();
    for (s, count) in counts.into_iter().enumerate() {
        let mut session = Vec::new();
        for t in 0..count {
            let mut operations = Vec::new();
            for e in 0..event_counts[tx_index] {
                let v = chunks.next().ok_or("missing event")?;
                if v[..3] != [s + 1, t, e] || v[3] > 1 || v[5] > 1 || (v[3] == 1 && v[5] != 0) {
                    return Err("invalid event position/kind/initial flag".into());
                }
                let key = u64::try_from(v[4]).map_err(|_| "key overflow")?;
                let version = u64::try_from(v[6]).map_err(|_| "version overflow")?;
                if v[5] == 1 && version != 0 {
                    return Err("initial source has nonzero version".into());
                }
                operations.push(if v[3] == 1 {
                    Event::write(key, version)
                } else {
                    Event::Read {
                        variable: key,
                        version: if v[5] == 1 { None } else { Some(version) },
                    }
                });
            }
            session.push(Transaction::committed(operations));
            tx_index += 1;
        }
        history.push(session);
    }
    Ok(history)
}
pub fn histories(output: &str) -> Vec<History> {
    let mut seen = BTreeSet::new();
    markers(output)
        .unwrap_or_else(|e| panic!("{e}"))
        .into_iter()
        .map(|marker| {
            let history = decode_history(marker).unwrap_or_else(|e| panic!("{e}: {marker}"));
            (marker.to_owned(), history)
        })
        .filter_map(|(marker, history)| seen.insert(marker).then_some(history))
        .collect()
}

pub fn error_boundaries() {
    assert!(verdicts("\"DBCOP_TLC_INPUT|0|1\"\n\"DBCOP_TLC_MATCH|2|0\"", 1).is_err());
    assert!(verdicts(
        "\"DBCOP_TLC_INPUT|0|1\"\n\"DBCOP_TLC_INVALID|1\"\n\"DBCOP_TLC_MATCH|1|0\"",
        1
    )
    .is_err());
    assert!(verdicts("DBCOP_TLC_INPUT|0|1", 1).is_err());
    assert!(verdicts("\"DBCOP_TLC_INPUT|0|1\"\n\"DBCOP_TLC_MATCH|1|7\"", 1).is_err());
    assert!(verdicts("", 1).is_err());
    assert_eq!(
        verdicts(
            "\"DBCOP_TLC_INPUT|0|1\"\n\"DBCOP_TLC_MATCH|1|0\"\n\"DBCOP_TLC_MATCH|1|0\"",
            1
        )
        .unwrap()[0][0],
        Verdict::Consistent
    );
    let decoded = decode_history("DBCOP_TLC_HISTORY|2|2:0,1|1:1|7:2,0,0,0,0,1,0")
        .expect("valid numeric history");
    assert!(decoded[0].is_empty());
    assert!(matches!(
        decoded[1][0].events[0],
        Event::Read {
            variable: 0,
            version: None
        }
    ));
    for marker in [
        "DBCOP_TLC_HISTORY|1|1:1|1:1|6:1,1,1,0,0,1",
        "DBCOP_TLC_HISTORY|1|1:1|1:1|7:1,1,2,0,0,1,0",
        "DBCOP_TLC_HISTORY|1|1:184467440737095516160|0:|0:",
    ] {
        assert!(decode_history(marker).is_err());
    }
    assert!(completion("@!@!@STARTMSG 2186:0", RunMode::Exhaustive).is_err());
    assert!(completion("@!@!@STARTMSG 2193:0", RunMode::Exhaustive).is_err());
    let jar = runtime().unwrap_or_else(|e| panic!("{e}"));
    let boundary_dir = std::env::temp_dir().join(format!(
        "dbcop-tlc-boundary-{}-{}",
        std::process::id(),
        RUN.fetch_add(1, Ordering::Relaxed)
    ));
    private_directory(&boundary_dir);
    assert!(verify_jar(&boundary_dir.join("missing.jar")).is_err());
    fs::write(boundary_dir.join("wrong.jar"), b"not the pinned jar")
        .expect("hash boundary fixture");
    assert!(verify_jar(&boundary_dir.join("wrong.jar")).is_err());
    let bad = execute(
        "HistoryOracle",
        "---- MODULE CaseData ----\nCases == BROKEN\n====",
        RunMode::Exhaustive,
        &jar,
        Duration::from_secs(120),
    );
    assert!(bad.is_err(), "invalid source cannot become inconsistency");
    let evaluation = data(&[vec![]]).replace("Cases == <<<<>>>>", "Cases == <<42>>");
    assert!(execute(
        "HistoryOracle",
        &evaluation,
        RunMode::Exhaustive,
        &jar,
        Duration::from_secs(120)
    )
    .is_err());
    let timed = execute(
        "HistoryOracle",
        &data(&[vec![]]),
        RunMode::Exhaustive,
        &jar,
        Duration::ZERO,
    )
    .err()
    .expect("zero deadline must fail");
    assert!(timed.timeout, "{timed:?}");
}
