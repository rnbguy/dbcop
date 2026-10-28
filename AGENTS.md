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
- MSRV follows the root Cargo.toml `rust-version`; keep .clippy.toml aligned. Do
  not use features requiring a newer minimum version.
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

Required checks include:

1. `build` (rust.yaml) -- cargo build, clippy, test, end-to-end checks
2. `format` (rust.yaml) -- uses `actions-rust-lang/rustfmt@v1` (nightly rustfmt,
   automatic)
3. `code-quality` (code-quality.yaml) -- taplo format --check on all .toml
   files, typos spell check
4. `Deno` (deno.yaml) -- builds WASM, runs deno fmt/lint/check, runs WASM
   integration tests
5. `coverage` (coverage.yaml) -- cargo-llvm-cov + Deno lcov + split Codecov
   uploads (flags: `rust`, `deno`; requires `CODECOV_TOKEN` secret)
6. `build` (go.yaml) -- native FFI, Go vet/build/tests
7. `model-validation` (rust.yaml) -- pinned TLC campaigns, actual DFS traces,
   real Apalache integration/race checks, and pure-Go server build/wire checks

## Code Constraints

- No emoji or non-technical unicode in `.rs`, `.ts`, or `.js` files. The
  pre-commit hook enforces this. Allowed: ASCII + box-drawing (U+2500-U+259F) +
  arrows (U+2190-U+21FF) + math operators (U+2200-U+22FF). Use unicode
  box-drawing characters for diagrams in doc comments (compact style preferred).
- Preserve no_std compatibility in `dbcop_core`. Never use std-only types
  without a feature gate.
- Serde derives must be gated:
  `#[cfg_attr(feature = "serde", derive(::serde::Serialize, ::serde::Deserialize))]`
- Rust JSON boundaries use typed Serde structs and derive attributes, not
  `serde_json::Value`. CLI/WASM share `consistency::witness::serialize_witness`
  for borrowed witness serialization; saturation graphs use edge lists rather
  than transaction-keyed JSON objects.
- Library errors derive `thiserror` with typed sources/`#[from]`; CLI reports
  use `color-eyre`. Keep Clap derives for argument parsing.
- Go RPC/ITF validation uses standard `encoding/json/v2` and
  `encoding/json/jsontext`; preserve the generic RPC decoder's existing
  `encoding/json` number/error behavior. Prefer standard `maps`/`slices`
  operations without changing deep-copy ownership or rollback ordering.
- Do NOT rename existing public types (e.g. `CommittedRead` stays
  `CommittedRead`).
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
          saturation/              saturation-based checkers (CommittedRead, RepeatableRead, AtomicRead, Causal)
            repeatable_read.rs     Repeatable Read checker
          linearization/           linearization-based checkers (Prefix, SnapshotIsolation, Serializable)
            constrained_linearization.rs  DFS engine + solver trait (1141 lines)
        history/
          raw/                     raw history types (Session, Transaction, Event)
          atomic/                  AtomicTransactionPO and AtomicTransactionHistory
      tests/                      integration tests, independent references, TLC harness
      benches/                    21 Criterion benchmarks (7 levels x 3 sizes)
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
    consistency-models.md         formal definitions of all seven levels
    cli-reference.md              generate and verify commands, flags, output formats
    history-format.md             JSON schema with annotated examples
    wasm-api.md                   WASM bindings API reference
    development.md                building, testing, contributing
  .github/workflows/
    rust.yaml                    build + format + model-validation CI
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
  `committed-read`, `repeatable-read`, `atomic-read`, `causal`, `prefix`,
  `snapshot-isolation`, `serializable`.
- `--verbose` -- on PASS prints witness details (Debug format), on FAIL prints
  full error details. Output: `{filename}: PASS\n  witness: {witness:?}` or
  `{filename}: FAIL\n  error: {error:?}`.
- `--json` -- outputs one JSON object per file to stdout. On PASS:
  `{"file": "...", "ok": true, "witness": {...}}`. On FAIL:
  `{"file": "...", "ok": false, "error": "..."}`. Errors are Debug-formatted
  strings. Commit/split witnesses use serde; saturation witnesses use
  `{"SaturationOrder": [[from, to], ...]}`, matching WASM. Do not serialize a
  transaction-keyed graph directly into a JSON object: its keys are not strings.

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
- `level`: one of `committed-read`, `repeatable-read`, `atomic-read`, `causal`,
  `prefix`, `snapshot-isolation`, `serializable`.

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

