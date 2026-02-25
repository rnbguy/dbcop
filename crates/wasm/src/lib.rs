//! wasm library for dbcop
//! compiled binary is uploaded as github action artifact

#![cfg_attr(not(test), no_std)]
#![cfg_attr(not(test), no_main)]

extern crate alloc;
extern crate std;

use alloc::collections::btree_map::Entry;
use alloc::collections::BTreeMap;
use alloc::format;
use alloc::string::{String, ToString};
use alloc::vec::Vec;
use core::cell::{Cell, RefCell};
use std::thread_local;

use dbcop_core::consistency::saturation::committed_read::check_committed_read;
use dbcop_core::consistency::witness::Witness;
use dbcop_core::history::atomic::types::{AtomicTransactionHistory, TransactionId};
use dbcop_core::history::atomic::AtomicTransactionPO;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::Consistency;
use wasm_bindgen::prelude::*;

thread_local! {
    static STEP_SESSIONS: RefCell<BTreeMap<String, CausalStepSession>> = const { RefCell::new(BTreeMap::new()) };
    static NEXT_SESSION_ID: Cell<u64> = const { Cell::new(1) };
}

struct CausalStepSession {
    step: u64,
    history_json: String,
    level: String,
    po: AtomicTransactionPO<u64>,
}

fn parse_level(level: &str) -> Option<Consistency> {
    match level {
        "committed-read" => Some(Consistency::CommittedRead),
        "repeatable-read" => Some(Consistency::RepeatableRead),
        "atomic-read" => Some(Consistency::AtomicRead),
        "causal" => Some(Consistency::Causal),
        "prefix" => Some(Consistency::Prefix),
        "snapshot-isolation" => Some(Consistency::SnapshotIsolation),
        "serializable" => Some(Consistency::Serializable),
        _ => None,
    }
}

fn map_sessions_to_u64(sessions: Vec<Session<String, u64>>) -> Vec<Session<u64, u64>> {
    let mut var_map: BTreeMap<String, u64> = BTreeMap::new();
    let mut next_id: u64 = 0;

    for session in &sessions {
        for txn in session {
            for event in &txn.events {
                let var_name = match event {
                    Event::Read { variable, .. } | Event::Write { variable, .. } => variable,
                };
                if let Entry::Vacant(slot) = var_map.entry(var_name.clone()) {
                    slot.insert(next_id);
                    next_id += 1;
                }
            }
        }
    }

    sessions
        .into_iter()
        .map(|session| {
            session
                .into_iter()
                .map(|txn| {
                    let events: Vec<Event<u64, u64>> = txn
                        .events
                        .into_iter()
                        .map(|event| match event {
                            Event::Read { variable, version } => Event::Read {
                                variable: var_map[&variable],
                                version,
                            },
                            Event::Write { variable, version } => Event::Write {
                                variable: var_map[&variable],
                                version,
                            },
                        })
                        .collect();
                    if txn.committed {
                        Transaction::committed(events)
                    } else {
                        Transaction::uncommitted(events)
                    }
                })
                .collect()
        })
        .collect()
}

fn parse_text_sessions_as_u64(text: &str) -> Result<Vec<Session<u64, u64>>, String> {
    let sessions = dbcop_parser::parse_history(text).map_err(|error| error.to_string())?;
    Ok(map_sessions_to_u64(sessions))
}

fn tid_to_json(tid: &TransactionId) -> serde_json::Value {
    serde_json::json!({
        "session_id": tid.session_id,
        "session_height": tid.session_height
    })
}

