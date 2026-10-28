# Architecture

## Workspace Overview

dbcop is a Rust workspace (v0.2.0, edition 2021, MSRV 1.99.0) with eight crates:

| Crate           | Path              | Purpose                                                                                                                |
| --------------- | ----------------- | ---------------------------------------------------------------------------------------------------------------------- |
| `dbcop_core`    | `crates/core/`    | Main library (`no_std` compatible). Consistency checking algorithms, graph data structures, and history types.         |
| `dbcop_cli`     | `crates/cli/`     | Command-line binary for generating and verifying transaction histories.                                                |
| `dbcop_wasm`    | `crates/wasm/`    | WebAssembly bindings via `wasm-bindgen` for browser-based checking.                                                    |
| `dbcop_sat`     | `crates/sat/`     | SAT solver backend using `rustsat` + BatSAT. Alternative to the DFS-based linearization solver for NP-complete levels. |
| `dbcop_testgen` | `crates/testgen/` | Random history generator with coherence guarantees (every read backed by a committed write).                           |
| `dbcop_parser`  | `crates/parser/`  | Text history parser (`winnow` + `logos`) for the compact `.hist` format.                                               |
| `dbcop_drivers` | `crates/drivers/` | Database drivers (currently unimplemented stubs) for executing histories against real databases.                       |
| `dbcop_ffi`     | `crates/ffi/`     | C ABI for batch checks and persistent incremental consistency oracles, used by the Go mockserver.                      |

## Data Flow

```
Raw History (JSON)
    │
    ▼
Vec<Session<V, Ver>>          ── crates/core/src/history/raw/types.rs
    │
    │ TryFrom validation (repeatable-read check, write uniqueness)
    ▼
AtomicTransactionHistory       ── crates/core/src/history/atomic/types.rs
    │
    │ Build partial order (session order, write-read relations)
    ▼
AtomicTransactionPO            ── crates/core/src/history/atomic/mod.rs
    │
    │ check() dispatches by consistency level
    ▼
Result<Witness, Error>         ── crates/core/src/consistency/mod.rs
```

## Core Library Modules (`dbcop_core`)

### `graph/`

- `digraph.rs` -- `DiGraph<T>`: directed graph with adjacency map. Supports
  `add_edge()`, `closure()`, `topological_sort()`, `union()`, `is_acyclic()`,
  `find_cycle_edge()`, `incremental_closure()`, `to_edge_list()`.
- `ugraph.rs` -- Undirected graph type.
- `biconnected_component.rs` -- Biconnected component decomposition for
  communication graph analysis.

### `history/`

- `raw/types.rs` -- Input types: `Event` (Read/Write enum), `Transaction`
  (events + committed flag), `Session` (Vec of transactions), `EventId`.
- `raw/mod.rs` -- Validation functions: `get_all_writes()`,
  `get_committed_writes()`, `is_valid_history()`.
- `raw/error.rs` -- Structural errors: `IncompleteHistory`, `UncommittedWrite`,
  `SameVersionWrite`, etc.
- `atomic/types.rs` -- `TransactionId` (session_id + session_height),
  `AtomicTransactionHistory`, `AtomicTransactionInfo`.
- `atomic/mod.rs` -- `AtomicTransactionPO`: partial order holding session order,
  write-read relations, visibility relation. Includes chain closure
  optimization.

### `consistency/`

- `mod.rs` -- `check()` entry point and `Consistency` enum. Routes to saturation
  or linearization checkers. Implements communication graph decomposition for
  NP-complete levels.
- `saturation/` -- Polynomial-time checkers: `committed_read.rs`,
  `atomic_read.rs`, `causal.rs`, `repeatable_read.rs`.
- `linearization/` -- NP-complete checkers: `constrained_linearization.rs` (DFS
  solver trait + solver-provided DFS options + legal-first move ordering +
  Zobrist/state-signature memoization hooks + killer/history + nogood/backjump
  - exact failed-frontier caching + randomized restarts + adaptive portfolio +
    principal variation/counter-move ordering), `prefix.rs`,
    `snapshot_isolation.rs`, `serializable.rs`.
- `decomposition.rs` -- Communication graph construction and biconnected
  component extraction (Theorem 5.2 from Biswas & Enea 2019).
- `witness.rs` -- `Witness` enum: `CommitOrder`, `SplitCommitOrder`,
  `SaturationOrder`.
