//! Database drivers for executing generated histories against real databases.
//!
//! Each driver connects to a specific database system, executes a generated
//! history (a set of concurrent sessions with transactions), and collects
//! the observed results for consistency verification.

use dbcop_core::history::raw::types::Session;
use dbcop_testgen::generator::History;

pub mod antidotedb;
pub mod cockroachdb;
pub mod galera;

/// Configuration for connecting to a database cluster.
pub struct ClusterConfig {
    /// Hostnames or IP addresses of the cluster nodes.
    pub hosts: Vec<String>,
    /// Port number for database connections.
    pub port: u16,
    /// Name of the database to use.
    pub db_name: String,
}

/// A driver capable of executing a generated history against a real database.
pub trait DbDriver {
    /// The error type returned by this driver.
    type Error: core::fmt::Debug;

    /// Connect to the database cluster with the given configuration.
    ///
    /// # Errors
    ///
    /// Returns an error if the connection cannot be established.
    fn connect(config: &ClusterConfig) -> Result<Self, Self::Error>
    where
        Self: Sized;

    /// Execute a generated history against the connected database and return
    /// the observed sessions (with actual read values filled in).
    ///
    /// Each session in the history is executed concurrently on a separate
    /// database connection, mirroring the original session structure.
    ///
    /// # Errors
    ///
    /// Returns an error if execution fails (connection lost, query error, etc.).
    fn execute(&self, history: &History) -> Result<Vec<Session<u64, u64>>, Self::Error>;
}
