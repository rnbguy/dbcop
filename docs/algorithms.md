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

**Used by:** Read Committed, Repeatable Read, Atomic Read, Causal Consistency

**Source:** `crates/core/src/consistency/saturation/`

The polynomial checkers derive commit-order constraints from the observed
session order (`so`) and write-read relation (`wr`). These constraints are not
additional causal observations: feeding inferred commit edges back into the rule
premises would reject legal histories.

### General Pattern

1. Validate committed reads and the level's repeated-read requirements.
2. Form the fixed premise relation from `so union wr`; Causal uses its
   transitive closure.
3. Derive the level's required commit-order edges from that fixed relation.
4. If the resulting commit graph is acyclic, return
   `Witness::SaturationOrder(commit_graph)`.
5. Otherwise return `Error::Cycle { level, a, b }` with a conflicting edge.

### Committed Read (`committed_read.rs`)

The simplest checker. Builds a committed order from write-read relations and
checks for cycles. Ensures no transaction reads an overwritten value.

### Repeatable Read (`repeatable_read.rs`)

Extends committed-read with intra-transaction read stability checks: repeated
reads of the same variable in one transaction must resolve to the same writer
(or to the latest local write when present). On success, returns the
committed-order graph as the saturation witness.

### Atomic Read (`atomic_read.rs`)

Extends repeatable-read with cross-variable atomicity constraints. It derives
write ordering once from the fixed union of session order and write-read edges,
then checks the resulting commit graph for cycles.

### Causal (`causal.rs`)

Compute causal reachability as the transitive closure of `so union wr`.
Algorithm 1 in the paper derives required writer ordering from this fixed
relation, then checks the commit graph for cycles. Inferred write-write edges
must not be fed back into causal reachability or used to derive further causal
premises.

## Constrained Linearization (NP-Complete Checkers)

**Used by:** Prefix, Snapshot Isolation, Serializable

**Source:** `crates/core/src/consistency/linearization/`

For the NP-complete levels, dbcop first runs the causal checker (as a
prerequisite), then searches for a valid linearization using depth-first search
over topological orderings.

Prefix and Snapshot Isolation split transactions into read and write phases.
Session-order and write-read dependencies connect a writer's commit phase to the
successor's read phase. Constraints inferred only for commit order connect write
phases instead; treating them as visibility would incorrectly force the
successor's snapshot to include an earlier commit. The DFS and SAT encodings use
the same distinction.

Complexity note: Worst-case complexity is NP-complete when the number of
sessions is unbounded.

For bounded session count `k`, let `n` be the history size. A frontier state is
determined by per-session progress (and phase bits for split solvers), so the
number of distinct frontier states is polynomial:

- non-split solvers: `O(n^k)`
- split solvers: `O((2n)^k) = O(n^k)` for fixed `k`

These bounds count frontiers, not every mutable state of this DFS
implementation. The solvers also retain active readers and, for SI, open
writers; memo signatures mix in that state. Equal frontiers must not be assumed
interchangeable without a state-equivalence argument. Finite tests are neither
that argument nor a proof of the implementation's complexity bound.