/// Convert a [`Witness`] into a JSON-safe [`serde_json::Value`].
///
/// `SaturationOrder` contains `DiGraph<TransactionId>` whose `HashMap` keys
/// are not strings, so `serde_json::json!` panics when serializing directly.
/// This helper converts the graph to an edge-list representation instead.
fn witness_to_json(witness: &Witness) -> serde_json::Value {
    match witness {
        Witness::SaturationOrder(graph) => {
            let edges: Vec<serde_json::Value> = graph
                .to_edge_list()
                .into_iter()
                .map(|(from, to)| serde_json::json!([tid_to_json(&from), tid_to_json(&to)]))
                .collect();
            serde_json::json!({ "SaturationOrder": edges })
        }
        Witness::CommitOrder(order) => {
            let tids: Vec<serde_json::Value> = order.iter().map(tid_to_json).collect();
            serde_json::json!({ "CommitOrder": tids })
        }
        Witness::SplitCommitOrder(order) => {
            let entries: Vec<serde_json::Value> = order
                .iter()
                .map(|(tid, is_write)| serde_json::json!([tid_to_json(tid), is_write]))
                .collect();
            serde_json::json!({ "SplitCommitOrder": entries })
        }
    }
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

fn next_step_session_id() -> String {
    NEXT_SESSION_ID.with(|counter| {
        let id = counter.get();
        counter.set(id + 1);
        format!("{id}")
    })
}

fn edge_to_json(from: TransactionId, to: TransactionId) -> serde_json::Value {
    serde_json::json!([tid_to_json(&from), tid_to_json(&to)])
}

fn parse_embedded_result(result_json: &str) -> serde_json::Value {
    serde_json::from_str::<serde_json::Value>(result_json)
        .unwrap_or_else(|e| serde_json::json!({"ok": false, "error": e.to_string()}))
}

fn step_init_impl(
    sessions: &[Session<u64, u64>],
    history_json: String,
    level: &str,
    consistency: Consistency,
) -> String {
    let session_id = next_step_session_id();
    if matches!(consistency, Consistency::Causal) {
        let atomic_history = match AtomicTransactionHistory::try_from(sessions) {
            Ok(h) => h,
            Err(e) => {
                return serde_json::json!({"error": e}).to_string();
            }
        };
        let mut po = AtomicTransactionPO::from(atomic_history);
        po.vis_includes(&po.get_wr());
        po.vis_is_trans();
        let state = CausalStepSession {
            step: 0,
            history_json,
            level: level.to_string(),
            po,
        };
        STEP_SESSIONS.with(|store| {
            store.borrow_mut().insert(session_id.clone(), state);
        });

        serde_json::json!({
            "session_id": session_id,
            "step": 0,
            "level": "causal",
            "steppable": true,
            "done": false,
            "new_edges": [],
            "total_edges": 0
        })
        .to_string()
    } else {
        let trace = check_consistency_trace(&history_json, level);
        let result = parse_embedded_result(&trace);
        serde_json::json!({
            "session_id": session_id,
            "step": 0,
            "level": level,
            "steppable": false,
            "done": true,
            "result": result
        })
        .to_string()
    }
}

#[must_use]
#[wasm_bindgen]
pub fn check_consistency_step_init(history_json: &str, level: &str) -> String {
    let Some(consistency) = parse_level(level) else {
        return serde_json::json!({"error": "unknown consistency level"}).to_string();
    };
    let sessions = match serde_json::from_str::<Vec<Session<u64, u64>>>(history_json) {
        Ok(s) => s,
        Err(e) => {
            return serde_json::json!({"error": e.to_string()}).to_string();
        }
    };

    step_init_impl(&sessions, history_json.to_string(), level, consistency)
}

#[must_use]
#[wasm_bindgen]
pub fn check_consistency_step_init_text(text: &str, level: &str) -> String {
    let Some(consistency) = parse_level(level) else {
        return serde_json::json!({"error": "unknown consistency level"}).to_string();
    };

    let mapped_sessions = match parse_text_sessions_as_u64(text) {
        Ok(s) => s,
        Err(e) => {
            return serde_json::json!({"error": e}).to_string();
        }
    };

    let history_json = match serde_json::to_string(&mapped_sessions) {
        Ok(s) => s,
        Err(e) => {
            return serde_json::json!({"error": e.to_string()}).to_string();
        }
    };

    step_init_impl(&mapped_sessions, history_json, level, consistency)
}

#[must_use]
#[wasm_bindgen]
pub fn check_consistency_step_next(session_id: &str) -> String {
    STEP_SESSIONS.with(|store| {
        let mut store = store.borrow_mut();
        let Some(mut state) = store.remove(session_id) else {
            return serde_json::json!({"error": "unknown session_id"}).to_string();
        };

        state.step += 1;

        let ww_rel = state.po.causal_ww();
        let mut new_edges = Vec::new();

        for ww_x in ww_rel.values() {
            for (src, dsts) in &ww_x.adj_map {
                for dst in dsts {
                    if !state.po.visibility_relation.has_edge(src, dst) {
                        new_edges.push((*src, *dst));
                    }
                }
            }
        }

        if new_edges.is_empty() {
            let total_edges = state.po.visibility_relation.to_edge_list().len() as u64;
            let trace = check_consistency_trace(&state.history_json, &state.level);
            let result = parse_embedded_result(&trace);

            serde_json::json!({
                "session_id": session_id,
                "step": state.step,
                "phase": "ww",
                "new_edges": [],
                "total_edges": total_edges,
                "done": true,
                "result": result
            })
            .to_string()
        } else {
            state
                .po
                .visibility_relation
                .incremental_closure(new_edges.clone());
            let total_edges = state.po.visibility_relation.to_edge_list().len() as u64;
            let step = state.step;
            let new_edges_json: Vec<serde_json::Value> = new_edges
                .into_iter()
                .map(|(from, to)| edge_to_json(from, to))
                .collect();

            store.insert(session_id.to_string(), state);

            serde_json::json!({
                "session_id": session_id,
                "step": step,
                "phase": "vis",
                "new_edges": new_edges_json,
                "total_edges": total_edges,
                "done": false
            })
            .to_string()
        }
    })
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
        Ok(witness) => {
            serde_json::json!({"ok": true, "witness": witness_to_json(&witness)}).to_string()
        }
        Err(error) => serde_json::json!({"ok": false, "error": error}).to_string(),
    }
}

