# Web Application and WASM API

dbcop includes a browser-based interface for interactive consistency checking,
powered by WebAssembly.

**Live demo:**
[https://rnbguy.github.io/dbcop/](https://rnbguy.github.io/dbcop/)

## WASM API

The `dbcop_wasm` crate (`crates/wasm/src/lib.rs`) exports two functions via
`wasm_bindgen`:

### `check_consistency(history_json: &str, level: &str) -> String`

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

### `check_consistency_trace(history_json: &str, level: &str) -> String`

Rich trace for visualization. Same parameters as `check_consistency`. Returns
additional metadata for rendering transaction graphs.

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
present for visualization.

## Web Application

### Tech Stack

- **Runtime:** Deno
- **Build tool:** esbuild (via `web/build.ts`)
- **Visualization:** cytoscape.js with dagre layout
- **Source files:** `web/index.html`, `web/main.ts`, `web/style.css`

### Features

- History JSON editor (textarea input)
- Pre-loaded examples: write-read, lost-update, serializable, causal-violation
- Consistency level dropdown (all 6 levels)
- Result display (pass/fail with details)
- Interactive transaction graph visualization with write-read and witness edges

### Running Locally

```bash
# Build WASM bindings first
deno task wasmbuild

# Start development server
deno task serve-web
```

The dev server runs on localhost and serves the web application with live WASM
integration.

### Building for Production

```bash
# Build WASM, then bundle the static site
deno task wasmbuild
deno task build
```

Output goes to `dist/` (gitignored). The build process:

1. esbuild bundles `web/main.ts` into `dist/main.js`
2. Copies `web/index.html` and `web/style.css` to `dist/`
3. Copies `wasmlib/` to `dist/wasmlib/`
4. Post-processes `dist/wasmlib/dbcop_wasm.js` for browser compatibility

### Browser Compatibility

The `@deno/wasmbuild` tool generates WASM bindings that use Deno-native ESM
syntax:

```javascript
import * as wasm from "./dbcop_wasm.wasm";
```

This syntax is not supported in Chrome stable. The build script (`web/build.ts`)
patches the dist copy to use `WebAssembly.instantiateStreaming` with a fallback
to `WebAssembly.instantiate` for servers that do not set the `application/wasm`
content type.

The source files in `wasmlib/` are never modified -- only the `dist/` copy is
patched.

### Deployment

The static site is automatically deployed to GitHub Pages on push to `main` via
the `pages.yaml` workflow.

## See Also

- [CLI Reference](cli-reference.md) -- command-line alternative
- [History Format](history-format.md) -- JSON schema for the history input
- [Architecture](architecture.md) -- how the WASM crate fits in
