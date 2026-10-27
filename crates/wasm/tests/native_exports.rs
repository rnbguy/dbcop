#[path = "../src/lib.rs"]
mod wasm;

use std::collections::{BTreeMap, BTreeSet};

use dbcop_core::consistency::error::Error as CoreError;
use dbcop_core::history::atomic::types::TransactionId;
use dbcop_core::history::raw::error::Error as HistoryError;
use dbcop_core::history::raw::types::{Event, Session, Transaction};
use serde::de::DeserializeOwned;
use serde::Deserialize;

type Edge = (TransactionId, TransactionId);

#[derive(Debug, Deserialize)]
enum Proof {
    #[serde(rename = "SaturationOrder")]
    Saturation(Vec<Edge>),
    #[serde(rename = "CommitOrder")]
    Commit(Vec<TransactionId>),
    #[serde(rename = "SplitCommitOrder")]
    Split(Vec<(TransactionId, bool)>),
}

#[derive(Debug, Deserialize)]
#[serde(untagged)]
enum ResponseError {
    Core(CoreError<u64, u64>),
    Message(String),
}

#[derive(Debug, Deserialize, PartialEq, Eq)]
#[serde(tag = "type")]
enum TraceEvent {
    R {
        variable: String,
        version: Option<u64>,
    },
    W {
        variable: String,
        version: u64,
    },
}

#[derive(Debug, Deserialize)]
struct TraceTransaction {
    id: TransactionId,
    reads: BTreeMap<String, Option<u64>>,
    writes: BTreeMap<String, u64>,
    events: Vec<TraceEvent>,
    committed: bool,
}

#[derive(Debug, Deserialize)]
struct Check {
    ok: bool,
    error: Option<ResponseError>,
    witness: Option<Proof>,
    level: Option<String>,
    session_count: Option<u64>,
    transaction_count: Option<u64>,
    sessions: Option<Vec<Vec<TraceTransaction>>>,
    witness_edges: Option<Vec<Edge>>,
    wr_edges: Option<Vec<Edge>>,
}

#[derive(Debug, Deserialize)]
struct StepState {
    error: Option<ResponseError>,
    session_id: Option<String>,
    step: Option<u64>,
    level: Option<String>,
    steppable: Option<bool>,
    done: Option<bool>,
    phase: Option<String>,
    new_edges: Option<Vec<Edge>>,
    total_edges: Option<u64>,
    result: Option<Check>,
}

#[derive(Debug, Deserialize)]
struct MessageError {
    error: String,
}

#[derive(Debug, Deserialize)]
struct Token {
    kind: String,
    start: usize,
    end: usize,
    text: String,
}

fn decode<T: DeserializeOwned>(output: impl AsRef<str>) -> T {
    serde_json::from_str(output.as_ref()).expect("export must return typed JSON")
}

const fn tid(session: u64, height: u64) -> TransactionId {
    TransactionId {
        session_id: session,
        session_height: height,
    }
}

const fn edge(from: u64, to: u64) -> Edge {
    (tid(from, 0), tid(to, 0))
}

fn edge_set(edges: &[Edge]) -> BTreeSet<Edge> {
    edges.iter().copied().collect()
}

fn assert_message(error: Option<&ResponseError>) {
    let Some(ResponseError::Message(message)) = error else {
        panic!("expected a message error, got {error:?}");
    };
    assert_ne!(message, "");
}

fn assert_unknown_session(id: &str) {
    let result: StepState = decode(wasm::check_consistency_step_next(id));
    assert_message(result.error.as_ref());
    assert!(result.session_id.is_none());
}

const LEVELS: [&str; 7] = [
    "committed-read",
    "repeatable-read",
    "atomic-read",
    "causal",
    "prefix",
    "snapshot-isolation",
    "serializable",
];

const CHAIN: &str = "[x:=1]\n---\n[x==1 y:=2]\n---\n[y==2]\n";

