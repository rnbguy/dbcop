//! dbcop CLI -- generate and verify transactional histories.

use clap::{Parser, Subcommand};

#[derive(Debug, Parser)]
#[command(name = "dbcop", about = "Runtime monitoring for transactional consistency")]
pub struct App {
    #[command(subcommand)]
    pub command: Command,
}

#[derive(Debug, Subcommand)]
pub enum Command {
    /// Generate random transactional histories
    Generate,
    /// Verify consistency of transactional histories
    Verify,
}
