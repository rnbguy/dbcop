use dbcop_core::history::raw::types::{Event, Session, Transaction};
/// Winnow-based parser for the compact history text DSL.
///
/// Grammar:
/// ```text
/// history      = session (separator session)*
/// separator    = NEWLINE DASH+ NEWLINE
/// session      = (comment | session_line)*
/// comment      = "//" REST_OF_LINE NEWLINE
/// session_line = transaction (WHITESPACE transaction)* NEWLINE
/// transaction  = "[" event (WHITESPACE event)* "]" "!"?
/// event        = variable ":=" version   -- write
///              | variable "==" version   -- read (versioned)
///              | variable "==?"          -- read (uninitialized)
/// variable     = IDENT
/// version      = INTEGER
/// ```
use winnow::ascii::{dec_uint, newline, till_line_ending};
use winnow::combinator::{alt, opt, repeat, separated};
use winnow::prelude::*;
use winnow::token::{literal, take_while};
use winnow::ModalResult;

// ---------------------------------------------------------------------------
// Public error type
// ---------------------------------------------------------------------------

/// A parse error with human-readable location information.
#[derive(Debug, Clone)]
pub struct ParseError {
    pub message: String,
    pub line: usize,
    pub column: usize,
}

impl core::fmt::Display for ParseError {
    fn fmt(&self, f: &mut core::fmt::Formatter<'_>) -> core::fmt::Result {
        write!(
            f,
            "parse error at line {}, column {}: {}",
            self.line, self.column, self.message
        )
    }
}

impl std::error::Error for ParseError {}

// ---------------------------------------------------------------------------
// Public entry point
// ---------------------------------------------------------------------------

/// Parse a compact history DSL string into a list of sessions.
///
/// # Errors
///
/// Returns a [`ParseError`] with line/column information when the input does
/// not conform to the grammar.
pub fn parse_history(input: &str) -> Result<Vec<Session<String, u64>>, ParseError> {
    let original = input;
    let mut stream: &str = input;
    match history_parser.parse_next(&mut stream) {
        Ok(sessions) => Ok(sessions),
        Err(e) => {
            // Compute how many bytes were consumed before the error.
            let remaining_len = stream.len();
            let consumed = original.len().saturating_sub(remaining_len);
            let (line, column) = offset_to_line_col(original, consumed);
            Err(ParseError {
                message: e.to_string(),
                line,
                column,
            })
        }
    }
}

// ---------------------------------------------------------------------------
// Line/column helper
// ---------------------------------------------------------------------------

/// Convert a byte offset into the original input to 1-based (line, column).
fn offset_to_line_col(input: &str, offset: usize) -> (usize, usize) {
    let safe_offset = offset.min(input.len());
    let prefix = &input[..safe_offset];
    let line = prefix.bytes().filter(|&b| b == b'\n').count() + 1;
    let column = prefix
        .rfind('\n')
        .map_or_else(|| prefix.len() + 1, |pos| prefix.len() - pos);
    (line, column)
}

// ---------------------------------------------------------------------------
// Whitespace helpers
// ---------------------------------------------------------------------------

/// Inline whitespace: spaces and tabs only (no newlines).
fn inline_ws(input: &mut &str) -> ModalResult<()> {
    take_while(1.., |c: char| c == ' ' || c == '\t')
        .void()
        .parse_next(input)
}

/// Optional inline whitespace.
fn opt_inline_ws(input: &mut &str) -> ModalResult<()> {
    take_while(0.., |c: char| c == ' ' || c == '\t')
        .void()
        .parse_next(input)
}

// ---------------------------------------------------------------------------
// Leaf parsers
// ---------------------------------------------------------------------------

/// Parse an identifier: one or more alphanumeric characters (or `_`).
fn variable(input: &mut &str) -> ModalResult<String> {
    // alphanumeric1 matches [a-zA-Z0-9]+; we also allow underscore.
    take_while(1.., |c: char| c.is_alphanumeric() || c == '_')
        .map(|s: &str| s.to_string())
        .parse_next(input)
}

/// Parse a non-negative integer version.
fn version(input: &mut &str) -> ModalResult<u64> {
    dec_uint.parse_next(input)
}

