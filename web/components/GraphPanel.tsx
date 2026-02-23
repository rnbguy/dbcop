import { useEffect, useRef, useState } from "preact/hooks";
import type {
  SessionTransaction,
  TraceResult,
  TransactionId,
} from "../types.ts";

// Viz global is loaded via CDN (viz-standalone.js). Declare minimal interface.
interface VizInstance {
  renderSVGElement(src: string): SVGSVGElement;
}

declare const Viz: {
  instance(): Promise<VizInstance>;
};

interface Props {
  result: TraceResult | null;
  onExportReady?: (fns: { exportPng: () => void } | null) => void;
  onHighlightReady?: (
    fn: ((edges: [TransactionId, TransactionId][]) => void) | null,
  ) => void;
}

let vizPromise: Promise<VizInstance> | null = null;

function getViz(): Promise<VizInstance> {
  if (!vizPromise) {
    // deno-lint-ignore no-explicit-any
    vizPromise = (globalThis as any).Viz?.instance?.() ??
      Promise.reject(new Error("Viz not loaded"));
  }
  return vizPromise!;
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

function buildLabel(txn: SessionTransaction): string {
  const parts: string[] = [];
  for (const [k, v] of Object.entries(txn.writes ?? {})) {
    parts.push(`W ${varLabel(k)} := ${v}`);
  }
  for (const [k, v] of Object.entries(txn.reads ?? {})) {
    parts.push(`R ${varLabel(k)} == ${v === null ? "?" : v}`);
  }
  return parts.join("\\n");
}

function buildDot(result: TraceResult): string {
  const css = getComputedStyle(document.documentElement);
  const c = {
    bgCanvas: css.getPropertyValue("--gh-bg-canvas").trim() || "#0d1117",
    bgDefault: css.getPropertyValue("--gh-bg-default").trim() || "#161b22",
    textPrimary: css.getPropertyValue("--gh-text-primary").trim() || "#e6edf3",
    textSecondary: css.getPropertyValue("--gh-text-secondary").trim() ||
      "#8b949e",
    accentFg: css.getPropertyValue("--gh-accent-fg").trim() || "#58a6ff",
    dangerFg: css.getPropertyValue("--gh-danger-fg").trim() || "#f85149",
    successFg: css.getPropertyValue("--gh-success-fg").trim() || "#3fb950",
    attentionFg: css.getPropertyValue("--gh-attention-fg").trim() || "#d29922",
    borderDefault: css.getPropertyValue("--gh-border-default").trim() ||
      "#30363d",
  };

  // Filter out the root session (session_id === 0)
  const sessions = (result.sessions ?? []).filter(
    (s) => s[0]?.id.session_id !== 0,
  );

  const lines: string[] = [];
  lines.push("digraph {");
  lines.push(`  bgcolor="${c.bgCanvas}";`);
  lines.push(
    `  graph [rankdir=TB, splines=spline, nodesep=1.0, ranksep=0.6, fontname="monospace", fontcolor="${c.textSecondary}", fontsize=10];`,
  );
  lines.push(
    `  node [fontname="monospace", fontsize=9, shape=box, style="rounded,filled", margin="0.2,0.12", width=1.8, fillcolor="${c.bgDefault}", color="${c.borderDefault}", fontcolor="${c.textPrimary}"];`,
  );
  lines.push(
    `  edge [fontname="monospace", fontsize=8, fontcolor="${c.textSecondary}"];`,
  );

  for (const session of sessions) {
    const sid = session[0].id.session_id;

    lines.push(`  subgraph cluster_s${sid} {`);
    lines.push(`    label="Session ${sid}";`);
    lines.push(`    style=rounded;`);
    lines.push(`    color="${c.borderDefault}";`);
    lines.push(`    fontcolor="${c.textSecondary}";`);
    lines.push(`    fontsize=10;`);

    for (const txn of session) {
      const id = txId(txn.id);
      const label = buildLabel(txn);
      const borderColor = txn.committed ? c.accentFg : c.dangerFg;
      const borderStyle = txn.committed
        ? "rounded,filled"
        : "rounded,filled,dashed";
      lines.push(
        `    ${id} [label="${label}", color="${borderColor}", style="${borderStyle}"];`,
      );
    }

    // SO edges (intra-session, dashed gray)
    for (let i = 0; i < session.length - 1; i++) {
      const src = txId(session[i].id);
      const tgt = txId(session[i + 1].id);
      lines.push(
        `    ${src} -> ${tgt} [style=dashed, color="${c.textSecondary}", arrowsize=0.6, penwidth=1];`,
      );
    }

    lines.push(`  }`);
  }

  // WR edges (green, solid, penwidth=2)
  for (const [src, tgt] of result.wr_edges ?? []) {
    if (src.session_id === 0 || tgt.session_id === 0) continue;
    lines.push(
      `  ${txId(src)} -> ${
        txId(tgt)
      } [color="${c.successFg}", penwidth=2, arrowsize=0.8];`,
    );
  }

  // CO/witness edges (amber, solid, penwidth=2)
  for (const [src, tgt] of result.witness_edges ?? []) {
    if (src.session_id === 0 || tgt.session_id === 0) continue;
    lines.push(
      `  ${txId(src)} -> ${
        txId(tgt)
      } [color="${c.attentionFg}", penwidth=2, arrowsize=0.8];`,
    );
  }

  lines.push("}");
  return lines.join("\n");
}

export function GraphPanel({ result, onExportReady, onHighlightReady }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const highlightTimers = useRef<number[]>([]);
  const [renderError, setRenderError] = useState<string | null>(null);

  // Always signal no PNG export
  useEffect(() => {
    onExportReady?.(null);
    return () => onExportReady?.(null);
  }, [onExportReady]);

  // Render graph when result or theme changes
  useEffect(() => {
    const container = containerRef.current;
    if (!container || !result?.sessions) return;

    let cancelled = false;

    async function render() {
      try {
        const viz = await getViz();
        if (cancelled) return;
        const dot = buildDot(result!);
        const svg = viz.renderSVGElement(dot);
        if (cancelled) return;
        // Style the SVG to fill its container
        svg.setAttribute("width", "100%");
        svg.setAttribute("height", "100%");
        svg.style.display = "block";
        container!.innerHTML = "";
        container!.appendChild(svg);
        setRenderError(null);

        // Register highlight handler AFTER svg is in DOM
        if (onHighlightReady) {
          onHighlightReady((pairs) => {
            if (pairs.length === 0) return;
            // Build set of edge title strings to match: "S1T0->S2T1"
            const toHighlight = new Set<string>();
            for (const [src, tgt] of pairs) {
              toHighlight.add(`${txId(src)}->${txId(tgt)}`);
              toHighlight.add(`${txId(src)}&#45;&gt;${txId(tgt)}`);
            }
            const edgeGs = container!.querySelectorAll("g.edge");
            const matched: Element[] = [];
            for (const g of edgeGs) {
              const title = g.querySelector("title")?.textContent ?? "";
              if (toHighlight.has(title)) {
                g.classList.add("graph-edge-highlight");
                matched.push(g);
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
        }
      } catch (err) {
        if (!cancelled) setRenderError(String(err));
      }
    }

    render();

    // Re-render on theme change
    const observer = new MutationObserver(() => {
      if (!cancelled) render();
    });
    observer.observe(document.documentElement, {
      attributeFilter: ["data-theme"],
    });

    return () => {
      cancelled = true;
      observer.disconnect();
      onHighlightReady?.(null);
      for (const t of highlightTimers.current) globalThis.clearTimeout(t);
      highlightTimers.current = [];
    };
  }, [result, onHighlightReady]);

  if (!result?.sessions) {
    return (
      <div class="graph-panel graph-panel-empty empty" aria-live="polite">
        <span>Run a check to see the transaction graph</span>
      </div>
    );
  }

  if (renderError) {
    return (
      <div class="graph-panel graph-panel-empty empty">
        <span>Graph render error: {renderError}</span>
      </div>
    );
  }

  return (
    <div class="graph-panel">
      <div
        ref={containerRef}
        class="graph-container"
        style={{ overflow: "auto" }}
      />
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
