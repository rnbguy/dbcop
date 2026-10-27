use dbcop_core::consistency::error::Error as CoreError;
use dbcop_core::consistency::witness::Witness;
use dbcop_core::history::atomic::types::TransactionId;
use serde::Serialize;

use super::alloc::collections::BTreeMap;
use super::alloc::string::String;
use super::alloc::vec::Vec;

pub type Edge = (TransactionId, TransactionId);

#[derive(Serialize)]
#[serde(untagged)]
pub enum ResponseError<V> {
    Core(CoreError<V, u64>),
    Message(String),
}

#[derive(Serialize)]
#[serde(untagged)]
pub enum CheckResponse<V> {
    Success {
        ok: bool,
        /// Serialize a [`Witness`] through a JSON-safe edge-list view.
        ///
        /// `SaturationOrder` contains `DiGraph<TransactionId>` whose `HashMap` keys
        /// are not strings, so serializing the graph directly as JSON fails.
        /// The shared adapter converts the graph to an edge-list representation instead.
        #[serde(serialize_with = "dbcop_core::consistency::witness::serialize_witness")]
        witness: Witness,
    },
    Failure {
        ok: bool,
        error: ResponseError<V>,
    },
}

#[derive(Serialize)]
#[serde(untagged)]
pub enum TraceResponse<V> {
    Checked {
        #[serde(flatten)]
        result: CheckResponse<V>,
        #[serde(flatten)]
        metadata: TraceMetadata,
        #[serde(flatten)]
        proof: Option<TraceEdges>,
    },
    Invalid {
        ok: bool,
        error: String,
    },
}

#[derive(Serialize)]
pub struct TraceMetadata {
    pub level: String,
    pub session_count: u64,
    pub transaction_count: u64,
    pub sessions: Vec<Vec<TraceTransaction>>,
}

#[derive(Serialize)]
pub struct TraceTransaction {
    pub id: TransactionId,
    pub reads: BTreeMap<String, Option<u64>>,
    pub writes: BTreeMap<String, u64>,
    pub events: Vec<TraceEvent>,
    pub committed: bool,
}

#[derive(Serialize)]
#[serde(tag = "type")]
pub enum TraceEvent {
    #[serde(rename = "R")]
    Read {
        variable: String,
        version: Option<u64>,
    },
    #[serde(rename = "W")]
    Write { variable: String, version: u64 },
}

#[derive(Serialize)]
pub struct TraceEdges {
    pub witness_edges: Vec<Edge>,
    pub wr_edges: Vec<Edge>,
}

#[derive(Serialize)]
#[serde(untagged)]
pub enum StepResponse {
    Error {
        error: ResponseError<u64>,
    },
    CausalInit {
        session_id: String,
        step: u64,
        level: &'static str,
        steppable: bool,
        done: bool,
        new_edges: Vec<Edge>,
        total_edges: u64,
    },
    CompleteInit {
        session_id: String,
        step: u64,
        level: String,
        steppable: bool,
        done: bool,
        result: TraceResponse<u64>,
    },
    Advance {
        session_id: String,
        step: u64,
        phase: &'static str,
        new_edges: Vec<Edge>,
        total_edges: u64,
        done: bool,
        #[serde(skip_serializing_if = "Option::is_none")]
        result: Option<TraceResponse<u64>>,
    },
}

#[derive(Serialize)]
pub struct MessageError {
    pub error: String,
}

#[derive(Serialize)]
#[serde(untagged)]
pub enum ParsedHistory<V> {
    Sessions(Vec<dbcop_core::history::raw::types::Session<V, u64>>),
    Error(MessageError),
}

#[derive(Serialize)]
pub struct TokenSpan<'a> {
    pub kind: &'a dbcop_parser::TokenKind,
    pub start: usize,
    pub end: usize,
    pub text: &'a str,
}
