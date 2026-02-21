# Consistency Models

dbcop checks six transactional consistency levels, based on the formal framework
from
["On the Complexity of Checking Transactional Consistency"](https://arxiv.org/abs/1908.04509)
by Ranadeep Biswas and Constantin Enea (OOPSLA 2019).

## Hierarchy

The six levels form a strict hierarchy (each level implies all weaker ones):

```
Read Committed  <  Atomic Read  <  Causal  <  Prefix  <  Snapshot Isolation  <  Serializable
      RC               RA           CC          PC             SI                  SER
  (polynomial)    (polynomial)  (polynomial) (NP-complete) (NP-complete)      (NP-complete)
```

## Summary Table

| Level              | CLI Flag             | Complexity  | Algorithm                       | Witness Type                                   |
| ------------------ | -------------------- | ----------- | ------------------------------- | ---------------------------------------------- |
| Read Committed     | `committed-read`     | Polynomial  | Saturation                      | `SaturationOrder(DiGraph)`                     |
| Atomic Read        | `atomic-read`        | Polynomial  | Saturation                      | `SaturationOrder(DiGraph)`                     |
| Causal             | `causal`             | Polynomial  | Saturation                      | `SaturationOrder(DiGraph)`                     |
| Prefix             | `prefix`             | NP-complete | Constrained linearization (DFS) | `CommitOrder(Vec<TransactionId>)`              |
| Snapshot Isolation | `snapshot-isolation` | NP-complete | Constrained linearization (DFS) | `SplitCommitOrder(Vec<(TransactionId, bool)>)` |
| Serializable       | `serializable`       | NP-complete | Constrained linearization (DFS) | `CommitOrder(Vec<TransactionId>)`              |

## Formal Framework

A **history** is a triple (T, so, wr) where:

- **T** is a set of committed transactions
- **so** is the session order (a partial order reflecting the order within each
  session)
- **wr** is the write-read relation identifying, for each read, the transaction
  that produced the value

Each consistency level is defined by a set of axioms constraining a **commit
order** (co) -- a total order over transactions that must be compatible with
session order and write-read relations.

The unified axiom schema (from the paper) is:

> For all variables x, for all transactions t1, t2: if t1 wrote the value that
> some transaction reads, and t2 also writes x, then t2 must precede t1 in co
> (unless the level's condition phi allows otherwise).

## Level Definitions

### Read Committed (RC)

**Informal:** Every read observes a value written by a committed transaction. No
dirty reads.

**Axiom:** For each variable x, if transaction t1 writes x and t2 reads the
value written by t1, then any other transaction t3 that also writes x must
either precede t1 or follow t2 in the commit order.

**Checker:** Saturation algorithm builds a committed order graph. If the graph
is acyclic, the history is RC-consistent.

**Source:** `crates/core/src/consistency/saturation/committed_read.rs`

### Atomic Read (RA)

**Informal:** All reads within a single transaction are atomic -- a transaction
cannot observe partial effects of another transaction. No fractured reads.

**Axiom:** Extends RC with the constraint that if transaction t1 is visible to
t2 (t2 reads any value from t1), then all of t1's writes are visible to t2.

**Checker:** Saturation algorithm extends committed-read visibility with
atomic-read constraints. Builds visibility relation to a fixed point; cycle
detection indicates violation.

**Source:** `crates/core/src/consistency/saturation/atomic_read.rs`

### Causal Consistency (CC)

**Informal:** If transaction t1 causally affects t2, then all transactions that
observe t2 must also observe t1 in the same order. Causality is transitive.

**Axiom:** The commit order must be compatible with the transitive closure of
session order and write-read relations. The visibility relation must be
transitively closed.

**Checker:** Saturation algorithm iteratively extends the visibility relation
with causal constraints (write-write and read-write ordering). Uses incremental
transitive closure for efficiency. Continues until fixed point or cycle.

**Source:** `crates/core/src/consistency/saturation/causal.rs`

### Prefix Consistency (PC)

**Informal:** Every transaction observes a consistent prefix of the global
commit order. If a transaction sees the effects of t1, it must also see all
transactions that precede t1 in the commit order.

**Axiom:** The commit order is a total order extending the causal order, and
every transaction's observed snapshot is a downward-closed prefix of this total
order.

**Checker:** First runs the causal checker. Then uses constrained depth-first
search to find a valid linearization where each transaction's read-set is
consistent with a prefix. Uses Zobrist hashing for memoization.

**Source:** `crates/core/src/consistency/linearization/prefix.rs`

### Snapshot Isolation (SI)

**Informal:** Each transaction reads from a consistent point-in-time snapshot.
Concurrent transactions that write to the same variable cannot both commit
(write-write conflict avoidance).

**Axiom:** Extends prefix consistency with the additional constraint that if two
transactions both write to the same variable, they must observe different
snapshots (their read phases are ordered).

**Checker:** First runs the causal checker. Then uses a split-phase
linearization: each transaction is split into a read half and a write half. The
DFS solver finds an ordering where read phases precede the corresponding write
phases and write-write conflicts are respected.

**Source:** `crates/core/src/consistency/linearization/snapshot_isolation.rs`

### Serializable (SER)

**Informal:** The execution is equivalent to some serial (sequential) execution
of all transactions. The strongest consistency guarantee.

**Axiom:** There exists a total order over all transactions such that every
transaction's reads are consistent with the writes of all preceding transactions
in that order.

**Checker:** First runs the causal checker. Then uses constrained DFS to find a
complete linearization. Every transaction must read values consistent with the
linear prefix preceding it.

**Source:** `crates/core/src/consistency/linearization/serializable.rs`

## Error Types

When a history fails a consistency check, `check()` returns one of:

| Error Variant               | Produced By                          | Meaning                                                                          |
| --------------------------- | ------------------------------------ | -------------------------------------------------------------------------------- |
| `NonAtomic(NonAtomicError)` | Validation (pre-check)               | Structural issue: uncommitted write read, incomplete history, etc.               |
| `Cycle { level, a, b }`     | Saturation checkers (RC, RA, CC)     | Cycle detected in visibility relation; `a` and `b` are conflicting transactions. |
| `Invalid(Consistency)`      | Linearization checkers (PC, SI, SER) | No valid linearization exists for the given level.                               |

## See Also

- [Algorithms](algorithms.md) -- detailed description of saturation and
  linearization algorithms
- [History Format](history-format.md) -- JSON schema for input histories
- [CLI Reference](cli-reference.md) -- how to run checks from the command line
