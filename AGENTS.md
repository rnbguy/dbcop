# AGENTS.md

This file documents conventions, tooling requirements, and learnings for AI
agents and contributors working on this repository.

## Workflow

All changes follow this flow:

1. Start from `main` branch (always synced to `origin/main`)
2. Create a feature branch (see Branch Naming below)
3. Implement the change, run tests and formatting locally
4. Push branch, open PR with conventional commit title
5. Wait for ALL CI checks to pass -- never merge with failing CI
6. Squash merge only: `gh pr merge N --squash --subject "<title>"`
7. Sync local: `git fetch origin && git reset --hard origin/main`
8. Repeat for next task

## Branch Naming

- `task/tN-short-description` -- plan tasks (e.g.
  `task/t23-session-order-chain`)
- `feat/description` -- new features
- `fix/description` -- bug fixes
- `perf/description` -- performance improvements
- `chore/description` -- tooling/config/housekeeping
- `docs/description` -- documentation only
- `refactor/description` -- code refactoring without behavior change

## Conventional Commit PR Titles

Format: `type(scope): description`

Types: feat, fix, perf, refactor, chore, docs, style, test

Scopes: core, cli, wasm, sat, testgen, history, hooks, ci

Examples:

- `perf(linearization): replace BTreeSet memoization with Zobrist hashing`
- `perf(history): specialize session-order closure for chain topology`
- `chore(hooks): use cargo +nightly fmt in pre-commit hook`
- `feat(core): add cycle detection via topological sort`
- `docs: add AGENTS.md with contributor and agent guidance`

## Toolchain Requirements

- Rust nightly is required for formatting. Always use
  `cargo +nightly fmt --all`. Never use `cargo fmt` (stable) -- `rustfmt.toml`
  uses nightly-only options.
- Linting: `cargo clippy -p <crate> -- -D warnings`
- TOML formatting: run `taplo format <file>` before committing any .toml change.
  Verify with `taplo format --check <file>`. CI enforces this on all .toml
  files.
- Tests: `cargo test -p <crate>` or `cargo test --workspace`
- no_std check: `cargo build -p dbcop_core --no-default-features` must always
  compile.

## CI Checks

All three must pass before merging:

1. `build` -- cargo build, clippy, test, end-to-end checks
2. `format` -- uses `actions-rust-lang/rustfmt@v1` (nightly rustfmt, automatic)
3. `code-quality` -- taplo format --check on all .toml files, deno fmt --check,
   typos

## Code Constraints

- No unicode or emoji in any `.rs`, `.ts`, or `.js` file. The pre-commit hook
  enforces this. ASCII only.
- Preserve no_std compatibility in `dbcop_core`. Never use std-only types
  without a feature gate.
- Serde derives must be gated:
  `#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]`
- Do NOT rename existing public types (e.g. `CommittedRead` stays
  `CommittedRead`).
- Do NOT add a `Consistency::RepeatableRead` variant.
- Do NOT change the `ConstrainedLinearizationSolver` trait API.

## Pre-commit Hook (.husky/pre-commit)

Two checks run on staged files:

1. Rejects any non-ASCII character in staged `.rs`, `.ts`, `.js` files.
2. Runs `cargo +nightly fmt --check --all` -- fails if any Rust file needs
   reformatting.

To install hooks after cloning: `deno task prepare`

## Repository Structure

```
dbcop/                          workspace root
  crates/
    core/                        main library (no_std) -- dbcop_core
      src/
        graph/digraph.rs           DiGraph<T> -- core graph type
        consistency/               check() entry point, consistency algorithms
          saturation/              saturation-based checkers (CommittedRead, Causal, etc.)
          linearization/           linearization-based checkers (Prefix, SnapshotIsolation, Serializable)
        history/atomic/            AtomicTransactionPO and AtomicTransactionHistory
    cli/                         CLI binary -- dbcop_cli
    wasm/                        WASM bindings -- dbcop_wasm
    sat/                         SAT solver backend -- dbcop_sat
    testgen/                     test history generator -- dbcop_testgen
    drivers/                     database drivers -- dbcop_drivers
  .github/workflows/
    rust.yml                     build + format CI
    code-quality.yml             taplo + deno fmt + typos CI
  .husky/pre-commit              ASCII check + cargo +nightly fmt
  taplo.toml                     TOML formatter config
  deno.json                      deno tasks: prepare (husky), wasmbuild
  rustfmt.toml                   nightly rustfmt config
```

## Key Types

