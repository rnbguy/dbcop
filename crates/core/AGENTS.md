# dbcop_core

`no_std` library (requires `alloc`). 12K LOC across 3 module families. All
consistency checking logic lives here.

## Module Map

```
src/
  lib.rs                    pub mod consistency, graph, history; pub use check, Consistency
  graph/
    digraph.rs              DiGraph<T> -- adjacency map, closure, topo sort, cycle detection (522 lines)
    ugraph.rs               UGraph<T> -- undirected graph for communication graph decomposition
    biconnected_component.rs  Tarjan-based biconnected component extraction
  consistency/
    mod.rs                  check() entry point + NPC decomposition dispatcher (616 lines)
    decomposition.rs        communication_graph() + biconnected_components() (337 lines)
    witness.rs              Witness enum: CommitOrder | SplitCommitOrder | SaturationOrder
    error.rs                Error enum: NonAtomic | Invalid | Cycle
    saturation/
      committed_read.rs     Read Committed checker -- builds committed order graph (377 lines)
      atomic_read.rs        Atomic Read checker -- single-pass saturation with ww edges
      causal.rs             Causal checker -- iterated saturation + incremental closure
      repeatable_read.rs    Repeatable Read checker -- validates same-variable read consistency
    linearization/
      constrained_linearization.rs  DFS engine + ConstrainedLinearizationSolver trait (1141 lines)
      prefix.rs             Prefix solver -- split-vertex with active_write (430 lines)
      snapshot_isolation.rs SI solver -- split-vertex with active_write + active_variable (284 lines)
      serializable.rs       Serializable solver -- non-split with active_write (214 lines)
  history/
    raw/
      types.rs              Session, Transaction, Event, TransactionId (482 lines)
      mod.rs                is_valid_history(), get_all_writes() validation
      display.rs            Display impls for history types
      error.rs              NonAtomicError variants
    atomic/
      mod.rs                raw -> AtomicTransactionPO conversion (544 lines)
      types.rs              AtomicTransactionPO, AtomicTransactionHistory structs
```

## Data Flow

```
Raw sessions ──> is_valid_history() ──> AtomicTransactionPO ──> check()
                                                                  │
                     ┌──── Polynomial ────┐          ┌── NP-complete ──┐
                     │ CommittedRead      │          │ check_npc()     │
                     │ AtomicRead         │          │   causal pre    │
                     │ Causal             │          │   decompose?    │
                     │   saturation loop  │          │   solve per     │
                     │   acyclicity check │          │   component     │
                     └─── Witness ────────┘          └── Witness ──────┘
```

**Polynomial path** (CommittedRead/AtomicRead/Causal): Build partial order, add
edges iteratively until fixpoint, check acyclicity. Returns
`Witness::SaturationOrder(DiGraph)`.

**NP-complete path** (Prefix/SI/Serializable):

1. Run causal pre-check (fails fast if causal violated)
2. Singleton fast-path: 1 session -> trivial witness from session order
3. Build communication graph, decompose into biconnected components
4. Solve each component via DFS linearization (`get_linearization()`)
5. Remap + merge witnesses

## Feature Flags

| Feature         | Effect                                                  |
| --------------- | ------------------------------------------------------- |
| (none/default)  | `no_std` + `alloc` only                                 |
| `serde`         | Serialize/Deserialize on DiGraph, TransactionId, etc.   |
| `compact-serde` | Enables `serde` (alias for forward compatibility)       |
| `schemars`      | JSON Schema generation (forces std due to schemars dep) |

**no_std gate** (lib.rs line 50):

```rust
#![cfg_attr(not(any(test, feature = "schemars")), no_std)]
```

Tests and `schemars` feature both pull in std. Everything else must be
`alloc`-only. Use `hashbrown` (not `std::collections::HashMap`).

## Adding a New Consistency Level

1. **Polynomial**: Add checker in `saturation/`, wire in `check()` match arm,
   return `Witness::SaturationOrder`.
2. **NP-complete**: Implement `ConstrainedLinearizationSolver` trait in
   `linearization/`, wire in `solve_npc_from_po()` match arm.
3. Add variant to `Consistency` enum (with `serde` cfg_attr if adding).
4. Add integration tests in `tests/` using `history!` macro from
   `tests/common/mod.rs`.
5. Add benchmark group in `benches/consistency.rs`.

**Frozen API**: Do NOT change `ConstrainedLinearizationSolver` trait.

## DFS Engine Key Concepts

The `constrained_linearization.rs` engine (1141 lines) drives all NPC solvers:

- **Zobrist hashing**: O(1) frontier-state memoization via `HashSet<u128>`
- **Solver hooks**: `search_options()`, `branch_score()`, `allow_next()`,
  `frontier_signature()`, `should_prune()`
- **Optimizations**: killer/history moves, nogood learning, conflict-directed
  backjumping, frontier-dominance pruning, randomized restarts with adaptive
  portfolio, principal variation ordering

Each solver provides constraint logic via trait methods; the DFS engine handles
the search mechanics.

## Test Infrastructure

- `tests/common/mod.rs`: `history!`, `session!`, `txn_committed!`,
  `txn_uncommitted!`, `ev!` macros for DSL-style test construction
- 7 integration test files covering polynomial, NP-complete, hierarchy,
  decomposition, and version-0 edge cases
- 18 Criterion benchmarks (6 levels x 3 sizes)
- Unit test modules (`#[cfg(test)]`) in most source files

## Inter-Crate Dependents

```
dbcop_cli ──> dbcop_core [serde, schemars]
dbcop_wasm ──> dbcop_core [serde]
dbcop_sat ──> dbcop_core
dbcop_parser ──> dbcop_core
dbcop_testgen ──> dbcop_core [serde]
dbcop_drivers ──> dbcop_core [serde]
```

All other crates depend on core. Changes here propagate everywhere.
