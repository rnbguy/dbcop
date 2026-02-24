# dbcop

Runtime monitoring for transactional consistency.

[![Build](https://github.com/rnbguy/dbcop/actions/workflows/rust.yaml/badge.svg)](https://github.com/rnbguy/dbcop/actions/workflows/rust.yaml)
[![codecov](https://codecov.io/gh/rnbguy/dbcop/branch/main/graph/badge.svg)](https://codecov.io/gh/rnbguy/dbcop)
[![Deno](https://github.com/rnbguy/dbcop/actions/workflows/deno.yaml/badge.svg)](https://github.com/rnbguy/dbcop/actions/workflows/deno.yaml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

## What is dbcop?

dbcop verifies whether recorded database transaction histories satisfy specified
transactional consistency levels. Given a trace of committed transactions
(reads, writes, and their session structure), dbcop checks if the observed
behavior is consistent with guarantees like causal consistency, snapshot
isolation, or serializability.

The tool implements the algorithms from
["On the Complexity of Checking Transactional Consistency"](https://arxiv.org/abs/1908.04509)
by Ranadeep Biswas and Constantin Enea (OOPSLA 2019). The paper establishes that
read committed, read atomic, and causal consistency are checkable in polynomial
time, while prefix consistency, snapshot isolation, and serializability are
NP-complete. dbcop uses saturation-based algorithms for the polynomial levels
and constrained depth-first search with Zobrist hashing for the NP-complete
levels, with communication graph decomposition (Theorem 5.2) to reduce the
search space.

The original implementation remains at the
[`oopsla-2019`](https://github.com/rnbguy/dbcop/tree/oopsla-2019) branch.

## Consistency Levels

| Level              | Complexity  | Algorithm                 |
| ------------------ | ----------- | ------------------------- |
| Read Committed     | Polynomial  | Saturation                |
| Atomic Read        | Polynomial  | Saturation                |
| Causal             | Polynomial  | Saturation                |
| Prefix             | NP-complete | Constrained linearization |
| Snapshot Isolation | NP-complete | Constrained linearization |
| Serializable       | NP-complete | Constrained linearization |

## Quick Start

```bash
# Install the CLI
cargo install --path crates/cli

# Generate test histories
dbcop generate \
  --n-hist 5 --n-node 3 --n-var 4 --n-txn 3 --n-evt 3 \
  --output-dir /tmp/histories

# Verify consistency
dbcop verify --input-dir /tmp/histories --consistency serializable
```

## Documentation

- [Architecture](docs/architecture.md) -- crate structure, data flow, key types
- [Consistency Models](docs/consistency-models.md) -- formal definitions of all
  six levels
- [CLI Reference](docs/cli-reference.md) -- generate and verify commands, flags,
  output formats
- [History Format](docs/history-format.md) -- JSON schema with annotated
  examples
- [Algorithms](docs/algorithms.md) -- saturation, linearization, decomposition,
  SAT encoding
- [WASM API](docs/wasm-api.md) -- WASM bindings API reference
- [Development](docs/development.md) -- building, testing, contributing

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
