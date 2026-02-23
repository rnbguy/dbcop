import { useEffect, useRef, useState } from "preact/hooks";
import type {
  SessionTransaction,
  TraceResult,
  TransactionId,
} from "../types.ts";

interface EdgeLine {
  id: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
  kind: "wr" | "co" | "so";
}

interface Props {
  result: TraceResult | null;
  onExportReady?: (fns: { exportPng: () => void } | null) => void;
  onHighlightReady?: (
    fn: ((edges: [TransactionId, TransactionId][]) => void) | null,
  ) => void;
}

function txId(t: TransactionId): string {
  return `S${t.session_id}T${t.session_height}`;
}

function varLabel(k: string): string {
  const n = parseInt(k, 10);
  if (isNaN(n)) return k;
  if (n >= 0 && n <= 25) {
    const letters = "xyzabcdefghijklmnopqrstuvw";
    return letters[n];
  }
  return `v${n}`;
}

function isRoot(t: TransactionId): boolean {
  return t.session_id === 0 && t.session_height === 0;
}

export function GraphPanel({ result, onExportReady, onHighlightReady }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const cardRefs = useRef<Record<string, HTMLDivElement | null>>({});
  const highlightTimersRef = useRef<number[]>([]);
  const [edges, setEdges] = useState<EdgeLine[]>([]);
  const [highlightSet, setHighlightSet] = useState<Set<string>>(new Set());

  useEffect(() => {
    onExportReady?.(null);
    return () => onExportReady?.(null);
  }, [onExportReady]);

  useEffect(() => {
    const root = containerRef.current;
    if (!root || !result?.sessions) {
      setEdges([]);
      return;
    }

    const frame = requestAnimationFrame(() => {
      const nextEdges: EdgeLine[] = [];
      const rootBox = root.getBoundingClientRect();
      const added = new Set<string>();

      // Session-order edges
      for (const session of result.sessions!) {
        for (let i = 0; i < session.length - 1; i++) {
          const src = session[i];
          const tgt = session[i + 1];
          if (isRoot(src.id)) continue;
          const srcCard = cardRefs.current[txId(src.id)];
          const tgtCard = cardRefs.current[txId(tgt.id)];
          if (!srcCard || !tgtCard) continue;
          const eid = `${txId(src.id)}-so-${txId(tgt.id)}`;
          if (added.has(eid)) continue;
          added.add(eid);
          const srcBox = srcCard.getBoundingClientRect();
          const tgtBox = tgtCard.getBoundingClientRect();
          nextEdges.push({
            id: eid,
            x1: srcBox.left + srcBox.width / 2 - rootBox.left,
            y1: srcBox.bottom - rootBox.top,
            x2: tgtBox.left + tgtBox.width / 2 - rootBox.left,
            y2: tgtBox.top - rootBox.top,
            kind: "so",
          });
        }
      }

      // Write-read edges
      if (result.wr_edges) {
        for (const [src, tgt] of result.wr_edges) {
          if (isRoot(src)) continue;
          const srcCard = cardRefs.current[txId(src)];
          const tgtCard = cardRefs.current[txId(tgt)];
          if (!srcCard || !tgtCard) continue;
          const eid = `${txId(src)}-wr-${txId(tgt)}`;
          if (added.has(eid)) continue;
          added.add(eid);
          const srcBox = srcCard.getBoundingClientRect();
          const tgtBox = tgtCard.getBoundingClientRect();
          nextEdges.push({
            id: eid,
            x1: srcBox.right - rootBox.left,
            y1: srcBox.top + srcBox.height / 2 - rootBox.top,
            x2: tgtBox.left - rootBox.left,
            y2: tgtBox.top + tgtBox.height / 2 - rootBox.top,
            kind: "wr",
          });
        }
      }

      // Witness / commit-order edges
      if (result.witness_edges) {
        for (const [src, tgt] of result.witness_edges) {
          if (isRoot(src)) continue;
          const srcCard = cardRefs.current[txId(src)];
          const tgtCard = cardRefs.current[txId(tgt)];
          if (!srcCard || !tgtCard) continue;
          const eid = `${txId(src)}-co-${txId(tgt)}`;
          if (added.has(eid)) continue;
          added.add(eid);
          const srcBox = srcCard.getBoundingClientRect();
          const tgtBox = tgtCard.getBoundingClientRect();
          nextEdges.push({
            id: eid,
            x1: srcBox.right - rootBox.left,
            y1: srcBox.top + srcBox.height / 2 - rootBox.top,
            x2: tgtBox.left - rootBox.left,
            y2: tgtBox.top + tgtBox.height / 2 - rootBox.top,
            kind: "co",
          });
        }
      }

      setEdges(nextEdges);
    });

    return () => cancelAnimationFrame(frame);
  }, [result]);

  useEffect(() => {
    if (!result?.sessions) {
      onHighlightReady?.(null);
      return;
    }

    onHighlightReady?.((pairs) => {
      if (pairs.length === 0) return;

      const ids = new Set<string>();
      for (const [src, tgt] of pairs) {
        const from = txId(src);
        const to = txId(tgt);
        ids.add(`${from}-co-${to}`);
        ids.add(`${from}-wr-${to}`);
        ids.add(`${from}-so-${to}`);
      }
      setHighlightSet(ids);

      const timer = globalThis.setTimeout(() => {
        setHighlightSet(new Set());
      }, 1500);
      highlightTimersRef.current.push(timer);
    });

    return () => {
      onHighlightReady?.(null);
      for (const timer of highlightTimersRef.current) {
        globalThis.clearTimeout(timer);
      }
      highlightTimersRef.current = [];
    };
  }, [result, onHighlightReady]);

  if (!result?.sessions) {
    return (
      <div class="graph-panel graph-panel-empty empty" aria-live="polite">
        <span>Run a check to see the transaction graph</span>
      </div>
    );
  }

  return (
    <div class="graph-panel">
      <div ref={containerRef} class="graph-container graph-grid">
        <svg class="graph-edges" aria-hidden="true" focusable="false">
          <defs>
            <marker
              id="arrow-wr"
              markerWidth="8"
              markerHeight="6"
              refX="8"
              refY="3"
              orient="auto"
            >
              <path d="M0,0 L8,3 L0,6" fill="var(--gh-success-fg)" />
            </marker>
            <marker
              id="arrow-co"
              markerWidth="8"
              markerHeight="6"
              refX="8"
              refY="3"
              orient="auto"
            >
              <path d="M0,0 L8,3 L0,6" fill="var(--gh-attention-fg)" />
            </marker>
            <marker
              id="arrow-so"
              markerWidth="8"
              markerHeight="6"
              refX="8"
              refY="3"
              orient="auto"
            >
              <path d="M0,0 L8,3 L0,6" fill="var(--gh-text-secondary)" />
            </marker>
          </defs>
          {edges.map((edge) => {
            const stroke = edge.kind === "wr"
              ? "var(--gh-success-fg)"
              : edge.kind === "co"
              ? "var(--gh-attention-fg)"
              : "var(--gh-text-secondary)";
            const marker = `url(#arrow-${edge.kind})`;
            const cls = highlightSet.has(edge.id)
              ? "graph-edge-highlight"
              : undefined;
            return (
              <line
                key={edge.id}
                x1={edge.x1}
                y1={edge.y1}
                x2={edge.x2}
                y2={edge.y2}
                stroke={stroke}
                stroke-width={edge.kind === "so" ? 1 : 2}
                stroke-dasharray={edge.kind === "so" ? "4 3" : undefined}
                marker-end={marker}
                class={cls}
              />
            );
          })}
        </svg>

        {result.sessions.map((session) => {
          const firstTxn = session[0];
          if (firstTxn && firstTxn.id.session_id === 0) return null;
          const sid = firstTxn?.id.session_id;
          return (
            <div key={sid} class="graph-session-col">
              <div class="graph-session-label">Session {sid}</div>
              {session.map((txn) => (
                <TxnCard
                  key={txId(txn.id)}
                  txn={txn}
                  refCb={(el) => {
                    cardRefs.current[txId(txn.id)] = el;
                  }}
                />
              ))}
            </div>
          );
        })}
      </div>
      <Legend />
    </div>
  );
}

