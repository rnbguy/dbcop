import { useEffect, useRef, useState } from "preact/hooks";
import type {
  SessionTransaction,
  TraceResult,
  TransactionId,
  TxEvent,
} from "../types.ts";

interface Props {
  result: TraceResult | null;
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

// -- TxCard -----------------------------------------------------------------

interface TxCardProps {
  txn: SessionTransaction;
}

function TxCard({ txn }: TxCardProps) {
  if (txn.events && txn.events.length > 0) {
    return (
      <div
        class={`tx-card ${txn.committed ? "tx-committed" : "tx-uncommitted"}`}
      >
        {txn.events.map((evt: TxEvent, i: number) =>
          evt.type === "W"
            ? (
              <div class="tx-event tx-write" key={`e-${i}`}>
                <span class="tx-op">W</span> {varLabel(evt.variable)} :={" "}
                {evt.version}
              </div>
            )
            : (
              <div class="tx-event tx-read" key={`e-${i}`}>
                <span class="tx-op">R</span> {varLabel(evt.variable)} =={" "}
                {evt.version === null ? "?" : evt.version}
              </div>
            )
        )}
      </div>
    );
  }
  // Fallback: render writes then reads (old behavior)
  const writes = Object.entries(txn.writes ?? {});
  const reads = Object.entries(txn.reads ?? {});
  return (
    <div
      class={`tx-card ${txn.committed ? "tx-committed" : "tx-uncommitted"}`}
    >
      {writes.map(([k, v]) => (
        <div class="tx-event tx-write" key={`w-${k}`}>
          <span class="tx-op">W</span> {varLabel(k)} := {v}
        </div>
      ))}
      {reads.map(([k, v]) => (
        <div class="tx-event tx-read" key={`r-${k}`}>
          <span class="tx-op">R</span> {varLabel(k)} == {v === null ? "?" : v}
        </div>
      ))}
    </div>
  );
}

// -- Edge types -------------------------------------------------------------

interface EdgeDef {
  key: string;
  src: TransactionId;
  tgt: TransactionId;
  kind: "wr" | "co" | "so";
}

// -- Main component ---------------------------------------------------------

export function GraphPanel({ result, onHighlightReady }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const slotRefs = useRef<Map<string, HTMLDivElement>>(new Map());
  const pathRefs = useRef<Map<string, SVGPathElement>>(new Map());
  const highlightTimers = useRef<number[]>([]);
  const [svgPaths, setSvgPaths] = useState<
    Array<{ key: string; d: string; kind: string }>
  >([]);

  // Filter sessions: remove root (session_id === 0)
  const sessions = (result?.sessions ?? []).filter(
    (s) => s[0]?.id.session_id !== 0,
  );
  const maxHeight = sessions.reduce(
    (mx, s) => Math.max(mx, s.length),
    0,
  );

  // Build edge definitions
  const edges: EdgeDef[] = [];
  if (result?.sessions) {
    // SO edges (intra-session consecutive)
    for (const session of sessions) {
      for (let i = 0; i < session.length - 1; i++) {
        edges.push({
          key: `${txId(session[i].id)}->${txId(session[i + 1].id)}`,
          src: session[i].id,
          tgt: session[i + 1].id,
          kind: "so",
        });
      }
    }
    // WR edges
    for (const [src, tgt] of result.wr_edges ?? []) {
      if (src.session_id === 0 || tgt.session_id === 0) continue;
      edges.push({
        key: `${txId(src)}->${txId(tgt)}`,
        src,
        tgt,
        kind: "wr",
      });
    }
    // CO/witness edges
    for (const [src, tgt] of result.witness_edges ?? []) {
      if (src.session_id === 0 || tgt.session_id === 0) continue;
      edges.push({
        key: `${txId(src)}->${txId(tgt)}`,
        src,
        tgt,
        kind: "co",
      });
    }
  }

  // Compute SVG paths after layout
  const computePaths = () => {
    const container = containerRef.current;
    if (!container || sessions.length === 0) {
      setSvgPaths([]);
      return;
    }
    const cRect = container.getBoundingClientRect();
    const newPaths: Array<{ key: string; d: string; kind: string }> = [];

    for (const edge of edges) {
      const srcEl = slotRefs.current.get(txId(edge.src));
      const tgtEl = slotRefs.current.get(txId(edge.tgt));
      if (!srcEl || !tgtEl) continue;

      const srcR = srcEl.getBoundingClientRect();
      const tgtR = tgtEl.getBoundingClientRect();

      let x1: number, y1: number, x2: number, y2: number;
      let d: string;

      if (edge.kind === "so") {
        // Vertical: bottom-center to top-center
        x1 = srcR.left + srcR.width / 2 - cRect.left;
        y1 = srcR.bottom - cRect.top;
        x2 = tgtR.left + tgtR.width / 2 - cRect.left;
        y2 = tgtR.top - cRect.top;
        d = `M ${x1} ${y1} L ${x2} ${y2}`;
      } else {
        // Horizontal: determine direction
        const srcCX = srcR.left + srcR.width / 2 - cRect.left;
        const tgtCX = tgtR.left + tgtR.width / 2 - cRect.left;
        const srcCY = srcR.top + srcR.height / 2 - cRect.top;
        const tgtCY = tgtR.top + tgtR.height / 2 - cRect.top;

        if (Math.abs(srcCX - tgtCX) < 2) {
          // Same column: use vertical path
          x1 = srcR.left + srcR.width / 2 - cRect.left;
          y1 = srcCY > tgtCY ? srcR.top - cRect.top : srcR.bottom - cRect.top;
          x2 = tgtR.left + tgtR.width / 2 - cRect.left;
          y2 = srcCY > tgtCY ? tgtR.bottom - cRect.top : tgtR.top - cRect.top;
          const cpOff = 20;
          d = `M ${x1} ${y1} C ${x1 + cpOff} ${y1} ${
            x2 + cpOff
          } ${y2} ${x2} ${y2}`;
        } else if (srcCX < tgtCX) {
          // Left to right
          x1 = srcR.right - cRect.left;
          y1 = srcCY;
          x2 = tgtR.left - cRect.left;
          y2 = tgtCY;
          d = `M ${x1} ${y1} C ${x1 + 40} ${y1} ${x2 - 40} ${y2} ${x2} ${y2}`;
        } else {
          // Right to left
          x1 = srcR.left - cRect.left;
          y1 = srcCY;
          x2 = tgtR.right - cRect.left;
          y2 = tgtCY;
          d = `M ${x1} ${y1} C ${x1 - 40} ${y1} ${x2 + 40} ${y2} ${x2} ${y2}`;
        }
      }

      newPaths.push({ key: edge.key, d, kind: edge.kind });
    }

    setSvgPaths(newPaths);
  };

  // Recompute paths on result change / resize / theme change
  useEffect(() => {
    if (!result?.sessions || sessions.length === 0) return;

    // Small delay to let DOM settle after render
    const raf = requestAnimationFrame(() => computePaths());

    const observer = new ResizeObserver(() => computePaths());
    if (containerRef.current) {
      observer.observe(containerRef.current);
    }

    // Re-render on theme change
    const themeObs = new MutationObserver(() => computePaths());
    themeObs.observe(document.documentElement, {
      attributeFilter: ["data-theme"],
    });

    return () => {
      cancelAnimationFrame(raf);
      observer.disconnect();
      themeObs.disconnect();
    };
  }, [result]);

  // Register highlight handler
  useEffect(() => {
    if (!onHighlightReady || !result?.sessions) return;

    onHighlightReady((pairs) => {
      if (!pairs || pairs.length === 0) return;
      const toHighlight = new Set<string>();
      for (const [src, tgt] of pairs) {
        toHighlight.add(`${txId(src)}->${txId(tgt)}`);
      }
      const matched: SVGPathElement[] = [];
      for (const [key, el] of pathRefs.current) {
        if (toHighlight.has(key)) {
          el.classList.add("graph-edge-highlight");
          matched.push(el);
        }
      }
      if (matched.length > 0) {
        const t = globalThis.setTimeout(() => {
          for (const el of matched) {
            el.classList.remove("graph-edge-highlight");
          }
        }, 1500);
        highlightTimers.current.push(t);
      }
    });

    return () => {
      onHighlightReady(null);
      for (const t of highlightTimers.current) globalThis.clearTimeout(t);
      highlightTimers.current = [];
    };
  }, [result, onHighlightReady, svgPaths]);

  // Store ref for a slot element
  const setSlotRef = (id: string) => (el: HTMLDivElement | null) => {
    if (el) {
      slotRefs.current.set(id, el);
    } else {
      slotRefs.current.delete(id);
    }
  };

  // Store ref for a path element
  const setPathRef = (key: string) => (el: SVGPathElement | null) => {
    if (el) {
      pathRefs.current.set(key, el);
    } else {
      pathRefs.current.delete(key);
    }
  };

  if (!result?.sessions) {
    return (
      <div class="graph-panel graph-panel-empty empty" aria-live="polite">
        <span>Run a check to see the transaction graph</span>
      </div>
    );
  }

  // Build session-to-txn lookup
  const sessionTxns = new Map<number, Map<number, SessionTransaction>>();
  for (const session of sessions) {
    const m = new Map<number, SessionTransaction>();
    for (const txn of session) {
      m.set(txn.id.session_height, txn);
    }
    sessionTxns.set(session[0].id.session_id, m);
  }

  const sessionIds = sessions.map((s) => s[0].id.session_id).sort(
    (a, b) => a - b,
  );

  return (
    <div class="graph-panel">
      <div
        ref={containerRef}
        class="graph-container"
        style={{ overflow: "auto", position: "relative", flex: "1" }}
      >
        <div class="graph-table-outer">
          {sessionIds.map((sid) => (
            <div class="session-col" key={sid}>
              <div class="session-col-header">Session {sid}</div>
              {Array.from({ length: maxHeight }, (_, h) => {
                const txn = sessionTxns.get(sid)?.get(h);
                const id = `S${sid}T${h}`;
                return (
                  <div
                    class="tx-slot"
                    key={id}
                    data-txid={id}
                    ref={setSlotRef(id)}
                  >
                    {txn ? <TxCard txn={txn} /> : <div class="tx-slot-empty" />}
                  </div>
                );
              })}
            </div>
          ))}
        </div>
        <svg
          class="graph-edges-svg"
          aria-hidden="true"
          focusable="false"
          style={{
            position: "absolute",
            inset: "0",
            width: "100%",
            height: "100%",
            pointerEvents: "none",
            overflow: "visible",
          }}
        >
          <defs>
            <marker
              id="arrow-wr"
              markerWidth="8"
              markerHeight="6"
              refX="7"
              refY="3"
              orient="auto"
            >
              <path
                d="M 0 0 L 8 3 L 0 6 z"
                fill="var(--gh-edge-wr)"
              />
            </marker>
            <marker
              id="arrow-co"
              markerWidth="8"
              markerHeight="6"
              refX="7"
              refY="3"
              orient="auto"
            >
              <path
                d="M 0 0 L 8 3 L 0 6 z"
                fill="var(--gh-attention-fg)"
              />
            </marker>
            <marker
              id="arrow-so"
              markerWidth="8"
              markerHeight="6"
              refX="7"
              refY="3"
              orient="auto"
            >
              <path
                d="M 0 0 L 8 3 L 0 6 z"
                fill="var(--gh-edge-so)"
              />
            </marker>
          </defs>
          {svgPaths.map((p) => (
            <path
              key={p.key}
              ref={setPathRef(p.key)}
              d={p.d}
              fill="none"
              stroke={p.kind === "wr"
                ? "var(--gh-edge-wr)"
                : p.kind === "co"
                ? "var(--gh-attention-fg)"
                : "var(--gh-edge-so)"}
              stroke-width={p.kind === "so" ? 1 : 2}
              stroke-dasharray={p.kind === "so" ? "4 3" : undefined}
              marker-end={`url(#arrow-${p.kind})`}
            />
          ))}
        </svg>
      </div>
      <Legend />
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
