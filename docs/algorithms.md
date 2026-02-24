# Algorithms

This document describes the algorithms used by dbcop to check transactional
consistency. For theoretical background, see
["On the Complexity of Checking Transactional Consistency"](https://arxiv.org/abs/1908.04509)
(Biswas & Enea, OOPSLA 2019).

## Table of Contents

- [Saturation (Polynomial Checkers)](#saturation-polynomial-checkers)
- [Constrained Linearization (NP-Complete Checkers)](#constrained-linearization-np-complete-checkers)
- [Communication Graph Decomposition](#communication-graph-decomposition)
- [SAT Encoding (Alternative Solver)](#sat-encoding-alternative-solver)
- [Performance Optimizations](#performance-optimizations)

## Saturation (Polynomial Checkers)

**Used by:** Read Committed, Atomic Read, Causal Consistency

**Source:** `crates/core/src/consistency/saturation/`

Saturation algorithms check consistency by incrementally building a **visibility
relation** -- a directed graph over transactions that tracks which transactions'
effects are visible to which others. The algorithm iterates until either a fixed
point is reached (consistent) or a cycle is detected (violation).

### General Pattern

1. Initialize the visibility relation from session order and write-read edges.
2. Apply level-specific rules to infer new visibility edges.
3. Repeat until no new edges are added (fixed point) or a cycle is found.
4. If acyclic at fixed point, return
   `Witness::SaturationOrder(visibility_graph)`.
5. If a cycle is detected, return `Error::Cycle { level, a, b }` with the
   conflicting edge.

### Committed Read (`committed_read.rs`)

The simplest checker. Builds a committed order from write-read relations and
checks for cycles. Ensures no transaction reads an overwritten value.

### Atomic Read (`atomic_read.rs`)

Extends committed-read: if transaction t2 reads any value from t1, then all of
t1's writes must be visible to t2. Adds visibility edges to enforce atomicity of
reads across variables.

### Causal (`causal.rs`)

Extends atomic-read with transitivity: the visibility relation must be
transitively closed. It enforces write-write ordering (`causal_ww`) constraints
to saturation fixed point, using incremental transitive closure for efficiency.

## Constrained Linearization (NP-Complete Checkers)

**Used by:** Prefix, Snapshot Isolation, Serializable

**Source:** `crates/core/src/consistency/linearization/`

For the NP-complete levels, dbcop first runs the causal checker (as a
prerequisite), then searches for a valid linearization using depth-first search
over topological orderings.

### The Solver Trait

The `ConstrainedLinearizationSolver` trait (`constrained_linearization.rs`)
defines the DFS framework:

```
trait ConstrainedLinearizationSolver {
    type Vertex;
    fn allow_next(&self, linearization: &[Self::Vertex], next: &Self::Vertex) -> bool;
    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]);
    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]);
}
```

- `allow_next()` -- Can this transaction be appended to the current partial
  linearization?
- `forward_book_keeping()` -- Update solver state after appending a transaction.
- `backtrack_book_keeping()` -- Undo state changes when backtracking.

The DFS engine (`get_linearization()`) explores topological orderings of the
partial order, calling these methods at each step. Zobrist hashing memoizes
visited frontier states to prune the search.

### Prefix (`prefix.rs`)

Splits each transaction into a read phase and a write phase. Finds a
linearization where the write phases form a valid commit order and each
transaction's read phase sees a consistent prefix. Returns
`Witness::CommitOrder` (write-phase transactions only).

### Snapshot Isolation (`snapshot_isolation.rs`)

Similar split into read and write phases, but additionally enforces that
concurrent transactions writing the same variable cannot both commit. Returns
`Witness::SplitCommitOrder` with the full split-phase ordering.

### Serializable (`serializable.rs`)

Finds a total order over all transactions such that every read is consistent
with the prefix of writes preceding it. Returns `Witness::CommitOrder`.

## Communication Graph Decomposition

**Source:** `crates/core/src/consistency/decomposition.rs`

**Theorem 5.2 (Biswas & Enea 2019):** A history satisfies a consistency
criterion if and only if its projection onto each connected component of the
communication graph satisfies that criterion.

The **communication graph** is an undirected graph where:

- Vertices are sessions (identified by session ID)
- An edge connects two sessions if they access at least one common variable (via
  write-read relations)

### Algorithm

1. Build the communication graph from the `AtomicTransactionPO`'s write-read
   relations.
2. Find connected components of the communication graph.
3. For each component with 2+ sessions, project the history onto those sessions
   and check independently.
4. Remap transaction IDs in each sub-witness back to original session IDs.
5. Merge all sub-witnesses into the final result.

### Impact

This decomposition reduces the search space from O(n!) to O(sum of k_i!) where
k_i are the component sizes. For histories where sessions interact sparsely
(common in practice), this can turn an intractable problem into a series of
small, fast checks.

Applied only to NP-complete levels (Prefix, Snapshot Isolation, Serializable).
Singleton components are handled via a trivial witness fast-path (no DFS/SAT
search) after the causal prerequisite check. The polynomial saturation checkers
are already efficient enough without decomposition.

## SAT Encoding (Alternative Solver)

**Source:** `crates/sat/src/lib.rs`

The `dbcop_sat` crate provides an alternative approach to checking NP-complete
consistency levels by encoding the problem as a Boolean satisfiability (SAT)
instance.

### DFS vs SAT Duality

Both the DFS solver (in `dbcop_core`) and the SAT solver (in `dbcop_sat`) check
the same consistency levels and produce the same `Witness`/`Error` types:

| Aspect           | DFS (`dbcop_core`)                       | SAT (`dbcop_sat`)                              |
| ---------------- | ---------------------------------------- | ---------------------------------------------- |
| Algorithm        | Constrained DFS with Zobrist memoization | SAT encoding with BatSAT solver                |
| Used by default  | Yes (called by `check()`)                | No (separate crate, must be called explicitly) |
| Levels supported | Prefix, Snapshot Isolation, Serializable | Prefix, Snapshot Isolation, Serializable       |
| Return type      | `Result<Witness, Error>`                 | `Result<Witness, Error>`                       |

### Encoding

For each pair of transactions (u, v), the SAT encoding introduces a Boolean
variable `before(u, v)` meaning "u precedes v in the commit order." Clauses
enforce:

- **Antisymmetry:** `before(u, v)` implies `not before(v, u)`
- **Transitivity:** `before(u, v)` and `before(v, w)` implies `before(u, w)`
- **Totality:** For each pair, either `before(u, v)` or `before(v, u)`
- **Consistency-specific constraints:** Axioms for the chosen level

On a satisfying assignment, the commit order is extracted by counting
predecessors: for each vertex u, its position is the number of vertices w where
`before(w, u)` is true.

## Performance Optimizations

### Zobrist Hashing

**Source:**
`crates/core/src/consistency/linearization/constrained_linearization.rs`

The DFS solver uses Zobrist hashing for O(1) memoization of visited frontier
states. Each transaction is assigned a random `u128` seed at initialization. The
frontier hash is maintained incrementally via XOR: adding a transaction XORs its
seed into the hash, removing it XORs again.

This replaces the naive approach of hashing `HashSet<BTreeSet<TransactionId>>`
which had O(T log T) cost per state.

### Chain Closure

**Source:** `crates/core/src/history/atomic/mod.rs`

Session order has a chain topology (each session is a linear sequence). The
chain closure optimization computes the transitive closure of session order in
O(S * T^2) using a forward scan grouped by session, rather than the general
O(V * (V+E)) closure algorithm.

### Incremental Transitive Closure

**Source:** `crates/core/src/graph/digraph.rs`

The causal checker needs to maintain the transitive closure of the visibility
relation as new edges are added during saturation. Rather than recomputing the
full closure each iteration, `incremental_closure()` extends an already-closed
graph: for each new edge (u, v), it finds all ancestors of u and all descendants
of v via BFS, then adds edges from every ancestor to every descendant.

### Visibility Adjacency Pre-fetch

**Source:** `crates/core/src/consistency/saturation/causal.rs`

In the causal checker's hot paths (`causal_ww()` and `causal_rw()`), adjacency
sets are pre-fetched from the visibility relation before iteration to avoid
repeated graph lookups and cloning during the saturation loop.

## See Also

- [Consistency Models](consistency-models.md) -- what each level means
- [Architecture](architecture.md) -- crate structure and data flow
