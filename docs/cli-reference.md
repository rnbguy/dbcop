# CLI Reference

## Installation

```bash
cargo install --path crates/cli
```

This installs the `dbcop` binary.

## Commands

### `dbcop generate`

Generate random transactional histories for testing.

```bash
dbcop generate [OPTIONS]
```

**Flags:**

| Flag                  | Type      | Description                                     |
| --------------------- | --------- | ----------------------------------------------- |
| `--n-hist <N>`        | `u64`     | Number of histories to generate                 |
| `--n-node <N>`        | `u64`     | Number of sessions (nodes) per history          |
| `--n-var <N>`         | `u64`     | Number of variables in the keyspace             |
| `--n-txn <N>`         | `u64`     | Number of transactions per session              |
| `--n-evt <N>`         | `u64`     | Number of events (reads/writes) per transaction |
| `--output-dir <PATH>` | `PathBuf` | Directory to write generated `.json` files      |

**Parameter interactions:**

- `n_node` controls parallelism: more sessions means more concurrent actors.
- `n_var` controls contention: fewer variables means more transactions touch the
  same keys.
- `n_txn * n_node` gives the total number of transactions per history.
- `n_evt` controls transaction size: more events means larger transactions.
- The generator ensures coherence: every read is backed by a committed write.

**Example:**

```bash
dbcop generate \
  --n-hist 5 \
  --n-node 3 \
  --n-var 4 \
  --n-txn 3 \
  --n-evt 3 \
  --output-dir /tmp/histories

# Output: Generated 5 histories to /tmp/histories
```

Each generated file is named `{id}.json` and contains the history in the
[JSON format](history-format.md).

### `dbcop verify`

Check whether transaction histories satisfy a consistency level.

```bash
dbcop verify [OPTIONS]
```

**Flags:**

| Flag                    | Type      | Description                                           |
| ----------------------- | --------- | ----------------------------------------------------- |
| `--input-dir <DIR>`     | `PathBuf` | Directory containing history `.json` files (required) |
| `--consistency <LEVEL>` | enum      | Consistency level to check (required)                 |
| `--verbose`             | `bool`    | Print witness on PASS, full error on FAIL             |
| `--json`                | `bool`    | Output one JSON object per file to stdout             |

**Consistency level values:** `committed-read`, `atomic-read`, `causal`,
`prefix`, `snapshot-isolation`, `serializable`

**Output Formats:**

Default:

```
0.json: PASS
1.json: FAIL (Invalid(Prefix))
```

With `--verbose`:

```
0.json: PASS
  witness: CommitOrder([TransactionId { session_id: 1, session_height: 0 }, ...])
1.json: FAIL
  error: Invalid(Prefix)
```

With `--json`:

```json
{"file":"0.json","ok":true,"witness":{"CommitOrder":[{"session_id":1,"session_height":0}]}}
{"file":"1.json","ok":false,"error":{"Invalid":"Prefix"}}
```

**Exit codes:**

- `0` -- all histories passed
- `1` -- at least one history failed (or I/O error)

**Example:**

```bash
dbcop verify \
  --input-dir /tmp/histories \
  --consistency serializable
```

## Debugging with RUST_LOG

The CLI uses `tracing-subscriber` with the `RUST_LOG` environment variable:

```bash
# Show checker entry/exit and results
RUST_LOG=debug dbcop verify --input-dir ./histories --consistency serializable

# Show per-iteration saturation details
RUST_LOG=dbcop_core=trace dbcop verify --input-dir ./histories --consistency causal
```

Log levels:

- `debug` -- checker entry/exit, results, decomposition info
- `trace` -- per-iteration saturation details, visibility relation updates

## Worked Example: Detecting a Violation

Consider a history with two sessions where both read variable 0 and then write
to it (a lost-update scenario):

**Input** (`lost-update.json`):

```json
{
  "params": {
    "id": 0,
    "n_node": 2,
    "n_variable": 1,
    "n_transaction": 1,
    "n_event": 2
  },
  "info": "lost-update example",
  "start": "2025-01-01T00:00:00Z",
  "end": "2025-01-01T00:00:01Z",
  "data": [
    [
      {
        "events": [{ "Write": { "variable": 0, "version": 0 } }],
        "committed": true
      },
      {
        "events": [
          { "Read": { "variable": 0, "version": 0 } },
          { "Write": { "variable": 0, "version": 1 } }
        ],
        "committed": true
      }
    ],
    [
      {
        "events": [
          { "Read": { "variable": 0, "version": 0 } },
          { "Write": { "variable": 0, "version": 2 } }
        ],
        "committed": true
      }
    ]
  ]
}
```

Session 1 and Session 2 both read variable 0 at version 0 (the initial write),
then each write a new version. This is a classic lost-update conflict.

**Checking at causal level (passes):**

```bash
$ dbcop verify --input-dir . --consistency causal
lost-update.json: PASS
```

**Checking at snapshot-isolation level (fails):**

```bash
$ dbcop verify --input-dir . --consistency snapshot-isolation
lost-update.json: FAIL (Invalid(SnapshotIsolation))
```

The history passes causal consistency because there is no causal cycle, but
fails snapshot isolation because two concurrent transactions both read the same
version and write to the same variable -- violating the write-write conflict
avoidance rule.

## See Also

- [History Format](history-format.md) -- JSON schema for input files
- [Consistency Models](consistency-models.md) -- what each level means
- [Web and WASM](web-and-wasm.md) -- browser-based alternative
