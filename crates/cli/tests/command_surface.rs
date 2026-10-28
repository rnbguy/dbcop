use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

use dbcop_core::history::atomic::types::TransactionId;
use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
struct Verification {
    file: String,
    ok: bool,
    witness: Option<WireWitness>,
    error: Option<String>,
}

#[derive(Debug, Deserialize)]
enum WireWitness {
    #[serde(rename = "SaturationOrder")]
    Saturation(Vec<(TransactionId, TransactionId)>),
    #[serde(rename = "CommitOrder")]
    Commit(Vec<TransactionId>),
    #[serde(rename = "SplitCommitOrder")]
    Split(Vec<(TransactionId, bool)>),
}

struct TempDir(PathBuf);

impl TempDir {
    fn new(tag: &str) -> Self {
        let path = std::env::temp_dir().join(format!(
            "dbcop-cli-surface-{tag}-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        std::fs::create_dir_all(&path).unwrap();
        Self(path)
    }

    fn path(&self) -> &Path {
        &self.0
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn cli() -> Command {
    // Cargo supplies the instrumented executable during cargo llvm-cov runs.
    let mut command = Command::new(env!("CARGO_BIN_EXE_dbcop"));
    command.env_remove("RUST_LOG");
    command
}

fn success(output: &Output) {
    assert!(
        output.status.success(),
        "stdout: {}\nstderr: {}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

fn failure(output: &Output) {
    assert_eq!(output.status.code(), Some(1));
}

fn verify(dir: &Path, level: &str, flags: &[&str]) -> Output {
    cli()
        .args(["verify", "--input-dir"])
        .arg(dir)
        .args(["--consistency", level])
        .args(flags)
        .output()
        .unwrap()
}

fn records(output: &Output) -> Vec<Verification> {
    String::from_utf8(output.stdout.clone())
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

fn generate(dir: &Path) -> Output {
    cli()
        .args([
            "generate",
            "--n-hist",
            "2",
            "--n-node",
            "2",
            "--n-var",
            "2",
            "--n-txn",
            "2",
            "--n-evt",
            "2",
            "--output-dir",
        ])
        .arg(dir)
        .output()
        .unwrap()
}

#[test]
fn generated_json_preserves_dimensions_and_verifies() {
    let dir = TempDir::new("generate");
    let output_dir = dir.path().join("nested/histories");
    success(&generate(&output_dir));
    assert_eq!(std::fs::read_dir(&output_dir).unwrap().count(), 2);
    for id in 0..2 {
        let bytes = std::fs::read(output_dir.join(format!("{id}.json"))).unwrap();
        let history: dbcop_testgen::generator::History = serde_json::from_slice(&bytes).unwrap();
        let params = history.get_params();
        assert_eq!(params.id, id);
        assert_eq!(params.n_node, 2);
        assert_eq!(params.n_variable, 2);
        assert_eq!(params.n_transaction, 2);
        assert_eq!(params.n_event, 2);
        let sessions = history.get_data();
        assert_eq!(sessions.len(), 2);
        for (index, transactions) in sessions.iter().enumerate() {
            // The first session also carries the initial-version transaction.
            assert_eq!(transactions.len(), if index == 0 { 3 } else { 2 });
            for transaction in transactions {
                assert!(transaction.committed);
                assert_eq!(transaction.events.len(), 2);
            }
        }
    }
    let output = verify(&output_dir, "committed-read", &["--json"]);
    success(&output);
    let results = records(&output);
    assert_eq!(results.len(), 2);
    for (id, result) in results.iter().enumerate() {
        assert_eq!(result.file, format!("{id}.json"));
        assert!(result.ok);
        assert!(result.witness.is_some());
        assert!(result.error.is_none());
    }
}

#[test]
fn generate_reports_directory_and_file_creation_failures_without_overwriting() {
    let dir = TempDir::new("generate-errors");
    let occupied = dir.path().join("occupied");
    std::fs::write(&occupied, b"keep me").unwrap();
    let output = generate(&occupied);
    failure(&output);
    assert_ne!(output.stderr, [] as [u8; 0]);
    assert_eq!(std::fs::read(&occupied).unwrap(), b"keep me");

    std::fs::create_dir(dir.path().join("0.json")).unwrap();
    let output = generate(dir.path());
    failure(&output);
    assert_ne!(output.stderr, [] as [u8; 0]);
    assert!(dir.path().join("0.json").is_dir());
}

#[test]
fn verify_reports_sorted_mixed_formats_and_all_consistency_levels() {
    let dir = TempDir::new("verify-formats");
    std::fs::write(dir.path().join("a.hist"), "[x:=1]\n---\n[x==1]\n").unwrap();
    std::fs::write(dir.path().join("b.txt"), "[x:=1]\n---\n[x==1]\n").unwrap();
    std::fs::write(dir.path().join("ignored.bin"), "not a history").unwrap();
    for level in [
        "committed-read",
        "repeatable-read",
        "atomic-read",
        "causal",
        "prefix",
        "snapshot-isolation",
        "serializable",
    ] {
        let output = verify(dir.path(), level, &["--json"]);
        success(&output);
        let results = records(&output);
        assert_eq!(results.len(), 2);
        assert_eq!(results[0].file, "a.hist");
        assert_eq!(results[1].file, "b.txt");
        assert!(results.iter().all(|result| result.ok));
        for result in &results {
            match (level, result.witness.as_ref().unwrap()) {
                ("prefix" | "serializable", WireWitness::Commit(order)) => {
                    assert_eq!(
                        order
                            .iter()
                            .filter(|id| id.session_id != 0)
                            .copied()
                            .collect::<Vec<_>>(),
                        vec![
                            TransactionId {
                                session_id: 1,
                                session_height: 0
                            },
                            TransactionId {
                                session_id: 2,
                                session_height: 0
                            },
                        ],
                    );
                }
                ("snapshot-isolation", WireWitness::Split(order)) => {
                    assert!(order.iter().any(|(_, write)| *write));
                    assert!(order.iter().any(|(_, write)| !write));
                }
                (
                    "committed-read" | "repeatable-read" | "atomic-read" | "causal",
                    WireWitness::Saturation(edges),
                ) => {
                    assert!(edges.contains(&(
                        TransactionId {
                            session_id: 1,
                            session_height: 0
                        },
                        TransactionId {
                            session_id: 2,
                            session_height: 0
                        },
                    )));
                }
                other => panic!("unexpected witness: {other:?}"),
            }
        }
    }
    let output = verify(dir.path(), "serializable", &["--verbose"]);
    success(&output);
    let stdout = String::from_utf8(output.stdout).unwrap();
    assert!(stdout.contains("a.hist: PASS"));
    assert!(stdout.contains("b.txt: PASS"));
    assert!(stdout.lines().count() > 2);
}

#[test]
fn verify_distinguishes_inconsistent_and_structurally_invalid_histories() {
    let dir = TempDir::new("verify-rejections");
    // Write skew is admitted by SI, but has no serial commit order.
    let expected_errors: Vec<_> = ["[y==? x:=1]\n---\n[x==? y:=2]\n", "[x==99]\n"]
        .iter()
        .map(|input| {
            let sessions = dbcop_parser::parse_history(input).unwrap();
            format!(
                "{:?}",
                dbcop_core::check(&sessions, dbcop_core::Consistency::Serializable).unwrap_err()
            )
        })
        .collect();
    std::fs::write(dir.path().join("a.hist"), "[y==? x:=1]\n---\n[x==? y:=2]\n").unwrap();
    success(&verify(dir.path(), "snapshot-isolation", &["--json"]));
    std::fs::write(dir.path().join("b.txt"), "[x==99]\n").unwrap();
    std::fs::write(dir.path().join("c.hist"), "[z:=1]\n").unwrap();
    for flags in [vec!["--json"], vec!["--verbose"], vec![]] {
        let output = verify(dir.path(), "serializable", &flags);
        failure(&output);
        assert_eq!(output.stderr, [] as [u8; 0]);
        if flags == ["--json"] {
            let results = records(&output);
            assert_eq!(results.len(), 3);
            for result in &results[..2] {
                assert!(!result.ok);
                assert!(result.witness.is_none());
            }
            assert_ne!(results[0].error, results[1].error);
            for (result, expected) in results.iter().zip(&expected_errors) {
                assert_eq!(result.error.as_ref(), Some(expected));
            }
            assert!(results[2].ok);
        } else {
            let stdout = String::from_utf8(output.stdout).unwrap();
            assert!(stdout.contains("a.hist: FAIL"));
            assert!(stdout.contains("b.txt: FAIL"));
            assert!(stdout.contains("c.hist: PASS"));
        }
    }
}

#[test]
fn verify_malformed_inputs_are_execution_errors_not_json_verdicts() {
    let dir = TempDir::new("verify-errors");
    for path in [dir.path().to_path_buf(), dir.path().join("missing")] {
        let output = verify(&path, "serializable", &["--json"]);
        failure(&output);
        assert_eq!(output.stdout, [] as [u8; 0]);
        assert_ne!(output.stderr, [] as [u8; 0]);
    }
    for (name, contents) in [
        ("bad.json", "{not json}"),
        ("bad.hist", "[x:=]"),
        ("bad.txt", "[unterminated"),
    ] {
        let path = dir.path().join(name);
        std::fs::write(&path, contents).unwrap();
        let output = verify(dir.path(), "serializable", &["--json"]);
        failure(&output);
        assert_eq!(output.stdout, [] as [u8; 0]);
        assert_ne!(output.stderr, [] as [u8; 0]);
        assert_eq!(std::fs::read_to_string(&path).unwrap(), contents);
        std::fs::remove_file(path).unwrap();
    }
}

#[test]
fn fmt_recurses_checks_without_writing_and_is_idempotent() {
    let dir = TempDir::new("fmt");
    let nested = dir.path().join("nested/deeper");
    std::fs::create_dir_all(&nested).unwrap();
    let first = dir.path().join("first.hist");
    let second = nested.join("second.hist");
    let ignored = nested.join("leave.txt");
    let original = "  [x:=1   y:=2]\n---\n[x==1]\n";
    std::fs::write(&first, original).unwrap();
    std::fs::write(&second, "[z:=3]! \n").unwrap();
    std::fs::write(&ignored, "not a history").unwrap();
    let before = dbcop_parser::parse_history(original).unwrap();
    let output = cli()
        .args(["fmt", "--check"])
        .arg(dir.path())
        .output()
        .unwrap();
    failure(&output);
    assert_eq!(std::fs::read_to_string(&first).unwrap(), original);
    assert_eq!(std::fs::read_to_string(&second).unwrap(), "[z:=3]! \n");

    success(&cli().arg("fmt").arg(dir.path()).output().unwrap());
    let formatted = std::fs::read_to_string(&first).unwrap();
    let after = dbcop_parser::parse_history(&formatted).unwrap();
    assert_eq!(after.len(), before.len());
    for (after, before) in after.iter().zip(&before) {
        assert_eq!(after.len(), before.len());
        for (after, before) in after.iter().zip(before) {
            assert_eq!(after.committed, before.committed);
            assert_eq!(after.events, before.events);
        }
    }
    assert_eq!(formatted, "[x:=1 y:=2]\n---\n[x==1]\n");
    assert_eq!(std::fs::read_to_string(&second).unwrap(), "[z:=3]!\n");
    assert_eq!(std::fs::read_to_string(&ignored).unwrap(), "not a history");
    success(
        &cli()
            .args(["fmt", "--check"])
            .arg(dir.path())
            .output()
            .unwrap(),
    );
    success(&cli().arg("fmt").arg(&first).arg(&second).output().unwrap());
    assert_eq!(std::fs::read_to_string(&first).unwrap(), formatted);
}

#[test]
fn fmt_reports_missing_malformed_and_unreadable_files_without_replacing_them() {
    let dir = TempDir::new("fmt-errors");
    failure(
        &cli()
            .arg("fmt")
            .arg(dir.path().join("missing"))
            .output()
            .unwrap(),
    );
    let malformed = dir.path().join("broken.hist");
    std::fs::write(&malformed, "[x:=]").unwrap();
    let output = cli().arg("fmt").arg(&malformed).output().unwrap();
    failure(&output);
    assert_ne!(output.stderr, [] as [u8; 0]);
    assert_eq!(std::fs::read(&malformed).unwrap(), b"[x:=]");
    let unreadable = dir.path().join("non-utf8.hist");
    std::fs::write(&unreadable, [0xff, 0xfe]).unwrap();
    let output = cli().arg("fmt").arg(&unreadable).output().unwrap();
    failure(&output);
    assert_ne!(output.stderr, [] as [u8; 0]);
    assert_eq!(std::fs::read(&unreadable).unwrap(), [0xff, 0xfe]);
}

#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum SchemaType {
    Single(String),
    Multiple(Vec<String>),
}

impl SchemaType {
    fn contains(&self, expected: &str) -> bool {
        match self {
            Self::Single(value) => value == expected,
            Self::Multiple(values) => values.iter().any(|value| value == expected),
        }
    }
}

// Decode only the JSON Schema vocabulary exercised by the command contract.
#[derive(Debug, Default, Deserialize)]
struct Schema {
    #[serde(rename = "type")]
    kind: Option<SchemaType>,
    #[serde(rename = "$ref")]
    reference: Option<String>,
    #[serde(rename = "$defs", alias = "definitions", default)]
    definitions: BTreeMap<String, Self>,
    items: Option<Box<Self>>,
    #[serde(default)]
    properties: BTreeMap<String, Self>,
    #[serde(default)]
    required: Vec<String>,
    #[serde(rename = "oneOf", default)]
    one_of: Vec<Self>,
    minimum: Option<u64>,
}

fn resolve<'a>(root: &'a Schema, schema: &'a Schema) -> &'a Schema {
    schema.reference.as_ref().map_or(schema, |reference| {
        let name = reference
            .strip_prefix("#/$defs/")
            .or_else(|| reference.strip_prefix("#/definitions/"))
            .unwrap();
        &root.definitions[name]
    })
}

#[test]
fn schema_describes_sessions_transactions_and_tagged_read_write_events() {
    let output = cli().arg("schema").output().unwrap();
    success(&output);
    let schema: Schema = serde_json::from_slice(&output.stdout).unwrap();
    assert!(schema.kind.as_ref().unwrap().contains("array"));
    let session = resolve(&schema, schema.items.as_ref().unwrap());
    assert!(session.kind.as_ref().unwrap().contains("array"));
    let transaction = resolve(&schema, session.items.as_ref().unwrap());
    assert!(transaction.kind.as_ref().unwrap().contains("object"));
    assert!(transaction.required.iter().any(|field| field == "events"));
    assert!(transaction
        .required
        .iter()
        .any(|field| field == "committed"));
    assert!(transaction.properties["committed"]
        .kind
        .as_ref()
        .unwrap()
        .contains("boolean"));
    let events = &transaction.properties["events"];
    assert!(events.kind.as_ref().unwrap().contains("array"));
    let event = resolve(&schema, events.items.as_ref().unwrap());
    assert_eq!(event.one_of.len(), 2);
    for tag in ["Read", "Write"] {
        let variant = event
            .one_of
            .iter()
            .find(|variant| variant.properties.contains_key(tag))
            .unwrap();
        assert!(variant.required.iter().any(|field| field == tag));
        let fields = &variant.properties[tag];
        assert!(fields.kind.as_ref().unwrap().contains("object"));
        let variable = &fields.properties["variable"];
        assert!(variable.kind.as_ref().unwrap().contains("integer"));
        assert_eq!(variable.minimum, Some(0));
        let version = &fields.properties["version"];
        assert!(version.kind.as_ref().unwrap().contains("integer"));
        if tag == "Read" {
            assert!(version.kind.as_ref().unwrap().contains("null"));
        } else {
            assert!(fields.required.iter().any(|field| field == "version"));
        }
    }
}

#[test]
fn numeric_json_rejection_preserves_core_debug_and_checks_later_files() {
    use dbcop_core::history::raw::types::{Event, Session, Transaction};

    #[derive(Serialize)]
    struct Input {
        params: dbcop_testgen::generator::HistParams,
        info: &'static str,
        start: &'static str,
        end: &'static str,
        data: Vec<Session<u64, u64>>,
    }

    let dir = TempDir::new("numeric-rejection");
    let input = Input {
        params: dbcop_testgen::generator::HistParams::default(),
        info: "",
        start: "2026-01-01T00:00:00+00:00",
        end: "2026-01-01T00:00:00+00:00",
        data: vec![vec![Transaction {
            events: vec![Event::Read {
                variable: 7,
                version: Some(99),
            }],
            committed: true,
        }]],
    };
    let expected = format!(
        "{:?}",
        dbcop_core::check(&input.data, dbcop_core::Consistency::Serializable).unwrap_err()
    );
    serde_json::to_writer(
        std::fs::File::create(dir.path().join("a.json")).unwrap(),
        &input,
    )
    .unwrap();
    std::fs::write(dir.path().join("b.hist"), "[x:=1]\n").unwrap();
    let output = verify(dir.path(), "serializable", &["--json"]);
    failure(&output);
    assert_eq!(output.stderr, [] as [u8; 0]);
    let results = records(&output);
    assert_eq!(results.len(), 2);
    assert_eq!(results[0].error.as_ref(), Some(&expected));
    assert!(!results[0].ok);
    assert!(results[1].ok);
}

#[test]
fn library_execution_errors_retain_typed_sources_and_paths() {
    use std::error::Error as _;

    let dir = TempDir::new("typed-errors");
    let args = dbcop_cli::VerifyArgs {
        input_dir: dir.path().join("missing"),
        consistency: dbcop_cli::ConsistencyLevel::Serializable,
        verbose: false,
        json: true,
    };
    let error = dbcop_cli::verify_histories(&args).unwrap_err();
    assert!(error.source().unwrap().is::<std::io::Error>());
    assert!(matches!(
        error,
        dbcop_cli::Error::Io { ref path, .. } if path == &args.input_dir
    ));

    let args = dbcop_cli::VerifyArgs {
        input_dir: dir.path().to_path_buf(),
        ..args
    };
    let text_path = dir.path().join("bad.hist");
    std::fs::write(&text_path, "[x:=]").unwrap();
    let error = dbcop_cli::verify_histories(&args).unwrap_err();
    assert!(error.source().unwrap().is::<dbcop_parser::ParseError>());
    assert!(matches!(error, dbcop_cli::Error::Parse { path, .. } if path == text_path));
    std::fs::remove_file(text_path).unwrap();

    let json_path = dir.path().join("bad.json");
    std::fs::write(&json_path, "{not json}").unwrap();
    let error = dbcop_cli::verify_histories(&args).unwrap_err();
    assert!(error.source().unwrap().is::<serde_json::Error>());
    assert!(matches!(error, dbcop_cli::Error::Json { path, .. } if path == json_path));

    let direct: dbcop_cli::Error = std::io::Error::other("output unavailable").into();
    assert!(direct.source().unwrap().is::<std::io::Error>());
}

#[cfg(target_os = "linux")]
#[test]
fn stdout_errors_fail_without_panicking() {
    let dir = TempDir::new("stdout-errors");
    std::fs::write(dir.path().join("a.hist"), "[x:=1]\n").unwrap();
    for args in [
        vec!["schema"],
        vec![
            "verify",
            "--input-dir",
            dir.path().to_str().unwrap(),
            "--consistency",
            "serializable",
            "--json",
        ],
        vec!["fmt", "--check", dir.path().to_str().unwrap()],
    ] {
        let output = cli()
            .args(args)
            .stdout(
                std::fs::OpenOptions::new()
                    .write(true)
                    .open("/dev/full")
                    .unwrap(),
            )
            .output()
            .unwrap();
        failure(&output);
        let stderr = String::from_utf8(output.stderr).unwrap();
        assert_ne!(stderr, "");
        assert!(!stderr.contains("panicked"));
    }
}
