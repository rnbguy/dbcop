# SAT Encoding Design for dbcop_sat

## SAT Solver Choice

**Crate**: `rustsat` (v0.7.5) + `rustsat-batsat` (pure Rust backend)

- `rustsat` is a framework library providing types, traits, and encodings
- `rustsat-batsat` is a pure-Rust SAT solver (no C bindings required)
- Neither supports `no_std` -- `dbcop_sat` must be a **separate crate**, not
  part of `dbcop_core`
- BatSAT works with WebAssembly (verified with Deno + wasmbuild 0.21.0)
- wasmbuild 0.21.0 requires `wasm-bindgen = "=0.2.106"`

## Encoding Approach

Instead of encoding the full linearization problem as a monolithic SAT instance,
we use SAT to decide the **topological ordering** of transactions given the
visibility partial order. The key constraint is `allow_next` from the DFS solver.

### Variables

For `n` transactions, we use ordering variables:

- `order[i] = k` means transaction `i` is placed at position `k` in the
  linearization
- Encoded as: `x_{i,k}` = true iff transaction `i` is at position `k`
  (one-hot encoding per transaction)

### Common Constraints

1. **Exactly-one position per transaction**: for each transaction `i`,
   exactly one `x_{i,k}` is true
2. **Exactly-one transaction per position**: for each position `k`,
   exactly one `x_{i,k}` is true
3. **Visibility order**: if `i` must come before `j` (visibility edge),
   then for all positions `k1 >= k2`: NOT(x_{i,k1} AND x_{j,k2})
   Simplified: sum of positions of `i` < sum of positions of `j`

### Serializable Constraints

The `allow_next` check for serializability:
- When transaction `v` is placed next, for each variable `x` that `v` writes:
  - Either no other transaction currently has active writes to `x`, OR
  - The only remaining reader of `x` (from a prior write) is `v` itself

Encoded as: for each pair of transactions `(i, j)` that write the same
variable `x`, if `i` comes before `j`, then all transactions that read
`x` from `i` must come between `i` and `j` in the ordering.

### Snapshot Isolation Additional Constraints

SI uses split-phase vertices `(TransactionId, bool)`:
- `(t, false)` = read phase of transaction `t`
- `(t, true)` = write phase of transaction `t`
- Read phase always precedes write phase of the same transaction

Additional SI constraint (`active_variable` disjointness):
- When placing the read phase `(t, false)`, the write set of `t` must not
  overlap with the write set of any transaction whose read phase has been
  placed but whose write phase has not yet been placed
- Encoded as: for concurrent transactions `t1`, `t2` with overlapping write
  sets, their read-write phase intervals cannot interleave

## Crate Boundary Decision

`dbcop_sat` is a **separate crate** because:
1. `rustsat`/`rustsat-batsat` require `std`
2. `dbcop_core` must remain `no_std`
3. Dependency direction: `dbcop_sat` depends on `dbcop_core`, not the reverse

## API Design

```rust
// dbcop_sat/src/lib.rs
pub fn check_serializable_sat(
    sessions: &[Session<u64, u64>],
) -> Result<(), Error<u64, u64>>;

pub fn check_snapshot_isolation_sat(
    sessions: &[Session<u64, u64>],
) -> Result<(), Error<u64, u64>>;
```

Both functions:
1. Build `AtomicTransactionPO` from sessions (reuse causal check from core)
2. Encode the ordering problem as SAT clauses
3. Solve with BatSAT
4. Return `Ok(())` if satisfiable, `Err(Error::Invalid(...))` if unsatisfiable