- `Consistency` enum: `CommittedRead`, `RepeatableRead`, `AtomicRead`, `Causal`,
  `Prefix`, `SnapshotIsolation`, `Serializable`.

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
  - Repeatable Read: `SaturationOrder(DiGraph<TransactionId>)` (committed order
    graph)
  - Atomic Read: `SaturationOrder(DiGraph<TransactionId>)` (commit constraints)
  - Causal: `SaturationOrder(DiGraph<TransactionId>)` (commit constraints)
  - Prefix: `CommitOrder(Vec<TransactionId>)` (transaction commit order)
  - Snapshot Isolation: `SplitCommitOrder(Vec<(TransactionId, bool)>)` (split
    read/write linearization)
  - Serializable: `CommitOrder(Vec<TransactionId>)` (transaction commit order)
  - Empty history: `CommitOrder(Vec::new())` (trivial witness)

- `Witness` enum: returned by `check()` on success. Variants:
  `CommitOrder(Vec<TransactionId>)` (Prefix, Serializable),
  `SplitCommitOrder(Vec<(TransactionId, bool)>)` (SnapshotIsolation),
  `SaturationOrder(DiGraph<TransactionId>)` (Committed Read, Repeatable Read,
  Atomic Read, Causal).

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

- Failed-signature learning (`constrained_linearization.rs`): caches whole
  failed signatures, not generalized conflict clauses. Cached jump depths do not
  constitute an explanation graph or first-UIP learning.

- Failed-frontier caching (`constrained_linearization.rs`): the
  `dominance_pruning` option requires both the same solver-state signature and
  equal frontiers. A failed superset does not imply that a smaller frontier
  fails: the smaller frontier may have already completed work that the larger
  one cannot place. Keep distinct failed frontiers instead of subsuming subsets.

- Randomized restarts (`constrained_linearization.rs`): NPC solvers can run
  budgeted attempts with randomized tie-breaking before a final exhaustive pass
  (completeness preserved by always running the final unbounded attempt).

- Heuristic portfolio (`constrained_linearization.rs`): supports online per-mode
  stats, but the default two restarts plus final attempt only try the three
  untried ordering modes; adaptive reselection requires further attempts.

- Principal variation ordering (`constrained_linearization.rs`): restart
  attempts carry forward the deepest path reached so far and prioritize that PV
  move at each depth on subsequent attempts.

- Counter-move table (`constrained_linearization.rs`): records parent/response
  pairs only while a successful result immediately unwinds the search. It does
  not currently guide later decisions within that invocation.

- Chain closure (`atomic/mod.rs`): computes session-order transitive closure
  with an O(S * T^2) forward scan grouped by session. Replaces general
  `closure()` (O(V*(V+E))). For chain graphs these are equivalent.

- Fixed causal premises (`saturation/causal.rs`, `incremental.rs`): derive
  writer ordering once from `(so union wr)+`, never from inferred commit
  constraints. Incremental causal reachability and commit closure have separate
  reversible journals. Atomic Read uses fixed `so union wr` without closure.
- Split-phase dependencies (`linearization/`, `sat/src/lib.rs`):
  session/write-read edges target read phases; commit-only ordering targets
  write phases. The WASM causal stepper derives writer ordering only once and
  labels it `phase: "co"`.

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
  as closure edges are inserted) to avoid repeated O(V\*E) reverse scans. The
  persistent checker maintains causal and commit closures separately.
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
- `crates/core/benches/consistency.rs` -- 21 Criterion benchmarks (7 consistency
  levels x 3 history sizes). Benchmark history generation now ensures reads
  always reference existing versions (or root version 0), so runs measure
  checker/solver behavior instead of early invalid-history rejection.
- `crates/sat/benches/npc_vs_sat.rs` measures seeded SAT and causal-compatible
  UNSAT families for Prefix/SI/Serializable through both public core and SAT
  paths. `DBCOP_BENCH_SEED` defaults to 42 and is always printed; invalid values
  fail. Construction and independent witness/obstruction checks are untimed.
  Cases cover two/four sessions, not worst-case hardness or heuristic ablations.
- `crates/core/tests/fixtures/apalache.json` freezes the saved
  273-history/prefix corpus with seven independently derived labels. SAT
  saved-corpus tests check all core/reference labels and the three SAT APIs. The
  real `TestApalacheSavedCorpusAgreement` checks all seven production
  commit-admission levels; empty initial histories use an isolation-neutral
  empty commit. RC has no negative cases in this particular corpus.
