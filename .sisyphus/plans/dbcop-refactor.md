# DBCop — Three-Pass Refactoring Plan

## TL;DR

> **Quick Summary**: Refactor the dbcop Rust workspace in three sequential passes: (A) low-risk module renames and structural clarity within `dbcop_core`; (B) domain-driven restructure with a unified API entry point, working CLI, and wired-up wasm; (C) feature-flag capability tiers, a pure-Rust SAT backend for SI+Serializable, and a new `dbcop_drivers` crate for real database clients.
>
> **Deliverables**:
> - Pass A: All modules renamed, solvers grouped by strategy, zero logic changes, all 18 tests still pass
> - Pass B: `consistency::check()` unified API, working `dbcop_cli` (generate + verify), wasm wired to core, integration tests written
> - Pass C: Cargo feature flags for capability tiers, `dbcop_sat` with splr for SI+Serializable, new `dbcop_drivers` crate scaffold with MySQL/Postgres/MongoDB/AntidoteDB/CockroachDB
>
> **Estimated Effort**: XL (three passes, sequential)
> **Parallel Execution**: YES — within each pass, independent tasks run in parallel
> **Critical Path**: A1 (baseline tests) → A2–A4 (renames) → B1 (unified API) → B2 (CLI) → B3 (wasm) → C1 (splr research) → C2 (SAT backend) → C3 (drivers scaffold)

---

## Context

### Original Request
"Refactor the codebase to come up with a better project structure. Check the readme. Compare with oopsla-2019 branch too."

### Interview Summary
**Key Discussions**:
- Option A (rename only): Low-risk structural clarity pass
- Option B (domain restructure + CLI): Unified entry point, working CLI with generate+verify, wasm wiring
- Option C (feature flags + SAT + drivers): Capability tiering, pure-Rust SAT via `splr`, `dbcop_drivers` crate
- All three in sequence (A → B → C)
- CLI serialization: both JSON and bincode, feature-flagged
- SAT backend scope: SI + Serializable only (Prefix explicitly excluded)
- DB drivers: separate `dbcop_drivers` crate, not in core or cli
- `no_std` must be preserved in `dbcop_core` throughout

**Research Findings**:
- `non_atomic/` is the raw input representation (misleadingly named)
- Two distinct solver strategies: saturation-based (4 solvers) vs linearization-based (3 solvers)
- `dbcop_cli` does NOT depend on `dbcop_core` currently — must be added in Pass B
- 18 unit tests exist in-module; zero integration tests (file is empty)
- `check_repeatable_read` returns `()`, not `AtomicTransactionPO` — it's a validation step, not a visibility builder
- SI uses `(TransactionId, bool)` vertex — more complex than Serializable's `TransactionId`

### Metis Review
**Identified Gaps** (addressed in this plan):
- Empty integration test file — add baseline test in Pass A Task 1 before any renames
- CLI missing `dbcop_core` dependency — explicitly in Pass B Task 1 scope
- `splr` no_std compatibility unverified — research task added as Pass C Task 1
- `prefix` solver scope: explicitly excluded from SAT backend
- No API surface documentation — inline doc task added

---

## Work Objectives

### Core Objective
Transform dbcop from a well-structured core with scaffolding periphery into a fully coherent workspace where naming reflects domain intent, a unified API enables all frontends (CLI, wasm, egui), and optional capability tiers (SAT, DB drivers) are cleanly expressed via Cargo features.

### Concrete Deliverables
- `dbcop_core/src/history/raw/` (renamed from `non_atomic/`)
- `dbcop_core/src/consistency/` (renamed from `solver/`) with `saturation/` + `linearization/` subgroups
- `consistency::check(sessions, Consistency) -> Result<(), Error>` unified entry point
- Working `dbcop_cli` binary with `generate` and `verify` subcommands
- `dbcop_wasm` wired to call `consistency::check`
- Cargo feature flags: `non-atomic`, `atomic`, `partial-order`, `sat`, `serde`
- `dbcop_sat` with `splr` backend for SI + Serializable
- `dbcop_drivers` crate scaffold with trait + per-DB modules

### Definition of Done
- [ ] `cargo test --workspace` passes (18+ unit tests, new integration tests)
- [ ] `cargo build --workspace` passes with no warnings
- [ ] `cargo build -p dbcop_core --no-default-features` compiles (no_std preserved)
- [ ] `dbcop generate --help` and `dbcop verify --help` show correct usage
- [ ] `dbcop_wasm` exposes `check_consistency(json_history, level) -> bool` to JS

### Must Have
- All 18 existing unit tests pass after every rename step
- `no_std` in `dbcop_core` never broken (verified by `cargo build --no-default-features`)
- Unified `check()` API before CLI is wired (B1 before B2)
- Baseline integration test before any renames (A1 is first)

### Must NOT Have (Guardrails)
- Do NOT change solver algorithm logic during renames (Pass A is structural only)
- Do NOT add `std` imports to `dbcop_core` for any reason
- Do NOT implement SAT backend for `prefix` (explicitly out of scope)
- Do NOT put DB driver code in `dbcop_core` or `dbcop_cli`
- Do NOT create a second `verify` binary in `dbcop_testgen` — CLI owns verification
- Do NOT use `minisat` or C bindings for SAT — `splr` only (pure Rust)
- Do NOT bloat CLI with DB connection logic — that goes in `dbcop_drivers`

---

## Verification Strategy

> **ZERO HUMAN INTERVENTION** — ALL verification is agent-executed.

### Test Decision
- **Infrastructure exists**: YES (cargo test, 18 unit tests in-module)
- **Automated tests**: Tests-after (each pass must not break existing tests; new tests added)
- **Framework**: Rust built-in (`cargo test`)
- **No TDD**: Refactoring passes don't lend themselves to RED-GREEN; instead verify each rename doesn't break compilation or tests

### QA Policy
Every task MUST include agent-executed QA scenarios (see TODO template below).
Evidence saved to `.sisyphus/evidence/task-{N}-{scenario-slug}.{ext}`.

- **Compilation**: Use Bash (`cargo build`) — assert exit 0, zero errors
- **Tests**: Use Bash (`cargo test`) — assert all tests pass
- **CLI**: Use Bash (`cargo run -- [subcommand] [args]`) — assert output
- **Feature flags**: Use Bash (`cargo build --no-default-features`) — assert compiles

---

## Execution Strategy

### Parallel Execution Waves

