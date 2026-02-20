//! wasm library for dbcop
//! compiled binary is uploaded as github action artifact

#![cfg_attr(not(test), no_std)]
#![cfg_attr(not(test), no_main)]

extern crate alloc;

use alloc::string::{String, ToString};
use alloc::vec::Vec;

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