- `graph::digraph` includes regression tests for incremental closure ancestor
  propagation across batched new edges and deep-chain iterative closure.
- `wasm` includes a regression test ensuring `text_to_json_sessions` shares the
  same variable-mapping behavior as the internal text-to-u64 parser helper.
- `crates/sat/tests/si_frontier_regression.rs` covers a deterministic interval
  search and a minimized known-serial history formerly rejected at SI by
  failed-superset frontier pruning.
- Always add tests when adding new functionality.

## Generic MySQL Mockserver

- `mockserver/` is a separate Go module over `go-mysql-server`, not a TPC-C
  implementation. Keep clients, benchmark source, and dependencies unmodified.
- `backend.NewEngine(provider)` installs the indexed-predicate analyzer hook.
  Native storage interfaces provide foreign keys, prefix indexes, and
  auto-increment IDs; SQL parsing and execution remain upstream.
- Start in conventional initialization mode, load through ordinary SQL, then
  `CALL dbcop_seal_initial_state()` once to establish initial KV values before
  weak execution. The store and persistent Rust oracle are process-local.
- Build the Rust FFI in release mode and Go with `-tags dbcop_release` for
  benchmarks. Clear inherited CGO link flags and set `LD_LIBRARY_PATH` to the
  same release directory; compilation is outside measured execution.
- `cmd/mockserver-apalache` builds with `CGO_ENABLED=0` and reuses the generic
  SQL/storage implementation. It requires an operator-provisioned v0.62.4
  explorer (`--apalache-rpc`, `--apalache-timeout`), never a Rust fallback. The
  legacy native `NewStore` constructor explicitly panics without cgo.
- `backend.Explorer.Step` consumes copied committed history and the ordered live
  prefix through `StepRequest`; `ReadSource.Local` uses a one-based staged write
  ordinal, not a committed version. `Store.CommittedHistory()` deep-copies
  transaction/event metadata without application values.
- The alternate backend takes one solver-selected successor, not native
  uniform/sparse sampling. `Admissions().Admitted=-1` means not enumerated.
  Latest-admitted overrides still query the model newest-to-oldest.
- Each operation loads a fresh model, records its selected source, and disposes
  every known RPC session before staging/publication. Auxiliary ranks are not
  observations. Cleanup failure or caller cancellation invalidates success;
  UNKNOWN/transport errors are SQL execution errors, never isolation rejection.
  Client deadlines cannot stop server rewriting; lost load responses can leave
  unknown sessions. Keep the JVM isolated and operator-managed.
- `mockserver/benchbase/` contains standard seeded client configurations for
  TPC-C, SmallBank, Voter, and Wikipedia. Voter selects unchanged upstream
  generic DDL because the MySQL DDL's `DROP VIEW ... CASCADE` is unsupported.
  Stock BenchBase output does not check the paper's A1-A17 invariants.
- Unique-key checks use historical equality postings to narrow candidates, then
  compare current rows. `Txn.latest` indexes staged cells without changing event
  order; statement rollback rebuilds it and drain/discard clears it.
- Mixed text/numeric predicates retain the engine predicate and bypass
  incompatible text-index ranges. Keep column-type coercion for stored rows and
  uniqueness checks; rejecting numeric posting keys there drops valid conflicts.
- Explicit AUTO_INCREMENT updates advance the allocator for larger values and
  must not rewind it for smaller values.
- Physical candidate-key order is not SQL sort order. `tableIndex` reports
  unordered access; `NewEngine` disables v0.20's `replaceIdxSort` rule, which
  drops ascending sorts even on unordered lookups. Keep ORDER BY/LIMIT coverage.
- Empty JDBC setup transactions release execution ownership between commands,
  retaining their transaction state. The next command reacquires ownership;
  transactions with observations or writes retain it until commit/rollback.
- Keyless updates/deletes retain physical keys for selected historical rows
  during the mutation statement. Identical rows remain interchangeable because
  the engine supplies row values rather than an explicit physical row ID.
- `cmd/assertions` evaluates A1-A17 externally over latest-admitted MySQL reads.
  `benchbase/run_assertions.go` checks the initialized baseline before sealing
  and counts final assertion outcomes separately from workload SQL errors.
  Validation errors and invalid baselines are never zero-violation runs.
- SQL runner phases keep stdout in `<phase>.log` and diagnostics in
  `<phase>.stderr.log`; never parse combined client output as Wikipedia's
  numeric history precondition.
