# WASM API

The `dbcop_wasm` crate (`crates/wasm/src/lib.rs`) exports two functions via
`wasm_bindgen`:

## `check_consistency(history_json: &str, level: &str) -> String`

Simple consistency check.

**Parameters:**

- `history_json` -- JSON-encoded array of sessions (raw format, without the
  metadata wrapper)
- `level` -- one of: `committed-read`, `atomic-read`, `causal`, `prefix`,
  `snapshot-isolation`, `serializable`

**Returns** a JSON string:

On success:

```json
{
  "ok": true,
  "witness": { "CommitOrder": [{ "session_id": 1, "session_height": 0 }] }
}
```

On check failure:

```json
{ "ok": false, "error": { "Invalid": "Prefix" } }
```

On invalid consistency level:

```json
{ "ok": false, "error": "unknown consistency level" }
```

On malformed JSON input:

```json
{ "ok": false, "error": "expected value at line 1 column 1" }
```

## `check_consistency_trace(history_json: &str, level: &str) -> String`

Rich trace output. Same parameters as `check_consistency`. Returns additional
metadata including parsed session data and graph edges.

On success:

```json
{
  "ok": true,
  "level": "serializable",
  "session_count": 2,
  "transaction_count": 4,
  "sessions": [
    [
      {
        "id": { "session_id": 1, "session_height": 0 },
        "reads": {},
        "writes": { "0": 1 },
        "committed": true
      }
    ]
  ],
  "witness": { "CommitOrder": [...] },
  "witness_edges": [
    [{"session_id": 1, "session_height": 0}, {"session_id": 2, "session_height": 0}]
  ],
  "wr_edges": [
    [{"session_id": 1, "session_height": 0}, {"session_id": 2, "session_height": 0}]
  ]
}
```

On check failure: same structure but with `"ok": false` and `"error"` instead of
`"witness"`/`"witness_edges"`. The `sessions` and `wr_edges` fields are still
present. On invalid input: `{"ok": false, "error": "..."}`.

## `check_consistency_trace_text(history_text: &str, level: &str) -> String`

Same as `check_consistency_trace` but accepts a compact text format:

```
[x==0 x:=1]
---
[x==0 x:=2]
```

Each line is a transaction (`[events...]`). Sessions are separated by `---`.
Events use `x==v` for reads and `x:=v` for writes.

## Building WASM

```bash
deno task wasmbuild
```

Output goes to `wasmlib/` (gitignored). The generated JS bindings can be
imported in any JavaScript environment that supports WebAssembly.

## See Also

- [CLI Reference](cli-reference.md) -- command-line alternative
- [History Format](history-format.md) -- JSON schema for the history input
- [Architecture](architecture.md) -- how the WASM crate fits in
