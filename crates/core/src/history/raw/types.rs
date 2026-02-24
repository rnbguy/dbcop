#[cfg(feature = "serde")]
use alloc::string::String;
use alloc::vec::Vec;
use core::fmt::{self, Debug, Display, Formatter};

use crate::history::atomic::types::TransactionId;

/// A single read or write operation within a transaction.
///
/// Serialization format depends on features:
/// - Default (`serde`): tagged enum `{"Read": {"variable": ..., "version": ...}}`
/// - Compact (`compact-serde`): tuple `["r", variable, version]` / `["w", variable, version]`
///
/// Deserialization always accepts both formats (backward-compatible).
#[cfg_attr(
    all(feature = "serde", not(feature = "compact-serde")),
    derive(::serde::Serialize)
)]
#[cfg_attr(feature = "schemars", derive(::schemars::JsonSchema))]
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

// -- Compact Serialize (feature = "compact-serde") ----------------------------

#[cfg(feature = "compact-serde")]
impl<Variable, Version> ::serde::Serialize for Event<Variable, Version>
where
    Variable: ::serde::Serialize,
    Version: ::serde::Serialize,
{
    fn serialize<S>(&self, serializer: S) -> Result<S::Ok, S::Error>
    where
        S: ::serde::Serializer,
    {
        use ::serde::ser::SerializeTuple;
        match self {
            Self::Read { variable, version } => {
                let mut tup = serializer.serialize_tuple(3)?;
                tup.serialize_element(&'r')?;
                tup.serialize_element(variable)?;
                tup.serialize_element(version)?;
                tup.end()
            }
            Self::Write { variable, version } => {
                let mut tup = serializer.serialize_tuple(3)?;
                tup.serialize_element(&'w')?;
                tup.serialize_element(variable)?;
                tup.serialize_element(version)?;
                tup.end()
            }
        }
    }
}

// -- Backward-compatible Deserialize (feature = "serde") ----------------------
// Accepts both tagged-enum format and compact tuple format.