- `TestActualBenchBaseWorkflowRetainsValidatedOutcomes` uses real unchanged Java
  clients when `DBCOP_BENCHBASE_MYSQL_DIR`, `DBCOP_NATIVE_SERVER`, and
  `DBCOP_ASSERTIONS_BIN` are provided; `java` and `mysql` must be on `PATH`.
  Missing all paths skips it, while partial provisioning fails.
- SmallBank A13 requires conserving workloads and balances initialized to 2500.
  Compare a separately repaired Amalgamate client against the stock client with
  Amalgamate excluded; the isolated client patch and actual-Java sequential
  regression are in `mockserver/benchbase/`. No app-specific backend changes.
- Maintained BenchBase Voter's A14 cap is 1000 (explicitly selected), not the
  paper artifact's 2. A11 preserves the artifact's executable threshold of 2; A7
  checks every line rather than reproducing its last-line overwrite.
- Native read selection defaults to uniform over admitted versions. The server
  and assertion runner also accept
  `--read-policy sparse|age-stratified|bounded-deviation`. Sparse uses
  `--deviation-probability 0.01` to choose an older admitted source.
  Age-stratified balances disjoint root, oldest, newest, and middle buckets,
  deduplicating singleton categories and never inferring root from index zero.
  Bounded-deviation caps sparse choices per SQL transaction with
  `--deviation-budget 1` (nonnegative); statement/savepoint rollback restores
  budget alongside the semantic prefix, and transaction end resets it. Replay
  and latest-admitted overrides do not spend budget; admission is unchanged.
  Outcomes retain policy/probability/budget alongside seeds, which do not
  reproduce concurrent JDBC scheduling.
- `mockserver/exploration` exposes `Guided` corpus mutation and lazy
  `Systematic` admitted-choice enumeration over fresh deterministic workload
  callbacks. Callers own oracle admission and transaction ordering; replay must
  honor supplied candidate indices and use newest admitted choices afterward.
  Session/variable-normalized read-from features are a coverage abstraction, not
  an equivalence reduction. Only exhausted systematic search is complete.
  `backend/exploration_test.go` compares uniform, sparse, and guided coverage
  against bounded references across all seven levels and replays guided corpora.
- `backend/sql_replay.go` and `sql_replay_trace.go` execute controlled whole SQL
  transactions and record semantic read sources/candidate sets. Physical SQL
  table namespaces are process-global; canonical keys use fixture-local table
  generations. Artifact raw variables/write versions are canonical, not runtime
  IDs. Strict replay fails closed on missing/extra/changed decisions.
- `cmd/replay` provides run/replay/minimize/explore/replay-corpus with strict
  versioned JSON, typed rows, baseline validation, latest-admitted rollback
  assertions, and exclusive synced artifact publication. `SQLWorkloadRunner`
  connects to generic explorers. Reduction preserves the assertion fingerprint
  and retained provenance, not application logic; statement deletion can change
  the root cause.
- `ExploreSQLWorkload` retains passing/failing SQL executions and interrupted
  prefixes with stable admitted-source universes. Only exhausted systematic
  search is complete. Strict corpus replay re-executes SQL and verifies outcomes
  and partial-work errors; transaction bounds reject rather than truncate SQL.
  Full fixture, session/transaction order, and SQL must be fixed. Arbitrary
  concurrent BenchBase control flow/results are not captured by these APIs.
- SQL replay reports attempted/committed/rolled-back/rejected/completed counts
  and workload-only elapsed time. Completed means successful commit or explicit
  rollback, never rejection. Equal-completed-work comparisons require matching
  counters; unchanged BenchBase runs remain separate equal-time experiments.
- `--collect-coverage` captures `CALL dbcop_coverage()` before final assertions.
  Its committed-history motifs normalize session roles and variable/source
  relationships without application values or fresh version IDs. First-discovery
  transaction positions are metadata, not feature identity. Aborted/live
  prefixes are excluded; backend commits are not assumed to be client
  completions.
- Exact DECIMAL runtime values must not be converted through inaccurate DOUBLE
  aggregate metadata. The upstream engine's Close returns context.Canceled from
  cancelling its own background context; other close errors remain failures.
- Named savepoints use ordered case-insensitive transaction checkpoints.
  Rollback retains the target and invalidates later names; statement rollback
  removes checkpoints beyond restored event/write/read/member lengths. Restore
  rebuilds latest writes, clears keyless statement identities, and resets native
  admission to the retained prefix. End-of-transaction clears all checkpoints.