- `TransactionId { session_id: u64, session_height: u64 }` Default value (0, 0)
  is the root node in session-order and visibility graphs. Ordered
  lexicographically by (session_id, session_height).

- `DiGraph<T>` -- directed graph with adjacency map. Key methods:
  `add_edge(from, to)`, `add_vertex(v)`, `closure()`, `topological_sort()`,
  `union(other)`, `is_acyclic()`, `to_edge_list()`,
  `find_cycle_edge() -> Option<(T, T)>` (returns an edge on a cycle via Kahn's
  algorithm).

- `AtomicTransactionPO` -- per-history partial order. Holds:
  `session_order: DiGraph<TransactionId>`,
  `write_read_relation: HashMap<Variable, DiGraph<TransactionId>>`,
  `wr_union: DiGraph<TransactionId>`,
  `visibility_relation: DiGraph<TransactionId>`.

- `Consistency` enum: `CommittedRead`, `AtomicRead`, `Causal`, `Prefix`,
  `SnapshotIsolation`, `Serializable`.

- `check()` entry point: returns `Result<Witness, Error<Variable, Version>>`.
  Each consistency level produces a specific `Witness` variant on success:
  - Committed Read: `SaturationOrder(DiGraph<TransactionId>)` (committed order
    graph)
  - Atomic Read: `SaturationOrder(DiGraph<TransactionId>)` (visibility relation)
  - Causal: `SaturationOrder(DiGraph<TransactionId>)` (visibility relation)
  - Prefix: `CommitOrder(Vec<TransactionId>)` (transaction commit order)
  - Snapshot Isolation: `SplitCommitOrder(Vec<(TransactionId, bool)>)` (split
    read/write linearization)
  - Serializable: `CommitOrder(Vec<TransactionId>)` (transaction commit order)
  - Empty history: `CommitOrder(Vec::new())` (trivial witness)

- `Witness` enum: returned by `check()` on success. Variants:
  `CommitOrder(Vec<TransactionId>)` (Prefix, Serializable),
  `SplitCommitOrder(Vec<(TransactionId, bool)>)` (SnapshotIsolation),
  `SaturationOrder(DiGraph<TransactionId>)` (Committed Read, Atomic Read,
  Causal).

- `Error<Variable, Version>` enum: returned by `check()` on failure. Variants:
  `NonAtomic(NonAtomicError)` (structural issue like uncommitted writes),
  `Invalid(Consistency)` (violates consistency level, no specific pair known --
  used by linearization failures),
  `Cycle { level: Consistency, a:
  TransactionId, b: TransactionId }` (cycle
  detected with a conflicting edge pair -- used by saturation checkers:
  Committed Read, Atomic Read, Causal).

- `check_committed_read()` returns `Result<DiGraph<TransactionId>, Error>` --
  the committed order graph on success.

## Ignored Directories

`.sisyphus/` and `.omc/` are in `.gitignore`. Do NOT commit anything from those
directories. They contain orchestration state, plans, notepads, and agent
memory.

## Performance Decisions

- Zobrist hashing (`constrained_linearization.rs`): uses `HashSet<u128>` with
  per-variable random u128 seeds for O(1) DFS memoization. Replaces
  `HashSet<BTreeSet<TransactionId>>` which had O(T log T) hash cost.

- Chain closure (`atomic/mod.rs`): computes session-order transitive closure
  with an O(S * T^2) forward scan grouped by session. Replaces general
  `closure()` (O(V*(V+E))). For chain graphs these are equivalent.

- Visibility adjacency pre-fetch (`saturation/causal.rs`): pre-fetches adjacency
  sets in `causal_ww()` and `causal_rw()` hot paths to avoid repeated DiGraph
  clones per iteration.

## Testing

- Unit tests: `#[cfg(test)] mod tests` blocks inside `src/` files.
- Integration tests: `tests/` directories under each crate.
- `crates/core/tests/paper_polynomial.rs` -- 13 tests verifying polynomial-time
  checker correctness against known histories.
- `crates/core/benches/consistency.rs` -- 18 Criterion benchmarks (6 consistency
  levels x 3 history sizes).
- Always add tests when adding new functionality.

## AGENTS.md Update Protocol

After every merged PR, update this file to reflect what changed:

- New types, methods, or fields added to the codebase
- New conventions or patterns established
- Corrections to anything that was wrong or outdated
- New performance decisions or architectural choices

Include the AGENTS.md update in the same PR as the code change. This keeps the
file accurate as a living reference for future agents.
