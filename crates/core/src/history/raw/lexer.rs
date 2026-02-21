//! Logos-based lexer for the compact history text DSL.
//!
//! The DSL describes transactional histories in a compact textual form.
//! Sessions are separated by lines of dashes (`---`), transactions are
//! enclosed in brackets (`[...]`), writes use `:=`, reads use `==`,
//! `?` marks an uninitialized read, and `!` marks an uncommitted transaction.
//!
//! # Example input
//!
//! ```text
//! // session 1
//! [x:=1 y:=1] [z==2 z:=3]
//! [y:=3]
//! ---
//! // session 2
//! [a==1 b:=3] [c:=3]
//! ```

use alloc::vec::Vec;
use core::ops::Range;

/// All token kinds produced by the DSL lexer.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(::logos::Logos, Debug, Clone, PartialEq, Eq)]
pub enum TokenKind {
    /// A line comment starting with `//` and running to end of line.
    #[regex(r"//[^\n]*")]
    Comment,

    /// One or more `-` characters on their own (session separator).
    #[regex(r"-+")]
    Dash,

    /// Opening bracket `[`.
    #[token("[")]
    BracketOpen,

    /// Closing bracket `]`.
    #[token("]")]
    BracketClose,

    /// Write operator `:=`.
    #[token(":=")]
    ColonEquals,

    /// Read operator `==`.
    #[token("==")]
    DoubleEquals,

    /// Uninitialized read marker `?`.
    #[token("?")]
    QuestionMark,

    /// Uncommitted transaction marker `!`.
    #[token("!")]
    Bang,

    /// An identifier: starts with a letter or underscore, followed by
    /// letters, digits, or underscores.
    #[regex(r"[a-zA-Z_][a-zA-Z0-9_]*")]
    Ident,

    /// An integer literal: one or more ASCII digits.
    #[regex(r"[0-9]+")]
    Integer,

    /// A newline (`\n` or `\r\n`).
    #[regex(r"\r?\n")]
    Newline,

    /// Spaces or tabs. Emitted so the tokenizer can be used for syntax
    /// highlighting where whitespace positioning matters.
    #[regex(r"[ \t]+")]
    Whitespace,
}

/// A single token with its kind and the byte-offset span in the source.
#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Token {
    /// What kind of token this is.
    pub kind: TokenKind,
    /// Byte range `start..end` into the original input string.
    pub span: Range<usize>,
}

impl Token {
    /// Construct a new [`Token`].
    #[must_use]
    pub const fn new(kind: TokenKind, span: Range<usize>) -> Self {
        Self { kind, span }
    }

    /// Return the source text for this token given the original input.
    #[must_use]
    pub fn text<'a>(&self, input: &'a str) -> &'a str {
        &input[self.span.clone()]
    }
}

/// Tokenize `input` and return all valid tokens.
///
/// Tokens that the lexer cannot recognise are silently skipped.
/// Use [`tokenize_with_text`] if you also need the source slice for each token.
#[must_use]
pub fn tokenize(input: &str) -> Vec<Token> {
    use logos::Logos as _;
    TokenKind::lexer(input)
        .spanned()
        .filter_map(|(result, span)| result.ok().map(|kind| Token { kind, span }))
        .collect()
}