#[test]
fn json_and_text_proofs_preserve_dependencies_for_every_witness_form() {
    let history = wasm::text_to_json_sessions(CHAIN);
    for level in LEVELS {
        let checked: Check = decode(wasm::check_consistency(&history, level));
        let trace: Check = decode(wasm::check_consistency_trace(&history, level));
        let text: Check = decode(wasm::check_consistency_trace_text(CHAIN, level));
        for result in [&checked, &trace, &text] {
            assert!(result.ok, "{level}: {result:?}");
        }
        for result in [&trace, &text] {
            assert_eq!(result.session_count, Some(3));
            assert_eq!(result.transaction_count, Some(3));
            assert_eq!(
                edge_set(result.wr_edges.as_ref().unwrap()),
                BTreeSet::from([edge(0, 1), edge(0, 2), edge(0, 3), edge(1, 2), edge(2, 3)])
            );
            let expected_edges: Vec<Edge> = match (level, result.witness.as_ref().unwrap()) {
                ("prefix" | "serializable", Proof::Commit(order)) => {
                    let transactions: Vec<_> = order
                        .iter()
                        .filter(|id| id.session_id != 0)
                        .copied()
                        .collect();
                    assert_eq!(transactions, vec![tid(1, 0), tid(2, 0), tid(3, 0)]);
                    order.windows(2).map(|pair| (pair[0], pair[1])).collect()
                }
                ("snapshot-isolation", Proof::Split(order)) => {
                    for session in 1..=3 {
                        let read = (tid(session, 0), false);
                        let write = (tid(session, 0), true);
                        assert_eq!(order.iter().filter(|entry| **entry == read).count(), 1);
                        assert_eq!(order.iter().filter(|entry| **entry == write).count(), 1);
                        let read_position = order.iter().position(|entry| *entry == read).unwrap();
                        let write_position =
                            order.iter().position(|entry| *entry == write).unwrap();
                        assert!(read_position < write_position);
                        if session < 3 {
                            let next_read = (tid(session + 1, 0), false);
                            assert!(
                                write_position
                                    < order.iter().position(|entry| *entry == next_read).unwrap()
                            );
                        }
                    }
                    order
                        .windows(2)
                        .map(|pair| (pair[0].0, pair[1].0))
                        .collect()
                }
                (
                    "committed-read" | "repeatable-read" | "atomic-read" | "causal",
                    Proof::Saturation(edges),
                ) => {
                    assert!(edges.contains(&edge(1, 2)));
                    assert!(edges.contains(&edge(2, 3)));
                    edges.clone()
                }
                _ => panic!("unexpected witness form for {level}: {result:?}"),
            };
            assert_eq!(
                edge_set(result.witness_edges.as_ref().unwrap()),
                edge_set(&expected_edges)
            );
        }
        assert_eq!(
            std::mem::discriminant(checked.witness.as_ref().unwrap()),
            std::mem::discriminant(trace.witness.as_ref().unwrap())
        );
    }
}

