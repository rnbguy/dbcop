//! wasm library for dbcop
//! compiled binary is uploaded as github action artifact

#![cfg_attr(not(test), no_std)]
#![cfg_attr(not(test), no_main)]

extern crate alloc;

use alloc::string::{String, ToString};
use alloc::vec::Vec;

use dbcop_core::consistency::saturation::committed_read::check_committed_read;
use dbcop_core::consistency::witness::Witness;
use dbcop_core::history::atomic::types::TransactionId;
use dbcop_core::history::raw::types::Session;
use dbcop_core::Consistency;
use wasm_bindgen::prelude::*;

fn parse_level(level: &str) -> Option<Consistency> {
    match level {
        "committed-read" => Some(Consistency::CommittedRead),
        "atomic-read" => Some(Consistency::AtomicRead),
        "causal" => Some(Consistency::Causal),
        "prefix" => Some(Consistency::Prefix),
        "snapshot-isolation" => Some(Consistency::SnapshotIsolation),
        "serializable" => Some(Consistency::Serializable),
        _ => None,
    }
}

fn tid_to_json(tid: &TransactionId) -> serde_json::Value {
    serde_json::json!({
        "session_id": tid.session_id,
        "session_height": tid.session_height
    })
}

fn witness_edges(witness: &Witness) -> Vec<serde_json::Value> {
    match witness {
        Witness::SaturationOrder(graph) => graph
            .to_edge_list()
            .into_iter()
            .map(|(from, to)| serde_json::json!([tid_to_json(&from), tid_to_json(&to)]))
            .collect(),
        Witness::CommitOrder(order) => order
            .windows(2)
            .map(|w| serde_json::json!([tid_to_json(&w[0]), tid_to_json(&w[1])]))
            .collect(),
        Witness::SplitCommitOrder(order) => order
            .windows(2)
            .map(|w| serde_json::json!([tid_to_json(&w[0].0), tid_to_json(&w[1].0)]))
            .collect(),
    }
}

/// Check whether a history (as JSON) satisfies the given consistency level.
///
/// Returns a JSON string:
/// - On success: `{"ok":true,"witness":{...}}`
/// - On failure: `{"ok":false,"error":{...}}`
/// - On invalid input: `{"ok":false,"error":"<description>"}`
#[must_use]
#[wasm_bindgen]
pub fn check_consistency(history_json: &str, level: &str) -> String {
    let Some(consistency) = parse_level(level) else {
        return serde_json::json!({"ok": false, "error": "unknown consistency level"}).to_string();
    };

    let sessions = match serde_json::from_str::<Vec<Session<u64, u64>>>(history_json) {
        Ok(s) => s,
        Err(e) => {
            return serde_json::json!({"ok": false, "error": e.to_string()}).to_string();
        }
    };

    match dbcop_core::check(&sessions, consistency) {
        Ok(witness) => serde_json::json!({"ok": true, "witness": witness}).to_string(),
        Err(error) => serde_json::json!({"ok": false, "error": error}).to_string(),
    }
}

fn build_sessions_json(sessions: &[Session<u64, u64>]) -> (serde_json::Value, u64, u64) {
    let session_count = sessions.len() as u64;
    let transaction_count: u64 = sessions.iter().map(|s| s.len() as u64).sum();

    let sessions_json: Vec<serde_json::Value> = sessions
        .iter()
        .enumerate()
        .map(|(sid, session)| {
            let txns: Vec<serde_json::Value> = session
                .iter()
                .enumerate()
                .map(|(th, txn)| {
                    let mut reads = serde_json::Map::new();
                    let mut writes = serde_json::Map::new();
                    for event in &txn.events {
                        match event {
                            dbcop_core::history::raw::types::Event::Read { variable, version } => {
                                reads.insert(
                                    variable.to_string(),
                                    version
                                        .as_ref()
                                        .map_or(serde_json::Value::Null, |v| serde_json::json!(v)),
                                );
                            }
                            dbcop_core::history::raw::types::Event::Write { variable, version } => {
                                writes.insert(variable.to_string(), serde_json::json!(version));
                            }
                        }
                    }
                    serde_json::json!({
                        "id": {
                            "session_id": (sid as u64) + 1,
                            "session_height": th as u64
                        },
                        "reads": reads,
                        "writes": writes,
                        "committed": txn.committed
                    })
                })
                .collect();
            serde_json::json!(txns)
        })
        .collect();

    (
        serde_json::json!(sessions_json),
        session_count,
        transaction_count,
    )
}

/// Check consistency and return a rich trace suitable for web visualization.
///
/// Returns a JSON string with session metadata, witness details, and graph edges
/// that a web UI can use to render the history and its consistency proof/violation.
///
/// On success:
/// ```json
/// {
///   "ok": true,
///   "level": "serializable",
///   "session_count": 3,
///   "transaction_count": 12,
///   "sessions": [[{"id":...,"reads":...,"writes":...,"committed":true},...],...],
///   "witness": { ... },
///   "witness_edges": [[from_id, to_id], ...],
///   "wr_edges": [[from_id, to_id], ...]
/// }
/// ```
///
/// On failure:
/// ```json
/// {
///   "ok": false,
///   "level": "causal",
///   "session_count": 2,
///   "transaction_count": 5,
///   "sessions": [...],
///   "error": { ... }
/// }
/// ```
///
/// On invalid input: `{"ok": false, "error": "..."}`
#[must_use]
#[wasm_bindgen]
pub fn check_consistency_trace(history_json: &str, level: &str) -> String {
    let Some(consistency) = parse_level(level) else {
        return serde_json::json!({"ok": false, "error": "unknown consistency level"}).to_string();
    };

    let sessions = match serde_json::from_str::<Vec<Session<u64, u64>>>(history_json) {
        Ok(s) => s,
        Err(e) => {
            return serde_json::json!({"ok": false, "error": e.to_string()}).to_string();
        }
    };

    let (sessions_json, session_count, transaction_count) = build_sessions_json(&sessions);

    let wr_edges: Vec<serde_json::Value> = check_committed_read(&sessions)
        .ok()
        .map(|graph| {
            graph
                .to_edge_list()
                .into_iter()
                .map(|(from, to)| serde_json::json!([tid_to_json(&from), tid_to_json(&to)]))
                .collect()
        })
        .unwrap_or_default();

    let level_str = level;

    match dbcop_core::check(&sessions, consistency) {
        Ok(witness) => {
            let edges = witness_edges(&witness);
            serde_json::json!({
                "ok": true,
                "level": level_str,
                "session_count": session_count,
                "transaction_count": transaction_count,
                "sessions": sessions_json,
                "witness": witness,
                "witness_edges": edges,
                "wr_edges": wr_edges
            })
            .to_string()
        }
        Err(error) => serde_json::json!({
            "ok": false,
            "level": level_str,
            "session_count": session_count,
            "transaction_count": transaction_count,
            "sessions": sessions_json,
            "error": error
        })
        .to_string(),
    }
}
