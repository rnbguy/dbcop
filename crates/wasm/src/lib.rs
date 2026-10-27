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
use core::hash::Hash;
use std::thread_local;

use dbcop_core::consistency::saturation::committed_read::check_committed_read;
use dbcop_core::consistency::witness::Witness;
use dbcop_core::history::atomic::types::{AtomicTransactionHistory, TransactionId};
use dbcop_core::history::atomic::AtomicTransactionPO;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use dbcop_core::Consistency;
use wasm_bindgen::prelude::*;

mod response;
use response::{
    CheckResponse, Edge, MessageError, ParsedHistory, ResponseError, StepResponse, TokenSpan,
    TraceEdges, TraceEvent, TraceMetadata, TraceResponse, TraceTransaction,
};

#[derive(Debug, thiserror::Error)]
enum InputError {
    #[error("unknown consistency level")]
    UnknownLevel,
    #[error(transparent)]
    Json(#[from] serde_json::Error),
    #[error(transparent)]
    Parse(#[from] dbcop_parser::ParseError),
}

fn encode<T: serde::Serialize>(response: &T) -> String {
    // These DTOs contain only JSON-safe string keys, integer values and edge-list witnesses.
    serde_json::to_string(response).expect("WASM response must be JSON serializable")
}

fn required_level(level: &str) -> Result<Consistency, InputError> {
    parse_level(level).ok_or(InputError::UnknownLevel)
}

thread_local! {
    static STEP_SESSIONS: RefCell<BTreeMap<String, CausalStepSession>> = const { RefCell::new(BTreeMap::new()) };
    static NEXT_SESSION_ID: Cell<u64> = const { Cell::new(1) };
}

struct CausalStepSession {
    step: u64,
    sessions: Vec<Session<u64, u64>>,
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

fn parse_text_sessions_as_u64(
    text: &str,
) -> Result<Vec<Session<u64, u64>>, dbcop_parser::ParseError> {
    Ok(map_sessions_to_u64(dbcop_parser::parse_history(text)?))
}

fn witness_edges(witness: &Witness) -> Vec<Edge> {
    match witness {
        Witness::SaturationOrder(graph) => graph.to_edge_list(),
        Witness::CommitOrder(order) => order.windows(2).map(|w| (w[0], w[1])).collect(),
        Witness::SplitCommitOrder(order) => order.windows(2).map(|w| (w[0].0, w[1].0)).collect(),
    }
}

fn next_step_session_id() -> String {
    NEXT_SESSION_ID.with(|counter| {
        let id = counter.get();
        counter.set(id + 1);
        format!("{id}")
    })
}

fn step_init_impl(
    sessions: Vec<Session<u64, u64>>,
    level: &str,
    consistency: Consistency,
) -> StepResponse {
    let session_id = next_step_session_id();
    if matches!(consistency, Consistency::Causal) {
        let atomic_history = match AtomicTransactionHistory::try_from(sessions.as_slice()) {
            Ok(h) => h,
            Err(error) => {
                return StepResponse::Error {
                    error: ResponseError::Core(error),
                }
            }
        };
        let mut po = AtomicTransactionPO::from(atomic_history);
        po.vis_includes(&po.get_wr());
        po.vis_is_trans();
        let state = CausalStepSession {
            step: 0,
            sessions,
            po,
        };
        STEP_SESSIONS.with(|store| {
            store.borrow_mut().insert(session_id.clone(), state);
        });

        StepResponse::CausalInit {
            session_id,
            step: 0,
            level: "causal",
            steppable: true,
            done: false,
            new_edges: Vec::new(),
            total_edges: 0,
        }
    } else {
        StepResponse::CompleteInit {
            session_id,
            step: 0,
            level: level.to_string(),
            steppable: false,
            done: true,
            result: trace_impl(&sessions, level, consistency),
        }
    }
}

#[must_use]
#[wasm_bindgen]
pub fn check_consistency_step_init(history_json: &str, level: &str) -> String {
    let result = (|| {
        let consistency = required_level(level)?;
        let sessions = serde_json::from_str::<Vec<Session<u64, u64>>>(history_json)?;
        Ok::<_, InputError>(step_init_impl(sessions, level, consistency))
    })();
    encode(&result.unwrap_or_else(|error| StepResponse::Error {
        error: ResponseError::Message(error.to_string()),
    }))
}

#[must_use]
#[wasm_bindgen]
pub fn check_consistency_step_init_text(text: &str, level: &str) -> String {
    let result = (|| {
        let consistency = required_level(level)?;
        let sessions = parse_text_sessions_as_u64(text)?;
        Ok::<_, InputError>(step_init_impl(sessions, level, consistency))
    })();
    encode(&result.unwrap_or_else(|error| StepResponse::Error {
        error: ResponseError::Message(error.to_string()),
    }))
}

#[must_use]
#[wasm_bindgen]
pub fn check_consistency_step_next(session_id: &str) -> String {
    let response = STEP_SESSIONS.with(|store| {
        let mut store = store.borrow_mut();
        let Some(mut state) = store.remove(session_id) else {
            return StepResponse::Error {
                error: ResponseError::Message("unknown session_id".to_string()),
            };
        };
        state.step += 1;
        let mut new_edges = Vec::new();

        // Commit-order constraints are derived once from fixed causal visibility.
        if state.step == 1 {
            for ww_x in state.po.causal_ww().values() {
                for (src, dsts) in &ww_x.adj_map {
                    for dst in dsts {
                        if !state.po.visibility_relation.has_edge(src, dst) {
                            new_edges.push((*src, *dst));
                        }
                    }
                }
            }
        }

        let done = new_edges.is_empty();
        let result = if done {
            Some(trace_impl(&state.sessions, "causal", Consistency::Causal))
        } else {
            state
                .po
                .visibility_relation
                .incremental_closure(new_edges.iter().copied());
            None
        };
        let response = StepResponse::Advance {
            session_id: session_id.to_string(),
            step: state.step,
            phase: if done { "ww" } else { "co" },
            new_edges,
            total_edges: state
                .po
                .visibility_relation
                .adj_map
                .values()
                .map(|destinations| destinations.len() as u64)
                .sum(),
            done,
            result,
        };
        if !done {
            store.insert(session_id.to_string(), state);
        }
        response
    });
    encode(&response)
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
    let result = (|| {
        let consistency = required_level(level)?;
        let sessions = serde_json::from_str::<Vec<Session<u64, u64>>>(history_json)?;
        Ok::<_, InputError>(check_impl(&sessions, consistency))
    })();
    encode(&result.unwrap_or_else(|error| CheckResponse::Failure {
        ok: false,
        error: ResponseError::Message(error.to_string()),
    }))
}

fn check_impl<V>(sessions: &[Session<V, u64>], consistency: Consistency) -> CheckResponse<V>
where
    V: Eq + Hash + Clone + Ord,
{
    match dbcop_core::check(sessions, consistency) {
        Ok(witness) => CheckResponse::Success { ok: true, witness },
        Err(error) => CheckResponse::Failure {
            ok: false,
            error: ResponseError::Core(error),
        },
    }
}

fn build_sessions<V: ToString>(sessions: &[Session<V, u64>], level: &str) -> TraceMetadata {
    TraceMetadata {
        level: level.to_string(),
        session_count: sessions.len() as u64,
        transaction_count: sessions.iter().map(|session| session.len() as u64).sum(),
        sessions: sessions
            .iter()
            .enumerate()
            .map(|(sid, session)| {
                session
                    .iter()
                    .enumerate()
                    .map(|(th, txn)| {
                        let mut reads = BTreeMap::new();
                        let mut writes = BTreeMap::new();
                        let events = txn
                            .events
                            .iter()
                            .map(|event| match event {
                                Event::Read { variable, version } => {
                                    reads.insert(variable.to_string(), *version);
                                    TraceEvent::Read {
                                        variable: variable.to_string(),
                                        version: *version,
                                    }
                                }
                                Event::Write { variable, version } => {
                                    writes.insert(variable.to_string(), *version);
                                    TraceEvent::Write {
                                        variable: variable.to_string(),
                                        version: *version,
                                    }
                                }
                            })
                            .collect();
                        TraceTransaction {
                            id: TransactionId {
                                session_id: sid as u64 + 1,
                                session_height: th as u64,
                            },
                            reads,
                            writes,
                            events,
                            committed: txn.committed,
                        }
                    })
                    .collect()
            })
            .collect(),
    }
}

fn trace_impl<V>(
    sessions: &[Session<V, u64>],
    level: &str,
    consistency: Consistency,
) -> TraceResponse<V>
where
    V: Eq + Hash + Clone + Ord + ToString + core::fmt::Debug,
{
    let metadata = build_sessions(sessions, level);
    let result = check_impl(sessions, consistency);
    let proof = match &result {
        CheckResponse::Success { witness, .. } => Some(TraceEdges {
            witness_edges: witness_edges(witness),
            wr_edges: check_committed_read(sessions)
                .expect("successful consistency check must be committed-read")
                .to_edge_list(),
        }),
        CheckResponse::Failure { .. } => None,
    };
    TraceResponse::Checked {
        result,
        metadata,
        proof,
    }
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
    let result = (|| {
        let consistency = required_level(level)?;
        let sessions = serde_json::from_str::<Vec<Session<u64, u64>>>(history_json)?;
        Ok::<_, InputError>(trace_impl(&sessions, level, consistency))
    })();
    encode(&result.unwrap_or_else(|error| TraceResponse::Invalid {
        ok: false,
        error: error.to_string(),
    }))
}

/// Parse a compact history DSL string and return JSON.
///
/// Returns a JSON string: `Vec<Session<String, u64>>` on success,
/// or `{"error": "message"}` on failure.
#[must_use]
#[wasm_bindgen]
pub fn parse_history_text(text: &str) -> String {
    let response = match dbcop_parser::parse_history(text) {
        Ok(sessions) => ParsedHistory::Sessions(sessions),
        Err(error) => ParsedHistory::Error(MessageError {
            error: error.to_string(),
        }),
    };
    encode(&response)
}

/// Check consistency from a compact history DSL string and return a rich trace.
///
/// Same output format as `check_consistency_trace` but accepts the text DSL
/// instead of JSON, and preserves string variable names in the output.
#[must_use]
#[wasm_bindgen]
pub fn check_consistency_trace_text(text: &str, level: &str) -> String {
    let result = (|| {
        let consistency = required_level(level)?;
        let sessions = dbcop_parser::parse_history(text)?;
        Ok::<_, InputError>(trace_impl(&sessions, level, consistency))
    })();
    encode(&result.unwrap_or_else(|error| TraceResponse::Invalid {
        ok: false,
        error: error.to_string(),
    }))
}

/// Tokenize a compact history DSL string for syntax highlighting.
///
/// Returns a JSON array of `{"kind": "...", "start": N, "end": N, "text": "..."}`.
#[must_use]
#[wasm_bindgen]
pub fn tokenize_history(text: &str) -> String {
    let tokens = dbcop_parser::tokenize(text);
    let result: Vec<TokenSpan<'_>> = tokens
        .iter()
        .map(|token| TokenSpan {
            kind: &token.kind,
            start: token.span.start,
            end: token.span.end,
            text: token.text(text),
        })
        .collect();
    encode(&result)
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
    match parse_text_sessions_as_u64(text) {
        Ok(sessions) => serde_json::to_string_pretty(&sessions).unwrap_or_else(|error| {
            encode(&MessageError {
                error: error.to_string(),
            })
        }),
        Err(error) => encode(&MessageError {
            error: error.to_string(),
        }),
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
