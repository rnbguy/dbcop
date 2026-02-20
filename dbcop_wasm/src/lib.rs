//! wasm library for dbcop
//! compiled binary is uploaded as github action artifact

#![cfg_attr(not(test), no_std)]
#![cfg_attr(not(test), no_main)]

extern crate alloc;

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

#[must_use]
#[wasm_bindgen]
pub fn check_consistency(history_json: &str, level: &str) -> bool {
    let Some(consistency) = parse_level(level) else {
        return false;
    };

    let Ok(sessions) = serde_json::from_str::<Vec<Session<u64, u64>>>(history_json) else {
        return false;
    };

    dbcop_core::check(&sessions, consistency).is_ok()
}
