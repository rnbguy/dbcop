# dbcop

Transactional consistency checking and weak-isolation SQL testing.

[![Build](https://github.com/rnbguy/dbcop/actions/workflows/rust.yaml/badge.svg)](https://github.com/rnbguy/dbcop/actions/workflows/rust.yaml)
[![codecov](https://codecov.io/gh/rnbguy/dbcop/branch/main/graph/badge.svg)](https://codecov.io/gh/rnbguy/dbcop)
[![Deno](https://github.com/rnbguy/dbcop/actions/workflows/deno.yaml/badge.svg)](https://github.com/rnbguy/dbcop/actions/workflows/deno.yaml)
[![Go Mockserver](https://github.com/rnbguy/dbcop/actions/workflows/go.yaml/badge.svg)](https://github.com/rnbguy/dbcop/actions/workflows/go.yaml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

## What is dbcop?

dbcop verifies whether recorded database transaction histories satisfy specified
transactional consistency levels. Given a trace of committed transactions
(reads, writes, and their session structure), dbcop checks if the observed
behavior is consistent with guarantees like causal consistency, snapshot
isolation, or serializability.

It also provides a [MonkeyDB-style SQL mockserver](mockserver/README.md) for
testing applications under weak isolation. Ordinary MySQL clients execute
against versioned storage; the selected consistency model determines which
historical values reads may observe. This complements checking recorded
histories with actively exploring application behavior.

The Rust checkers implement the algorithms from
["On the Complexity of Checking Transactional Consistency"](https://arxiv.org/abs/1908.04509)
by Ranadeep Biswas and Constantin Enea (OOPSLA 2019). The paper establishes that
read committed, read atomic, and causal consistency are checkable in polynomial
time, while prefix consistency, snapshot isolation, and serializability are
NP-complete. dbcop uses saturation-based algorithms for the polynomial levels
and constrained depth-first search with Zobrist hashing for the NP-complete
levels, with communication graph decomposition (Theorem 5.2) to reduce the
search space.

The original implementation is preserved in the
[historical snapshot](https://github.com/rnbguy/dbcop/tree/e3bdd5ade6100970177dc82c3fe39e9e95120d3e).

## Consistency Levels

| Level              | Complexity  | Algorithm                 |
| ------------------ | ----------- | ------------------------- |
| Read Committed     | Polynomial  | Saturation                |
| Repeatable Read    | Polynomial  | Saturation                |
| Atomic Read        | Polynomial  | Saturation                |
| Causal             | Polynomial  | Saturation                |
| Prefix             | NP-complete | Constrained linearization |
| Snapshot Isolation | NP-complete | Constrained linearization |
| Serializable       | NP-complete | Constrained linearization |

## Quick Start

For the history-checking CLI:

```bash
# Install the CLI
cargo install --locked --path crates/cli

# Generate test histories
dbcop generate \
  --n-hist 5 --n-node 3 --n-var 4 --n-txn 3 --n-evt 3 \
  --output-dir /tmp/histories

# Verify consistency
dbcop verify --input-dir /tmp/histories --consistency serializable
```

## Weak-Isolation SQL Testing

The Go mockserver follows the approach in
[MonkeyDB](https://arxiv.org/abs/2103.02830). It reuses `go-mysql-server` for
SQL execution and the MySQL wire protocol, with two admission backends:

- **Native:** persistent incremental Rust checking through the C ABI.
- **Apalache:** a pure-Go comparison server using the canonical TLA+
  specifications for all seven isolation levels and an external explorer.

Load a schema and fixture through ordinary SQL, then call
`dbcop_seal_initial_state()` before the weak-isolation workload. The native
server supports uniform, sparse, age-stratified, and bounded-deviation read
selection. Controlled SQL workloads additionally support systematic/guided
exploration, strict replay, and failure minimization.

The [mockserver guide](mockserver/README.md) covers builds, startup,
[unchanged BenchBase clients](mockserver/README.md#run-unchanged-benchbase-clients)
for TPC-C, SmallBank, Voter, and Wikipedia, and the external
[A1-A17 assertion checker](mockserver/README.md#count-application-assertion-violations).
SQL errors and invalid baselines are separate from application violations; these
experiments are not a matched reproduction of the paper's results.

This is an in-memory experiment server, not a production database or physical
replica cluster. Data transactions are serialized; legal historical reads expose
weak behavior. Seeds alone do not reproduce concurrent JDBC scheduling.

## Documentation

- [Architecture](docs/architecture.md) -- crate structure, data flow, key types
- [Consistency Models](docs/consistency-models.md) -- formal definitions of all
  seven levels
- [CLI Reference](docs/cli-reference.md) -- generate and verify commands, flags,
  output formats
- [History Format](docs/history-format.md) -- JSON schema with annotated
  examples
- [Algorithms](docs/algorithms.md) -- saturation, linearization, decomposition,
  SAT encoding
- [WASM API](docs/wasm-api.md) -- WASM bindings API reference
- [Development](docs/development.md) -- building, testing, contributing
- [SQL Mockservers](mockserver/README.md) -- native/Apalache backends,
  BenchBase, read policies, assertions, and controlled exploration

## Citation

```bibtex
@inproceedings{biswas2019complexity,
  title     = {On the Complexity of Checking Transactional Consistency},
  author    = {Biswas, Ranadeep and Enea, Constantin},
  booktitle = {Proceedings of the ACM on Programming Languages (OOPSLA)},
  year      = {2019},
  doi       = {10.1145/3360591}
}
```

## License

Apache-2.0. See [LICENSE](LICENSE) for details.
