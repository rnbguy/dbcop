use super::types::Event;
use crate::history::raw::types::EventId;

/// Error converting a raw history to an atomic transactional history
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug)]
pub enum Error<Variable, Version> {
    /// Reads an absent value
    IncompleteHistory {
        event: Event<Variable, Version>,
        id: EventId,
    },
    /// Different events wrote same version
    SameVersionWrite {
        event: Event<Variable, Version>,
        ids: [EventId; 2],
    },
    InconsistentLocalRead {
        read_event_id: EventId,
        write_event_id: EventId,
        read_event: Event<Variable, Version>,
    },
    /// Read an failed event
    UnsuccessfulEventRead {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        write_event: Event<Variable, Version>,
        write_event_id: EventId,
    },
    /// Read a write of an aborted transaction
    UnsuccessfulTransactionRead {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        write_event: Event<Variable, Version>,
        write_event_id: EventId,
    },
    /// Read two different writes from two different committed transactions
    NonRepeatableRead {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        write_event_ids: [EventId; 2],
    },
    /// Read a write that is overwritten within a committed transaction
    OverwrittenRead {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        overwritten_write_event_id: EventId,
        committed_write_event: Event<Variable, Version>,
        committed_write_event_id: EventId,
    },
    /// Read a write that is from an uncommitted transaction
    UncommittedWrite {
        read_event: Event<Variable, Version>,
        read_event_id: EventId,
        write_event_id: EventId,
    },
}
