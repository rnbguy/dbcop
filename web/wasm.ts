// Single entry point for WASM imports.
// All components import from here instead of reaching into wasmlib/ directly.
// This keeps a single external reference for the esbuild bundler.
export {
  check_consistency_trace,
  check_consistency_trace_text,
  tokenize_history,
} from "../wasmlib/dbcop_wasm.js";