// ---------------------------------------------------------------------------
// Event parsers
// ---------------------------------------------------------------------------

/// `variable ":=" version`  -- write event
fn write_event(input: &mut &str) -> ModalResult<Event<String, u64>> {
    let var = variable.parse_next(input)?;
    literal(":=").parse_next(input)?;
    let ver = version.parse_next(input)?;
    Ok(Event::write(var, ver))
}

/// `variable "==?" `  -- uninitialized read event
fn read_empty_event(input: &mut &str) -> ModalResult<Event<String, u64>> {
    let var = variable.parse_next(input)?;
    literal("==?").parse_next(input)?;
    Ok(Event::read_empty(var))
}

/// `variable "==" version`  -- versioned read event
fn read_event(input: &mut &str) -> ModalResult<Event<String, u64>> {
    let var = variable.parse_next(input)?;
    literal("==").parse_next(input)?;
    let ver = version.parse_next(input)?;
    Ok(Event::read(var, ver))
}

/// Any event: try write first, then uninitialized read (must come before
/// versioned read because `==?` starts with `==`), then versioned read.
fn event(input: &mut &str) -> ModalResult<Event<String, u64>> {
    alt((write_event, read_empty_event, read_event)).parse_next(input)
}

// ---------------------------------------------------------------------------
// Transaction parser
// ---------------------------------------------------------------------------

/// `"[" event (WS event)* "]" "!"?`
///
/// Trailing `!` marks the transaction as *uncommitted*.
fn transaction(input: &mut &str) -> ModalResult<Transaction<String, u64>> {
    literal("[").parse_next(input)?;
    let events: Vec<Event<String, u64>> = separated(1.., event, inline_ws).parse_next(input)?;
    literal("]").parse_next(input)?;
    let bang = opt(literal("!")).parse_next(input)?;
    let committed = bang.is_none();
    if committed {
        Ok(Transaction::committed(events))
    } else {
        Ok(Transaction::uncommitted(events))
    }
}

// ---------------------------------------------------------------------------
// Session-line and comment parsers
// ---------------------------------------------------------------------------

/// A comment line: `"//" <rest-of-line> NEWLINE`.
/// Returns `None` (comments produce no transactions).
fn comment_line(input: &mut &str) -> ModalResult<Option<Vec<Transaction<String, u64>>>> {
    literal("//").parse_next(input)?;
    till_line_ending.parse_next(input)?;
    newline.parse_next(input)?;
    Ok(None)
}

/// A session line: one or more transactions separated by inline whitespace,
/// terminated by a newline.
fn session_line(input: &mut &str) -> ModalResult<Option<Vec<Transaction<String, u64>>>> {
    opt_inline_ws.parse_next(input)?;
    let txns: Vec<Transaction<String, u64>> =
        separated(1.., transaction, inline_ws).parse_next(input)?;
    opt_inline_ws.parse_next(input)?;
    newline.parse_next(input)?;
    Ok(Some(txns))
}

/// A blank line (only whitespace + newline). Produces nothing.
fn blank_line(input: &mut &str) -> ModalResult<Option<Vec<Transaction<String, u64>>>> {
    opt_inline_ws.parse_next(input)?;
    newline.parse_next(input)?;
    Ok(None)
}

/// One item inside a session: comment, blank line, or transaction line.
fn session_item(input: &mut &str) -> ModalResult<Option<Vec<Transaction<String, u64>>>> {
    // A separator line (only dashes) must NOT be parsed as a session item;
    // we detect it by peeking for '-' after optional whitespace.
    alt((comment_line, blank_line, session_line)).parse_next(input)
}

// ---------------------------------------------------------------------------
// Separator parser
// ---------------------------------------------------------------------------

/// A separator is a line consisting of one or more `-` characters (possibly
/// surrounded by inline whitespace), terminated by a newline.
fn separator(input: &mut &str) -> ModalResult<()> {
    opt_inline_ws.parse_next(input)?;
    take_while(1.., '-').parse_next(input)?;
    opt_inline_ws.parse_next(input)?;
    newline.parse_next(input)?;
    Ok(())
}