```
PASS A — Low-Risk Renames (sequential within A, ordered to preserve compilability)

Wave A1 (Foundation — must be first):
└── Task A1: Write baseline integration test before any renames

Wave A2 (Parallel renames — all independent file moves):
├── Task A2: Rename non_atomic/ → raw/ in history/
├── Task A3: Group solvers into saturation/ and linearization/ subdirs
└── Task A4: Move Consistency enum into consistency/ module (pre-name)

Wave A3 (Verification):
└── Task A5: Verify full test suite + no_std after all A renames

---

PASS B — Domain Restructure + CLI + WASM (after Pass A complete)

Wave B1 (Foundation):
├── Task B1: Rename solver/ → consistency/, add check() unified entry point
└── Task B2: Add dbcop_core dependency to dbcop_cli

Wave B2 (Parallel — B1 must finish first):
├── Task B3: Implement dbcop_cli generate subcommand
├── Task B4: Implement dbcop_cli verify subcommand
└── Task B5: Wire dbcop_wasm to dbcop_core check()

Wave B3 (Integration verification):
└── Task B6: Write integration tests for CLI and wasm + API docs

---

PASS C — Feature Flags + SAT + Drivers (after Pass B complete)

Wave C1 (Research first):
└── Task C1: Verify splr no_std compatibility + encode SI/Serializable constraints

Wave C2 (Parallel after C1):
├── Task C2: Add Cargo feature flags (capability tiers)
├── Task C3: Implement dbcop_sat with splr for Serializable
└── Task C4: Implement dbcop_sat for SnapshotIsolation

Wave C3 (Drivers scaffold, parallel with C2):
└── Task C5: Create dbcop_drivers crate with trait + per-DB module stubs

Wave C4 (Final verification):
└── Task C6: Verify feature-flag matrix and drivers crate builds

Critical Path: A1 → A2-A4 → A5 → B1+B2 → B3+B4+B5 → B6 → C1 → C2+C3+C4+C5 → C6
```

### Agent Dispatch Summary
- **Wave A1**: Task A1 → `quick`
- **Wave A2**: Tasks A2-A4 → `quick` (x3, parallel)
- **Wave A3**: Task A5 → `quick`
- **Wave B1**: Tasks B1-B2 → `unspecified-high` (x2, parallel)
- **Wave B2**: Tasks B3-B5 → `unspecified-high` (x3, parallel)
- **Wave B3**: Task B6 → `unspecified-high`
- **Wave C1**: Task C1 → `deep`
- **Wave C2**: Tasks C2-C4 → `unspecified-high` (x3, parallel)
- **Wave C3**: Task C5 → `unspecified-high`
- **Wave C4**: Task C6 → `quick`

---

## TODOs

---

### PASS A — Low-Risk Renames

- [ ] A1. **Write Baseline Integration Test (Pre-Rename Anchor)**

  **What to do**:
  - Create `dbcop_core/tests/consistency.rs` (currently 1 newline — empty)
  - Write one end-to-end test per solver: build a minimal `Vec<Session<u64,u64>>`, call each `check_*` function, assert `Ok(())`  or `Err(...)` for a known-good/bad history
  - Use the test data pattern already in `causal.rs` inline tests as a reference (7-session chain pattern)
  - Cover all 6: `committed_read`, `repeatable_read`, `atomic_read`, `causal`, `prefix`, `serializable` (and SI if feasible)
  - Do NOT change any implementation code — test only

  **Must NOT do**:
  - Do not add `std` to `dbcop_core` — tests can use `std` via `#[cfg(test)]` but not the library code
  - Do not change any solver logic
  - Do not add new public API — call existing `check_*` functions directly

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: none needed

  **Parallelization**:
  - **Can Run In Parallel**: NO — must be first
  - **Parallel Group**: Wave A1 (sole task)
  - **Blocks**: A2, A3, A4
  - **Blocked By**: Nothing — start immediately

  **References**:
  - `dbcop_core/src/solver/causal.rs` — inline test at bottom: 7-session chain, how to construct Session<u64,u64> and call check_causal_read
  - `dbcop_core/src/history/non_atomic/types.rs` — Event::Read, Event::Write, Transaction, Session type aliases
  - `dbcop_core/src/solver/committed_read.rs` — `check_committed_read(histories: &[Session<V,V>]) -> Result<(), Error>`
  - `dbcop_core/src/solver/repeatable_read.rs` — `check_repeatable_read`
  - `dbcop_core/src/solver/atomic_read.rs` — `check_atomic_read`
  - `dbcop_core/src/solver/causal.rs` — `check_causal_read`
  - `dbcop_core/src/solver/prefix.rs` — `PrefixConsistencySolver` (needs to be instantiated)
  - `dbcop_core/src/solver/serializable.rs` — `SerializabilitySolver`

  **Acceptance Criteria**:
  - [ ] `dbcop_core/tests/consistency.rs` has at least 6 test functions (one per solver)
  - [ ] `cargo test -p dbcop_core` passes (18 original + new integration tests)

  ```
  Scenario: Integration tests compile and pass
    Tool: Bash (cargo)
    Preconditions: No code changes, only new test file
    Steps:
      1. Run: cargo test -p dbcop_core 2>&1 | tee .sisyphus/evidence/taskA1-tests.txt
      2. Assert: exit code 0
      3. Assert: output contains "test result: ok"
    Expected Result: All tests pass, new integration tests included in count
    Failure Indicators: Compilation error, any test failure
    Evidence: .sisyphus/evidence/taskA1-tests.txt
  ```

  **Commit**: YES — `test(core): add baseline integration tests before structural renames`

---

- [ ] A2. **Rename `non_atomic/` → `raw/` in `history/`**

  **What to do**:
  - Move `dbcop_core/src/history/non_atomic/` → `dbcop_core/src/history/raw/`
  - Update `dbcop_core/src/history/mod.rs`: `pub mod non_atomic` → `pub mod raw`
  - Add `pub use raw as non_atomic;` backwards-compat alias (or update all internal references instead)
  - Search all files for `use dbcop_core::history::non_atomic` and update import paths
  - Update all references within the same crate: `history::non_atomic::` → `history::raw::`
  - The `Session`, `Transaction`, `Event`, `EventId` types stay identical — only the module path changes
  - Add a doc comment to `raw/mod.rs`: "Raw history representation — the input format for consistency verification"
  - Add a doc comment to `atomic/mod.rs`: "Processed atomic representation — derived from raw sessions for use by solvers"

  **Must NOT do**:
  - Do not rename or move any types inside the module — only the directory/module name changes
  - Do not change `atomic/` — only `non_atomic/`
  - Do not touch solver code yet

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: none

  **Parallelization**:
  - **Can Run In Parallel**: YES — with A3 and A4
  - **Parallel Group**: Wave A2 (with A3, A4)
  - **Blocks**: A5
  - **Blocked By**: A1

  **References**:
  - `dbcop_core/src/history/mod.rs` — change `pub mod non_atomic` → `pub mod raw`
  - `dbcop_core/src/history/non_atomic/` — files to move: `mod.rs`, `types.rs`, `error.rs`
  - `dbcop_core/src/history/atomic/mod.rs` — imports from non_atomic — update
  - `dbcop_core/src/solver/*.rs` — search for `non_atomic` imports and update

  **Acceptance Criteria**:
  - [ ] `dbcop_core/src/history/non_atomic/` directory no longer exists
  - [ ] `dbcop_core/src/history/raw/` exists with same files
  - [ ] `cargo build -p dbcop_core` exits 0

  ```
  Scenario: Rename compiles clean
    Tool: Bash (cargo)
    Steps:
      1. Run: cargo build -p dbcop_core 2>&1 | tee .sisyphus/evidence/taskA2-build.txt
      2. Assert: exit code 0, zero "error[" lines
    Expected Result: Clean compilation
    Evidence: .sisyphus/evidence/taskA2-build.txt

  Scenario: no_std still works after rename
    Tool: Bash (cargo)
    Steps:
      1. Run: cargo build -p dbcop_core --no-default-features 2>&1 | tee .sisyphus/evidence/taskA2-nostd.txt
      2. Assert: exit code 0
    Expected Result: no_std compilation unaffected
    Evidence: .sisyphus/evidence/taskA2-nostd.txt
  ```

  **Commit**: Groups with A3, A4 into single commit after A5