The replica campaigns use one ordered session per replica, so their fixed
replica count also fixes `k`. Independent clients or reconnects can increase the
session count; a physical replica bound alone does not impose this convention.
See
[bounded model validation](development.md#independent-tlatlc-semantic-oracle)
for the tested domain and evidence limits.

### The Solver Trait

The `ConstrainedLinearizationSolver` trait (`constrained_linearization.rs`)
defines the DFS framework:

```
trait ConstrainedLinearizationSolver {
    type Vertex;
    fn search_options(&self) -> DfsSearchOptions;
    fn branch_score(&self, linearization: &[Self::Vertex], next: &Self::Vertex) -> i64;
    fn zobrist_value(&self, next: &Self::Vertex) -> u128;
    fn frontier_signature(&self, frontier_hash: u128, linearization: &[Self::Vertex]) -> u128;
    fn should_prune(&self, linearization: &[Self::Vertex], frontier_len: usize) -> bool;
    fn allow_next(&self, linearization: &[Self::Vertex], next: &Self::Vertex) -> bool;
    fn forward_book_keeping(&mut self, linearization: &[Self::Vertex]);
    fn backtrack_book_keeping(&mut self, linearization: &[Self::Vertex]);
}
```

- `allow_next()` -- Can this transaction be appended to the current partial
  linearization?
- `search_options()` -- Solver-provided DFS policy (memoization and branch
  ordering mode).
- `branch_score()` -- Optional heuristic score for frontier ordering (used by
  high-score/low-score modes).
- `zobrist_value()` -- Solver-provided Zobrist token source for frontier-state
  signatures.
- `frontier_signature()` -- Optional solver-state fingerprint mixed into
  memoization keys (e.g., active-write sets).
- `should_prune()` -- Optional branch-and-bound style pruning hook.
- `forward_book_keeping()` -- Update solver state after appending a transaction.
- `backtrack_book_keeping()` -- Undo state changes when backtracking.

The DFS engine (`get_linearization()`) explores topological orderings of the
partial order, calling these methods at each step. Zobrist hashing memoizes
visited frontier states to prune the search. Candidate ordering first
prioritizes currently legal moves (`allow_next = true`), then applies
`branch_score()` according to `search_options()`.

The current DFS search stack also includes:

- killer/history move ordering (learned move boosts),
- counter-move ordering (learned parent->response replies),
- nogood learning on failed signatures,
- conflict-directed backjumping with learned jump depths,
- failed-frontier caching at fixed solver-state signature, requiring equal
  frontiers,
- randomized, budgeted restarts with final exhaustive fallback,
- adaptive attempt-level heuristic portfolio selection,
- principal variation (PV) ordering across restart attempts.

The `dominance_pruning` option only reuses failures for equal frontiers.
Containment alone is unsound: a smaller frontier can represent work completed
before an interval started, while the failed larger frontier still has that work
blocked inside the interval. The cache retains distinct failed frontiers
separately rather than discarding subsets.

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
criterion if and only if its projection onto each biconnected component of the
communication graph satisfies that criterion.

The **communication graph** is an undirected graph where:

- Vertices are sessions (identified by session ID)
- An edge connects two sessions if they access at least one common variable (via
  write-read relations)

### Algorithm

1. Build the communication graph from the `AtomicTransactionPO`'s write-read
   relations.
2. Find biconnected components of the communication graph.
3. For each component with 2+ sessions, project the history onto those sessions
   and check independently.
4. If components are disjoint, remap and merge sub-witnesses directly.
5. If components overlap on articulation sessions, fall back to solving on the
   full PO to emit a non-duplicated witness.

### Impact

For disjoint decompositions, this reduces the search space from O(n!) to O(sum
of k_i!) where k_i are the component sizes. For articulation-overlap cases,
dbcop falls back to a full solve for correctness.

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
states. Each transaction is assigned a deterministic `u128` token using fixed
hash seeds. The frontier hash is maintained incrementally via XOR: adding a
transaction XORs its token into the hash, removing it XORs again. Repeated
hashing of the same state must agree for memoization and backtracking to work.

This replaces the naive approach of hashing `HashSet<BTreeSet<TransactionId>>`
which had O(T log T) cost per state.

Prefix, Snapshot Isolation, and Serializable solvers strengthen this with
state-aware signatures: memo keys include active solver state (`active_write`
and, for SI, `active_variable`) in addition to frontier hash. Reader and count
tokens include their variable, so equal reader changes on different variables
cannot systematically cancel and alias distinct states during failed-frontier
caching. These fingerprints can still collide; they are not exact state
representations.

### Chain Closure

**Source:** `crates/core/src/history/atomic/mod.rs`

Session order has a chain topology (each session is a linear sequence). The
chain closure optimization computes the transitive closure of session order in
O(S * T^2) using a forward scan grouped by session, rather than the general
O(V * (V+E)) closure algorithm.

### Incremental Transitive Closure

**Source:** `crates/core/src/graph/digraph.rs`

`incremental_closure()` extends an already-closed graph: for each new edge (u,
v), it finds all ancestors of u and all descendants of v, then inserts the
cross-product. The persistent checker journals incremental changes to causal
reachability and commit-order closure separately. Inferred commit edges never
enter causal reachability.

### Visibility Adjacency Pre-fetch

**Source:** `crates/core/src/history/atomic/mod.rs`

The `causal_ww()` and `causal_rw()` graph helpers pre-fetch adjacency sets to
avoid repeated graph lookups and clones. The causal batch checker applies
`causal_ww()` once to fixed causal reachability, not in a feedback loop.

## See Also

- [Consistency Models](consistency-models.md) -- what each level means
- [Architecture](architecture.md) -- crate structure and data flow
