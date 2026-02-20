//! Maintains the core algorithms.

#![cfg_attr(not(test), no_std)]
#![cfg_attr(not(test), no_main)]
extern crate alloc;

pub mod graph;
pub mod history;
pub mod solver;

pub use solver::Consistency;