// -- TxnCard ----------------------------------------------------------------

function TxnCard(
  { txn, refCb }: {
    txn: SessionTransaction;
    refCb: (el: HTMLDivElement | null) => void;
  },
) {
  const writes = Object.entries(txn.writes);
  const reads = Object.entries(txn.reads);
  return (
    <div
      class={`txn-card ${txn.committed ? "" : "txn-uncommitted"}`}
      ref={refCb}
    >
      <div class="txn-header">
        <span class="txn-id mono">
          S{txn.id.session_id}T{txn.id.session_height}
        </span>
        {!txn.committed && <span class="badge badge-fail">uncommitted</span>}
      </div>
      {writes.length > 0 && (
        <div class="txn-events">
          {writes.map(([k, v]) => (
            <div key={`w-${k}`} class="event-row event-write">
              <span class="event-op">W</span>
              <span class="mono">{varLabel(k)} := {v}</span>
            </div>
          ))}
        </div>
      )}
      {reads.length > 0 && (
        <div class="txn-events">
          {reads.map(([k, v]) => (
            <div key={`r-${k}`} class="event-row event-read">
              <span class="event-op">R</span>
              <span class="mono">
                {varLabel(k)} == {v === null ? "?" : v}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// -- Legend ------------------------------------------------------------------

function Legend() {
  return (
    <div class="graph-legend">
      <div class="legend-item">
        <span class="legend-node legend-committed" />
        <span>Committed</span>
      </div>
      <div class="legend-item">
        <span class="legend-node legend-uncommitted" />
        <span>Uncommitted</span>
      </div>
      <div class="legend-item">
        <span class="legend-edge legend-wr" />
        <span>Write-Read (WR)</span>
      </div>
      <div class="legend-item">
        <span class="legend-edge legend-co" />
        <span>Commit Order (CO)</span>
      </div>
      <div class="legend-item">
        <span class="legend-edge legend-so" />
        <span>Session Order (SO)</span>
      </div>
    </div>
  );
}
