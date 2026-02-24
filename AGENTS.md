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
- Deno checks: `deno task deno:ci` runs fmt, lint, and type checks. Requires
  wasmlib built first via `deno task wasmbuild`.
- Tests: `cargo test -p <crate>` or `cargo test --workspace`
- no_std check: `cargo build -p dbcop_core --no-default-features` must always
  compile.
- Workflow validation: `act --list` validates workflow structure locally before
  pushing. `act` binary is installed at /usr/bin/act.
- Security linting: `zizmor .github/workflows/` checks for security issues.
  Config: `.github/zizmor.yml` suppresses hash-pin warnings (we use tag-pinned
  refs, not SHA hashes). Fix all other zizmor lints.
- Coverage:
  `cargo llvm-cov --workspace --all-features --lcov --output-path lcov.info`.
  Requires `llvm-tools-preview` component and `cargo-llvm-cov` installed.

## CI Checks

All five must pass before merging:

1. `build` (rust.yaml) -- cargo build, clippy, test, end-to-end checks
2. `format` (rust.yaml) -- uses `actions-rust-lang/rustfmt@v1` (nightly rustfmt,
   automatic)
3. `code-quality` (code-quality.yaml) -- taplo format --check on all .toml
   files, typos spell check
4. `Deno` (deno.yaml) -- builds WASM, runs deno fmt/lint/check, runs WASM
   integration tests
5. `coverage` (coverage.yaml) -- cargo-llvm-cov + Codecov upload (requires
   `CODECOV_TOKEN` secret)

## Code Constraints

- No emoji or non-technical unicode in `.rs`, `.ts`, or `.js` files. The
  pre-commit hook enforces this. Allowed: ASCII + box-drawing (U+2500-U+259F) +
  arrows (U+2190-U+21FF) + math operators (U+2200-U+22FF). Use unicode
  box-drawing characters for diagrams in doc comments (compact style preferred).
- Preserve no_std compatibility in `dbcop_core`. Never use std-only types
  without a feature gate.
- Serde derives must be gated:
  `#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]`
- Do NOT rename existing public types (e.g. `CommittedRead` stays
  `CommittedRead`).
- Do NOT add a `Consistency::RepeatableRead` variant.
- Do NOT change the `ConstrainedLinearizationSolver` trait API.

## Pre-commit Hook (.husky/pre-commit)

Three checks run on staged files:

1. Rejects disallowed unicode in staged `.rs`, `.ts`, `.js` files. Allowed:
   ASCII + box-drawing (U+2500-U+259F) + arrows (U+2190-U+21FF) + math operators
   (U+2200-U+22FF).
2. Runs `cargo +nightly fmt --check --all` -- fails if any Rust file needs
   reformatting.
3. Runs `deno task deno:ci` -- checks Deno formatting, linting, and types
   (requires wasmlib built first).

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
      tests/
        wasm.test.ts             WASM integration tests (deno test)
  .github/workflows/
    rust.yaml                    build + format CI
    code-quality.yaml            taplo + typos CI
    deno.yaml                    Deno fmt/lint/check + WASM tests CI
  .github/zizmor.yml             zizmor config: suppress hash-pin lint (allow tag-pinned refs)
  .husky/pre-commit              ASCII check + cargo +nightly fmt + deno:ci
  taplo.toml                     TOML formatter config
  deno.json                      deno tasks: prepare, wasmbuild, deno:fmt/lint/check/ci
  rustfmt.toml                   nightly rustfmt config
