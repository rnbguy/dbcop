use alloc::string::String;
use core::fmt::{Display, Write};

use crate::history::raw::types::Session;

/// Format a complete history as the compact text DSL.
///
/// Sessions are separated by `---`. Each transaction is on its own line.
/// The output always ends with a trailing newline so that it round-trips
/// through `parse_history` without needing any external fixup.
#[must_use]
pub fn format_history<Variable, Version>(sessions: &[Session<Variable, Version>]) -> String
where
    Variable: Display,
    Version: Display,
{
    let mut output = String::new();
    for (i, session) in sessions.iter().enumerate() {
        if i > 0 {
            output.push_str("---\n");
        }
        for transaction in session {
            let _ = writeln!(output, "{transaction}");
        }
    }
    output
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::history::raw::types::{Event, Transaction};

    #[test]
    fn test_format_history_single_session() {
        let sessions = vec![vec![
            Transaction::committed(vec![Event::write("x", 1), Event::write("y", 1)]),
            Transaction::committed(vec![Event::read("z", 2), Event::write("z", 3)]),
        ]];
        let result = format_history(&sessions);
        assert_eq!(result, "[x:=1 y:=1]\n[z==2 z:=3]\n");
    }

    #[test]
    fn test_format_history_two_sessions() {
        let sessions = vec![
            vec![
                Transaction::committed(vec![Event::write("x", 1), Event::write("y", 1)]),
                Transaction::committed(vec![Event::read("z", 2), Event::write("z", 3)]),
            ],
            vec![Transaction::committed(vec![
                Event::read("a", 1),
                Event::write("b", 3),
            ])],
        ];
        let result = format_history(&sessions);
        assert_eq!(result, "[x:=1 y:=1]\n[z==2 z:=3]\n---\n[a==1 b:=3]\n");
    }

    #[test]
    fn test_format_history_empty() {
        let sessions: Vec<Session<&str, u64>> = vec![];
        let result = format_history(&sessions);
        assert_eq!(result, "");
    }

    #[test]
    fn test_format_history_uncommitted() {
        let sessions = vec![vec![Transaction::uncommitted(vec![Event::write("x", 1)])]];
        let result = format_history(&sessions);
        assert_eq!(result, "[x:=1]!\n");
    }
}