#[test]
fn trace_preserves_local_event_order_initial_reads_and_uncommitted_metadata() {
    let text = "[beta==? beta:=1 beta==1 beta:=2 beta==2]\n[alpha:=9]!\n";
    let history = vec![vec![
        Transaction::committed(vec![
            Event::Read {
                variable: 0,
                version: None,
            },
            Event::Write {
                variable: 0,
                version: 1,
            },
            Event::Read {
                variable: 0,
                version: Some(1),
            },
            Event::Write {
                variable: 0,
                version: 2,
            },
            Event::Read {
                variable: 0,
                version: Some(2),
            },
        ]),
        Transaction::uncommitted(vec![Event::Write {
            variable: 1,
            version: 9,
        }]),
    ]];
    let trace: Check = decode(wasm::check_consistency_trace(
        &serde_json::to_string(&history).unwrap(),
        "causal",
    ));
    assert!(trace.ok, "{trace:?}");
    assert_eq!(trace.transaction_count, Some(2));
    let sessions = trace.sessions.as_ref().unwrap();
    assert!(!sessions[0][1].committed);
    assert_eq!(
        sessions[0][1].writes,
        BTreeMap::from([("1".to_string(), 9)])
    );
    let transaction = &sessions[0][0];
    assert_eq!(transaction.id, tid(1, 0));
    assert_eq!(
        transaction.reads,
        BTreeMap::from([("0".to_string(), Some(2))])
    );
    assert_eq!(transaction.writes, BTreeMap::from([("0".to_string(), 2)]));
    assert_eq!(
        transaction.events,
        vec![
            TraceEvent::R {
                variable: "0".to_string(),
                version: None
            },
            TraceEvent::W {
                variable: "0".to_string(),
                version: 1
            },
            TraceEvent::R {
                variable: "0".to_string(),
                version: Some(1)
            },
            TraceEvent::W {
                variable: "0".to_string(),
                version: 2
            },
            TraceEvent::R {
                variable: "0".to_string(),
                version: Some(2)
            },
        ]
    );
    let named: Check = decode(wasm::check_consistency_trace_text(text, "causal"));
    assert!(named.ok, "{named:?}");
    let named_sessions = named.sessions.as_ref().unwrap();
    assert_eq!(
        named_sessions[0][0].reads,
        BTreeMap::from([("beta".to_string(), Some(2))])
    );
    assert_eq!(
        named_sessions[0][0].writes,
        BTreeMap::from([("beta".to_string(), 2)])
    );
    assert_eq!(
        named_sessions[0][0].events[0],
        TraceEvent::R {
            variable: "beta".to_string(),
            version: None
        }
    );
    assert!(!named_sessions[0][1].committed);
    let mapped: Vec<Session<u64, u64>> = decode(wasm::text_to_json_sessions(text));
    for (actual, expected) in mapped[0].iter().zip(&history[0]) {
        assert_eq!(actual.events, expected.events);
        assert_eq!(actual.committed, expected.committed);
    }
    assert_eq!(mapped[0].len(), history[0].len());
    assert!(trace.wr_edges.as_ref().unwrap().contains(&edge(0, 1)));
}

#[test]
fn text_mapping_preserves_shared_variables_and_committed_state() {
    let text = "[beta:=1]\n[alpha==? alpha:=2]\n---\n[beta==1 alpha==2]\n";
    let parsed: Vec<Session<String, u64>> = decode(wasm::parse_history_text(text));
    assert_eq!(
        parsed[0][0].events[0],
        Event::Write {
            variable: "beta".to_string(),
            version: 1
        }
    );
    assert_eq!(
        parsed[1][0].events[1],
        Event::Read {
            variable: "alpha".to_string(),
            version: Some(2)
        }
    );
    let mapped_json = wasm::text_to_json_sessions(text);
    let mapped: Vec<Session<u64, u64>> = decode(&mapped_json);
    assert_eq!(
        mapped[0][0].events[0],
        Event::Write {
            variable: 0,
            version: 1
        }
    );
    assert_eq!(
        mapped[0][1].events[0],
        Event::Read {
            variable: 1,
            version: None
        }
    );
    assert_eq!(
        mapped[1][0].events,
        vec![
            Event::Read {
                variable: 0,
                version: Some(1)
            },
            Event::Read {
                variable: 1,
                version: Some(2)
            },
        ]
    );
    assert!(mapped.iter().flatten().all(|txn| txn.committed));
    let checked: Check = decode(wasm::check_consistency(&mapped_json, "serializable"));
    assert!(checked.ok);
}