```

## CLI Usage

The `dbcop` binary has four subcommands: `generate`, `verify`, `fmt`, and
`schema`.

### Verify Flags

- `--input-dir <DIR>` -- directory containing history JSON files (required)
- `--consistency <LEVEL>` -- consistency level to check (required). Values:
  `committed-read`, `atomic-read`, `causal`, `prefix`, `snapshot-isolation`,
  `serializable`.
- `--verbose` -- on PASS prints witness details (Debug format), on FAIL prints
  full error details. Output: `{filename}: PASS\n  witness: {witness:?}` or
  `{filename}: FAIL\n  error: {error:?}`.
- `--json` -- outputs one JSON object per file to stdout. On PASS:
  `{"file": "...", "ok": true, "witness": {...}}`. On FAIL:
  `{"file": "...", "ok": false, "error": {...}}`. Witness and error are
  serialized via serde (requires the `serde` feature on `dbcop_core`, which the
  CLI enables by default).

Default output (no flags): `{filename}: PASS` or `{filename}: FAIL ({error:?})`
on a single line.

### Debugging with RUST_LOG

The CLI initializes a `tracing-subscriber` that reads `RUST_LOG`. Use this to
see detailed consistency checking logs:

```bash
RUST_LOG=debug dbcop verify --input-dir ./histories --consistency serializable
RUST_LOG=dbcop_core=trace dbcop verify --input-dir ./histories --consistency causal
```

Log levels: `debug` shows checker entry/exit and results, `trace` shows
per-iteration saturation details. The `dbcop_core` crate uses `tracing::debug!`
and `tracing::trace!` for instrumentation.

### Schema Subcommand

`dbcop schema` prints the JSON Schema for the history input format to stdout.

```bash
dbcop schema > history.schema.json
```

The schema is generated at runtime from the `Transaction<u64, u64>` Rust type
via `schemars`. It describes the expected JSON format: an array of sessions,
where each session is an array of transactions.

## WASM Usage

The `dbcop_wasm` crate exposes two functions via `wasm_bindgen`:

### `check_consistency(history_json: &str, level: &str) -> String`

- `history_json`: JSON-encoded array of sessions (same format as CLI input
  files).
- `level`: one of `committed-read`, `atomic-read`, `causal`, `prefix`,
  `snapshot-isolation`, `serializable`.

Returns a JSON string with the following schema:

- On success: `{"ok": true, "witness": {...}}` -- witness is the serialized
  `Witness` enum (same structure as CLI `--json` output).
- On check failure: `{"ok": false, "error": {...}}` -- error is the serialized
  `Error` enum.
- On invalid consistency level:
  `{"ok": false, "error": "unknown consistency level"}`.
- On malformed JSON input:
  `{"ok": false, "error": "<parse error description>"}`.

### `check_consistency_trace(history_json: &str, level: &str) -> String`

Same parameters as `check_consistency`. Returns a richer JSON response including
parsed session data and graph edges.

On success:

```json
{
  "ok": true,
  "level": "serializable",
  "session_count": 3,
  "transaction_count": 12,
  "sessions": [
    [
      {
        "id": { "session_id": 1, "session_height": 0 },
        "reads": { "0": 1 },
        "writes": { "0": 2 },
        "committed": true
      }
    ]
  ],
  "witness": { "CommitOrder": [...] },
  "witness_edges": [
    [{ "session_id": 1, "session_height": 0 }, { "session_id": 2, "session_height": 0 }]
  ],
  "wr_edges": [
    [{ "session_id": 0, "session_height": 0 }, { "session_id": 1, "session_height": 0 }]
  ]
}
```

On check failure: same structure but with `"ok": false` and `"error"` instead of
`"witness"`/`"witness_edges"`. The `sessions` and `wr_edges` fields are still
present for visualization. On invalid input: `{"ok": false, "error": "..."}`.

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

- SAT checker functions (`dbcop_sat`): `check_serializable()`, `check_prefix()`,
  `check_snapshot_isolation()` all return `Result<Witness, Error>`. On
  satisfiable: the commit order is extracted from the SAT model by counting
  predecessors (for each vertex `u`, its position is the number of other
  vertices `w` where `before(w, u)` is true in the satisfying assignment).
  Serializable returns `Witness::CommitOrder`, Prefix returns
  `Witness::CommitOrder` (write-phase vertices only), Snapshot Isolation returns
  `Witness::SplitCommitOrder` (full split-phase ordering). On unsatisfiable:
  returns `Error::Invalid(Consistency::*)`.

## Ignored Directories

`.sisyphus/`, `.omc/`, and `.claude/` are in `.gitignore`. Do NOT commit
anything from those directories. They contain orchestration state, plans,
notepads, and agent memory.

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

- Communication graph decomposition (`consistency/mod.rs`, `dbcop_sat/lib.rs`):
  decomposes history by connected components of the communication graph (Theorem
  5.2 from Biswas & Enea 2019). Checks each component independently, then remaps
  and merges witnesses. Reduces DFS/SAT search space from O(n!) to O(sum of
  k_i!) where k_i are component sizes. Applied to NP-complete levels only:
  Prefix, SnapshotIsolation, Serializable.

- Incremental transitive closure (`digraph.rs`): `incremental_closure()` extends
  an existing closed graph with new edges using BFS ancestor/descendant
  cross-product. Used in causal checker to avoid O(V\*(V+E)) full closure on
  each saturation iteration.

## Testing

- Unit tests: `#[cfg(test)] mod tests` blocks inside `src/` files.
- Integration tests: `tests/` directories under each crate.
- `crates/core/tests/paper_polynomial.rs` -- 13 tests verifying polynomial-time
  checker correctness against known histories.
- `crates/core/tests/decomposition_check.rs` -- 3 tests verifying communication
  graph decomposition in NP-complete checkers (independent clusters, single
  cluster fallback, write-skew detection across components).
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
