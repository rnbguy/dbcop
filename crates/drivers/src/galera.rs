//! Galera Cluster driver (synchronous multi-master `MySQL` replication).

use dbcop_core::history::raw::types::Session;
use dbcop_testgen::generator::History;

use crate::{ClusterConfig, DbDriver};

/// Driver for Galera Cluster (MySQL-compatible).
pub struct GaleraDriver {
    _config: ClusterConfig,
}

impl DbDriver for GaleraDriver {
    type Error = GaleraError;

    /// Unimplemented stub: panics via `todo!()`.
    fn connect(_config: &ClusterConfig) -> Result<Self, Self::Error> {
        todo!("Galera driver connection not yet implemented")
    }

    /// Unimplemented stub: panics via `todo!()`.
    fn execute(&self, _history: &History) -> Result<Vec<Session<u64, u64>>, Self::Error> {
        todo!("Galera driver execution not yet implemented")
    }
}

/// Errors from the Galera driver.
#[derive(Debug, thiserror::Error)]
pub enum GaleraError {
    /// Failed to connect to the cluster.
    #[error("connection failed: {0}")]
    Connection(String),
    /// Query execution failed.
    #[error("query execution failed: {0}")]
    Execution(String),
}
