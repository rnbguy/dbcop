//! dbcop CLI -- generate and verify transactional histories.

use std::path::PathBuf;

use clap::{Parser, Subcommand, ValueEnum};

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