// ---------------------------------------------------------------------------
// Session and history parsers
// ---------------------------------------------------------------------------

/// A session is zero or more session items (comments, blank lines, transaction
/// lines) that appear *before* a separator or end-of-input.
///
/// We parse items one at a time; we stop when we see a separator prefix (`-`)
/// or reach end-of-input.
fn session(input: &mut &str) -> ModalResult<Session<String, u64>> {
    let mut transactions: Vec<Transaction<String, u64>> = Vec::new();

    loop {
        // Peek: if next non-whitespace char is '-', this is a separator -- stop.
        let trimmed = input.trim_start_matches([' ', '\t']);
        if trimmed.starts_with('-') || trimmed.is_empty() {
            break;
        }
        if let Some(mut txns) = session_item.parse_next(input)? {
            transactions.append(&mut txns);
        }
    }

    Ok(transactions)
}

/// The top-level history: `session (separator session)*` followed by optional
/// trailing whitespace/newlines and end-of-input.
fn history_parser(input: &mut &str) -> ModalResult<Vec<Session<String, u64>>> {
    let first = session.parse_next(input)?;
    let mut sessions = Vec::new();
    sessions.push(first);

    loop {
        // Consume a separator if present.
        if separator.parse_next(input).is_err() {
            break;
        }
        let s = session.parse_next(input)?;
        sessions.push(s);
    }

    // Consume any trailing blank lines / whitespace.
    repeat::<_, _, (), _, _>(0.., blank_line).parse_next(input)?;

    // Verify we are at end-of-input.
    if !input.is_empty() {
        // Return a backtrack error so the caller sees remaining input.
        return Err(winnow::error::ErrMode::Backtrack(
            winnow::error::ContextError::new(),
        ));
    }

    Ok(sessions)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

#[cfg(test)]
mod tests {
    use super::*;

    // Shorthand constructors for tests.
    fn w(var: &str, ver: u64) -> Event<String, u64> {
        Event::write(var.to_string(), ver)
    }
    fn r(var: &str, ver: u64) -> Event<String, u64> {
        Event::read(var.to_string(), ver)
    }
    fn re(var: &str) -> Event<String, u64> {
        Event::read_empty(var.to_string())
    }
    fn _committed(events: Vec<Event<String, u64>>) -> Transaction<String, u64> {
        Transaction::committed(events)
    }
    fn _uncommitted(events: Vec<Event<String, u64>>) -> Transaction<String, u64> {
        Transaction::uncommitted(events)
    }

    // -----------------------------------------------------------------------
    // Happy-path tests
    // -----------------------------------------------------------------------

    #[test]
    fn test_single_session() {
        let input = "[x:=1 y:=1]\n";
        let result = parse_history(input).expect("should parse");
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].len(), 1);
        assert_eq!(result[0][0].events, vec![w("x", 1), w("y", 1)]);
        assert!(result[0][0].committed);
    }

    #[test]
    fn test_multi_session_with_separator() {
        let input = "[x:=1]\n---\n[y:=2]\n";
        let result = parse_history(input).expect("should parse");
        assert_eq!(result.len(), 2);
        assert_eq!(result[0][0].events, vec![w("x", 1)]);
        assert_eq!(result[1][0].events, vec![w("y", 2)]);
    }

    #[test]
    fn test_multiple_transactions_per_line() {
        let input = "[x:=1 y:=1] [z==2 z:=3]\n[y:=3]\n";
        let result = parse_history(input).expect("should parse");
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].len(), 3);
        assert_eq!(result[0][0].events, vec![w("x", 1), w("y", 1)]);
        assert_eq!(result[0][1].events, vec![r("z", 2), w("z", 3)]);
        assert_eq!(result[0][2].events, vec![w("y", 3)]);
    }

    #[test]
    fn test_uncommitted_transaction() {
        let input = "[x:=1]!\n";
        let result = parse_history(input).expect("should parse");
        assert!(!result[0][0].committed);
        assert_eq!(result[0][0].events, vec![w("x", 1)]);
    }

    #[test]
    fn test_uninitialized_read() {
        let input = "[x==?]\n";
        let result = parse_history(input).expect("should parse");
        assert_eq!(result[0][0].events, vec![re("x")]);
    }

    #[test]
    fn test_comments_are_skipped() {
        let input = "// session 1\n[x:=1]\n---\n// session 2\n[y:=2]\n";
        let result = parse_history(input).expect("should parse");
        assert_eq!(result.len(), 2);
        assert_eq!(result[0][0].events, vec![w("x", 1)]);
        assert_eq!(result[1][0].events, vec![w("y", 2)]);
    }

    #[test]
    fn test_empty_session_between_separators() {
        let input = "[x:=1]\n---\n---\n[y:=2]\n";
        let result = parse_history(input).expect("should parse");
        assert_eq!(result.len(), 3);
        assert_eq!(result[0].len(), 1);
        assert_eq!(result[1].len(), 0); // empty session
        assert_eq!(result[2].len(), 1);
    }

    #[test]
    fn test_full_example() {
        let input = "\
// session 1
[x:=1 y:=1] [z==2 z:=3]
[y:=3]
---
// session 2
[a==1 b:=3] [c:=3]
[d==3]
";
        let result = parse_history(input).expect("should parse full example");
        assert_eq!(result.len(), 2);

        let s0 = &result[0];
        assert_eq!(s0.len(), 3);
        assert_eq!(s0[0].events, vec![w("x", 1), w("y", 1)]);
        assert_eq!(s0[1].events, vec![r("z", 2), w("z", 3)]);
        assert_eq!(s0[2].events, vec![w("y", 3)]);

        let s1 = &result[1];
        assert_eq!(s1.len(), 3);
        assert_eq!(s1[0].events, vec![r("a", 1), w("b", 3)]);
        assert_eq!(s1[1].events, vec![w("c", 3)]);
        assert_eq!(s1[2].events, vec![r("d", 3)]);
    }

    #[test]
    fn test_committed_and_uncommitted_mix() {
        let input = "[x:=1] [y:=2]!\n";
        let result = parse_history(input).expect("should parse");
        assert!(result[0][0].committed);
        assert!(!result[0][1].committed);
    }

    #[test]
    fn test_multiple_events_in_transaction() {
        let input = "[a:=1 b==2 c==? d:=5]\n";
        let result = parse_history(input).expect("should parse");
        assert_eq!(
            result[0][0].events,
            vec![w("a", 1), r("b", 2), re("c"), w("d", 5)]
        );
    }

    #[test]
    fn test_blank_lines_in_session() {
        let input = "[x:=1]\n\n[y:=2]\n";
        let result = parse_history(input).expect("should parse with blank lines");
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].len(), 2);
    }

    #[test]
    fn test_empty_history_single_session() {
        // A history with no transactions at all.
        let input = "// just a comment\n";
        let result = parse_history(input).expect("should parse empty session");
        assert_eq!(result.len(), 1);
        assert_eq!(result[0].len(), 0);
    }

    // -----------------------------------------------------------------------
    // Error tests
    // -----------------------------------------------------------------------

    #[test]
    fn test_parse_error_has_line_column() {
        // Invalid token at line 2, column 1.
        let input = "[x:=1]\n@bad\n";
        let err = parse_history(input).expect_err("should fail");
        // The error should point to line 2.
        assert_eq!(err.line, 2, "expected error on line 2, got: {err}");
    }

    #[test]
    fn test_parse_error_display() {
        let input = "[x:=1]\n@bad\n";
        let err = parse_history(input).expect_err("should fail");
        let msg = err.to_string();
        assert!(
            msg.contains("parse error"),
            "display should contain 'parse error': {msg}"
        );
        assert!(msg.contains("line"), "display should contain 'line': {msg}");
    }

    #[test]
    fn test_offset_to_line_col_first_line() {
        // Offset 0 on first line.
        let (line, col) = offset_to_line_col("hello\nworld\n", 0);
        assert_eq!(line, 1);
        assert_eq!(col, 1);
    }

    #[test]
    fn test_offset_to_line_col_second_line() {
        // "hello\n" is 6 bytes; offset 6 is start of second line.
        let (line, col) = offset_to_line_col("hello\nworld\n", 6);
        assert_eq!(line, 2);
        assert_eq!(col, 1);
    }
}