- `sql_semantic_matrix_test.go` compares supported latest SQL with upstream
  memory storage. `sql_semantic_reference_test.go` exhausts six weak-read
  fixtures at all seven levels and compares indexed/full-candidate-scan result,
  history, and contextual admission sets. Private per-store candidate hooks are
  test-only, not a server option. Explicit absent-point probes are required.

## Independent Checker Validation

- `crates/core/tests/common/axiomatic_reference.rs` is shared test-only source
  for core and SAT integration tests. It scans raw events and evaluates the
  paper's axioms by total-order enumeration, without production graph or checker
  helpers. The completed-committed domain has at most two sessions, three total
  transactions, four total events, and two variables.
- `crates/core/tests/incremental_reference.rs` compares source-available arrival
  orders, candidate trials/acceptance, rejection/reset, and successor admission
  with the independent reference and fresh incremental instances.
- `ReplicaTraces.tla` and `backend/apalache_trace_*_test.go` generate complete
  bounded replica histories through the real Apalache server, without isolation
  admission. Fixed-capacity maps keep symbolic storage bounded; `Result`
  projects completed histories rather than schedules. Generation has a shared
  ten-second first-model budget and ten seconds per later model, with separate
  five-second cleanup. Adapt depth/history bounds only after safe disposal;
  timeouts and incomplete enumeration never certify exhaustion. Generated
  histories and persistent trial/reset/commit prefixes use the independent
  TLC/core/reference/witness bridge. The Zobrist search policy is unchanged.
- RC commit constraints include earlier external reads of any variable when
  their source also writes the current variable. Initial reads participate;
  reverting behind an observed writer is not permitted. Live incremental RC/RR
  retains all previously observed sources, not only the latest source per
  variable. Empty sessions must not add phantom witness vertices.
- The raw `committed=false` flag is not a fully specified aborted-transaction
  axiomatic model. Batch explicit write-version zero and the incremental API's
  reserved zero are different input contracts. See `docs/development.md`.
- `mockserver/apalachebackend/spec/Isolation.tla` is the canonical pure TLA+
  oracle, independent of production graphs and the Rust reference. Online raw
  write-event projections are validated for exact coverage/content/positions and
  uniqueness in TLA before shared predicates use them. Go never derives
  SO/WR/CO/reachability or validity answers.
- Online `ReadFacts`, `WriterFacts`, and dependency edges are TLA-derived typed
  cells, not Go-computed relations. Only RC/RR premises depend on read position;
  evaluate the other premises once per competing-writer/reader pair. Repeating
  SI shared-write joins per read can exhaust the symbolic rewriter. Iterate the
  axiom over concrete raw transaction/event domains, not symbolic read-record
  sets; keep all positions and source/writer checks unchanged.
- `crates/core/tests/tlc_model.rs` contains explicitly ignored finite semantic,
  replica, simulation and incremental gates. Require the pinned v1.7.4 TLC JAR;
  explicit invocation without its runtime fails. Standalone campaign commands
  skip only `tlc_go_histories`, the input-dependent adapter invoked by Go tests.
  See `docs/development.md` for commands, hashes and bounds.
- `linearization/tlc_tests.rs` captures actual solver/search/undo boundaries
  under `cfg(test)` only. `SearchTrace.tla` requires existential matching of the
  entire execution and its outer terminal result; one dead matching branch does
  not imply rejection. Semantic mutations and incomplete traces fail.
- Replica campaigns use one ordered session per replica. Finite agreement does
  not prove polynomial runtime for the hashed/heuristic implementation, nor does
  a physical replica bound alone bound arbitrary client sessions.
- Real-service Go SQL tests validate observed histories through TLC, the Rust
  axiomatic reference, core witnesses and native admission. TLC and Apalache
  share the model, so their agreement alone is not independent axiom evidence.
  `BenchmarkNativeApalacheSQL` includes per-operation load/SMT/RPC/disposal
  cost; initialization and verification are untimed, and source distributions
  differ.

## AGENTS.md Update Protocol

After every merged PR, update this file to reflect what changed:

- New types, methods, or fields added to the codebase
- New conventions or patterns established
- Corrections to anything that was wrong or outdated
- New performance decisions or architectural choices

Include the AGENTS.md update in the same PR as the code change. This keeps the
file accurate as a living reference for future agents.