#[test]
fn violations_keep_history_metadata_but_never_publish_a_proof() {
    let lost_update = "[x==? x:=1]\n---\n[x==? x:=2]\n";
    let history = wasm::text_to_json_sessions(lost_update);
    for level in ["snapshot-isolation", "serializable"] {
        for result in [
            decode::<Check>(wasm::check_consistency(&history, level)),
            decode(wasm::check_consistency_trace(&history, level)),
            decode(wasm::check_consistency_trace_text(lost_update, level)),
        ] {
            assert!(!result.ok, "{result:?}");
            assert!(matches!(result.error, Some(ResponseError::Core(_))));
            assert!(result.witness.is_none());
            assert!(result.witness_edges.is_none());
            if let Some(sessions) = result.sessions {
                assert_eq!(result.session_count, Some(2));
                assert_eq!(result.transaction_count, Some(2));
                assert!(matches!(
                    sessions[1][0].events[1],
                    TraceEvent::W { version: 2, .. }
                ));
            }
        }
    }
    assert!(decode::<Check>(wasm::check_consistency(&history, "prefix")).ok);
    let missing = wasm::text_to_json_sessions("[x==99]\n");
    let trace: Check = decode(wasm::check_consistency_trace(&missing, "causal"));
    assert!(!trace.ok);
    assert!(matches!(
        trace.error,
        Some(ResponseError::Core(CoreError::NonAtomic(
            HistoryError::IncompleteHistory { .. }
        )))
    ));
    assert_eq!(
        trace.sessions.unwrap()[0][0].events[0],
        TraceEvent::R {
            variable: "0".to_string(),
            version: Some(99)
        }
    );
    for step in [
        decode::<StepState>(wasm::check_consistency_step_init(&missing, "causal")),
        decode(wasm::check_consistency_step_init_text(
            "[x==99]\n",
            "causal",
        )),
    ] {
        assert!(matches!(
            step.error,
            Some(ResponseError::Core(CoreError::NonAtomic(
                HistoryError::IncompleteHistory { .. }
            )))
        ));
        assert!(step.session_id.is_none());
    }
}

#[test]
fn invalid_requests_cannot_create_step_sessions_or_successful_results() {
    for level in ["causal", "unknown"] {
        for result in [
            decode::<Check>(wasm::check_consistency("{", level)),
            decode(wasm::check_consistency_trace("{", level)),
            decode(wasm::check_consistency_trace_text("[", level)),
        ] {
            assert!(!result.ok);
            assert_message(result.error.as_ref());
            assert!(result.sessions.is_none());
            assert!(result.witness.is_none());
        }
        for result in [
            decode::<StepState>(wasm::check_consistency_step_init("{", level)),
            decode(wasm::check_consistency_step_init_text("[", level)),
        ] {
            assert_message(result.error.as_ref());
            assert!(result.session_id.is_none());
        }
    }
    for result in [
        decode::<MessageError>(wasm::parse_history_text("[")),
        decode(wasm::text_to_json_sessions("[")),
    ] {
        assert_ne!(result.error, "");
    }
    assert_unknown_session("missing-handle");
}

#[test]
fn causal_steps_add_inferred_order_then_finish_and_dispose_independently() {
    // Both writers are visible to the reader, so x=1 must precede x=2.
    let text = "[x:=1 marker:=1]\n---\n[x:=2]\n---\n[marker==1 x==2]\n";
    let history = wasm::text_to_json_sessions(text);
    let first: StepState = decode(wasm::check_consistency_step_init(&history, "causal"));
    let second: StepState = decode(wasm::check_consistency_step_init_text(text, "causal"));
    assert_ne!(first.session_id, second.session_id);
    for state in [&first, &second] {
        assert_eq!(state.step, Some(0));
        assert_eq!(state.level.as_deref(), Some("causal"));
        assert_eq!(state.steppable, Some(true));
        assert_eq!(state.done, Some(false));
        assert_eq!(state.new_edges.as_deref(), Some([].as_slice()));
    }
    let first_id = first.session_id.as_deref().unwrap();
    let second_id = second.session_id.as_deref().unwrap();
    let advanced: StepState = decode(wasm::check_consistency_step_next(first_id));
    assert_eq!(advanced.step, Some(1));
    assert_eq!(advanced.phase.as_deref(), Some("co"));
    assert_eq!(advanced.done, Some(false));
    assert_eq!(
        edge_set(advanced.new_edges.as_ref().unwrap()),
        BTreeSet::from([edge(1, 2)])
    );
    assert_eq!(advanced.total_edges, Some(6));
    assert_unknown_session("invalid");
    let finished: StepState = decode(wasm::check_consistency_step_next(first_id));
    assert_eq!(finished.step, Some(2));
    assert_eq!(finished.phase.as_deref(), Some("ww"));
    assert_eq!(finished.new_edges.as_deref(), Some([].as_slice()));
    assert_eq!(finished.total_edges, Some(6));
    assert_eq!(finished.done, Some(true));
    let result = finished.result.as_ref().unwrap();
    assert!(result.ok);
    assert!(result.witness_edges.as_ref().unwrap().contains(&edge(1, 2)));
    assert_unknown_session(first_id);
    let other: StepState = decode(wasm::check_consistency_step_next(second_id));
    assert_eq!(other.step, Some(1));
    assert_eq!(
        edge_set(other.new_edges.as_ref().unwrap()),
        edge_set(advanced.new_edges.as_ref().unwrap())
    );
    let other_done: StepState = decode(wasm::check_consistency_step_next(second_id));
    assert_eq!(other_done.done, Some(true));
    assert!(other_done.result.unwrap().ok);
    assert_unknown_session(second_id);
}

