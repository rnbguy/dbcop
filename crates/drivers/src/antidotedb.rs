//! `AntidoteDB` driver (geo-replicated CRDT database).

use dbcop_core::history::raw::types::Session;
use dbcop_testgen::generator::History;

use crate::{ClusterConfig, DbDriver};

/// Driver for `AntidoteDB`.
pub struct AntidoteDbDriver {
    _config: ClusterConfig,
}

impl DbDriver for AntidoteDbDriver {
    type Error = AntidoteDbError;

    fn connect(_config: &ClusterConfig) -> Result<Self, Self::Error> {
        todo!("AntidoteDB driver connection not yet implemented")
    }

    fn execute(&self, _history: &History) -> Result<Vec<Session<u64, u64>>, Self::Error> {
        todo!("AntidoteDB driver execution not yet implemented")
    }
}

/// Errors from the `AntidoteDB` driver.
#[derive(Debug)]
pub enum AntidoteDbError {
    /// Failed to connect to the cluster.
    Connection(String),
    /// Query execution failed.
    Execution(String),
}