/// Tokenize `input` and return tokens paired with their source text slices.
///
/// Tokens that the lexer cannot recognise are silently skipped.
#[must_use]
pub fn tokenize_with_text(input: &str) -> Vec<(Token, &str)> {
    use logos::Logos as _;
    TokenKind::lexer(input)
        .spanned()
        .filter_map(|(result, span)| {
            result.ok().map(|kind| {
                let text = &input[span.clone()];
                (Token { kind, span }, text)
            })
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::{tokenize, tokenize_with_text, TokenKind};

    fn kinds(input: &str) -> Vec<TokenKind> {
        tokenize(input).into_iter().map(|t| t.kind).collect()
    }

    #[test]
    fn test_basic_history() {
        let input = "[x:=1 y:=1]\n[z==2]\n";
        let tokens = tokenize(input);
        let expected_kinds = [
            TokenKind::BracketOpen,
            TokenKind::Ident, // x
            TokenKind::ColonEquals,
            TokenKind::Integer, // 1
            TokenKind::Whitespace,
            TokenKind::Ident, // y
            TokenKind::ColonEquals,
            TokenKind::Integer, // 1
            TokenKind::BracketClose,
            TokenKind::Newline,
            TokenKind::BracketOpen,
            TokenKind::Ident, // z
            TokenKind::DoubleEquals,
            TokenKind::Integer, // 2
            TokenKind::BracketClose,
            TokenKind::Newline,
        ];
        let got_kinds: Vec<_> = tokens.iter().map(|t| t.kind.clone()).collect();
        assert_eq!(got_kinds, expected_kinds);
    }

    #[test]
    fn test_comment_tokenization() {
        let input = "// this is a comment\n[x:=1]\n";
        let ks = kinds(input);
        assert_eq!(ks[0], TokenKind::Comment);
        assert_eq!(ks[1], TokenKind::Newline);
        assert_eq!(ks[2], TokenKind::BracketOpen);
    }

    #[test]
    fn test_separator_tokenization() {
        let input = "---\n";
        let ks = kinds(input);
        assert_eq!(ks[0], TokenKind::Dash);
        assert_eq!(ks[1], TokenKind::Newline);

        // A single dash is also a valid Dash token.
        let input2 = "-\n";
        let ks2 = kinds(input2);
        assert_eq!(ks2[0], TokenKind::Dash);

        // Many dashes.
        let input3 = "----------\n";
        let ks3 = kinds(input3);
        assert_eq!(ks3[0], TokenKind::Dash);
    }

    #[test]
    fn test_operator_tokens() {
        let input = ":= == ? !";
        let ks = kinds(input);
        assert_eq!(ks[0], TokenKind::ColonEquals);
        assert_eq!(ks[1], TokenKind::Whitespace);
        assert_eq!(ks[2], TokenKind::DoubleEquals);
        assert_eq!(ks[3], TokenKind::Whitespace);
        assert_eq!(ks[4], TokenKind::QuestionMark);
        assert_eq!(ks[5], TokenKind::Whitespace);
        assert_eq!(ks[6], TokenKind::Bang);
    }

    #[test]
    fn test_tokenize_with_text_spans() {
        let input = "[x:=42]";
        let pairs = tokenize_with_text(input);
        let texts: Vec<&str> = pairs.iter().map(|(_, s)| *s).collect();
        assert_eq!(texts, &["[", "x", ":=", "42", "]"]);
    }

    #[test]
    fn test_token_text_helper() {
        let input = "abc:=99";
        let tokens = tokenize(input);
        assert_eq!(tokens[0].text(input), "abc");
        assert_eq!(tokens[1].text(input), ":=");
        assert_eq!(tokens[2].text(input), "99");
    }

    #[test]
    fn test_full_example() {
        let input = "// session 1\n[x:=1 y:=1] [z==2 z:=3]\n[y:=3]\n---\n// session 2\n[a==1 b:=3] [c:=3]\n";
        let pairs = tokenize_with_text(input);
        // First token is the comment.
        assert_eq!(pairs[0].0.kind, TokenKind::Comment);
        assert_eq!(pairs[0].1, "// session 1");
        // Find the separator.
        let sep = pairs.iter().find(|(t, _)| t.kind == TokenKind::Dash);
        assert!(sep.is_some());
        assert_eq!(sep.unwrap().1, "---");
    }

    #[test]
    fn test_question_mark_and_bang() {
        let input = "[x==? y:=1]!";
        let ks = kinds(input);
        assert!(ks.contains(&TokenKind::QuestionMark));
        assert!(ks.contains(&TokenKind::Bang));
    }

    #[test]
    fn test_integer_and_ident() {
        let input = "foo_bar 123 _under";
        let ks = kinds(input);
        assert_eq!(ks[0], TokenKind::Ident); // foo_bar
        assert_eq!(ks[1], TokenKind::Whitespace);
        assert_eq!(ks[2], TokenKind::Integer); // 123
        assert_eq!(ks[3], TokenKind::Whitespace);
        assert_eq!(ks[4], TokenKind::Ident); // _under
    }

    #[test]
    fn test_span_correctness() {
        let input = "[abc]";
        let tokens = tokenize(input);
        // '[' at 0..1
        assert_eq!(tokens[0].span, 0..1);
        // 'abc' at 1..4
        assert_eq!(tokens[1].span, 1..4);
        // ']' at 4..5
        assert_eq!(tokens[2].span, 4..5);
    }
}