---

- [ ] A3. **Group solvers into `saturation/` and `linearization/` subdirs**

  **What to do**:
  - Create `dbcop_core/src/solver/saturation/` with: `committed_read.rs`, `repeatable_read.rs`, `atomic_read.rs`, `causal.rs`
  - Create `dbcop_core/src/solver/linearization/` with: `prefix.rs`, `snapshot_isolation.rs`, `serializable.rs`, `constrained_linearization.rs` (rename: `constrained.rs` in new dir is fine)
  - Update `dbcop_core/src/solver/mod.rs`: replace flat `pub mod` list with `pub mod saturation; pub mod linearization;` — re-export everything at the `solver` level for backwards compat: `pub use saturation::{committed_read, repeatable_read, ...}; pub use linearization::{prefix, ...}`
  - Move `error.rs` to remain in `solver/` root (not in a subdir)
  - Add doc comments to each subdir `mod.rs`:
    - saturation/: "Saturation-based checkers: add visibility edges iteratively to fixpoint"
    - linearization/: "Linearization-based checkers: DFS over valid transaction orderings"

  **Must NOT do**:
  - Do not change any solver algorithm logic — only move files
  - Do not remove the `solver` module name — only add internal structure
  - Do not yet rename `solver/` → `consistency/` — that is Pass B

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: none

  **Parallelization**:
  - **Can Run In Parallel**: YES — with A2 and A4 (no shared files)
  - **Parallel Group**: Wave A2
  - **Blocks**: A5
  - **Blocked By**: A1

  **References**:
  - `dbcop_core/src/solver/mod.rs` — current flat module list to restructure
  - `dbcop_core/src/solver/committed_read.rs`, `repeatable_read.rs`, `atomic_read.rs`, `causal.rs` — move to saturation/
  - `dbcop_core/src/solver/prefix.rs`, `snapshot_isolation.rs`, `serializable.rs`, `constrained_linearization.rs` — move to linearization/
  - Cross-references: solvers import from each other (e.g., `repeatable_read` calls `check_committed_read`) — update internal use paths

  **Acceptance Criteria**:
  - [ ] `dbcop_core/src/solver/saturation/` exists with 4 solver files
  - [ ] `dbcop_core/src/solver/linearization/` exists with 4 files (3 solvers + constrained trait)
  - [ ] `cargo build -p dbcop_core` exits 0

  ```
  Scenario: Solver restructure compiles
    Tool: Bash (cargo)
    Steps:
      1. Run: cargo build -p dbcop_core 2>&1 | tee .sisyphus/evidence/taskA3-build.txt
      2. Assert: exit code 0
    Expected Result: Clean compilation
    Evidence: .sisyphus/evidence/taskA3-build.txt
  ```

  **Commit**: Groups with A2, A4 into single commit after A5

---

- [ ] A4. **Move `Consistency` enum into `solver/mod.rs`**

  **What to do**:
  - Cut `pub enum Consistency { ... }` from `dbcop_core/src/lib.rs`
  - Paste into `dbcop_core/src/solver/mod.rs`
  - In `lib.rs`, add: `pub use solver::Consistency;` so existing users don't break
  - Update all internal `use crate::Consistency` → `use crate::solver::Consistency` (or just `use super::Consistency` where appropriate)
  - Add a doc comment: "Consistency levels supported by dbcop, ordered from weakest (CommittedRead) to strongest (Serializable)"

  **Must NOT do**:
  - Do not add or remove variants from the enum
  - Do not change `lib.rs` public re-export (keep `pub use solver::Consistency`)

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: none

  **Parallelization**:
  - **Can Run In Parallel**: YES — with A2 and A3
  - **Parallel Group**: Wave A2
  - **Blocks**: A5
  - **Blocked By**: A1

  **References**:
  - `dbcop_core/src/lib.rs` — current location of `pub enum Consistency`
  - `dbcop_core/src/solver/mod.rs` — destination

  **Acceptance Criteria**:
  - [ ] `Consistency` no longer defined in `lib.rs` (only re-exported)
  - [ ] `cargo build -p dbcop_core` exits 0

  ```
  Scenario: Consistency enum move compiles
    Tool: Bash (cargo)
    Steps:
      1. Run: cargo build -p dbcop_core 2>&1 | tee .sisyphus/evidence/taskA4-build.txt
      2. Assert: exit code 0
    Expected Result: Clean compilation
    Evidence: .sisyphus/evidence/taskA4-build.txt
  ```

  **Commit**: Groups with A2, A3

---

- [ ] A5. **Full test suite + no_std verification (Pass A close)**

  **What to do**:
  - Run full workspace test suite
  - Run no_std build
  - Run clippy — fix any warnings introduced by renames
  - Create the Pass A commit combining A2+A3+A4 changes

  **Must NOT do**:
  - Do not fix logic bugs found — only structural issues introduced by the rename

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO — must be after A2, A3, A4
  - **Parallel Group**: Wave A3 (sole task)
  - **Blocks**: B1, B2
  - **Blocked By**: A2, A3, A4

  **References**:
  - All changed files from A2-A4

  **Acceptance Criteria**:
  - [ ] `cargo test --workspace` shows all tests pass (≥18)
  - [ ] `cargo build -p dbcop_core --no-default-features` exits 0
  - [ ] `cargo clippy --workspace` exits 0 or only pre-existing warnings

  ```
  Scenario: All tests pass after Pass A renames
    Tool: Bash (cargo)
    Steps:
      1. Run: cargo test --workspace 2>&1 | tee .sisyphus/evidence/taskA5-tests.txt
      2. Assert: "test result: ok" in output
      3. Assert: "FAILED" does not appear
      4. Run: cargo build -p dbcop_core --no-default-features 2>&1 | tee .sisyphus/evidence/taskA5-nostd.txt
      5. Assert: exit code 0
    Expected Result: All tests pass, no_std holds
    Evidence: .sisyphus/evidence/taskA5-tests.txt, .sisyphus/evidence/taskA5-nostd.txt
  ```

  **Commit**: YES — `refactor(core): rename non_atomic→raw, group solvers by strategy, move Consistency enum`
  - Pre-commit: `cargo test --workspace`

---

### PASS B — Domain Restructure + Unified API + CLI + WASM

