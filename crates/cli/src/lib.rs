//! dbcop CLI -- generate and verify transactional histories.

use std::io::{self, Write};
use std::path::PathBuf;

use clap::{Parser, Subcommand, ValueEnum};
use dbcop_core::consistency::witness::Witness;
use serde::Serialize;

/// An execution failure while generating or verifying histories.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("Failed to {operation} {}: {source}", path.display())]
    Io {
        operation: &'static str,
        path: PathBuf,
        #[source]
        source: io::Error,
    },
    #[error("Failed to {operation} {}: {source}", path.display())]
    Json {
        operation: &'static str,
        path: PathBuf,
        #[source]
        source: serde_json::Error,
    },
    #[error("Failed to parse {}: {source}", path.display())]
    Parse {
        path: PathBuf,
        #[source]
        source: dbcop_parser::ParseError,
    },
    #[error("No .json, .hist, or .txt files found in {}", .0.display())]
    NoHistories(PathBuf),
    #[error("Failed to write output: {0}")]
    Output(#[from] io::Error),
    #[error("Failed to serialize output: {0}")]
    Serialize(#[from] serde_json::Error),
}

#[derive(Debug, thiserror::Error)]
enum Rejection {
    #[error("{0:?}")]
    Numeric(#[from] dbcop_core::consistency::error::Error<u64, u64>),
    #[error("{0:?}")]
    Named(#[from] dbcop_core::consistency::error::Error<String, u64>),
}

#[derive(Serialize)]
struct Passed<'a> {
    file: &'a str,
    ok: bool,
    #[serde(serialize_with = "dbcop_core::consistency::witness::serialize_witness")]
    witness: &'a Witness,
}

#[derive(Serialize)]
struct Failed<'a> {
    error: &'a str,
    file: &'a str,
    ok: bool,
}

#[derive(Debug, Parser)]
#[command(
    name = "dbcop",
    about = "Runtime monitoring for transactional consistency"
)]
pub struct App {
    #[command(subcommand)]
    pub command: Command,
}

#[derive(Debug, Subcommand)]
pub enum Command {
    /// Generate random transactional histories
    Generate(GenerateArgs),
    /// Verify consistency of transactional histories
    Verify(VerifyArgs),
    /// Format compact history (.hist) files
    Fmt(FmtArgs),
    /// Print the JSON Schema for the history input format to stdout
    Schema,
}

#[derive(Debug, Parser)]
pub struct GenerateArgs {
    /// Number of histories to generate
    #[arg(long)]
    pub n_hist: u64,
    /// Number of nodes (sessions)
    #[arg(long)]
    pub n_node: u64,
    /// Number of variables
    #[arg(long)]
    pub n_var: u64,
    /// Number of transactions per node
    #[arg(long)]
    pub n_txn: u64,
    /// Number of events per transaction
    #[arg(long)]
    pub n_evt: u64,
    /// Output directory for generated history files
    #[arg(long)]
    pub output_dir: PathBuf,
}

#[derive(Debug, Parser)]
pub struct VerifyArgs {
    /// Input directory containing history JSON files
    #[arg(long)]
    pub input_dir: PathBuf,
    /// Consistency level to check
    #[arg(long)]
    pub consistency: ConsistencyLevel,
    /// Print witness details on PASS and full error details on FAIL
    #[arg(long)]
    pub verbose: bool,
    /// Output results as JSON (one object per file)
    #[arg(long)]
    pub json: bool,
}

#[derive(Debug, Clone, ValueEnum)]
pub enum ConsistencyLevel {
    CommittedRead,
    RepeatableRead,
    AtomicRead,
    Causal,
    Prefix,
    SnapshotIsolation,
    Serializable,
}

#[derive(Debug, Parser)]
pub struct FmtArgs {
    /// Input files or directories to format (glob patterns supported)
    #[arg(required = true)]
    pub paths: Vec<PathBuf>,
    /// Check formatting without modifying files (exit 1 if unformatted)
    #[arg(long)]
    pub check: bool,
}

impl From<ConsistencyLevel> for dbcop_core::Consistency {
    fn from(level: ConsistencyLevel) -> Self {
        match level {
            ConsistencyLevel::CommittedRead => Self::CommittedRead,
            ConsistencyLevel::RepeatableRead => Self::RepeatableRead,
            ConsistencyLevel::AtomicRead => Self::AtomicRead,
            ConsistencyLevel::Causal => Self::Causal,
            ConsistencyLevel::Prefix => Self::Prefix,
            ConsistencyLevel::SnapshotIsolation => Self::SnapshotIsolation,
            ConsistencyLevel::Serializable => Self::Serializable,
        }
    }
}
/// Generate random histories and write them as JSON files into `args.output_dir`.
///
/// Returns the number of histories written. The binary prints `Err` and exits 1.
///
/// # Errors
///
/// Returns `Err` if the output directory or any history file cannot be created.
pub fn generate_histories(args: &GenerateArgs) -> Result<usize, Error> {
    std::fs::create_dir_all(&args.output_dir).map_err(|source| Error::Io {
        operation: "create output directory",
        path: args.output_dir.clone(),
        source,
    })?;

    let histories = dbcop_testgen::generator::generate_mult_histories(
        args.n_hist,
        args.n_node,
        args.n_var,
        args.n_txn,
        args.n_evt,
    );

    for history in &histories {
        let path = args.output_dir.join(format!("{}.json", history.get_id()));
        let file = std::fs::File::create(&path).map_err(|source| Error::Io {
            operation: "create",
            path: path.clone(),
            source,
        })?;
        serde_json::to_writer_pretty(file, history).map_err(|source| Error::Json {
            operation: "write",
            path,
            source,
        })?;
    }

    writeln!(
        io::stdout().lock(),
        "Generated {} histories to {}",
        histories.len(),
        args.output_dir.display()
    )?;
    Ok(histories.len())
}

