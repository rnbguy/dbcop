pub mod error;
pub mod linearization;
pub mod saturation;

// Re-export submodules at the solver level for backwards compatibility.
pub use linearization::{constrained_linearization, prefix, serializable, snapshot_isolation};
pub use saturation::{atomic_read, causal, committed_read, repeatable_read};

/// Consistency levels supported by dbcop, ordered from weakest to strongest.
#[derive(Debug, Copy, Clone)]
pub enum Consistency {
    CommittedRead,
    AtomicRead,
    Causal,
    Prefix,
    SnapshotIsolation,
    Serializable,
}