- [ ] B1. **Rename `solver/` → `consistency/`, add `check()` unified entry point**

  **What to do**:
  - Rename `dbcop_core/src/solver/` → `dbcop_core/src/consistency/`
  - Update `lib.rs`: `pub mod solver` → `pub mod consistency`, `pub use solver::Consistency` → `pub use consistency::Consistency`
  - Add backwards-compat: `pub use consistency as solver;` in lib.rs if needed (optional — this is a breaking rename)
  - Add to `dbcop_core/src/consistency/mod.rs` a new public function:
    ```rust
    pub fn check<V, W>(sessions: &[Session<V, W>], level: Consistency) -> Result<(), Error<V, W>>
    where V: Hash + Eq + Clone + Ord + Debug, W: Clone + Eq + Debug
    ```
    This dispatches to the appropriate `check_*` function based on the `level` variant
  - Document each `Consistency` variant with a doc comment explaining what it checks
  - Add `check()` to `lib.rs` re-exports: `pub use consistency::check;`

  **Must NOT do**:
  - Do not change any solver logic — this is a rename + dispatch wrapper
  - Do not add `std` to core
  - Do not change the `Consistency` enum variants

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Requires understanding the type signatures of all 6 solvers to write correct dispatch
  - **Skills**: none

  **Parallelization**:
  - **Can Run In Parallel**: YES — with B2 (B2 only adds a Cargo.toml dep, no code overlap)
  - **Parallel Group**: Wave B1
  - **Blocks**: B3, B4, B5
  - **Blocked By**: A5

  **References**:
  - `dbcop_core/src/solver/mod.rs` → `consistency/mod.rs` destination
  - `dbcop_core/src/solver/saturation/*.rs` — function signatures: `check_committed_read(&[Session<V,V>]) -> Result<(), Error>`, etc.
  - `dbcop_core/src/solver/linearization/*.rs` — these return `Result<AtomicTransactionPO, Error>` not `Result<(), Error>` — the dispatch wrapper must normalize: map to `Ok(())` on success
  - `dbcop_core/src/solver/error.rs` — `Error<Variable, Version>` type
  - `dbcop_core/src/lib.rs` — current module declarations to update

  **Acceptance Criteria**:
  - [ ] `dbcop_core::check(sessions, Consistency::Serializable)` compiles and returns `Result<(), Error>`
  - [ ] All 6 consistency levels handled in match arm (no `_ => unimplemented!()`)
  - [ ] `cargo test -p dbcop_core` passes

  ```
  Scenario: Unified check() compiles and dispatches
    Tool: Bash (cargo)
    Steps:
      1. Run: cargo build -p dbcop_core 2>&1 | tee .sisyphus/evidence/taskB1-build.txt
      2. Assert: exit code 0
      3. Run: cargo test -p dbcop_core 2>&1 | tee .sisyphus/evidence/taskB1-tests.txt
      4. Assert: all tests pass
    Expected Result: Unified API available, all tests pass
    Evidence: .sisyphus/evidence/taskB1-build.txt, .sisyphus/evidence/taskB1-tests.txt

  Scenario: check() returns Err for known-bad history
    Tool: Bash (cargo test)
    Steps:
      1. In integration test, construct a history that violates Serializable
      2. Assert: check(sessions, Consistency::Serializable) returns Err(...)
    Expected Result: Error returned, not panic
    Evidence: .sisyphus/evidence/taskB1-tests.txt (same run)
  ```

  **Commit**: Groups with B2-B6 into Pass B commit

---

