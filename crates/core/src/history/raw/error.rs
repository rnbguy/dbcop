use super::types::Event;
use crate::history::raw::types::EventId;

/// Error converting a raw history to an atomic transactional history
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug, thiserror::Error)]
pub enum Error<Variable, Version> {
    /// Reads an absent value
    #[error("event {id:?} reads an absent value")]
    IncompleteHistory {
        event: Event<Variable, Version>,
        id: EventId,
    },
    /// Different events wrote same version
    #[error("events {ids:?} wrote the same version")]
    SameVersionWrite {
        event: Event<Variable, Version>,
        ids: [EventId; 2],
    },
    #[error("read {read_event_id:?} does not match the preceding local write {write_event_id:?}")]
    InconsistentLocalRead {
        read_event_id: EventId,
        write_event_id: EventId,
        read_event: Event<Variable, Version>,
    },
    /// Read an failed event
    #[error("read {read_event_id:?} observes unsuccessful event {write_event_id:?}")]
    UnsuccessfulEventRead {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        write_event: Event<Variable, Version>,
        write_event_id: EventId,
    },
    /// Read a write of an aborted transaction
    #[error("read {read_event_id:?} observes an aborted transaction's write {write_event_id:?}")]
    UnsuccessfulTransactionRead {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        write_event: Event<Variable, Version>,
        write_event_id: EventId,
    },
    /// Read two different writes from two different committed transactions
    #[error("read {read_event_id:?} observes different writes {write_event_ids:?}")]
    NonRepeatableRead {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        write_event_ids: [EventId; 2],
    },
    /// Read a write that is overwritten within a committed transaction
    #[error("read {read_event_id:?} observes overwritten write {overwritten_write_event_id:?}")]
    OverwrittenRead {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        overwritten_write_event_id: EventId,
        committed_write_event: Event<Variable, Version>,
        committed_write_event_id: EventId,
    },
    /// Read a write that is from an uncommitted transaction
    #[error("read {read_event_id:?} observes uncommitted write {write_event_id:?}")]
    UncommittedWrite {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        write_event_id: EventId,
    },
}
