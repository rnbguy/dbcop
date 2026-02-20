//! Maintains the core algorithms.

#![cfg_attr(not(test), no_std)]
#![cfg_attr(not(test), no_main)]
extern crate alloc;

pub mod consistency;
pub mod graph;
pub mod history;

pub use consistency::{check, Consistency};
