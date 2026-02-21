// Single entry point for WASM imports.
// All components import from here instead of reaching into wasmlib/ directly.
// This keeps a single external reference for the esbuild bundler.
import * as wasm from "../wasmlib/dbcop_wasm.js";

type WasmFn = (...args: string[]) => string;

function getFn(name: string): WasmFn {
  const fn = (wasm as Record<string, unknown>)[name];
  if (typeof fn !== "function") {
    throw new Error(`missing wasm export: ${name}`);
  }
  return fn as WasmFn;
}

export const check_consistency_trace = getFn("check_consistency_trace");
export const check_consistency_trace_text = getFn(
  "check_consistency_trace_text",
);
export const check_consistency_step_init = getFn("check_consistency_step_init");
export const check_consistency_step_next = getFn("check_consistency_step_next");
export const tokenize_history = getFn("tokenize_history");