#[cfg(feature = "serde")]
impl<'de, Variable, Version> ::serde::Deserialize<'de> for Event<Variable, Version>
where
    Variable: ::serde::Deserialize<'de>,
    Version: ::serde::Deserialize<'de>,
{
    fn deserialize<D>(deserializer: D) -> Result<Self, D::Error>
    where
        D: ::serde::Deserializer<'de>,
    {
        use ::serde::de::{self, MapAccess, SeqAccess, Visitor};

        struct EventVisitor<V, W>(core::marker::PhantomData<(V, W)>);

        impl<'de, V, W> Visitor<'de> for EventVisitor<V, W>
        where
            V: ::serde::Deserialize<'de>,
            W: ::serde::Deserialize<'de>,
        {
            type Value = Event<V, W>;

            fn expecting(&self, f: &mut Formatter) -> fmt::Result {
                f.write_str("an Event as tagged enum or compact tuple [\"r\"/\"w\", var, ver]")
            }

            // Compact tuple: ["r", variable, version] or ["w", variable, version]
            fn visit_seq<A>(self, mut seq: A) -> Result<Self::Value, A::Error>
            where
                A: SeqAccess<'de>,
            {
                let tag: char = seq
                    .next_element()?
                    .ok_or_else(|| de::Error::invalid_length(0, &"3"))?;
                let variable: V = seq
                    .next_element()?
                    .ok_or_else(|| de::Error::invalid_length(1, &"3"))?;

                match tag {
                    'r' => {
                        let version: Option<W> = seq
                            .next_element()?
                            .ok_or_else(|| de::Error::invalid_length(2, &"3"))?;
                        Ok(Event::Read { variable, version })
                    }
                    'w' => {
                        let version: W = seq
                            .next_element()?
                            .ok_or_else(|| de::Error::invalid_length(2, &"3"))?;
                        Ok(Event::Write { variable, version })
                    }
                    other => Err(de::Error::custom(alloc::format!(
                        "unknown tag '{other}', expected 'r' or 'w'"
                    ))),
                }
            }

            // Tagged enum: {"Read": {...}} or {"Write": {...}}
            fn visit_map<A>(self, mut map: A) -> Result<Self::Value, A::Error>
            where
                A: MapAccess<'de>,
            {
                let key: String = map
                    .next_key()?
                    .ok_or_else(|| de::Error::custom("expected Read or Write key"))?;

                match key.as_str() {
                    "Read" => {
                        #[derive(::serde::Deserialize)]
                        struct ReadFields<V, W> {
                            variable: V,
                            version: Option<W>,
                        }
                        let fields: ReadFields<V, W> = map.next_value()?;
                        Ok(Event::Read {
                            variable: fields.variable,
                            version: fields.version,
                        })
                    }
                    "Write" => {
                        #[derive(::serde::Deserialize)]
                        struct WriteFields<V, W> {
                            variable: V,
                            version: W,
                        }
                        let fields: WriteFields<V, W> = map.next_value()?;
                        Ok(Event::Write {
                            variable: fields.variable,
                            version: fields.version,
                        })
                    }
                    other => Err(de::Error::unknown_variant(other, &["Read", "Write"])),
                }
            }
        }

        deserializer.deserialize_any(EventVisitor(core::marker::PhantomData))
    }
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
    fn fmt(&self, f: &mut Formatter) -> fmt::Result {
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
    fn fmt(&self, f: &mut Formatter) -> fmt::Result {
        write!(f, "{:?}", self.events)?;
        if !self.committed {
            write!(f, "!")?;
        }
        Ok(())
    }
}

impl<Variable, Version> Display for Event<Variable, Version>
where
    Variable: Display,
    Version: Display,
{
    fn fmt(&self, f: &mut Formatter) -> fmt::Result {
        match self {
            Self::Write { variable, version } => write!(f, "{variable}:={version}"),
            Self::Read { variable, version } => {
                if let Some(version) = version {
                    write!(f, "{variable}=={version}")
                } else {
                    write!(f, "{variable}==?")
                }
            }
        }
    }
}

impl<Variable, Version> Display for Transaction<Variable, Version>
where
    Variable: Display,
    Version: Display,
{
    fn fmt(&self, f: &mut Formatter) -> fmt::Result {
        write!(f, "[")?;
        for (i, event) in self.events.iter().enumerate() {
            if i > 0 {
                write!(f, " ")?;
            }
            write!(f, "{event}")?;
        }
        write!(f, "]")?;
        if !self.committed {
            write!(f, "!")?;
        }
        Ok(())
    }
}

/// A sequence of events executed atomically, either committed or aborted.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[cfg_attr(feature = "schemars", derive(::schemars::JsonSchema))]
#[derive(Clone)]
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

    #[test]
    fn test_event_display() {
        assert_eq!(format!("{}", Event::<&str, u64>::write("x", 1)), "x:=1");
        assert_eq!(format!("{}", Event::<&str, u64>::read("x", 1)), "x==1");
        assert_eq!(format!("{}", Event::<&str, u64>::read_empty("x")), "x==?");
    }

    #[test]
    fn test_transaction_display() {
        let txn = Transaction::committed(vec![Event::write("x", 1), Event::read("y", 2)]);
        assert_eq!(format!("{txn}"), "[x:=1 y==2]");
        let txn = Transaction::uncommitted(vec![Event::write("x", 1)]);
        assert_eq!(format!("{txn}"), "[x:=1]!");
    }

    // -- Serde tests ----------------------------------------------------------

    /// Deserialize from tagged-enum JSON (the default serde format).
    #[cfg(feature = "serde")]
    #[test]
    fn test_serde_tagged_roundtrip() {
        let event: Event<u64, u64> = Event::write(0, 1);
        let json = serde_json::to_string(&event).unwrap();
        let back: Event<u64, u64> = serde_json::from_str(&json).unwrap();
        assert_eq!(event, back);

        let read_event: Event<u64, u64> = Event::read(0, 42);
        let json = serde_json::to_string(&read_event).unwrap();
        let back: Event<u64, u64> = serde_json::from_str(&json).unwrap();
        assert_eq!(read_event, back);

        let empty: Event<u64, u64> = Event::read_empty(0);
        let json = serde_json::to_string(&empty).unwrap();
        let back: Event<u64, u64> = serde_json::from_str(&json).unwrap();
        assert_eq!(empty, back);
    }

    /// Deserialize compact tuple format even when compiled without compact-serde.
    #[cfg(feature = "serde")]
    #[test]
    fn test_serde_deserialize_compact_tuple() {
        // Write: ["w", variable, version]
        let json = r#"["w", 0, 1]"#;
        let event: Event<u64, u64> = serde_json::from_str(json).unwrap();
        assert_eq!(event, Event::write(0, 1));

        // Read: ["r", variable, version]
        let json = r#"["r", 0, 42]"#;
        let event: Event<u64, u64> = serde_json::from_str(json).unwrap();
        assert_eq!(event, Event::read(0, 42));

        // Read empty: ["r", variable, null]
        let json = r#"["r", 0, null]"#;
        let event: Event<u64, u64> = serde_json::from_str(json).unwrap();
        assert_eq!(event, Event::read_empty(0));
    }

    /// Deserialize tagged-enum format always works.
    #[cfg(feature = "serde")]
    #[test]
    fn test_serde_deserialize_tagged_enum() {
        let json = r#"{"Write":{"variable":0,"version":1}}"#;
        let event: Event<u64, u64> = serde_json::from_str(json).unwrap();
        assert_eq!(event, Event::write(0, 1));

        let json = r#"{"Read":{"variable":0,"version":42}}"#;
        let event: Event<u64, u64> = serde_json::from_str(json).unwrap();
        assert_eq!(event, Event::read(0, 42));

        let json = r#"{"Read":{"variable":0,"version":null}}"#;
        let event: Event<u64, u64> = serde_json::from_str(json).unwrap();
        assert_eq!(event, Event::read_empty(0));
    }

    /// Transaction serde roundtrip.
    #[cfg(feature = "serde")]
    #[test]
    fn test_serde_transaction_roundtrip() {
        let txn = Transaction::committed(vec![Event::write(0u64, 1u64), Event::read(1, 2)]);
        let json = serde_json::to_string(&txn).unwrap();
        let back: Transaction<u64, u64> = serde_json::from_str(&json).unwrap();
        assert_eq!(back.events, txn.events);
        assert_eq!(back.committed, txn.committed);
    }
}
