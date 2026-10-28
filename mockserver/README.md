# mockserver - MySQL-wire SQL with MonkeyDB-style reads

[`go-mysql-server`](https://github.com/dolthub/go-mysql-server) supplies the
MySQL protocol, SQL parser, planner, joins, and aggregations. The custom backend
supplies versioned key/value storage and, by default, read admission through
`dbcop_core`, following the approach in
[MonkeyDB](https://arxiv.org/abs/2103.02830).

The standalone server starts with no application databases or tables. Clients
create their own schema with ordinary SQL. There is no built-in TPC-C schema,
workload-specific SQL rewrite, or required client session variable. SQL
compatibility is limited to the engine and implemented storage interfaces; this
is not a complete MySQL implementation.

## Build an optimized server

Run from the repository root, with Rust, Cargo, Go, and a C toolchain installed:

```bash
CARGO_PROFILE_RELEASE_LTO=true CARGO_PROFILE_RELEASE_CODEGEN_UNITS=1 \
  RUSTFLAGS="-C target-cpu=native" cargo build --release -p dbcop_ffi
(
  cd mockserver
  env -u CGO_LDFLAGS -u CGO_CFLAGS -u CGO_CPPFLAGS -u CGO_CXXFLAGS \
    go build -tags dbcop_release -o mockserver ./cmd/mockserver
)
```

The `dbcop_release` tag links `target/release`; without it the Go backend links
`target/debug`. Clear inherited CGO build flags rather than allowing a debug
library search path to override the selected profile. The native CPU setting is
appropriate for the machine running the server; the built library may not run on
another CPU.

Start the already-built binary separately from compilation, again from the
repository root:

```bash
LD_LIBRARY_PATH="$PWD/target/release" \
  ./mockserver/mockserver --addr 127.0.0.1:3307 --iso serializable --seed 1
```

The default bind address remains `127.0.0.1:3307`. The default isolation level
is `read-committed`; the example chooses `serializable` as a strong baseline for
the consistency checks. The startup line always reports the chosen seed (default
`1`). SIGINT or SIGTERM stops the listener, drains connections, and releases the
engine and native oracle resources.

The standalone server starts in initialization mode: transactions execute
serially and read the latest values. After preparation, the explicit
`CALL dbcop_seal_initial_state()` operation atomically makes the completed
dataset the initial KV state and enables the selected weak-isolation policy.
Preparation still uses ordinary SQL transactions, not one giant transaction. The
boundary is atomic to subsequent workload operations, not crash-durable.

This is a local experiment server, not a production database. Keep the loopback
bind unless remote access is intentional.

## Apalache comparison backend

`cmd/mockserver-apalache` reuses the same generic SQL engine and KV storage, but
asks an operator-managed Apalache v0.62.4 explorer for each semantic
read/write/commit successor. Its embedded `Isolation.tla` states all seven
isolation criteria; no Rust FFI is linked into this executable.

```bash
CGO_ENABLED=0 go -C mockserver build -o /tmp/dbcop-apalache ./cmd/mockserver-apalache
/tmp/dbcop-apalache --addr 127.0.0.1:3307 --iso serializable --seed 1 \
  --apalache-rpc "$DBCOP_APALACHE_RPC" --apalache-timeout 30s
```

Provision the pinned JVM service separately and set `DBCOP_APALACHE_RPC` to its
exact HTTP endpoint. Startup requires a real capability round trip, not just
health; health does not attest the version. Isolate or firewall that service:
the client's loopback default does not restrict the JVM's listening interface.
Loading and `CALL dbcop_seal_initial_state()` are unchanged.

Ordinary reads choose one satisfying solver model, not a uniform or fair sample.
The printed seed identifies the shared Go store/run, not the external SMT
solver. `dbcop_read_latest=1` still tries newest-admitted sources first.
`Admissions().Admitted=-1` means the admitted set was not enumerated.

Each operation loads the full observed history into a fresh disposable solver
session; auxiliary rankings are never fixed across operations. This is an
experimental comparison path, not a constant-time incremental solver or a
production database. Client deadlines do not guarantee server-side rewriting has
stopped; a lost load response can leave an unknown remote session. See
[model-validation checks](../docs/development.md#independent-tlatlc-semantic-oracle).

The native `--read-policy`, `--deviation-probability`, and `--deviation-budget`
flags are deliberately unsupported. The assertion runner targets the native
server; start the alternate server separately when using the ordinary MySQL
clients. Compare supported outcomes and latency, not equal-seed anomaly
frequencies.

## Run unchanged BenchBase clients

[BenchBase](https://github.com/cmu-db/benchbase) is the maintained OLTPBench
successor. The configurations in `benchbase/` use its unmodified MySQL/JDBC
clients for the four SQL applications evaluated in MonkeyDB. Install JDK 27 and
Maven, then build the upstream MySQL distribution separately from timings:

```bash
DBCOP="$PWD"
BENCHBASE="$HOME/benchbase"
git clone --depth 1 https://github.com/cmu-db/benchbase.git "$BENCHBASE"
(
  cd "$BENCHBASE"
  mvn -B -P mysql -DskipTests -Dfmt.skip=true \
    -Dmaven.compiler.release=23 package
)
```

The upstream build targets Java 23; the compiler and runtime can be JDK 27.
`maven.compiler.release=23` selects that API/bytecode target without using an
older compiler. `fmt.skip=true` skips the upstream source-formatting plugin,
whose bundled formatter is incompatible with newer javac internals. No client
source or dependencies are patched.

Start the release server as above. In another shell, prepare one application,
seal its completed fixture, and execute its stock transaction mix:

```bash
DBCOP="$PWD"
BENCHBASE="$HOME/benchbase"
benchmark=smallbank # tpcc, smallbank, voter, or wikipedia
config="$DBCOP/mockserver/benchbase/$benchmark.xml"
results="$(mktemp -d)"
echo "Results: $results"
mysql --host=127.0.0.1 --port=3307 --user=root \
  --execute="CREATE DATABASE $benchmark"
(
  cd "$BENCHBASE/target/benchbase-mysql"
  java -jar benchbase.jar -b "$benchmark" -c "$config" \
    --create=true --load=true --execute=false -d "$results/load"
)
mysql --host=127.0.0.1 --port=3307 --user=root --database="$benchmark" \
  --execute='CALL dbcop_seal_initial_state()'
(
  cd "$BENCHBASE/target/benchbase-mysql"
  java -jar benchbase.jar -b "$benchmark" -c "$config" \
    --create=false --load=false --execute=true -d "$results/run"
)
```

Keep the same server process for preparation, sealing, and execution. Restart
with a fresh store for each application/isolation experiment. Sealing is a
one-time administration operation outside a user transaction: it discards
preparation history, not the loaded values or schema. Configure connection
details in the XML files. Their JDBC isolation setting is independent of the
server's seven-level `--iso` admission policy.

All supplied configurations select seed 1, one loader thread, two terminals,
zero warmup, ten seconds, and a requested rate of ten transactions per second.
Actual completions and transaction-type coverage must be read from the output,
not inferred from the configured duration or weights.

| Application | Scale factor | Fixture                                                         |
| ----------- | -----------: | --------------------------------------------------------------- |
| TPC-C       |            1 | One warehouse with standard upstream item/customer/order counts |
| SmallBank   |     0.000005 | Five accounts, each with savings and checking rows              |
| Voter       |            1 | Standard contestants and area-code fixture                      |
| Wikipedia   |        0.002 | Scaled upstream users, pages, revisions, and text               |

Voter selects the upstream `ddl-generic.sql` through BenchBase's `ddlpath`
configuration: its MySQL DDL uses `DROP VIEW ... CASCADE`, which the pinned SQL
parser rejects. This selects an unchanged upstream schema, not a SQL rewrite.
The relative path requires running from the distribution directory shown above.

Stock BenchBase execution is not the paper's assertion harness. Successful
transactions, retryable isolation conflicts (MySQL 1213 / SQLSTATE 40001), and
unexpected SQL errors are separate outcomes. The paper's A1-A17 application
invariants are not automatically checked by these commands. Its 100-run
violation frequencies and custom small fixtures are not reproduced by a single
ten-second run.

## Count application assertion violations

The external `cmd/assertions` client evaluates A1-A12 (TPC-C), A13 (SmallBank),
A14 (Voter), and A15-A17 (Wikipedia). It uses ordinary MySQL queries,
latest-admitted reads, and rolls validation back. SQL/scan errors are errors,
never successful checks or assertion violations. Valid and deliberately
corrupted fixtures cover all seventeen predicates in its tests.

Build the checker and experiment runner outside timings:

```bash
cd mockserver
go build -o /tmp/dbcop-assertions ./cmd/assertions
go build -o /tmp/dbcop-assertion-runner ./benchbase
/tmp/dbcop-assertion-runner \
  --server "$PWD/mockserver" --assertions /tmp/dbcop-assertions \
  --client-dir "$HOME/benchbase/target/benchbase-mysql" --java java \
  --config "$PWD/benchbase/smallbank.xml" \
  --output-dir /tmp/smallbank-causal-assertions \
  --benchmark smallbank --level causal --runs 10 --seed 1
```

Use the same release `LD_LIBRARY_PATH` as the server. Each experiment starts a
fresh server, loads through unchanged BenchBase, validates the initialized
baseline, seals it, executes the workload, and evaluates assertions. Seeds are
printed and incremented per experiment. The output directory must not exist.
Repeat for the four applications and seven server levels. Each `outcome.json`
records its seed, phase, baseline, final assertion booleans, and workload
histograms. Nonempty unexpected-SQL-error or unknown-status histograms produce
`phase: "workload-error"` even if Java exits zero. Final assertion observations
are retained, but these are not clean completed experiments.

Checker stdout is stored as `baseline.json` or `assertions.json`; stderr is
stored separately as `baseline.stderr.log` or `assertions.stderr.log`. Driver
diagnostics do not corrupt the JSON or hide the structured checker error.

For each assertion, count independent completed experiments with `holds: false`,
not transactions or mismatching rows. Report violations, completed evaluations,
and attempted experiments separately. Failed baselines, timeouts, and SQL errors
are incomplete experiments, not zero-violation runs. Ten experiments are not the
paper's hundred-experiment frequency study; `--runs 100` selects that repetition
count.

The runner applies explicit fixture prerequisites before sealing:

- SmallBank keeps the configured stock procedure mix, including Amalgamate, and
  initializes savings/checking balances to 2500 using ordinary SQL.
- Wikipedia resets initial edit counts to zero only after proving that loading
  created no recentchanges or logging entries. It never resets workload results.
- A14 uses the unchanged maintained BenchBase client's limit of 1000 votes per
  phone, as explicitly selected for this experiment, not the paper's limit of 2.
  Its large random phone domain does not reproduce the paper's contention.
- A11 preserves the artifact's executable small-fixture threshold of 2 delivered
  orders, not its contradictory SQL comment. A7 checks every line rather than
  copying the artifact's last-scanned-line overwrite.
- Monetary equalities use exact rational arithmetic over returned SQL values.
  Maintained BenchBase's FLOAT fields/Java float arithmetic can fail these
  predicates even under Serializable; retain the strong-level control instead of
  automatically attributing every failure to weak isolation.

### Read-selection policies

The server and assertion runner accept
`--read-policy uniform|sparse|age-stratified|bounded-deviation` (default
`uniform`), `--deviation-probability` (default `0.01`, finite and in `[0,1]`),
and `--deviation-budget` (default `1`, a nonnegative integer):

- `uniform` preserves the original uniform sampling over admitted versions.
- `sparse` chooses newest admitted, except with the configured probability it
  chooses uniformly among older admitted versions, including root when legal.
- `age-stratified` chooses uniformly among nonempty, disjoint age buckets:
  root/initial, oldest non-initial, newest non-initial, and remaining middle
  versions. It chooses uniformly within the middle bucket. Singleton categories
  are deduplicated; a rejected root is not replaced by the oldest version.
- `bounded-deviation` uses sparse's eligible-read probability but caps older
  choices per SQL transaction at the budget. A singleton admitted set never
  spends budget. Commit, full rollback, and disconnect reset it; statement and
  savepoint rollback restore the budget spent by the retained semantic prefix.
  Use probability `1` to deviate at the first budget-eligible reads, or budget
  `0` to always choose newest admitted.

For example, append
`--read-policy bounded-deviation --deviation-probability 1
--deviation-budget 1`
to the assertion-runner command above. Keep the same configurations and seeds;
each outcome records policy, probability, and budget. Probability applies only
to sparse/bounded-deviation; the budget applies only to bounded-deviation.
Initialization, oracle admission, commit validation, explicit replay schedules,
and latest-admitted assertion validation are unchanged. Overrides do not spend
deviation budget. The seed reproduces policy choices for the same request order,
not concurrent JDBC scheduling.

These are selection heuristics, not a PCT bug-depth guarantee. Newest-admitted
reads do not guarantee serializability. Compare bug-finding yield and execution
quality separately; policy frequencies are not estimates of a production
database's anomaly rates.

For policy comparisons, pass `--collect-coverage` to the assertion runner. It
captures `CALL dbcop_coverage()` after workload execution and before the final
assertion client runs. Outcomes include stock BenchBase's measurement duration,
measured requests, client completion/error histograms, and committed-history
coverage. Capture/decoding failures remain experiment errors, not zero coverage.

Coverage records normalized WR, same-reader WR/WR, and SO/WR motifs: session
roles, root reads, repeated variables, and shared source transactions. It omits
application values and does not reward fresh keys/version IDs. Each feature has
a first committed-transaction position for prefix comparisons. Aborted/live
prefixes are excluded. Backend committed-transaction counts are not assumed to
equal client completions, and feature equality is not history equivalence.

Use equal configured measurement windows for unchanged BenchBase comparisons,
and retain actual elapsed time, completed transactions, and errors. Use the
fixed-workload SQL runner below for separate exact-completed-work comparisons;
BenchBase's normal time-limited mode is not an exact completion-count budget.

Large fixtures also need headroom for the Go heap, Rust allocations, and the
client. On the 8 GiB benchmark VMs, `GOMEMLIMIT=3GiB` allowed the full TPC-C
validation scan to finish after an uncapped run exhausted memory. This standard
Go environment variable is a soft runtime-memory limit, not a process RSS cap;
native allocations are outside it. Set and record the same limit on both sides
of a policy comparison. See the
[Go GC guide](https://go.dev/doc/gc-guide#Memory_limit).

### Deterministic trace exploration

The `exploration` Go package searches read-choice traces through a caller-owned
`Runner`, separately from the server's native read-selection policies:

- `Guided(run, maxRuns, maxDecisions, seed)` retains executions with new
  semantic features or observable outcomes, mutates one admitted choice, and
  re-executes the workload. It is a heuristic, never an exhaustive verdict.
- `Systematic(run, maxRuns, maxDecisions)` enumerates admitted-choice leaves
  without equivalence reductions. `Complete` is true only after all branches
  finish. Branches are generated lazily, including at the run-budget boundary.

The runner must start fresh, honor every supplied admitted-candidate index
exactly, then select newest admitted candidates for the remaining reads. Report
every external read, including singleton choices, with candidates ordered
initial/oldest to newest. Oracle admission remains the runner's responsibility.
Return SQL/client errors as errors, not assertion failures. `Execution.Failed`
records an application assertion, and `Outcome` identifies the observable
result, including relevant aborts. Runs exceeding the decision bound or
diverging from the replay prefix are rejected, not silently truncated.

Features normalize session and variable identities by first appearance. They
track reader/writer relationships, successive read sources and variables,
initial reads, and newest-versus-historical choices. New IDs alone earn no
coverage. This abstraction does not prove execution equivalence.

Exact replay requires fixed transaction order and all other nondeterminism. An
uncontrolled concurrent BenchBase client cannot meet that contract using a seed
alone; these APIs are not a new BenchBase `--read-policy` value.

The oracle-backed comparison uses fractured reads, write skew, and dependency
chains at all seven levels: ten seeds, sixteen executions per policy, and a
complete bounded reference per scenario/level. Every guided corpus entry is
replayed. To emit the 630 per-campaign coverage measurements from the repository
root:

```bash
cargo build -p dbcop_ffi
env -u CGO_LDFLAGS LD_LIBRARY_PATH="$PWD/target/debug" \
  go -C mockserver test ./backend \
  -run '^TestExplorationPoliciesAgainstBoundedOracleReference$' -count=1 -v
```

Compare observed outcomes and semantic features against the reference, not
failure frequency alone. Guided exploration is not guaranteed to improve on
uniform sampling; the current small scenarios do not establish that advantage.

### Controlled SQL failure replay

`cmd/replay` executes a complete fixture and a fixed list of whole transactions.
It does not capture concurrent BenchBase/JDBC traffic or explore transaction
order. See
[`testdata/replay/lost-update.json`](testdata/replay/lost-update.json) for
schema version 1: setup SQL, immutable transaction/statement IDs, positive
session IDs, explicit commit/rollback, seed/policy, and a SELECT assertion with
typed `baseline_rows` and final `want_rows`.

From the repository root, after building `dbcop_ffi`:

```bash
env -u CGO_LDFLAGS LD_LIBRARY_PATH="$PWD/target/debug" \
  go -C mockserver run ./cmd/replay run \
  --workload testdata/replay/lost-update.json --artifact /tmp/sql-failure.json
env -u CGO_LDFLAGS LD_LIBRARY_PATH="$PWD/target/debug" \
  go -C mockserver run ./cmd/replay replay --artifact /tmp/sql-failure.json
env -u CGO_LDFLAGS LD_LIBRARY_PATH="$PWD/target/debug" \
  go -C mockserver run ./cmd/replay minimize --artifact /tmp/sql-failure.json \
  --output /tmp/sql-minimized.json --max-trials 128
```

The example's seed 2 produces a balance total of 210 rather than 200. `run`
prints the actual seed/policy and exits 1 only after persisting an assertion
failure; a passing assertion exits 0. Invalid input, baseline, SQL, iterator,
commit, replay, or persistence errors exit 2, never count as violations.
`go run` wraps nonzero program exits; invoke a built binary for exact codes.
Replay and minimization exit 0 only after fresh same-failure verification. Each
invocation emits one JSON result on stdout and diagnostics on stderr.

Artifacts include full SQL/configuration, typed observations, canonical raw
history and variable mapping, and ordered admitted read candidates. Source
identities use producer transaction/statement/write position and semantic cell
keys, not process-global table/version IDs. Strict replay rejects changed,
missing or extra reads/candidates; revision drift is informational. Existing
server `SetSchedule` behavior is unchanged. `SQLWorkloadRunner` adapts these
controlled workloads to `Guided` and `Systematic`.

Minimization deletes transaction ranges, transactions, and statements, then
tries newest read choices. It preserves the assertion fingerprint and retained
read sources, not application logic or the original bug's cause. Inspect the
result: deleting half a transfer can itself violate conservation. The result
reports `fixed_point` or `budget_exhausted`, never global minimality. Outputs
are exclusive, synced files published by hard link; existing paths are not
overwritten, and filesystems without hard-link support return an error.

Assertions use latest-admitted reads in a separate rolled-back session. Values
distinguish NULL, signed/unsigned integers, exact DECIMAL, finite float, text,
and base64 binary; unsupported date/time, JSON, or spatial values are errors.
Text and semantic identities must be valid UTF-8; invalid bytes are rejected
rather than silently replaced during JSON encoding. Binary values remain base64.
Actual DECIMAL aggregate values stay exact even when engine metadata says
DOUBLE. This validates in-process engine values, not MySQL-wire equivalence.
Measured SQL rejects DDL, transaction boundaries, session-variable changes, and
nondeterministic or stateful expressions (including sequence advancement). Table
names require qualification or USE in their session; assertion SELECTs must
provide deterministic ordering.

Measured transactions support `SAVEPOINT`, `ROLLBACK TO SAVEPOINT`, and
`RELEASE SAVEPOINT`. Names are case-insensitive; reusing a name replaces it.
Rollback retains the named prefix and that checkpoint, drops later checkpoints,
and restores staged writes, membership, read/event history, and native admission
state. Release keeps the transaction's work. Unknown rollback/release names
return a savepoint-not-found error; transaction end clears all names.

The self-contained SQL validation compares latest-state results/errors against
the upstream memory provider and completely enumerates six small weak-read
fixtures at all seven levels, comparing indexed and full-predicate-scan outcome
and admission sets. Both providers share the parser/executor, and upstream
memory sessions do not implement savepoints; named rollback is checked against
explicit prefix, publication, and admission expectations instead. This is
bounded storage/lowering evidence, not independent MySQL certification. See
[development checks](../docs/development.md#sql-storage-and-lifecycle-validation).

### Fixed-workload SQL exploration

`ExploreSQLWorkload` connects generic SQL replay to guided or systematic source
exploration. Each execution starts a fresh store with the complete fixture,
fixed sessions and transaction order. Prefix choices replay exactly; subsequent
reads choose newest admitted sources. No admission rule is relaxed.

From the repository root:

```bash
env -u CGO_LDFLAGS LD_LIBRARY_PATH="$PWD/target/debug" \
  go -C mockserver run ./cmd/replay explore \
  --workload cmd/replay/testdata/fixed-sql.json --artifact /tmp/sql-corpus.json \
  --mode guided --seed 42 --max-runs 128 --max-decisions 4096 \
  --max-transactions 2
env -u CGO_LDFLAGS LD_LIBRARY_PATH="$PWD/target/debug" \
  go -C mockserver run ./cmd/replay replay-corpus \
  --artifact /tmp/sql-corpus.json
```

`--mode systematic` enumerates the bounded source-choice tree; only
`complete: true` reports exhaustion. Guided search is heuristic. Decision bounds
include singleton external reads; transaction bounds reject oversized workloads
rather than truncate SQL. Search mutation seeds and workload/store seeds are
separate.

Artifacts retain all evaluated executions, including passing coverage cases,
stable source identities/admitted universes, the mutation corpus, and
interrupted execution evidence. Exploration exits 1 for application assertion
failures and 2 for execution errors. `replay-corpus` exits 0 after reproducing
all recorded passing/failing outcomes; forged observations or changed sources
are errors.

Ordinary `RunSQLWorkload(workload, nil)` samples the configured native policy;
workloads also accept optional `deviation_budget` (default 1). Results separate
attempted, committed, explicitly rolled-back, rejected, and completed work.
Completed means successful commit or explicit rollback, never rejection.
Workload elapsed time excludes fixture setup and final assertion validation.
Compare exact work only when these counters establish matching completed work.

These APIs do not capture arbitrary concurrent BenchBase/JDBC programs. Such
replay also needs bound SQL, client control flow/results, complete
initialization, and recorded ordering; a seed is insufficient. Stock BenchBase
policy campaigns remain separate, unmodified, equal-time experiments.

### SmallBank repaired-versus-excluded comparison

The pinned BenchBase revision `33c00473807ebd49304d114a6d769d2d2b2bbb34`
contains an Amalgamate bug independent of isolation: it zeroes source checking
and debits destination savings without a matching credit. Direct sequential
calls to the actual Java method under Serializable reduce an initial total of
10000 to 2500.

These repaired/excluded fixtures are historical controls, not the stock policy
campaign. Stock policy runs apply neither the patch nor procedure exclusions.

`amalgamate-conservation.patch` is the explicitly requested, isolated client
repair. Apply it only to a separate BenchBase checkout and rebuild its MySQL
distribution with the same Maven command above; do not patch the stock client or
server. The repair reads both source balances, clears/debits them, and credits
the combined amount to destination checking. It preserves upstream comments and
license metadata.

Run two separate experiment sets with the same seeds and server levels:

- Repaired client plus `smallbank.xml`: the paper's conserving mix,
  Amalgamate/Balance/SendPayment with weights 30/30/40.
- Stock client plus `smallbank_no_amalgamate.xml`: Balance/SendPayment only,
  renormalizing their remaining 3:4 ratio.

Both use A13's fixed-total assertion. DepositChecking, TransactSavings, and
WriteCheck are intentionally excluded because they legitimately change the
total; applying fixed-total A13 to the default six-procedure mix would be
invalid.

`AmalgamateSequential.java` is an assert-based, actual-Java integration check.
Point each invocation at a fresh dedicated database on a Serializable server:

```bash
java --class-path "$DIST/benchbase.jar:$DIST/lib/*" \
  "$DBCOP/mockserver/benchbase/AmalgamateSequential.java" \
  'jdbc:mysql://127.0.0.1:3307/smallbank?useSSL=false&allowPublicKeyRetrieval=true' \
  root '' fixed
```

Use `original` with the stock distribution to verify the money-loss
counterexample; use `fixed` with the separately repaired distribution to verify
that both sequential transfers preserve every expected balance and the total.

The adapter implements native foreign-key metadata/editors, prefix indexes, and
auto-increment IDs using `go-mysql-server` storage interfaces. The engine
dependency remains v0.20.0, its latest published release when checked; arbitrary
MySQL compatibility is not implied by these workloads.

## Isolation and storage lifetime

`--iso` accepts exactly `read-committed`, `repeatable-read`, `atomic-read`,
`causal`, `prefix`, `snapshot-isolation`, or `serializable`; unknown names are
errors. The native store chooses uniformly among admitted versions by default,
using the printed seed. Explicit replay schedules can override those choices. A
seed reproduces native random choices only with the same operation order;
concurrent client scheduling can change that order.

Transactions that observe or write data are serialized by the backend. Empty
JDBC setup transactions release execution ownership between commands without
ending the SQL transaction. Historical reads can still expose weak-isolation
anomalies; client threads do not imply parallel data-transaction execution.

The native isolation oracle is persistent **within the server process**.
Committed transactions update retained history and dependency state. Scalar read
trials reuse the accepted live-transaction prefix; only the chosen read is
retained. Rollback restores that prefix, and commit checks the complete ordered
transaction. SQL data, versions, and oracle state are in memory and are lost at
shutdown. Writerless initial reads retain compact reader constraints rather than
a graph per field; a later writer materializes the dependencies before checking
it.

Row existence is versioned alongside data cells. Historical index candidates
remain available after updates and deletes; SQL predicates still validate the
selected row values. Ordinary primary-key lookups avoid scanning every row.
Physical candidate-key order is not SQL sort order. Indexes explicitly report
unordered access, and the engine retains SQL sorting and limits instead of
replacing them with a native index-order assumption. Tables expose approximate
historical-key cardinalities to the native optimizer so it can cost indexed
lookups and hash joins. These estimates are not exact counts of currently
visible rows; visibility still comes from the oracle. Unique-key enforcement
narrows candidates with the same collation-aware historical postings, then
checks current row values. Staged cell lookups are indexed within a transaction;
statement rollback restores the previous lookup.

Mixed text/numeric predicates retain the original SQL filter and bypass
incompatible text-index ranges; other compatible predicates may still bound the
candidate set. Writes and unique-key checks retain column-type conversion.
Updating an AUTO_INCREMENT column advances the allocator for larger values and
does not rewind it for smaller values.

The standalone server uses `backend.NewEngine(provider)`, which adds a native
analyzer hook for supported indexed predicates. `go-mysql-server` v0.20.0 does
not dispatch `FilteredTable` itself. The hook prunes unrelated metadata
candidates before oracle reads and leaves SQL filters in place to validate the
selected values; embeddings must use the same constructor.

The engine supplies full old/new rows to `RowUpdater`, without SET-target
metadata. The backend records changed cells, but cannot distinguish an explicit
no-op assignment from an unchanged field supplied by the engine. Keyless
mutations retain the physical keys of their selected historical rows for the
statement. Identical selected rows remain interchangeable because the engine
does not provide an explicit row-ID handle. These are SQL-to-history mapping
limits, not relaxed consistency checks.

## Layout

| Path                       | Contents                                                                                   |
| -------------------------- | ------------------------------------------------------------------------------------------ |
| `backend/`                 | Versioned cell store, shared SQL interfaces, native oracle, and Apalache successor adapter |
| `apalachebackend/`         | Pure-Go JSON-RPC client and embedded independent isolation/step specifications             |
| `cmd/mockserver/`          | Native standalone MySQL-wire server (`--addr`, `--iso`, `--seed`)                          |
| `cmd/mockserver-apalache/` | Pure-Go comparison server (`--apalache-rpc`, `--apalache-timeout`)                         |
