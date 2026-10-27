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

/// Serialize a witness using the CLI/WASM wire format without an intermediate JSON tree.
///
/// Saturation graphs are edge lists because JSON object keys cannot contain
/// transaction IDs. Commit and split orders retain their existing representation.
///
/// # Errors
///
/// Returns the serializer's error if writing the witness fails.
#[cfg(feature = "serde")]
pub fn serialize_witness<S>(witness: &Witness, serializer: S) -> Result<S::Ok, S::Error>
where
    S: serde::Serializer,
{
    #[derive(serde::Serialize)]
    enum WitnessRef<'a> {
        #[serde(rename = "CommitOrder")]
        Commit(&'a [TransactionId]),
        #[serde(rename = "SplitCommitOrder")]
        Split(&'a [(TransactionId, bool)]),
        #[serde(rename = "SaturationOrder")]
        Saturation(#[serde(serialize_with = "serialize_edges")] &'a DiGraph<TransactionId>),
    }

    fn serialize_edges<S>(graph: &DiGraph<TransactionId>, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: serde::Serializer,
    {
        serializer.collect_seq(
            graph
                .adj_map
                .iter()
                .flat_map(|(from, targets)| targets.iter().map(move |to| (from, to))),
        )
    }

    let view = match witness {
        Witness::CommitOrder(order) => WitnessRef::Commit(order),
        Witness::SplitCommitOrder(order) => WitnessRef::Split(order),
        Witness::SaturationOrder(graph) => WitnessRef::Saturation(graph),
    };
    serde::Serialize::serialize(&view, serializer)
}
