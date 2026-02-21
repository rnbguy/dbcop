import type { TraceResult } from "../types.ts";

interface Props {
  result: TraceResult | null;
  loading: boolean;
}

export function ResultBar({ result, loading }: Props) {
  if (loading) {
    return (
      <div class="result-bar">
        <span class="spinner" /> Checking...
      </div>
    );
  }

  if (!result) {
    return (
      <div class="result-bar">
        <span class="badge badge-neutral">Ready</span>
        <span class="result-hint">Select an example and click Check</span>
      </div>
    );
  }

  if (result.ok) {
    const witnessType = result.witness
      ? Object.keys(result.witness)[0] ?? "OK"
      : "OK";
    return (
      <div class="result-bar">
        <span class="badge badge-pass">PASS</span>
        <span class="result-meta">
          {result.level ?? ""}
          {result.session_count != null && (
            <span class="result-stat">
              {result.session_count} sessions, {result.transaction_count ?? 0}
              {" "}
              txns
            </span>
          )}
        </span>
        <span class="result-witness">{witnessType}</span>
      </div>
    );
  }

  const errMsg = typeof result.error === "string"
    ? result.error
    : typeof result.error === "object" && result.error !== null
    ? Object.keys(result.error)[0] ?? "Error"
    : "Error";
  return (
    <div class="result-bar">
      <span class="badge badge-fail">FAIL</span>
      <span class="result-meta">{result.level ?? ""}</span>
      <span class="result-error">{errMsg}</span>
    </div>
  );
}
