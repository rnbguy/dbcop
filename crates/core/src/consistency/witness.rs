use alloc::vec::Vec;

use crate::graph::digraph::DiGraph;
use crate::history::atomic::types::TransactionId;

/// Evidence that a history satisfies a given consistency level.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Witness {
    /// Commit order as a linearization of transactions.
    /// Returned by Prefix and Serializability checkers.
    CommitOrder(Vec<TransactionId>),
    /// Split commit order for Snapshot Isolation.
    /// Each transaction is split: `(TransactionId, bool)` where `bool` indicates the write half.
    SplitCommitOrder(Vec<(TransactionId, bool)>),
    /// Saturation-based visibility order.
    /// Returned by Read Committed, Repeatable Read, Read Atomic, and Causal checkers.
    SaturationOrder(DiGraph<TransactionId>),
}