fn build_sessions_json<V, W>(sessions: &[Session<V, W>]) -> (serde_json::Value, u64, u64)
where
    V: ToString,
    W: serde::Serialize + ToString,
{
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
                    let events: Vec<serde_json::Value> = txn
                        .events
                        .iter()
                        .map(|event| match event {
                            Event::Read { variable, version } => {
                                reads.insert(
                                    variable.to_string(),
                                    version
                                        .as_ref()
                                        .map_or(serde_json::Value::Null, |v| serde_json::json!(v)),
                                );
                                serde_json::json!({
                                    "type": "R",
                                    "variable": variable.to_string(),
                                    "version": version.as_ref().map_or(
                                        serde_json::Value::Null,
                                        |v| serde_json::json!(v),
                                    )
                                })
                            }
                            Event::Write { variable, version } => {
                                writes.insert(variable.to_string(), serde_json::json!(version));
                                serde_json::json!({
                                    "type": "W",
                                    "variable": variable.to_string(),
                                    "version": serde_json::json!(version)
                                })
                            }
                        })
                        .collect();
                    serde_json::json!({
                        "id": {
                            "session_id": (sid as u64) + 1,
                            "session_height": th as u64
                        },
                        "reads": reads,
                        "writes": writes,
                        "events": events,
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
                "witness": witness_to_json(&witness),
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

/// Parse a compact history DSL string and return JSON.
///
/// Returns a JSON string: `Vec<Session<String, u64>>` on success,
/// or `{"error": "message"}` on failure.
#[must_use]
#[wasm_bindgen]
pub fn parse_history_text(text: &str) -> String {
    match dbcop_parser::parse_history(text) {
        Ok(sessions) => serde_json::to_string(&sessions)
            .unwrap_or_else(|e| serde_json::json!({"error": e.to_string()}).to_string()),
        Err(e) => serde_json::json!({"error": e.to_string()}).to_string(),
    }
}

/// Check consistency from a compact history DSL string and return a rich trace.
///
/// Same output format as `check_consistency_trace` but accepts the text DSL
/// instead of JSON, and preserves string variable names in the output.
#[must_use]
#[wasm_bindgen]
pub fn check_consistency_trace_text(text: &str, level: &str) -> String {
    let Some(consistency) = parse_level(level) else {
        return serde_json::json!({"ok": false, "error": "unknown consistency level"}).to_string();
    };

    let sessions = match dbcop_parser::parse_history(text) {
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
                "witness": witness_to_json(&witness),
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

/// Tokenize a compact history DSL string for syntax highlighting.
///
/// Returns a JSON array of `{"kind": "...", "start": N, "end": N, "text": "..."}`.
#[must_use]
#[wasm_bindgen]
pub fn tokenize_history(text: &str) -> String {
    let tokens = dbcop_parser::tokenize(text);
    let result: Vec<serde_json::Value> = tokens
        .iter()
        .map(|t| {
            serde_json::json!({
                "kind": format!("{:?}", t.kind),
                "start": t.span.start,
                "end": t.span.end,
                "text": t.text(text),
            })
        })
        .collect();
    serde_json::to_string(&result).unwrap_or_else(|_| "[]".to_string())
}

/// Convert a compact history DSL string to pretty-printed JSON sessions.
///
/// Parses the text DSL, maps variable names to sequential u64 IDs
/// (first-appearance order), and returns `serde_json::to_string_pretty`
/// of the resulting `Vec<Session<u64, u64>>`.
///
/// Returns a JSON string on success, or `{"error": "..."}` on failure.
#[must_use]
#[wasm_bindgen]
pub fn text_to_json_sessions(text: &str) -> String {
    let mapped_sessions = match parse_text_sessions_as_u64(text) {
        Ok(s) => s,
        Err(e) => {
            return serde_json::json!({"error": e}).to_string();
        }
    };

    match serde_json::to_string_pretty(&mapped_sessions) {
        Ok(s) => s,
        Err(e) => serde_json::json!({"error": e.to_string()}).to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_level_supports_repeatable_read() {
        assert!(matches!(
            parse_level("repeatable-read"),
            Some(Consistency::RepeatableRead)
        ));
    }

    #[test]
    fn test_version_zero_causal_json() {
        // Verify the core check passes for version-zero causal histories.
        let input = r#"[[{"events":[{"Read":{"variable":0,"version":0}},{"Write":{"variable":0,"version":1}}],"committed":true}],[{"events":[{"Read":{"variable":0,"version":0}},{"Write":{"variable":0,"version":2}}],"committed":true}]]"#;
        let sessions: Vec<Session<u64, u64>> = serde_json::from_str(input).unwrap();
        let result = dbcop_core::check(&sessions, Consistency::Causal);
        assert!(
            result.is_ok(),
            "version-zero causal should pass: {result:?}"
        );
    }

    #[test]
    fn test_version_zero_causal_text() {
        let result = check_consistency_trace_text("[x==0 x:=1]\n---\n[x==0 x:=2]\n", "causal");
        let parsed: serde_json::Value = serde_json::from_str(&result).unwrap();
        assert_eq!(
            parsed["ok"], true,
            "version-zero text causal should pass: {result}"
        );
    }

    #[test]
    fn text_to_json_sessions_uses_shared_mapping_logic() {
        let input = "[beta:=1]\n[alpha==? alpha:=2]\n---\n[beta==1 alpha==2]\n";
        let direct = parse_text_sessions_as_u64(input).expect("text parse should succeed");
        let json = text_to_json_sessions(input);
        let from_json: Vec<Session<u64, u64>> =
            serde_json::from_str(&json).expect("text_to_json_sessions should return session JSON");
        assert_eq!(
            serde_json::to_value(&direct).unwrap(),
            serde_json::to_value(&from_json).unwrap()
        );
    }
}
