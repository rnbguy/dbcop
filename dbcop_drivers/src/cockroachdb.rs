//! `CockroachDB` driver (distributed SQL, `PostgreSQL` wire-compatible).

use dbcop_core::history::raw::types::Session;
use dbcop_testgen::generator::History;

use crate::{ClusterConfig, DbDriver};

/// Driver for `CockroachDB`.
pub struct CockroachDbDriver {
    _config: ClusterConfig,
}

impl DbDriver for CockroachDbDriver {
    type Error = CockroachDbError;

    fn connect(_config: &ClusterConfig) -> Result<Self, Self::Error> {
        todo!("CockroachDB driver connection not yet implemented")
    }

    fn execute(&self, _history: &History) -> Result<Vec<Session<u64, u64>>, Self::Error> {
        todo!("CockroachDB driver execution not yet implemented")
    }
}

/// Errors from the `CockroachDB` driver.
#[derive(Debug)]
pub enum CockroachDbError {
    /// Failed to connect to the cluster.
    Connection(String),
    /// Query execution failed.
    Execution(String),
}
