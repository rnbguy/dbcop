use alloc::vec::Vec;
use core::fmt::{Debug, Formatter, Result};

use crate::history::atomic::types::TransactionId;

/// A single read or write operation within a transaction.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Clone, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub enum Event<Variable, Version> {
    Read {
        variable: Variable,
        // None represents uninitialized version
        version: Option<Version>,
    },
    Write {
        variable: Variable,
        version: Version,
    },
}

impl<Variable, Version> Event<Variable, Version>
where
    Variable: Clone,
    Version: Clone,
{
    #[must_use]
    pub fn variable(&self) -> Variable {
        match self {
            Self::Read { variable, .. } | Self::Write { variable, .. } => variable.clone(),
        }
    }

    #[must_use]
    pub fn version(&self) -> Option<Version> {
        match self {
            Self::Read { version, .. } => version.clone(),
            Self::Write { version, .. } => Some(version.clone()),
        }
    }
}

impl<Variable, Version> Debug for Event<Variable, Version>
where
    Variable: Debug,
    Version: Debug,
{
    fn fmt(&self, f: &mut Formatter) -> Result {
        match self {
            Self::Read { variable, version } => {
                write!(f, "{variable:?}=>")?;
                if let Some(version) = version {
                    write!(f, "{version:?}")?;
                } else {
                    write!(f, "?")?;
                }
            }
            Self::Write { variable, version } => {
                write!(f, "{variable:?}<={version:?}")?;
            }
        }
        Ok(())
    }
}

impl<Variable, Version> Event<Variable, Version> {
    pub const fn read_empty(variable: Variable) -> Self {
        Self::Read {
            variable,
            version: None,
        }
    }

    pub const fn read(variable: Variable, version: Version) -> Self {
        Self::Read {
            variable,
            version: Some(version),
        }
    }

    pub const fn write(variable: Variable, version: Version) -> Self {
        Self::Write { variable, version }
    }
}

impl<Variable, Version> Debug for Transaction<Variable, Version>
where
    Variable: Debug,
    Version: Debug,
{
    fn fmt(&self, f: &mut Formatter) -> Result {
        write!(f, "{:?}", self.events)?;
        if !self.committed {
            write!(f, "!")?;
        }
        Ok(())
    }
}

/// A sequence of events executed atomically, either committed or aborted.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
pub struct Transaction<Variable, Version> {
    pub events: Vec<Event<Variable, Version>>,
    pub committed: bool,
}

impl<Variable, Version> Transaction<Variable, Version> {
    #[must_use]
    pub const fn committed(events: Vec<Event<Variable, Version>>) -> Self {
        Self {
            events,
            committed: true,
        }
    }

    #[must_use]
    pub const fn uncommitted(events: Vec<Event<Variable, Version>>) -> Self {
        Self {
            events,
            committed: false,
        }
    }
}

/// An ordered sequence of transactions from a single client/node.
pub type Session<Variable, Version> = Vec<Transaction<Variable, Version>>;

/// Uniquely identifies an event within a history by session, transaction, and position.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug, Copy, Clone, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct EventId {
    pub session_id: u64,
    pub session_height: u64,
    pub transaction_height: u64,
}

impl EventId {
    #[must_use]
    pub const fn transaction_id(&self) -> TransactionId {
        TransactionId {
            session_id: self.session_id,
            session_height: self.session_height,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_event() {
        let mut event = Event::read_empty(1);
        assert_eq!(
            event,
            Event::Read {
                variable: 1,
                version: None
            }
        );
        event = Event::write(1, 2);
        assert_eq!(
            event,
            Event::Write {
                variable: 1,
                version: 2
            }
        );
    }

    #[test]
    fn test_event_id() {
        let event_id = EventId {
            session_id: 1,
            session_height: 2,
            transaction_height: 3,
        };
        assert_eq!(
            event_id.transaction_id(),
            TransactionId {
                session_id: 1,
                session_height: 2
            }
        );
    }

    #[test]
    fn test_event_debug() {
        let mut event = Event::read_empty(1);
        assert_eq!(format!("{event:?}"), "1=>?");
        event = Event::Read {
            variable: 1,
            version: Some(3),
        };
        assert_eq!(format!("{event:?}"), "1=>3");
        event = Event::write(1, 2);
        assert_eq!(format!("{event:?}"), "1<=2");
    }

    #[test]
    fn test_transaction_debug() {
        let mut transaction = Transaction {
            events: vec![Event::read_empty(1), Event::write(1, 2)],
            committed: true,
        };
        assert_eq!(format!("{transaction:?}"), "[1=>?, 1<=2]");
        transaction.committed = false;
        assert_eq!(format!("{transaction:?}"), "[1=>?, 1<=2]!");
    }
}
