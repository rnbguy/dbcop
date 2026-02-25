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
- MSRV: Rust 1.93.1 (set in Cargo.toml and .clippy.toml). Do not use features
  requiring a newer minimum version.
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
  `cargo llvm-cov --workspace --all-features --lcov --output-path coverage/rust.lcov`.
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
5. `coverage` (coverage.yaml) -- cargo-llvm-cov + Deno lcov + split Codecov
   uploads (flags: `rust`, `deno`; requires `CODECOV_TOKEN` secret)

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
- Raw-history validation must preserve read-your-write semantics within a
  transaction: a local read is valid only when it matches the latest preceding
  local write of that variable in the same transaction.

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
          ugraph.rs               UGraph<T> -- undirected graph for decomposition
          biconnected_component.rs  biconnected component extraction
        consistency/               check() entry point, consistency algorithms
          decomposition.rs          communication graph + biconnected decomposition
          witness.rs                Witness enum (CommitOrder, SplitCommitOrder, SaturationOrder)
          error.rs                  Error enum (NonAtomic, Cycle, Invalid)
          saturation/              saturation-based checkers (CommittedRead, AtomicRead, Causal)
            repeatable_read.rs     internal checker (not exposed via Consistency enum)
          linearization/           linearization-based checkers (Prefix, SnapshotIsolation, Serializable)
            constrained_linearization.rs  DFS engine + solver trait (1141 lines)
        history/
          raw/                     raw history types (Session, Transaction, Event)
          atomic/                  AtomicTransactionPO and AtomicTransactionHistory
      tests/                      7 integration tests + common/ helper macros
      benches/                    18 Criterion benchmarks (6 levels x 3 sizes)
    cli/                         CLI binary -- dbcop_cli
    wasm/                        WASM bindings -- dbcop_wasm
      tests/
        wasm.test.ts             WASM integration tests (deno test)
    sat/                         SAT-based NPC solvers -- dbcop_sat (rustsat + batsat)
      tests/
        cross_check.rs           DFS vs SAT agreement + differential fuzz test
      benches/
        npc_vs_sat.rs            Criterion comparison: core DFS vs SAT solvers
    parser/                      text history parser -- dbcop_parser (winnow + logos)
    testgen/                     random history generator -- dbcop_testgen
    drivers/                     database drivers -- dbcop_drivers (antidotedb, cockroachdb, galera)
  docs/
    architecture.md               crate structure, data flow, key types
    algorithms.md                 saturation, linearization, decomposition, SAT encoding
    consistency-models.md         formal definitions of all six levels
    cli-reference.md              generate and verify commands, flags, output formats
    history-format.md             JSON schema with annotated examples
    wasm-api.md                   WASM bindings API reference
    development.md                building, testing, contributing
  .github/workflows/
    rust.yaml                    build + format CI
    code-quality.yaml            taplo + typos CI
    deno.yaml                    Deno fmt/lint/check + WASM tests CI
    coverage.yaml                Rust + Deno coverage generation and Codecov upload
  codecov.yml                    Codecov status config (project/patch + rust/deno flags)
  .github/zizmor.yml             zizmor config: suppress hash-pin lint (allow tag-pinned refs)
  .husky/pre-commit              ASCII check + cargo +nightly fmt + deno:ci
  taplo.toml                     TOML formatter config
  deno.json                      deno tasks: prepare, wasmbuild, deno:fmt/lint/check/ci
  rustfmt.toml                   nightly rustfmt config
  typos.toml                     spell checker config (project-specific word list)
  wasmlib/                       pre-built WASM artifacts (.wasm, .d.ts, .js)
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
  `add_edge(from, to)`, `add_edges(&from, targets)`, `add_vertex(v)`,
  `closure()`, `topological_sort()`, `union(other)`, `is_acyclic()`,
  `to_edge_list()`, `find_cycle_edge() -> Option<(T, T)>` (returns an edge on a
  cycle via Kahn's algorithm).

- `AtomicTransactionPO` -- per-history partial order. Holds:
  `session_order: DiGraph<TransactionId>`,
  `write_read_relation: HashMap<Variable, DiGraph<TransactionId>>`,
  `wr_union: DiGraph<TransactionId>`,
  `visibility_relation: DiGraph<TransactionId>`.

- `Consistency` enum: `CommittedRead`, `AtomicRead`, `Causal`, `Prefix`,
  `SnapshotIsolation`, `Serializable`.

- `DfsSearchOptions` / `BranchOrdering`
  (`consistency/linearization/constrained_linearization.rs`): trait-level DFS
  policy for NPC solvers. Options include memoization/nogood toggles,
  legal-first frontier ordering (`prefer_allowed_first`), dominance pruning
  mode, tie-breaking mode, restart policy (`restart_max_attempts`,
  `restart_node_budget`), adaptive portfolio mode, principal variation mode, and
  branch ordering (`AsProvided`, `HighScoreFirst`, `LowScoreFirst`).

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
  frontier-state signatures for O(1) DFS memoization. Replaces
  `HashSet<BTreeSet<TransactionId>>` which had O(T log T) hash cost. Zobrist
  token generation is now solver-provider controlled via trait method
  `zobrist_value()`.

- DFS policy hooks (`constrained_linearization.rs`): the solver trait now
  exposes `search_options()`, `branch_score()`, `frontier_signature()`, and
  `should_prune()` so each NPC checker can provide branch ordering, state-aware
  memoization keys, and pruning behavior without changing the shared DFS engine.

- Legal-first move ordering (`constrained_linearization.rs`): DFS now computes
  `allow_next` once per frontier candidate and prioritizes legal moves before
  illegal ones (then applies score ordering). This is a chess-style move
  ordering optimization that reduces failed branch expansions.

- Conflict-driven branching
  (`linearization/{prefix,snapshot_isolation,serializable}.rs`): NPC solvers
  bias branch scores toward candidates that reduce outstanding dependency
  pressure (e.g., unresolved readers and active-variable releases), not just raw
  out-degree.

- State-aware memo signatures
  (`linearization/{prefix,snapshot_isolation,serializable}.rs`): memo keys now
  mix frontier hash with solver state (`active_write`, and for SI also
  `active_variable`) to reduce transposition aliasing between distinct search
  states.

- Killer/history move ordering (`constrained_linearization.rs`): DFS maintains
  per-depth killer moves and global history scores, then boosts candidate order
  using those learned statistics.

- Nogood learning (`constrained_linearization.rs`): failed signatures are stored
  and reused to prune repeated unsatisfiable states early.

- Conflict-directed backjumping (`constrained_linearization.rs`): DFS tracks
  learned jump depths per failed signature and propagates non-chronological
  backjumps when a subtree conflict is independent of the current decision.

- Frontier-dominance pruning (`constrained_linearization.rs`): for the same
  solver-state signature, if a failed frontier is a superset of the current
  frontier, the current state is pruned as dominated.

- Randomized restarts (`constrained_linearization.rs`): NPC solvers can run
  budgeted attempts with randomized tie-breaking before a final exhaustive pass
  (completeness preserved by always running the final unbounded attempt).

- Adaptive heuristic portfolio (`constrained_linearization.rs`): restart
  attempts choose among multiple ordering modes (solver-biased, frontier-heavy,
  diverse) using online per-mode stats.

- Principal variation ordering (`constrained_linearization.rs`): restart
  attempts carry forward the deepest path reached so far and prioritize that PV
  move at each depth on subsequent attempts.

- Counter-move heuristic (`constrained_linearization.rs`): DFS learns
  parent→response move pairs from successful recursion paths and boosts the
  learned reply when the same parent move appears again.

- Chain closure (`atomic/mod.rs`): computes session-order transitive closure
  with an O(S * T^2) forward scan grouped by session. Replaces general
  `closure()` (O(V*(V+E))). For chain graphs these are equivalent.

- Visibility adjacency pre-fetch (`saturation/causal.rs`): pre-fetches adjacency
  sets in `causal_ww()` and `causal_rw()` hot paths to avoid repeated DiGraph
  clones per iteration.

- Communication graph decomposition (`consistency/mod.rs`, `dbcop_sat/lib.rs`):
  decomposes history by biconnected components of the communication graph
  (Theorem 5.2 from Biswas & Enea 2019). For disjoint components, checks each
  independently then remaps/merges witnesses. For articulation-overlap
  components, falls back to a full solve so witness construction never drops
  external writer context. Applied to NP-complete levels only: Prefix,
  SnapshotIsolation, Serializable.
- Singleton NPC fast-path (`consistency/mod.rs`, `dbcop_sat/lib.rs`): when the
  projected history has exactly one session, skip DFS/SAT linearization search
  and synthesize the trivial witness from session order after causal check. Also
  applied per component during decomposition to avoid recursive re-checking and
  SAT solving for singleton components.

- Incremental transitive closure (`digraph.rs`): `incremental_closure()` extends
  an existing closed graph with new edges using ancestor/descendant
  cross-product. Ancestor traversal now uses a reverse adjacency index (updated
  as closure edges are inserted) to avoid repeated O(V\*E) reverse scans. Used
  in causal checker to avoid O(V\*(V+E)) full closure on each saturation
  iteration.
- Iterative reachability closure (`digraph.rs`): closure computation now uses an
  explicit stack instead of recursive DFS to avoid stack overflow on deep
  graphs.
- Single-pass closure-change detection (`digraph.rs`, `history/atomic/mod.rs`):
  `DiGraph::closure_with_change()` computes transitive closure and its
  change-flag together; `vis_is_trans()` now uses this directly instead of a
  separate post-closure diff scan.
- WASM text mapping deduplication (`wasm/src/lib.rs`): variable-name-to-u64
  conversion is centralized in shared helpers
  (`map_sessions_to_u64`/`parse_text_sessions_as_u64`) used by both step-init
  and text-to-json paths to prevent behavior drift.
- Writer-only `ww`/`rw` saturation (`history/atomic/mod.rs`): `causal_ww()` and
  `causal_rw()` must iterate only over writers of each variable. `wr_x` graphs
  contain reader vertices too (as `add_edge` targets); including those readers
  as `t1`/`t2` creates spurious dependencies and false cycles.

## Testing

- Unit tests: `#[cfg(test)] mod tests` blocks inside `src/` files.
- Integration tests: `tests/` directories under each crate.
- `history::atomic` includes regression tests
  `causal_ww_ignores_non_writer_vertices` and
  `causal_rw_ignores_non_writer_vertices` to prevent reader-only vertices from
  generating invalid `ww`/`rw` edges.
- `history::raw` includes local read ordering tests:
  `test_consistent_local_read_after_write` and
  `test_inconsistent_local_read_before_write`.
- `crates/core/tests/paper_polynomial.rs` -- 13 tests verifying polynomial-time
  checker correctness against known histories.
- `crates/core/tests/decomposition_check.rs` -- regression tests verifying
  communication graph decomposition in NP-complete checkers, including
  singleton-component witness preservation, biconnected-overlap witness
  de-duplication, and single-session trivial witnesses for
  Prefix/SI/Serializable.
- `crates/sat/tests/cross_check.rs` includes SAT witness regression tests for
  singleton-component preservation and single-session fast-path coverage in
  Prefix/SnapshotIsolation/Serializable, plus a bounded differential fuzz test
  against core NPC solvers (`DBCOP_DIFF_FUZZ_SAMPLES`, default 256).
- `crates/core/benches/consistency.rs` -- 18 Criterion benchmarks (6 consistency
  levels x 3 history sizes). Benchmark history generation now ensures reads
  always reference existing versions (or root version 0), so runs measure
  checker/solver behavior instead of early invalid-history rejection.
- `crates/sat/benches/npc_vs_sat.rs` -- Criterion comparison benchmark for
  Prefix/SnapshotIsolation/Serializable using randomly sampled valid histories;
  reports prebench SAT/Core average latency ratio and benchmarks both solver
  paths.
- `graph::digraph` includes regression tests for incremental closure ancestor
  propagation across batched new edges and deep-chain iterative closure.
- `wasm` includes a regression test ensuring `text_to_json_sessions` shares the
  same variable-mapping behavior as the internal text-to-u64 parser helper.
- Always add tests when adding new functionality.

## AGENTS.md Update Protocol

After every merged PR, update this file to reflect what changed:

- New types, methods, or fields added to the codebase
- New conventions or patterns established
- Corrections to anything that was wrong or outdated
- New performance decisions or architectural choices

Include the AGENTS.md update in the same PR as the code change. This keeps the
file accurate as a living reference for future agents.