/// Verify every history file in `args.input_dir` against a consistency level.
///
/// Prints one PASS/FAIL line per file. Returns `Ok(any_failed)`; `Err`
/// covers unreadable directories, missing files, and IO/parse failures.
///
/// # Errors
///
/// Returns [`Error`] on unreadable input, an empty file set, or output failure.
pub fn verify_histories(args: &VerifyArgs) -> Result<bool, Error> {
    let level = dbcop_core::Consistency::from(args.consistency.clone());
    let mut any_failed = false;

    let mut entries: Vec<_> = std::fs::read_dir(&args.input_dir)
        .map_err(|source| Error::Io {
            operation: "read input directory",
            path: args.input_dir.clone(),
            source,
        })?
        .filter(|entry| {
            entry.as_ref().map_or(true, |entry| {
                entry
                    .path()
                    .extension()
                    .is_some_and(|ext| ext == "json" || ext == "hist" || ext == "txt")
            })
        })
        .collect::<Result<Vec<_>, _>>()
        .map_err(|source| Error::Io {
            operation: "read input directory entry",
            path: args.input_dir.clone(),
            source,
        })?;

    entries.sort_by_key(std::fs::DirEntry::path);

    if entries.is_empty() {
        return Err(Error::NoHistories(args.input_dir.clone()));
    }
    let mut output = io::stdout().lock();

    for entry in entries {
        let path = entry.path();
        let filename = path.file_name().unwrap_or_default().to_string_lossy();

        // outcome: Ok(witness) = pass, Err(rejection) = a typed consistency failure.
        let outcome: Result<Witness, Rejection> =
            if path.extension().is_some_and(|ext| ext == "json") {
                let file = std::fs::File::open(&path).map_err(|source| Error::Io {
                    operation: "open",
                    path: path.clone(),
                    source,
                })?;
                let history: dbcop_testgen::generator::History = serde_json::from_reader(file)
                    .map_err(|source| Error::Json {
                        operation: "parse",
                        path: path.clone(),
                        source,
                    })?;
                dbcop_core::check(history.get_data(), level).map_err(Rejection::from)
            } else {
                // .hist or .txt
                let content = std::fs::read_to_string(&path).map_err(|source| Error::Io {
                    operation: "read",
                    path: path.clone(),
                    source,
                })?;
                let sessions =
                    dbcop_parser::parse_history(&content).map_err(|source| Error::Parse {
                        path: path.clone(),
                        source,
                    })?;
                dbcop_core::check(&sessions, level).map_err(Rejection::from)
            };

        match outcome {
            Ok(witness) => {
                if args.json {
                    // JSON object keys cannot represent transaction IDs; use the WASM edge-list format.
                    serde_json::to_writer(
                        &mut output,
                        &Passed {
                            file: &filename,
                            ok: true,
                            witness: &witness,
                        },
                    )?;
                    writeln!(output)?;
                } else if args.verbose {
                    writeln!(output, "{filename}: PASS")?;
                    writeln!(output, "  witness: {witness:?}")?;
                } else {
                    writeln!(output, "{filename}: PASS")?;
                }
            }
            Err(e) => {
                any_failed = true;
                if args.json {
                    let error = e.to_string();
                    serde_json::to_writer(
                        &mut output,
                        &Failed {
                            error: &error,
                            file: &filename,
                            ok: false,
                        },
                    )?;
                    writeln!(output)?;
                } else if args.verbose {
                    writeln!(output, "{filename}: FAIL")?;
                    writeln!(output, "  error: {e}")?;
                } else {
                    writeln!(output, "{filename}: FAIL ({e})")?;
                }
            }
        }
    }

    Ok(any_failed)
}

#[cfg(test)]
mod tests {
    use clap::ValueEnum;

    use super::ConsistencyLevel;

    #[test]
    fn consistency_levels_map_to_core_levels() {
        assert!(matches!(
            dbcop_core::Consistency::from(ConsistencyLevel::CommittedRead),
            dbcop_core::Consistency::CommittedRead
        ));
        assert!(matches!(
            dbcop_core::Consistency::from(ConsistencyLevel::RepeatableRead),
            dbcop_core::Consistency::RepeatableRead
        ));
        assert!(matches!(
            dbcop_core::Consistency::from(ConsistencyLevel::AtomicRead),
            dbcop_core::Consistency::AtomicRead
        ));
        assert!(matches!(
            dbcop_core::Consistency::from(ConsistencyLevel::Causal),
            dbcop_core::Consistency::Causal
        ));
        assert!(matches!(
            dbcop_core::Consistency::from(ConsistencyLevel::Prefix),
            dbcop_core::Consistency::Prefix
        ));
        assert!(matches!(
            dbcop_core::Consistency::from(ConsistencyLevel::SnapshotIsolation),
            dbcop_core::Consistency::SnapshotIsolation
        ));
        assert!(matches!(
            dbcop_core::Consistency::from(ConsistencyLevel::Serializable),
            dbcop_core::Consistency::Serializable
        ));
    }

    #[test]
    fn repeatable_read_cli_value_is_exposed() {
        let value = ConsistencyLevel::RepeatableRead
            .to_possible_value()
            .expect("RepeatableRead should be exposed as a clap value");
        assert_eq!(value.get_name(), "repeatable-read");
    }
}
