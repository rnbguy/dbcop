# Development Quickstart

## Prerequisites

- **Rust** meeting the workspace `rust-version` in [Cargo.toml](../Cargo.toml),
  plus nightly for formatting
- **Deno** (for WASM builds and TypeScript tooling)
- **taplo** (TOML formatter)
- **typos** (spell checker)
- **Go 1.27** (for the MySQL mockservers)
- **C toolchain** (for the native Go server's Rust FFI/CGO linkage); the pure-Go
  Apalache executable instead needs the external explorer
- **Java 25** (for the optional pinned Apalache service); the TLC campaigns also
  require Java and the pinned `DBCOP_TLC_JAR`

## Implementation conventions

- Use typed Serde request/response structs and derive attributes at Rust JSON
  boundaries, not `serde_json::Value`. CLI and WASM witnesses share
  `consistency::witness::serialize_witness`: saturation graphs stream as edge
  lists because transaction IDs cannot be JSON object keys.
- Derive library errors with `thiserror`, retaining typed sources and `#[from]`
  conversions. Keep `color-eyre` reports at the CLI boundary and Clap derives
  for command-line parsing.
- Go RPC envelopes and Apalache ITF records use the standard `encoding/json/v2`
  and `encoding/json/jsontext` packages. Duplicate names and invalid UTF-8 are
  rejected. The generic RPC result decoder retains `encoding/json`'s `UseNumber`
  and error types for existing callers.
- Prefer standard `maps`, `slices`, `strings` and `errors` operations over
  forwarding helpers and manual loops. Preserve deep-copy ownership, nilness,
  rollback ordering and error chains; `maps.Clone` is only a shallow copy.

## Build

```bash
# Build all crates
cargo build --workspace

# Verify no_std compatibility of core crate
cargo build -p dbcop_core --no-default-features
```

### SQL mockservers and workloads

The [mockserver guide](../mockserver/README.md) is the command reference for the
[native release build](../mockserver/README.md#build-an-optimized-server), the
[pure-Go Apalache server](../mockserver/README.md#apalache-comparison-backend),
and
[unchanged BenchBase clients](../mockserver/README.md#run-unchanged-benchbase-clients).
Load through ordinary SQL and seal the initial fixture before weak execution;
the generic SQL implementation is shared across both admission backends.
BenchBase additionally needs its documented Java/Maven/MySQL client setup.

For deterministic failure investigation, use
[controlled SQL replay](../mockserver/README.md#controlled-sql-failure-replay)
and
[bounded exploration](../mockserver/README.md#fixed-workload-sql-exploration).
These are separate from the unimplemented real-database driver crates.

## Test

```bash
# Run all tests, including exhaustive reference comparisons
cargo test --release --workspace --all-features

# Run tests for a specific crate
cargo test -p dbcop_core

# Run WASM integration tests (requires wasmlib built first)
deno task wasmbuild
deno test -A crates/wasm/tests/
```

### Independent bounded checker reference

```bash
cargo test --release -p dbcop_core --test axiomatic_reference -- --nocapture
cargo test --release -p dbcop_core --test incremental_reference -- --nocapture
cargo test --release -p dbcop_sat --test axiomatic_reference -- --nocapture
```

The shared test-only reference enumerates total commit orders and evaluates the
[paper's axioms](https://arxiv.org/html/1908.04509) directly, without using
production validation, graph closure, saturation, decomposition, or solvers. The
finite domain has at most two sessions, three transactions total, and four
events total over two variables. Tests report structurally invalid assignments
separately and independently validate supplied witnesses. The SAT serializable
API returns only a verdict, so it has no witness to validate.

Use release mode for the exhaustive suites: they retain the same bounds and
assertions, but debug execution repeats enough reference-order enumeration to be
impractical for routine full-suite runs.

The axiomatic domain contains completed, committed transactions. Directed
structural tests cover dirty external reads and malformed provenance; they do
not specify complete aborted-transaction semantics. Batch reads of `Some(0)`
resolve to an explicit version-zero writer when one exists, otherwise initial;
`None` always means initial. The incremental API instead reserves write zero and
treats both read forms as initial. Its comparisons use only source-available
arrival orders and verify trial, accept, rejection, reset, and successor
admission against fresh instances and the reference.

These are bounded checks, not a proof of general correctness or hash collision
freedom. DFS memoization still uses state signatures; the reference does not
justify treating distinct colliding states as equal.

### Frozen Apalache corpus across implementations

`crates/core/tests/fixtures/apalache.json` retains the exact 273 histories and
source-available prefixes from the saved campaign, including observations from
its inconclusive larger bound. Names identify immutable cases; seven Boolean
labels follow RC/RR/Atomic Read/Causal/Prefix/SI/SER order. The independent
raw-event reference supplies the labels; the preceding TLC campaign checked
them. This is a fixed regression corpus, not a new completeness claim.

```bash
cargo test --release -p dbcop_sat --test saved_corpus -- --nocapture
```

The Rust test checks every frozen label against the reference and core, runs all
three applicable SAT APIs, and independently validates returned witnesses.
Serializable's SAT API returns only a verdict. With the pinned Apalache service
and native FFI prerequisites:

```bash
go -C mockserver test -race ./backend \
  -run '^TestApalacheSavedCorpusAgreement$' -count=1 -timeout=3h -v
```

This test queries the production Apalache `Step` implementation for every
case/level, using a full-context commit request with the last source-available
transaction proposed. It compares all YES and NO answers, including negative
contexts, rather than treating an execution error as rejection. Each query has a
ten-second deadline. Empty histories have no transaction to propose and use an
isolation-neutral empty initial commit. This is admission-level comparison, not
replay of every SQL/cell operation. Set `DBCOP_REQUIRE_APALACHE=1` so absent
runtime cannot skip an explicitly requested run.

The recorded replay completed all 1,911 case/level queries with no disagreement
in about 22 minutes under the Go race detector. Counts matched core/reference
for all seven levels and SAT for its three supported levels:

| Level              | YES | NO |
| ------------------ | --: | -: |
| Committed Read     | 273 |  0 |
| Repeatable Read    | 269 |  4 |
| Atomic Read        | 267 |  6 |
| Causal             | 267 |  6 |
| Prefix             | 267 |  6 |
| Snapshot Isolation | 259 | 14 |
| Serializable       | 251 | 22 |

The corpus has RC-positive cases only. Other levels have both signs; this corpus
does not replace the existing directed RC-negative tests or cover every
adjacent-level separator.

### Reproducible SAT/UNSAT search benchmarks

```bash
cargo bench -p dbcop_sat --bench npc_vs_sat -- --test
DBCOP_BENCH_SEED=42 cargo bench -p dbcop_sat --bench npc_vs_sat
```

The default seed is 42; it is always printed and an invalid override fails.
Criterion's ordinary command-line options remain available. Twelve cases cover
three NPC levels, two history shapes and both satisfiable/unsatisfiable inputs,
with the same inputs measured through public core and SAT APIs. The seed fixes
input construction, not production hash iteration or branch ordering.

SAT cases have known seeded serial executions. UNSAT cases append a
causal-compatible obstruction after a connected prelude: an incompatible
snapshot ring for Prefix, conflicting snapshot writers for SI, and a write-skew
ring for Serializable. Independent small-reference decisions and available
witnesses are checked outside timing. The negative inputs are not malformed
histories or early causal rejections. They are representative families, not
worst-case hardness evidence.

Timings include public checker preprocessing and SAT encoding, not isolated DFS
node costs. These benchmarks establish a baseline; heuristic ablations,
allocation/node counters and performance optimizations are not implemented by
this change.

Recorded baseline: seed 42, Intel Xeon W-11955M, Rust 1.99.1, 40 Criterion
samples per measurement with three-second warmup and measurement targets. The
owned Apalache service was stopped before measurement. This was not an exclusive
or CPU-pinned host. Four-session cases below use four prelude rounds; values are
Criterion mean estimates in microseconds, including preprocessing/encoding: The
SAT column uses BatSat 0.6.0 through rustsat-batsat 0.7.5.

| Level        | Verdict | Transactions | Core mean (us) | SAT mean (us) |
| ------------ | ------- | -----------: | -------------: | ------------: |
| Prefix       | SAT     |           16 |          95.75 |       5617.95 |
| Prefix       | UNSAT   |           24 |        1735.48 |      17977.13 |
| SI           | SAT     |           16 |          99.13 |       5277.00 |
| SI           | UNSAT   |           20 |        1925.19 |      10594.93 |
| Serializable | SAT     |           16 |          69.85 |        624.88 |
| Serializable | UNSAT   |           20 |         290.32 |       1163.49 |

Core was faster on these measured families. This is not a general solver ranking
or an improvement over a previous implementation. Full per-case estimates and
confidence intervals are retained under `target/criterion/npc_vs_sat_*`.

#### Modern solver pilot

A benchmark-only isolated harness also exercised embedded Kissat 4.0.4,
installed CaDiCaL 3.0.1, and the official Satsuma-iter + Kissat pipeline, the
[2026 Main Track winner](https://satcompetition.github.io/2026/downloads/satcomp26slides.pdf).
This did not change production solver selection or add repository dependencies.
There is no universal fastest solver across instance families.

Twelve CNFs were captured once from the existing encoder, retaining numeric
variable IDs and clause order. Embedded BatSat/Kissat and both external solvers
received those identical inputs. Every expected SAT/UNSAT verdict matched;
returned SAT assignments satisfied the original clauses. Independent expected
history verdicts and available end-to-end witnesses were checked before timing.

The separate end-to-end run reused the checker/encoding source with only the
solver type changed. Four-session means below include causal preprocessing,
encoding, solving, and witness extraction where the API returns a witness:

| Level        | Verdict | Core mean (us) | Kissat mean (us) |
| ------------ | ------- | -------------: | ---------------: |
| Prefix       | SAT     |          84.03 |          6772.32 |
| Prefix       | UNSAT   |        1676.61 |         22898.22 |
| SI           | SAT     |          91.65 |          5515.50 |
| SI           | UNSAT   |        1709.75 |         13480.32 |
| Serializable | SAT     |          62.27 |           644.66 |
| Serializable | UNSAT   |         263.92 |          1207.13 |

The same workstation and 40-sample Criterion settings were used in an isolated
Cargo release profile. The owned exhaustive regression was paused during these
sequential measurements and resumed afterward. This remains an unpinned,
non-exclusive host, not a matched performance study against the earlier build.

Embedded CNF timings separately include solver creation, clause loading, solving
and destruction, excluding encoding and process startup. Kissat did not
consistently improve on BatSat on these small families. External timings use
five measured runs after one warmup and include DIMACS I/O, startup, output
parsing, and the winning pipeline's symmetry breaking/proof logging. Do not
compare them as bare solver CPU time. Emitted UNSAT proofs were not
independently checked.

The unmodified winning archive is
[`anders.tar.xz`](https://satcompetition.github.io/2026/downloads/solvers/anders.tar.xz),
SHA-256 `1951097402831eaaa9cdd6b670a09b9e9cbe1714e16d48b17d420e3b40816623`.
Input seed is 42; embedded Kissat uses its default solver seed 0, CaDiCaL uses
42, and the official pipeline retains its default options. RustSAT 0.7.5's
Kissat option wrapper mistakes a previous value of zero for failure, so no
runtime option override was used for the embedded pilot.

### Fast CI and daily exhaustive validation

Pull-request and push workflows have a 15-minute limit per job, with a target of
less than five minutes. Rust tests and PR coverage exclude the two
`complete_bounded_domain` reference tests and
`exhaustive_incremental_admission_matches_exact_tentative_histories`; directed
regressions, saved-corpus comparisons, and other ordinary tests remain enabled.
The coverage report therefore describes this bounded CI suite, not the
exhaustive campaigns.

Codecov combines the Rust LCOV report (`rust`), Deno LCOV report (`deno`), and
native Go coverage profile (`go`). The Go job instruments every package in
`mockserver` with `-coverpkg=./...` and atomic counters, so SQL integration
tests also credit the libraries they exercise. Generated `wasmlib` and build
outputs remain excluded. External-service tests skipped by ordinary CI
contribute no coverage; a green report is not evidence that those paths were
exercised.

[`Exhaustive Validation`](../.github/workflows/exhaustive.yaml) runs daily at
03:23 UTC on `main` only. Its independent jobs run the full release-mode Rust
workspace suite without exclusions, and the existing pinned TLC, actual DFS
trace, real Apalache/race, and pure-Go wire campaigns. Their bounds and
per-operation deadlines are unchanged; each job has a six-hour outer limit.
Manual dispatch is also restricted to `main`:

```bash
gh workflow run exhaustive.yaml --ref main
```

The schedule becomes active after the workflow is merged into `main`. Exhaustive
validation is not a PR-required check. See the workflow run for progress;
timeout or cancellation is not successful exhaustion.

### Independent TLA+/TLC semantic oracle

The ignored `tlc_model` integration tests require Java, `sha256sum`, and
`DBCOP_TLC_JAR` pointing to the official TLC v1.7.4 JAR. The harness verifies
SHA-256 `936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88`; an
unavailable runtime, malformed output, or timeout is an execution failure, never
an inconsistency verdict.

```bash
export DBCOP_TLC_JAR=/usr/share/java/tla2tools.jar
cargo test --release -p dbcop_core --test tlc_model -- --ignored --skip tlc_go_histories --nocapture --test-threads=1
```

`mockserver/apalachebackend/spec/Isolation.tla` states the seven isolation
criteria independently from the production graphs and algorithms. The semantic
campaign exhausts the same 253,537 raw histories as the Rust reference, checks
directed/malformed cases, and validates every successful core witness. TLC
constructs commit orders and computes causal closure itself; Rust supplies
numeric raw events, not expected answers or computed relations.

The separate operational model generates Begin/Read/Write/Commit/Deliver
behaviors with one ordered session per replica. Its finite campaigns use
`k=1,2,3`, three transactions total, four events total, and two keys. The larger
campaign samples 100 traces at `k=3`, six transactions total, twelve events
total, depth 60, seed 42. Operational histories are not the entire axiomatic
domain, and explored-state/transition counts are not schedule counts.

Whole replica graphs have a 1,200-second deadline; oracle batches contain at
most 256 histories with a 120-second deadline. Up to four single-worker JVM
batches run concurrently; returned verdicts preserve input order. A timed-out
batch is retried without dropping inputs in batches of 16. Failed runs retain
exact models, inputs, and logs under the printed private temporary directory.
Simulation completion requires its saved trace files as well as TLC's completion
messages; it cannot certify a negative semantic verdict.

These checks provide bounded correctness evidence, not a complexity proof.
Bounding physical replicas bounds sessions here only because each replica is one
ordered session; arbitrary independent clients do not satisfy that premise.

Go model-validation tests send their observed primitive histories through this
same runner, including the Rust reference, core verdicts, and witness checks.
They require both `DBCOP_APALACHE_RPC` and `DBCOP_TLC_JAR`; Rust is a validation
dependency, not a runtime dependency of the separate pure-Go server. The
input-dependent `tlc_go_histories` bridge is compiled with the existing `serde`
feature and invoked by the Go tests, not by the standalone finite campaigns.

### Actual search trace compatibility

```bash
cargo test --release -p dbcop_core --lib tlc_search_trace_compatibility -- --ignored --nocapture
```

Test-only hooks record actual PC/SI/SER DFS executions, including cached
legality, bookkeeping, undo, pruning, backjumps, restarts, and the terminal
result. `SearchTrace.tla` independently reconstructs semantic active readers and
SI open writers, checks graph readiness, and searches finite continuations
before accepting an unsatisfiability claim. Budget abandonment is not such a
claim.

Following [Cirstea et al.](https://arxiv.org/html/2404.16075v2), compatibility
requires at least one complete model behavior to consume the entire capture and
its terminal result. A dead-end matching branch is not global rejection. Missing
or extra terminal observations are malformed, not partial traces. Negative
controls must finish model checking and reject semantically; a runtime or
encoding error cannot count as a successful rejection.

Profiles exercise default policies, deterministic exhaustive search, and
budgeted restarts followed by an unbounded attempt. Successful witnesses are
also checked against raw axioms. Capture is scoped per thread and compiled out
of ordinary/no_std builds; no public solver-trait API or production trace
feature is added.

### SQL storage and lifecycle validation

No external MySQL server is needed. Build the native oracle, then run the Go
module's real in-process SQL, replay/CLI, and savepoint tests:

```bash
cargo build -p dbcop_ffi
env -u CGO_LDFLAGS -u CGO_CFLAGS -u CGO_CPPFLAGS -u CGO_CXXFLAGS \
  LD_LIBRARY_PATH="$PWD/target/debug" go -C mockserver test ./...
env -u CGO_LDFLAGS -u CGO_CFLAGS -u CGO_CPPFLAGS -u CGO_CXXFLAGS \
  LD_LIBRARY_PATH="$PWD/target/debug" go -C mockserver test ./backend \
  -run 'TestSQLSemanticMatrix|TestNamedSavepoint' -v
```

Latest-state SQL compares the custom provider against the installed upstream
memory provider. Weak-read checks compare complete result/history and contextual
admission sets with indexed discovery versus an explicit full candidate
universe, including absent-key probes. Each scenario has at most three sessions,
four transactions, and three ambiguous reads with at most three candidates;
singleton bookkeeping reads do not consume that ambiguous bound. An enumeration
limit hit is a failure, not an exhaustive result.

Savepoint checks use explicit expected rows/events and fresh native/batch
admission, because upstream memory sessions lack savepoints. Frozen-state
predicate partition checks use only deterministic latest/initialization reads;
independently sampled weak queries are not a metamorphic oracle. See the
[controlled replay format and limits](../mockserver/README.md#controlled-sql-failure-replay).

### Apalache successor comparison

Provision the verified v0.62.4 JAR and isolated Java 25 service using the pinned
downloads, hashes, image and startup command in
[`model-validation`](../.github/workflows/exhaustive.yaml). Publish only its
loopback RPC port; health does not attest the tool version. Then run the
real-service gates, including SQL rollback/failure boundaries and all seven wire
levels:

```bash
export DBCOP_APALACHE_RPC=http://127.0.0.1:18822/rpc
export DBCOP_REQUIRE_APALACHE=1
# DBCOP_TLC_JAR remains required for independent history validation.
cargo build -p dbcop_ffi
env -u CGO_LDFLAGS -u CGO_CFLAGS -u CGO_CPPFLAGS -u CGO_CXXFLAGS \
  LD_LIBRARY_PATH="$PWD/target/debug" \
  go -C mockserver test -race ./...
CGO_ENABLED=0 go -C mockserver build -o /tmp/dbcop-apalache ./cmd/mockserver-apalache
CGO_ENABLED=0 go -C mockserver test ./apalachebackend ./cmd/mockserver-apalache
```

The model receives raw history and a checked syntax-only write projection;
source joins, commit rankings and causal closure stay in TLA+. Each operation
loads a fresh session and disposes it before publishing success. UNKNOWN,
timeouts and cleanup failures are execution errors, not rejected histories.
Client cancellation cannot guarantee server rewriting stops; a lost load reply
can leave an unknown remote session. This is an experimental comparison oracle,
not a production database or a uniform/fair history sampler. Each MySQL
connection is a history session; physical replica delivery is modeled separately
by the TLC generator.

This server is successor-driven execution, not an autonomous workload generator.
The separate bounded history campaign below generates whole replica histories
without isolation filtering.

Typed read/write fact cells and dependency edges are derived once in TLA+. RC/RR
retain per-read positions; other premises are evaluated once per
competing-writer/reader pair without changing the axiom. Concrete raw
transaction/event domains avoid symbolic read-record joins while retaining every
read position and source. Failed SQL fixtures emit `APALACHE_REPRO` loadSpec
parameters in their logs so the exact numeric model can be replayed.

For a bounded SQL A/B measurement, build the native FFI in release mode and use
the matching Go link tag/library directory:

```bash
cargo build --release -p dbcop_ffi
env -u CGO_LDFLAGS -u CGO_CFLAGS -u CGO_CPPFLAGS -u CGO_CXXFLAGS \
  LD_LIBRARY_PATH="$PWD/target/release" \
  go -C mockserver test -tags dbcop_release ./backend -run '^$' \
  -bench '^BenchmarkNativeApalacheSQL$' -benchtime=1x -count=6
```

Setup, initialization and independent history validation are untimed; measured
SQL includes every operation's model load, SMT, RPC and disposal. Compare the
first measurement with subsequent runs without calling an already running JVM
cold. Normal and latest-forced cases have different source-selection semantics;
do not infer equal-seed anomaly counts from their latency measurements.

### Exhaustive bounded Apalache histories

With the same pinned service, native FFI and TLC prerequisites, run:

```bash
go -C mockserver test ./backend -run '^TestApalacheReplicaTraceCampaign$' -count=1 -timeout=3h -v
```

`ReplicaTraces.tla` preserves Begin/Read/Write/Commit/Deliver semantics with one
ordered session per replica and two keys. Fixed-capacity maps avoid symbolic
nested-sequence growth; the bundled Apalache module constructs only the final
sequence projection. Completed-state padding represents histories reachable
within the declared depth. No isolation checker filters generation. For the
three targeted partitions, the two commuting Begin actions are normalized into
Init and still count toward logical depth eight. Root-only partitions omit
unused Deliver actions. The model documents why these target-specific reductions
preserve projected histories; they are not a general-purpose partial-order
reduction.

The first model has one shared ten-second deadline covering load,
initialization, all unrolled steps and the first result. Every subsequent
`nextModel` attempt gets ten seconds. Independent classification time is outside
that budget; session disposal has its own five-second deadline. Only an
acknowledged exhaustion with a valid old model certifies a complete bounded
projection. Timeout/UNKNOWN, missing model, duplicate blocked history and
cleanup errors cannot mean exhaustion.

The campaign includes unrestricted one-transaction profiles over one to three
replicas, plus two-transaction/two-event profiles at depth six. Three separate
depth-eight workload partitions exhaust write skew, competing snapshot writers
and a reader changing its external source. These are operation/source patterns,
not verdicts supplied to the generator. Their independently checked seven-level
classifications must separate PC/SI/SER and RC/RR as expected.

A safely cleaned-up timeout reduces unrestricted depth and event/transaction
bounds, while retaining the original bound as inconclusive. Targeted partitions
cannot silently lose their required events to fit the budget; failure is
inconclusive. Cleanup failure or an unknown session after a lost load reply
stops the campaign: client cancellation cannot guarantee server rewriting stops.

Every generated history is checked for local/source identity, compared across
all seven Rust batch verdicts, and replayed through persistent transaction
trial/reset/commit. Batches of histories and prefixes then pass through the
existing independent TLC/Rust-reference/witness bridge. One-transaction and
targeted enumerations additionally match independently constructed complete
history sets, not just reported counts. Empty sessions and transactions are
retained.

Logs show completed profile cells as a percentage, current bounds, unique
histories and classification masks. There is no percentage estimate of remaining
SMT work. Artifacts under the printed private temporary directory retain model
and input hashes, numeric input, raw RPC responses, projected histories, timeout
and exhaustion status. The external SMT seed is explicitly not configured. These
are distinct completed histories, not all schedules or all axiomatic histories.
The broader search proposals remain in the
[checker improvement plan](checker-improvement-plan.md).

### Coverage measurements

Measure the entire Rust workspace with all features, not just checker verdicts:

```bash
mkdir -p coverage
cargo llvm-cov --release --workspace --all-features --lcov --output-path coverage/rust.lcov
```

Use a fresh `CARGO_LLVM_COV_TARGET_DIR` for each source revision. A report-only
run can otherwise discover obsolete instrumented executables left in the target
directory and combine incompatible source mappings. Path-included native WASM
tests also report `tests/../src` aliases; distinguish those from additional
production code when reporting per-file results.

Go's ordinary suite does not start external tools. To include the actual
BenchBase pipeline, build an unchanged upstream MySQL distribution as described
in
[the mockserver guide](../mockserver/README.md#run-unchanged-benchbase-clients)
and provide these paths:

```bash
export DBCOP_BENCHBASE_MYSQL_DIR="$BENCHBASE/target/benchbase-mysql"
export DBCOP_NATIVE_SERVER=/path/to/native-mockserver
export DBCOP_ASSERTIONS_BIN=/path/to/assertions
```

The native server must use the matching FFI library; keep `java` and `mysql` on
`PATH`. `TestActualBenchBaseWorkflowRetainsValidatedOutcomes` uses the unchanged
Voter, conserving SmallBank and Wikipedia clients with one connection, a
one-second workload and seed 42. It checks initialization, baseline assertions,
sealing, workload SQL outcomes, final assertions and failed phase boundaries.
Missing all three paths skips this integration test; partial configuration
fails. For complete Apalache coverage, also provision and export its service as
above. Then collect the whole module, including command packages:

```bash
mkdir -p coverage
env -u CGO_LDFLAGS -u CGO_CFLAGS -u CGO_CPPFLAGS -u CGO_CXXFLAGS \
  LD_LIBRARY_PATH="$PWD/target/debug" \
  go -C mockserver test -race ./... -timeout=3h -covermode=atomic \
  -coverpkg=./... -coverprofile=../coverage/go.cover.out
go -C mockserver tool cover -func=../coverage/go.cover.out
```

Report full-module and per-file coverage separately. Runs without real-service
prerequisites do not cover those paths. Do not exclude error handling to meet a
coverage target or add counts from duplicated profile blocks; union identical
source blocks by whether any run executed them.

## Lint and Format

```bash
# Rust formatting (requires nightly)
cargo +nightly fmt --all

# Clippy linting
cargo clippy --workspace -- -D warnings

# TOML formatting
taplo format *.toml

# Deno checks (fmt + lint + type check)
deno task deno:ci
```

## Contributor Guide

For full contributor guidelines, including workflow, branch naming conventions,
CI checks, code constraints, pre-commit hooks, and the update protocol, see
[AGENTS.md](../AGENTS.md).

AGENTS.md covers:

- Git workflow and branch naming (`feat/`, `fix/`, `perf/`, etc.)
- Conventional commit PR titles
- PR/push CI checks (Rust, Go, Deno, coverage, code quality) and daily main-only
  exhaustive validation
- Code constraints (no emoji, `no_std` compatibility, serde gates)
- Pre-commit hook details
- Repository structure reference
- CLI and WASM API usage details
- Testing strategy and performance decisions

## See Also

- [Architecture](architecture.md) -- crate structure and data flow
- [CLI Reference](cli-reference.md) -- using the `dbcop` binary
- [WASM API](wasm-api.md) -- WASM bindings API reference
- [SQL Mockservers](../mockserver/README.md) -- builds, BenchBase, read
  policies, assertions, and replay
