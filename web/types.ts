// Types shared across all web components.
// Mirrors the WASM API response shapes from check_consistency_trace.

export interface TransactionId {
  session_id: number;
  session_height: number;
}

export interface SessionTransaction {
  id: TransactionId;
  reads: Record<string, number | null>;
  writes: Record<string, number>;
  committed: boolean;
}

export interface TraceResult {
  ok: boolean;
  level?: string;
  session_count?: number;
  transaction_count?: number;
  sessions?: SessionTransaction[][];
  witness?: Record<string, unknown>;
  witness_edges?: [TransactionId, TransactionId][];
  wr_edges?: [TransactionId, TransactionId][];
  error?: unknown;
}

export interface HighlightToken {
  kind: string;
  start: number;
  end: number;
  text: string;
}

export type ConsistencyLevel =
  | "committed-read"
  | "atomic-read"
  | "causal"
  | "prefix"
  | "snapshot-isolation"
  | "serializable";

export type InputFormat = "text" | "json";

export type Theme = "dark" | "light";
