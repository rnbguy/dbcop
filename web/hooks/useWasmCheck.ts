import { useCallback, useRef, useState } from "preact/hooks";
import type { ConsistencyLevel, InputFormat, TraceResult } from "../types.ts";

const CHECK_TIMEOUT_MS = 10_000;

interface WasmCheckState {
  result: TraceResult | null;
  loading: boolean;
  timedOut: boolean;
}

interface WasmCheckActions {
  runCheck: (
    text: string,
    level: ConsistencyLevel,
    format: InputFormat,
  ) => void;
  clear: () => void;
}

export function useWasmCheck(): [WasmCheckState, WasmCheckActions] {
  const [result, setResult] = useState<TraceResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [timedOut, setTimedOut] = useState(false);
  const abortRef = useRef(false);

  const runCheck = useCallback(
    (text: string, level: ConsistencyLevel, format: InputFormat) => {
      abortRef.current = false;
      setLoading(true);
      setTimedOut(false);
      setResult(null);

      const timeout = setTimeout(() => {
        setTimedOut(true);
      }, CHECK_TIMEOUT_MS);

      // Run WASM check asynchronously via dynamic import
      (async () => {
        try {
          const wasm = await import("../wasm.ts");
          let json: string;
          if (format === "text") {
            json = wasm.check_consistency_trace_text(text, level);
          } else {
            json = wasm.check_consistency_trace(text, level);
          }
          if (!abortRef.current) {
            setResult(JSON.parse(json) as TraceResult);
          }
        } catch (err) {
          if (!abortRef.current) {
            setResult({
              ok: false,
              error: err instanceof Error ? err.message : String(err),
            });
          }
        } finally {
          clearTimeout(timeout);
          if (!abortRef.current) {
            setLoading(false);
            setTimedOut(false);
          }
        }
      })();
    },
    [],
  );

  const clear = useCallback(() => {
    abortRef.current = true;
    setResult(null);
    setLoading(false);
    setTimedOut(false);
  }, []);

  return [{ result, loading, timedOut }, { runCheck, clear }];
}