#[test]
fn cyclic_causal_state_and_nonsteppable_levels_return_final_verdicts() {
    let cycle = "[x:=1 y==2]\n---\n[y:=2 x==1]\n";
    let init: StepState = decode(wasm::check_consistency_step_init_text(cycle, "causal"));
    assert!(matches!(
        init.error,
        Some(ResponseError::Core(CoreError::Cycle { .. }))
    ));
    assert!(init.session_id.is_none());
    for level in LEVELS.into_iter().filter(|level| *level != "causal") {
        let direct: StepState = decode(wasm::check_consistency_step_init_text(CHAIN, level));
        assert_eq!(direct.done, Some(true));
        assert_eq!(direct.steppable, Some(false));
        let result = direct.result.as_ref().unwrap();
        assert!(result.ok);
        assert_eq!(result.level.as_deref(), Some(level));
        assert_eq!(result.transaction_count, Some(3));
        assert_unknown_session(direct.session_id.as_deref().unwrap());
    }
    let failed: StepState = decode(wasm::check_consistency_step_init_text(
        "[x==? x:=1]\n---\n[x==? x:=2]\n",
        "snapshot-isolation",
    ));
    assert_eq!(failed.done, Some(true));
    assert!(!failed.result.unwrap().ok);
}

#[test]
fn tokenization_retains_exact_source_spans_and_syntax_roles() {
    let source = "[account:=12 account==?]";
    let tokens: Vec<Token> = decode(wasm::tokenize_history(source));
    assert_eq!(
        tokens
            .iter()
            .map(|token| token.text.as_str())
            .collect::<String>(),
        source
    );
    let mut end = 0;
    for token in &tokens {
        assert_eq!(token.start, end);
        assert_eq!(token.text, &source[token.start..token.end]);
        end = token.end;
    }
    assert_eq!(end, source.len());
    let writes = tokens.iter().find(|token| token.text == ":=").unwrap();
    let reads = tokens.iter().find(|token| token.text == "==").unwrap();
    assert_ne!(writes.kind, reads.kind);
    assert_eq!(decode::<Vec<Token>>(wasm::tokenize_history("")).len(), 0);
}

#[test]
fn inferred_commit_order_never_becomes_a_new_causal_read_premise() {
    let history = "[a:=46 b==?]\n[c==56 a==49]\n---\n[b:=48 a==? a:=49]\n[c:=56]\n";
    let init: StepState = decode(wasm::check_consistency_step_init_text(history, "causal"));
    let id = init.session_id.as_deref().unwrap();
    let first: StepState = decode(wasm::check_consistency_step_next(id));
    assert_eq!(first.step, Some(1));
    assert_eq!(first.done, Some(false));
    let edges = first.new_edges.as_ref().unwrap();
    assert!(edges.contains(&edge(1, 2)));
    assert!(edges
        .iter()
        .all(|(_, destination)| destination.session_id != 0));
    let done: StepState = decode(wasm::check_consistency_step_next(id));
    assert_eq!(done.step, Some(2));
    assert_eq!(done.new_edges.as_deref(), Some([].as_slice()));
    assert_eq!(done.done, Some(true));
    assert!(done.result.as_ref().unwrap().ok, "{done:?}");
    assert_eq!(done.total_edges, first.total_edges);
    assert!(decode::<Check>(wasm::check_consistency_trace_text(history, "causal")).ok);
}