- [ ] B2. **Add `dbcop_core` dependency to `dbcop_cli`**

  **What to do**:
  - Edit `dbcop_cli/Cargo.toml`: add `dbcop_core = { workspace = true, features = ["serde"] }`
  - Add `clap = { workspace = true, features = ["derive"] }` if not already present
  - Add `serde_json` and `bincode` as deps with feature flags:
    ```toml
    [features]
    default = ["json"]
    json = ["dep:serde_json", "dbcop_core/serde"]
    bincode = ["dep:bincode", "dbcop_core/serde"]
    ```
  - Add `serde_json` and `bincode` to workspace deps in root `Cargo.toml`
  - `dbcop_cli/src/lib.rs`: delete the `println!("Hello, world!")` stub

  **Must NOT do**:
  - Do not implement subcommand logic yet (that's B3/B4)
  - Do not add `dbcop_testgen` dep to cli — testgen is a library, CLI calls it directly

  **Recommended Agent Profile**:
  - **Category**: `quick`

  **Parallelization**:
  - **Can Run In Parallel**: YES — with B1 (no overlap)
  - **Parallel Group**: Wave B1
  - **Blocks**: B3, B4
  - **Blocked By**: A5

  **References**:
  - `dbcop_cli/Cargo.toml` — add dependencies
  - `Cargo.toml` (workspace root) — add serde_json, bincode to workspace.dependencies
  - `dbcop_testgen/Cargo.toml` — reference for how dbcop_core+serde is declared

  **Acceptance Criteria**:
  - [ ] `cargo build -p dbcop` compiles (even with stub main)
  - [ ] `dbcop_core` is in dbcop_cli's dependency tree

  ```
  Scenario: CLI dep compiles
    Tool: Bash (cargo)
    Steps:
      1. Run: cargo build -p dbcop 2>&1 | tee .sisyphus/evidence/taskB2-build.txt
      2. Assert: exit code 0
    Expected Result: Compiles with dbcop_core available
    Evidence: .sisyphus/evidence/taskB2-build.txt
  ```

  **Commit**: Groups with B1, B3-B6

---

- [ ] B3. **Implement `dbcop_cli` `generate` subcommand**

  **What to do**:
  - Replace `dbcop_cli/src/lib.rs` App stub with a real clap v4 CLI structure using `#[derive(Parser)]`
  - Subcommand `generate`:
    - Args: `--n-hist <N>`, `--n-node <N>`, `--n-var <N>`, `--n-txn <N>`, `--n-evt <N>`, `--output-dir <PATH>`
    - Action: call `dbcop_testgen::generate_mult_histories(n_hist, n_node, n_var, n_txn, n_evt)`
    - Serialize each History to file: `{output_dir}/{id}.json` (if json feature) or `{output_dir}/{id}.bincode` (if bincode feature)
    - Print: `Generated {n_hist} histories to {output_dir}`
  - Add `dbcop_testgen` to `dbcop_cli/Cargo.toml` deps

  **Must NOT do**:
  - Do not implement verify subcommand here (that's B4)
  - Do not put serialization logic in dbcop_core — only in cli layer

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Must integrate clap v4 derive macros, dbcop_testgen generator, and serde_json/bincode serialization
  - **Skills**: none

  **Parallelization**:
  - **Can Run In Parallel**: YES — with B4 and B5 (independent subcommands)
  - **Parallel Group**: Wave B2
  - **Blocks**: B6
  - **Blocked By**: B1, B2

  **References**:
  - `dbcop_testgen/src/generator.rs` — `generate_mult_histories(n_hist, n_node, n_var, n_txn, n_evt) -> Vec<History>`, `History` struct shape
  - `dbcop_testgen/src/lib.rs` — what's pub-exported
  - oopsla-2019 CLI design (from plan context): `generate` subcommand used nhist/nnode/nvar/ntxn/nevt args — follow same arg naming
  - clap v4 workspace dep: `clap = { version = "4.4", features = ["derive"] }` — use `#[derive(Parser, Subcommand, Args)]`
  - `serde_json::to_writer_pretty` for JSON, `bincode::serialize` for bincode

  **Acceptance Criteria**:
  - [ ] `cargo run -p dbcop -- generate --help` prints usage without panic
  - [ ] `cargo run -p dbcop -- generate --n-hist 3 --n-node 2 --n-var 5 --n-txn 3 --n-evt 4 --output-dir /tmp/dbcop-test` creates 3 files

  ```
  Scenario: generate subcommand creates history files
    Tool: Bash
    Steps:
      1. Run: mkdir -p /tmp/dbcop-test-gen
      2. Run: cargo run -p dbcop -- generate --n-hist 3 --n-node 2 --n-var 5 --n-txn 3 --n-evt 4 --output-dir /tmp/dbcop-test-gen 2>&1 | tee .sisyphus/evidence/taskB3-generate.txt
      3. Assert: exit code 0
      4. Assert: output contains "Generated 3 histories"
      5. Run: ls /tmp/dbcop-test-gen | wc -l → assert 3 files
    Expected Result: 3 history files in output dir
    Failure Indicators: panic, non-zero exit, fewer than 3 files
    Evidence: .sisyphus/evidence/taskB3-generate.txt

  Scenario: generate --help works
    Tool: Bash
    Steps:
      1. Run: cargo run -p dbcop -- generate --help 2>&1 | tee .sisyphus/evidence/taskB3-help.txt
      2. Assert: contains "--n-hist", "--n-node", "--output-dir"
    Expected Result: Usage printed
    Evidence: .sisyphus/evidence/taskB3-help.txt
  ```

  **Commit**: Groups with B1-B6

---

- [ ] B4. **Implement `dbcop_cli` `verify` subcommand**

  **What to do**:
  - Subcommand `verify`:
    - Args: `--input-dir <PATH>`, `--consistency <LEVEL>` (enum: committed-read, repeatable-read, atomic-read, causal, prefix, snapshot-isolation, serializable), `--output-dir <PATH>` (optional, for results)
    - For each file in `input-dir` matching `*.json` or `*.bincode`:
      1. Deserialize `History` from file
      2. Call `dbcop_core::check(&history.data, level)`
      3. Print: `{filename}: PASS` or `{filename}: FAIL ({error})`
    - Exit code 0 if all pass, 1 if any fail
  - Map CLI string → `Consistency` enum in argument parsing

  **Must NOT do**:
  - Do not re-implement consistency checking logic — only call `dbcop_core::check()`
  - Do not add DB connection code here

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Must wire deserialization + `check()` dispatch + error display + exit codes

  **Parallelization**:
  - **Can Run In Parallel**: YES — with B3 and B5
  - **Parallel Group**: Wave B2
  - **Blocks**: B6
  - **Blocked By**: B1, B2

  **References**:
  - `dbcop_core::check` — unified entry point added in B1
  - `dbcop_core::Consistency` enum variants — map to CLI strings (kebab-case)
  - `dbcop_testgen::generator::History` — the deserialized type (`data: Vec<Session<u64,u64>>`)
  - oopsla-2019 design: `verify` subcommand printed pass/fail per history — follow same pattern
  - `std::process::exit(1)` for failure exit code

  **Acceptance Criteria**:
  - [ ] `cargo run -p dbcop -- verify --help` prints usage
  - [ ] Given a generated history dir, `verify --consistency committed-read` exits 0
  - [ ] `verify --consistency serializable` on a known-bad history exits 1 and prints "FAIL"

  ```
  Scenario: verify PASS on valid histories
    Tool: Bash
    Preconditions: /tmp/dbcop-test-gen/ exists from B3
    Steps:
      1. Run: cargo run -p dbcop -- verify --input-dir /tmp/dbcop-test-gen --consistency committed-read 2>&1 | tee .sisyphus/evidence/taskB4-verify-pass.txt
      2. Assert: exit code 0
      3. Assert: each filename followed by "PASS"
    Expected Result: All files report PASS for committed-read
    Evidence: .sisyphus/evidence/taskB4-verify-pass.txt

  Scenario: verify FAIL exit code 1
    Tool: Bash
    Preconditions: A known-serializable-violating history JSON file exists (construct one in test)
    Steps:
      1. Run: cargo run -p dbcop -- verify --input-dir /tmp/dbcop-bad --consistency serializable; echo "exit: $?"
      2. Assert: exit code is 1
      3. Assert: output contains "FAIL"
    Expected Result: Non-zero exit on consistency violation
    Evidence: .sisyphus/evidence/taskB4-verify-fail.txt
  ```

  **Commit**: Groups with B1-B6

---

- [ ] B5. **Wire `dbcop_wasm` to `dbcop_core::check()`**

  **What to do**:
  - Add `dbcop_core` dep to `dbcop_wasm/Cargo.toml` (with `serde` feature for JSON input)
  - Replace the `greet()` stub in `dbcop_wasm/src/lib.rs` with:
    ```rust
    #[wasm_bindgen]
    pub fn check_consistency(history_json: &str, level: &str) -> bool
    ```
    - Parse `level: &str` → `Consistency` enum (return false on unknown)
    - Deserialize `history_json` → `Vec<Session<u64, u64>>` via serde_json
    - Call `dbcop_core::check(&sessions, level)`
    - Return `true` if `Ok(())`, `false` if `Err(...)`
  - Add `serde_json` dep to `dbcop_wasm/Cargo.toml` (no_std compatible via `alloc` feature)
  - Verify wasm compilation: `cargo build -p dbcop_wasm --target wasm32-unknown-unknown`

  **Must NOT do**:
  - Do not use `std` in `dbcop_wasm` — only alloc + wasm_bindgen
  - Do not expose raw Rust types to JS — only primitives and strings

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Requires wasm-bindgen API knowledge + no_std serde_json handling

  **Parallelization**:
  - **Can Run In Parallel**: YES — with B3 and B4
  - **Parallel Group**: Wave B2
  - **Blocks**: B6
  - **Blocked By**: B1

  **References**:
  - `dbcop_wasm/src/lib.rs` — current greet() stub to replace
  - `dbcop_core::check` + `dbcop_core::Consistency` — the API to call
  - `wasm_bindgen` v0.2.91 — the `#[wasm_bindgen]` attribute, JS-compatible types
  - serde_json no_std: use `serde_json` with `default-features = false, features = ["alloc"]`
  - wasm target: `wasm32-unknown-unknown`

  **Acceptance Criteria**:
  - [ ] `cargo build -p dbcop_wasm --target wasm32-unknown-unknown` exits 0
  - [ ] `check_consistency(json, "serializable")` is exported symbol in wasm

  ```
  Scenario: wasm compilation succeeds
    Tool: Bash
    Steps:
      1. Run: cargo build -p dbcop_wasm --target wasm32-unknown-unknown 2>&1 | tee .sisyphus/evidence/taskB5-wasm.txt
      2. Assert: exit code 0, no "error[" lines
    Expected Result: wasm binary builds
    Evidence: .sisyphus/evidence/taskB5-wasm.txt

  Scenario: wasm binary exports check_consistency symbol
    Tool: Bash (wasm-objdump or wasm-nm)
    Steps:
      1. Run: wasm-nm target/wasm32-unknown-unknown/debug/dbcop_wasm.wasm 2>/dev/null | grep check_consistency || strings target/wasm32-unknown-unknown/debug/dbcop_wasm.wasm | grep check_consistency
      2. Assert: "check_consistency" found in output
    Expected Result: Symbol exported
    Evidence: .sisyphus/evidence/taskB5-symbols.txt
  ```

  **Commit**: Groups with B1-B6

---

- [ ] B6. **Integration tests for CLI, wasm API, and inline API docs**

  **What to do**:
  - Expand `dbcop_core/tests/consistency.rs` with:
    - `check()` dispatch tests: for each `Consistency` variant, call `check(sessions, variant)` directly
    - Round-trip test: `generate → serialize → deserialize → check` (end-to-end)
  - Add CLI integration tests in `dbcop_cli/tests/cli.rs` using `assert_cmd` crate:
    - `generate` produces files
    - `verify` passes on valid, fails on invalid
  - Add `/// Doc comment` to every public item in `dbcop_core`: `check()`, `Consistency`, `Event`, `Transaction`, `Session`, `Error`
  - Add `dbcop_core` example in `dbcop_core/examples/basic.rs`: minimal generate+check flow

  **Must NOT do**:
  - Do not add `std` to core even in examples (use `extern crate std` in example only, not in lib)
  - Do not test internal implementation details — only public API

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Writing good integration tests + API docs requires understanding the full pipeline

  **Parallelization**:
  - **Can Run In Parallel**: NO — needs B3, B4, B5 complete
  - **Parallel Group**: Wave B3 (sole task)
  - **Blocks**: C1
  - **Blocked By**: B3, B4, B5

  **References**:
  - `dbcop_core/tests/consistency.rs` — expand this
  - `assert_cmd` crate — for CLI testing (add to dev-deps)
  - `dbcop_core/src/consistency/mod.rs` — `check()` signature
  - All public types in `dbcop_core`

  **Acceptance Criteria**:
  - [ ] `cargo test --workspace` passes including new CLI tests
  - [ ] `cargo doc -p dbcop_core` generates docs without warnings
  - [ ] `cargo run --example basic -p dbcop_core` runs without error

  ```
  Scenario: All tests pass after Pass B
    Tool: Bash
    Steps:
      1. Run: cargo test --workspace 2>&1 | tee .sisyphus/evidence/taskB6-tests.txt
      2. Assert: "test result: ok", no "FAILED"
    Expected Result: All tests pass
    Evidence: .sisyphus/evidence/taskB6-tests.txt

  Scenario: Docs generate without warnings
    Tool: Bash
    Steps:
      1. Run: cargo doc -p dbcop_core 2>&1 | tee .sisyphus/evidence/taskB6-docs.txt
      2. Assert: exit 0, no "warning: missing documentation" for public items
    Expected Result: Clean docs
    Evidence: .sisyphus/evidence/taskB6-docs.txt
  ```

  **Commit**: YES — `feat(cli,wasm,core): unified check() API, generate+verify CLI, wasm wiring, integration tests, API docs`
  - Pre-commit: `cargo test --workspace`

---

### PASS C — Feature Flags + SAT Backend + Drivers Crate

- [ ] C1. **Research `splr` no_std compatibility and encode SI/Serializable constraints**

  **What to do**:
  - Check `splr` crate (latest version on crates.io): does it support `no_std`? Does it require `std`?
  - Document the constraint encoding plan for both solvers:
    - Serializable: `allow_next(v)` = ∀ written vars of v: not in `active_write` OR exactly one reader == v
    - SI: same as Serializable PLUS `active_variable` disjointness constraint
  - Write this as a design doc in `.sisyphus/drafts/sat-encoding.md`
  - Determine: should `dbcop_sat` be `no_std` or can it use `std`? (SAT solvers typically need `std`)
  - Determine: can `dbcop_sat` be an optional `dbcop_core` feature or must it be a separate crate? (answer: separate crate is correct because it needs `std` if splr does)

  **Must NOT do**:
  - Do not write any Rust code yet — research and doc only
  - Do not start implementing SAT before verifying splr is usable

  **Recommended Agent Profile**:
  - **Category**: `deep`
    - Reason: Requires reading splr docs/source, understanding SAT encoding, making architectural decisions

  **Parallelization**:
  - **Can Run In Parallel**: NO — must precede C2-C4
  - **Parallel Group**: Wave C1 (sole task)
  - **Blocks**: C2, C3, C4
  - **Blocked By**: B6

  **References**:
  - `splr` crate: `https://crates.io/crates/splr` — check features, no_std support, API
  - `dbcop_core/src/consistency/linearization/serializable.rs` — `allow_next()` logic to encode as SAT
  - `dbcop_core/src/consistency/linearization/snapshot_isolation.rs` — additional `active_variable` constraint
  - `dbcop_core/src/consistency/linearization/constrained.rs` — `ConstrainedLinearizationSolver` trait — SAT backend must implement this same trait

  **Acceptance Criteria**:
  - [ ] `.sisyphus/drafts/sat-encoding.md` exists with: splr std requirements, encoding plan for SER, encoding plan for SI, decision on crate boundary
  - [ ] Decision made on whether `dbcop_sat` can be `no_std`

  ```
  Scenario: Research doc created
    Tool: Bash
    Steps:
      1. Run: cat .sisyphus/drafts/sat-encoding.md | wc -l
      2. Assert: > 30 lines (non-trivial doc)
    Expected Result: Design doc exists with substance
    Evidence: .sisyphus/evidence/taskC1-research.txt
  ```

  **Commit**: NO (research only, no code)

---

- [ ] C2. **Add Cargo feature flags (capability tiers)**

  **What to do**:
  - Add feature flags to `dbcop_core/Cargo.toml`:
    ```toml
    [features]
    default = ["non-atomic"]
    non-atomic = []
    atomic = ["non-atomic"]
    partial-order = ["atomic"]
    serde = ["dep:serde"]
    ```
  - Gate code in `dbcop_core` behind features:
    - `non-atomic` feature: gates `committed_read`, `repeatable_read` + raw history types (currently all unconditional)
    - `atomic` feature: gates `atomic_read`, `causal`, `AtomicTransactionPO`
    - `partial-order` feature: gates `prefix`, `snapshot_isolation`, `serializable`, `ConstrainedLinearizationSolver`
  - Update `check()` to return `Err` with a descriptive message when called with a level gated behind an unselected feature (or use compile-time cfg)
  - Update all downstream crates' deps to specify which feature tier they need:
    - `dbcop_cli`: `dbcop_core = { ..., features = ["partial-order", "serde"] }`
    - `dbcop_wasm`: `dbcop_core = { ..., features = ["partial-order", "serde"] }`
    - `dbcop_testgen`: `dbcop_core = { ..., features = ["non-atomic", "serde"] }`

  **Must NOT do**:
  - Do not gate the `graph/` module or `Consistency` enum behind features
  - Do not break `cargo build -p dbcop_core` (default features should still work)
  - Do not add a `sat` feature to `dbcop_core` — that belongs in `dbcop_sat` crate

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Feature-gating in Rust requires careful cfg attribute placement and testing all combinations

  **Parallelization**:
  - **Can Run In Parallel**: YES — with C3, C4, C5
  - **Parallel Group**: Wave C2
  - **Blocks**: C6
  - **Blocked By**: C1

  **References**:
  - `dbcop_core/Cargo.toml` — add features table
  - `dbcop_core/src/consistency/mod.rs` — add `#[cfg(feature = "partial-order")]` guards
  - `dbcop_core/src/consistency/saturation/mod.rs` — add `#[cfg(feature = "atomic")]` etc.
  - Cargo book feature flags: use `#[cfg(feature = "...")]` on `pub mod` declarations

  **Acceptance Criteria**:
  - [ ] `cargo build -p dbcop_core` (default) exits 0
  - [ ] `cargo build -p dbcop_core --no-default-features` exits 0
  - [ ] `cargo build -p dbcop_core --features partial-order` exits 0
  - [ ] `cargo test -p dbcop_core --features partial-order` passes all tests

  ```
  Scenario: Feature flag matrix compiles
    Tool: Bash
    Steps:
      1. cargo build -p dbcop_core --no-default-features 2>&1 | tee .sisyphus/evidence/taskC2-nodefault.txt → assert exit 0
      2. cargo build -p dbcop_core --features atomic 2>&1 | tee .sisyphus/evidence/taskC2-atomic.txt → assert exit 0
      3. cargo build -p dbcop_core --features partial-order 2>&1 | tee .sisyphus/evidence/taskC2-partial.txt → assert exit 0
      4. cargo test -p dbcop_core --all-features 2>&1 | tee .sisyphus/evidence/taskC2-tests.txt → assert all pass
    Expected Result: All four combinations compile and tests pass
    Evidence: .sisyphus/evidence/taskC2-*.txt
  ```

  **Commit**: Groups with C3, C4, C5 into Pass C commit

---

- [ ] C3. **Implement `dbcop_sat` with `splr` for Serializable**

  **What to do**:
  - Update `dbcop_sat/Cargo.toml`: add `splr` dep, add `dbcop_core = { ..., features = ["partial-order"] }`
  - Remove the TODO stub in `dbcop_sat/src/lib.rs`
  - Implement `SatSerializabilitySolver` that implements `ConstrainedLinearizationSolver`:
    - Instead of DFS, encode the ordering problem as SAT:
      1. Variables: `x_{i,j}` = "transaction i before transaction j"
      2. `allow_next()` constraint → SAT clause
      3. Transitivity clauses
    - Or simpler: use `SatSolver` as a drop-in for the existing DFS by replacing `get_linearization()` with a SAT solve
  - Add a public function: `check_serializable_sat(sessions: &[Session<u64,u64>]) -> Result<(), Error>`
  - Add unit tests

  **Must NOT do**:
  - Do not implement SAT for prefix (out of scope)
  - Do not make `dbcop_core` depend on `dbcop_sat` — dependency only goes one way
  - Do not use C bindings or minisat

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: SAT encoding is non-trivial; requires understanding constraint representation and splr's API

  **Parallelization**:
  - **Can Run In Parallel**: YES — with C2, C4, C5 (independent crates)
  - **Parallel Group**: Wave C2
  - **Blocks**: C6
  - **Blocked By**: C1

  **References**:
  - `.sisyphus/drafts/sat-encoding.md` — from C1: encoding plan for Serializable
  - `dbcop_core/src/consistency/linearization/serializable.rs` — `SerializabilitySolver`: `allow_next()`, `children_of()`, `active_write` — the logic to encode
  - `dbcop_core/src/consistency/linearization/constrained.rs` — `ConstrainedLinearizationSolver` trait interface
  - `splr` crate API: `Solver::new()`, `add_clause()`, `solve()` → `Ok(Certificate::SAT(model))` or `Ok(Certificate::UNSAT)`
  - `dbcop_sat/src/lib.rs` — replace stub here

  **Acceptance Criteria**:
  - [ ] `cargo build -p dbcop_sat` exits 0
  - [ ] `cargo test -p dbcop_sat` passes (SAT returns same answer as DFS solver on same inputs)

  ```
  Scenario: SAT solver gives same verdict as DFS solver
    Tool: Bash (cargo test)
    Preconditions: Test constructs 3 histories: 1 serializable, 1 not, 1 edge case
    Steps:
      1. cargo test -p dbcop_sat 2>&1 | tee .sisyphus/evidence/taskC3-tests.txt
      2. Assert: all tests pass
      3. Assert: SAT result matches DFS result for each history
    Expected Result: Agreement between solvers
    Evidence: .sisyphus/evidence/taskC3-tests.txt
  ```

  **Commit**: Groups with C2, C4, C5

---

- [ ] C4. **Implement `dbcop_sat` for Snapshot Isolation**

  **What to do**:
  - Add `SatSnapshotIsolationSolver` following same pattern as C3 but:
    - Must handle the `(TransactionId, bool)` split-phase vertex (read/write sections of each transaction)
    - Must encode `active_variable` disjointness constraint (SI-specific): "if transaction T reads variable x, no other concurrent transaction may write x"
  - Add: `check_snapshot_isolation_sat(sessions: &[Session<u64,u64>]) -> Result<(), Error>`
  - Add unit tests cross-verifying with DFS solver

  **Must NOT do**:
  - Do not skip the `active_variable` constraint — SI is different from Serializable because of it

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: SI has more complex constraint than SER; split-phase vertex must be correctly modeled in SAT

  **Parallelization**:
  - **Can Run In Parallel**: YES — with C2, C3, C5
  - **Parallel Group**: Wave C2
  - **Blocks**: C6
  - **Blocked By**: C1

  **References**:
  - `.sisyphus/drafts/sat-encoding.md` — from C1: encoding plan for SI
  - `dbcop_core/src/consistency/linearization/snapshot_isolation.rs` — `SnapshotIsolationSolver`: `active_variable`, split vertex `(TransactionId, bool)`, `allow_next()` logic
  - `dbcop_sat/src/lib.rs` — add alongside Serializable implementation
  - C3 implementation — build on same splr patterns

  **Acceptance Criteria**:
  - [ ] `cargo test -p dbcop_sat` passes (including SI tests)
  - [ ] SI SAT result matches DFS result on 3+ test histories

  ```
  Scenario: SI SAT matches DFS
    Tool: Bash (cargo test)
    Steps:
      1. cargo test -p dbcop_sat 2>&1 | tee .sisyphus/evidence/taskC4-tests.txt
      2. Assert: all tests pass, including SI-specific tests
    Evidence: .sisyphus/evidence/taskC4-tests.txt
  ```

  **Commit**: Groups with C2, C3, C5

---

- [ ] C5. **Create `dbcop_drivers` crate with trait + per-DB module stubs**

  **What to do**:
  - Create `dbcop_drivers/` directory and `dbcop_drivers/Cargo.toml`:
    ```toml
    [package]
    name = "dbcop_drivers"
    version.workspace = true
    
    [dependencies]
    dbcop_core = { workspace = true, features = ["non-atomic", "serde"] }
    dbcop_testgen = { workspace = true }
    
    [features]
    mysql = ["dep:mysql"]
    postgres = ["dep:postgres"]
    mongodb = ["dep:mongodb"]
    antidotedb = ["dep:antidotedb"]   # if crate exists, else stub
    cockroachdb = ["dep:postgres"]    # CockroachDB uses Postgres wire protocol
    
    [dependencies.mysql]
    version = "..."
    optional = true
    # ... etc
    ```
  - Add `dbcop_drivers` to workspace `Cargo.toml` members
  - Create `dbcop_drivers/src/lib.rs` with:
    ```rust
    pub trait DbDriver {
        type Error: std::error::Error;
        fn execute_history(&self, history: &History) -> Result<Vec<Session<u64,u64>>, Self::Error>;
        fn connect(config: &DriverConfig) -> Result<Self, Self::Error> where Self: Sized;
    }
    pub struct DriverConfig { pub hosts: Vec<String>, pub port: u16, pub db_name: String }
    ```
  - Create per-DB module stubs: `src/mysql.rs`, `src/postgres.rs`, `src/mongodb.rs`, `src/galera.rs` (Galera = MySQL cluster), `src/cockroachdb.rs`
  - Each module stub: `pub struct MysqlDriver;` implementing `DbDriver` with `todo!()` bodies
  - Move/link `dbcop_testgen/src/driver/galera.rs` here — galera is a DB driver, not a test generator

  **Must NOT do**:
  - Do not implement actual DB connection logic (just stubs with `todo!()`)
  - Do not put this in `dbcop_core` or `dbcop_cli`
  - Do not make `dbcop_core` depend on `dbcop_drivers`

  **Recommended Agent Profile**:
  - **Category**: `unspecified-high`
    - Reason: Crate creation + workspace wiring + feature-gated optional deps + trait design

  **Parallelization**:
  - **Can Run In Parallel**: YES — with C2, C3, C4 (new crate, no conflicts)
  - **Parallel Group**: Wave C2
  - **Blocks**: C6
  - **Blocked By**: C1 (for consistency on driver trait design)

  **References**:
  - `Cargo.toml` (workspace root) — add `dbcop_drivers` to members
  - `dbcop_testgen/src/driver/galera.rs` — currently empty, move to drivers crate
  - `dbcop_testgen/Cargo.toml` — reference for workspace dep pattern
  - oopsla-2019 had: MySQL, Postgres, MongoDB, AntidoteDB, CockroachDB dev-deps — replicate this set as optional features

  **Acceptance Criteria**:
  - [ ] `cargo build -p dbcop_drivers` exits 0 (no features selected — compiles trait only)
  - [ ] `cargo build -p dbcop_drivers --features mysql` compiles (may need mysql crate)
  - [ ] `dbcop_drivers` appears in `cargo metadata` workspace members

  ```
  Scenario: Drivers crate builds with no features
    Tool: Bash
    Steps:
      1. cargo build -p dbcop_drivers 2>&1 | tee .sisyphus/evidence/taskC5-build.txt
      2. Assert: exit 0
    Expected Result: Trait-only build succeeds
    Evidence: .sisyphus/evidence/taskC5-build.txt

  Scenario: Workspace metadata includes drivers
    Tool: Bash
    Steps:
      1. cargo metadata --no-deps --format-version 1 | python3 -c "import sys,json; m=json.load(sys.stdin); print([p['name'] for p in m['packages']])"
      2. Assert: "dbcop_drivers" in list
    Evidence: .sisyphus/evidence/taskC5-workspace.txt
  ```

  **Commit**: Groups with C2, C3, C4

---

- [ ] C6. **Verify feature-flag matrix + Pass C commit**

  **What to do**:
  - Run all feature-flag combinations:
    - `cargo build -p dbcop_core --no-default-features`
    - `cargo build -p dbcop_core --features non-atomic`
    - `cargo build -p dbcop_core --features atomic`
    - `cargo build -p dbcop_core --features partial-order`
    - `cargo build -p dbcop_core --all-features`
  - Run full workspace tests: `cargo test --workspace --all-features`
  - Run `cargo clippy --workspace --all-features`
  - Create the Pass C commit

  **Recommended Agent Profile**:
  - **Category**: `quick`
  - **Skills**: [`git-master`]

  **Parallelization**:
  - **Can Run In Parallel**: NO — must be after C2, C3, C4, C5
  - **Parallel Group**: Wave C4 (sole task)
  - **Blocks**: Final Verification Wave
  - **Blocked By**: C2, C3, C4, C5

  **Acceptance Criteria**:
  - [ ] All 5 feature combinations compile
  - [ ] `cargo test --workspace --all-features` passes
  - [ ] Pass C commit created

  ```
  Scenario: Full feature matrix builds and tests
    Tool: Bash
    Steps:
      1. for f in "" "non-atomic" "atomic" "partial-order" "partial-order,serde"; do cargo build -p dbcop_core --no-default-features --features "$f" || exit 1; done
      2. cargo test --workspace --all-features 2>&1 | tee .sisyphus/evidence/taskC6-tests.txt
      3. Assert: all pass
    Evidence: .sisyphus/evidence/taskC6-tests.txt
  ```

  **Commit**: YES — `feat(core): capability feature flags; feat(sat): splr backend for SI+SER; feat(drivers): dbcop_drivers crate scaffold`
  - Pre-commit: `cargo test --workspace --all-features`

---

## Final Verification Wave

- [ ] FV1. **Plan Compliance Audit** — `oracle`
  Read plan end-to-end. Verify all Must Have conditions. Search for forbidden patterns (std in core, minisat dep, SAT for prefix). Check evidence files in .sisyphus/evidence/.
  Output: `Must Have [N/N] | Must NOT Have [N/N] | Tasks [N/N] | VERDICT: APPROVE/REJECT`

- [ ] FV2. **Code Quality Review** — `unspecified-high`
  Run `cargo build --workspace` + `cargo test --workspace` + `cargo clippy`. Review all changed files. Check no_std constraint with `cargo build -p dbcop_core --no-default-features`.
  Output: `Build [PASS/FAIL] | Tests [N pass/N fail] | Clippy [N warnings] | no_std [PASS/FAIL] | VERDICT`

- [ ] FV3. **Real QA** — `unspecified-high`
  Run all QA scenarios from every task. Test CLI end-to-end. Test feature flags. Test wasm compilation. Save evidence to `.sisyphus/evidence/final-qa/`.
  Output: `Scenarios [N/N pass] | Integration [N/N] | VERDICT`

- [ ] FV4. **Scope Fidelity Check** — `deep`
  For each task: verify what was built matches spec. No algorithm logic changes in Pass A. No std in core. No SAT for prefix. No DB code in core/cli.
  Output: `Tasks [N/N compliant] | Forbidden patterns [CLEAN/N] | VERDICT`

---

## Commit Strategy

- After Task A5: `refactor(core): rename non_atomic→raw, group solvers by strategy`
- After Task B6: `feat(cli): implement generate+verify subcommands; feat(wasm): wire check() API`
- After Task C6: `feat: add capability feature flags; feat(sat): splr backend for SI+SER; feat(drivers): new dbcop_drivers crate`

---

## Success Criteria

### Verification Commands
```bash
cargo build --workspace                          # Expected: exit 0, zero errors
cargo build -p dbcop_core --no-default-features # Expected: exit 0 (no_std preserved)
cargo test --workspace                           # Expected: all tests pass (18+)
cargo run -p dbcop -- generate --help            # Expected: usage printed
cargo run -p dbcop -- verify --help              # Expected: usage printed
```

### Final Checklist
- [ ] All "Must Have" present
- [ ] All "Must NOT Have" absent
- [ ] All tests pass (18 original + new integration tests)
- [ ] `no_std` core still compiles without std feature
- [ ] CLI has working generate + verify subcommands
- [ ] wasm exposes check_consistency to JS
- [ ] Feature flags compile clean (all combinations)
- [ ] dbcop_drivers crate in workspace
