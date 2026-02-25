//! Consistency checking for transactional histories.
//!
//! `dbcop_core` verifies whether a recorded database history satisfies a given
//! transactional consistency level. It supports six levels, ordered from weakest
//! to strongest:
//!
//! 1. **Read Committed** -- no transaction reads uncommitted writes.
//! 2. **Atomic Read** -- reads within a transaction are atomic across variables
//!    (no fractured reads).
//! 3. **Causal Consistency** -- causally related operations appear in a
//!    consistent order to all participants.
//! 4. **Prefix Consistency** -- every transaction observes a consistent prefix
//!    of the global write history.
//! 5. **Snapshot Isolation** -- each transaction reads from a point-in-time
//!    snapshot and concurrent writers touch disjoint key sets.
//! 6. **Serializability** -- the history is equivalent to some serial execution
//!    of all transactions.
//!
//! The first three levels (Read Committed through Causal) are checked using
//! polynomial-time saturation algorithms that incrementally build a visibility
//! relation until a fixed point or a cycle is found. The last three (Prefix
//! through Serializability) first run the Causal checker, then attempt to find
//! a valid linearization via constrained depth-first search.
//!
//! # Entry point
//!
//! The main entry point is [`check()`], which takes a slice of sessions and a
//! [`Consistency`] level, and returns either a [`Witness`] proving the history
//! satisfies the level, or an [`Error`](consistency::error::Error) explaining
//! the violation.
//!
//! ```rust,ignore
//! use dbcop_core::{check, Consistency};
//!
//! let result = check(&sessions, Consistency::Causal);
//! match result {
//!     Ok(witness) => println!("consistent: {witness:?}"),
//!     Err(err) => println!("violation: {err:?}"),
//! }
//! ```
//!
//! # Crate features
//!
//! - **`serde`** -- enables `Serialize`/`Deserialize` derives on core types
//!   (`DiGraph`, `TransactionId`, `Consistency`, `Witness`, `Error`).
//!
//! This crate is `no_std` compatible (requires `alloc`). The parser and lexer
//! live in the separate `dbcop_parser` crate.

#![cfg_attr(not(any(test, feature = "schemars")), no_std)]
extern crate alloc;

pub mod consistency;
pub mod graph;
pub mod history;

pub use consistency::{check, Consistency};