- `error.rs` -- `Error` enum: `NonAtomic`, `Invalid`, `Cycle`.

## Key Types

| Type                  | Location                  | Description                                                                                                                                                |
| --------------------- | ------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `TransactionId`       | `history/atomic/types.rs` | `{ session_id: u64, session_height: u64 }`. Default `(0,0)` is the root node. Ordered lexicographically.                                                   |
| `DiGraph<T>`          | `graph/digraph.rs`        | Directed graph backed by `HashMap<T, HashSet<T>>`. Core data structure for all relation graphs.                                                            |
| `AtomicTransactionPO` | `history/atomic/mod.rs`   | Per-history partial order. Holds `session_order`, `write_read_relation` (per variable), `wr_union`, and `visibility_relation` as `DiGraph<TransactionId>`. |
| `Consistency`         | `consistency/mod.rs`      | Enum: `CommittedRead`, `RepeatableRead`, `AtomicRead`, `Causal`, `Prefix`, `SnapshotIsolation`, `Serializable`.                                            |
| `Witness`             | `consistency/witness.rs`  | Proof of consistency. `CommitOrder(Vec<TransactionId>)`, `SplitCommitOrder(Vec<(TransactionId, bool)>)`, `SaturationOrder(DiGraph<TransactionId>)`.        |
| `Error<V, Ver>`       | `consistency/error.rs`    | `NonAtomic(NonAtomicError)`, `Invalid(Consistency)`, `Cycle { level, a, b }`.                                                                              |

## MySQL Mockserver

`mockserver/` is a separate Go module. `go-mysql-server` provides the MySQL wire
protocol and SQL execution; the backend maps table cells and row existence to
versioned keys. Clients create their own databases and tables. The server does
not include a TPC-C schema or workload implementation.

The standalone server initially uses serial, latest-value initialization.
`CALL dbcop_seal_initial_state()` atomically turns the completed dataset into
root values and starts weak execution. Preparation transactions are outside the
measured history; post-seal transactions are retained. This is an initialization
boundary, not a durable database checkpoint.

Each store lazily creates a native `IncrementalChecker` handle per requested
consistency level. Go sends committed transaction deltas and scalar read
candidates. Accepted live reads retain their graph work; rejected trials undo
only their changes. Commit checks the complete ordered transaction and retains
writer indexes, dependency relations, closure state, and reusable order
information. Search caches are invalidated when constraints change.

Transactions execute serially across sessions, but reads can select historical
versions admitted by the configured level. Commit publishes cells, versioned
existence, and the ordered transaction log only after the oracle accepts.
Rollback and disconnect discard staged state. Storage and native oracle state
are process-local, not durable. See [mockserver usage](../mockserver/README.md)
for release builds and commands using the unmodified official TPC-C client.

## End-to-End Testing Pipeline

For testing real databases, dbcop aims at a generate-execute-verify pipeline.
The execute step is currently an unimplemented stub (`dbcop_drivers` is
`todo!()`); only generate + verify run today:

1. **Generate** (`dbcop_testgen`): Create random transaction histories with
   coherence guarantees.
2. **Execute** (`dbcop_drivers`): Run histories against a real database cluster
   via the `DbDriver` trait. STUBBED -- not yet implemented.
3. **Verify** (`dbcop_core`): Check whether the observed results satisfy a
   consistency level.

The `DbDriver` trait (`crates/drivers/src/lib.rs`) defines:

```rust
pub trait DbDriver {
    type Error: core::fmt::Debug;
    fn connect(config: &ClusterConfig) -> Result<Self, Self::Error>;
    fn execute(&self, history: &History) -> Result<Vec<Session<u64, u64>>, Self::Error>;
}
```

`ClusterConfig` holds `hosts`, `port`, and `db_name`. Three driver stubs exist
(all `todo!()`):

- `galera.rs` -- Galera Cluster (MySQL/MariaDB)
- `antidotedb.rs` -- AntidoteDB (CRDT-based)
- `cockroachdb.rs` -- CockroachDB (distributed SQL)

## See Also

- [Consistency Models](consistency-models.md) -- the seven levels and their
  formal definitions
- [Algorithms](algorithms.md) -- how the checkers work
- [History Format](history-format.md) -- JSON schema for transaction histories
- [CLI Reference](cli-reference.md) -- command-line usage
