import { useEffect, useRef } from "preact/hooks";
import type { TraceResult, TransactionId } from "../types.ts";

// Cytoscape is loaded via CDN -- declared globally.
declare const cytoscape: (opts: Record<string, unknown>) => CyInstance;

interface CyInstance {
  destroy: () => void;
  resize: () => void;
  fit: (padding?: number) => void;
  style: () => { fromJson: (s: unknown[]) => { update: () => void } };
  json: (opts: { style: unknown[] }) => void;
}

interface Props {
  result: TraceResult | null;
}

export function GraphPanel({ result }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const cyRef = useRef<CyInstance | null>(null);

  useEffect(() => {
    if (cyRef.current) {
      cyRef.current.destroy();
      cyRef.current = null;
    }
    if (!containerRef.current || !result?.sessions) return;

    const elements = buildElements(result);
    if (elements.length === 0) return;

    const style = buildGraphStyle();

    cyRef.current = cytoscape({
      container: containerRef.current,
      elements,
      style,
      layout: {
        name: "dagre",
        rankDir: "LR",
        nodeSep: 50,
        rankSep: 80,
        padding: 30,
      },
      userZoomingEnabled: true,
      userPanningEnabled: true,
      boxSelectionEnabled: false,
    });

    return () => {
      if (cyRef.current) {
        cyRef.current.destroy();
        cyRef.current = null;
      }
    };
  }, [result]);

  // Re-fit on resize
  useEffect(() => {
    const observer = new ResizeObserver(() => {
      if (cyRef.current) {
        cyRef.current.resize();
        cyRef.current.fit(30);
      }
    });
    if (containerRef.current) observer.observe(containerRef.current);
    return () => observer.disconnect();
  }, []);

  if (!result?.sessions) {
    return (
      <div class="graph-panel empty">
        <span>Run a check to see the graph</span>
      </div>
    );
  }

  return (
    <div class="graph-panel">
      <div ref={containerRef} class="graph-container" />
      <Legend />
    </div>
  );
}

// -- Legend component --------------------------------------------------------

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

// -- Build cytoscape elements -----------------------------------------------

function txId(t: TransactionId): string {
  return `S${t.session_id}T${t.session_height}`;
}

function txLabel(
  sessions: TraceResult["sessions"],
  tid: TransactionId,
): string {
  if (!sessions) return txId(tid);
  const session = sessions.find((s) =>
    s.some((t) => t.id.session_id === tid.session_id)
  );
  if (!session) return txId(tid);
  const txn = session.find((t) =>
    t.id.session_id === tid.session_id &&
    t.id.session_height === tid.session_height
  );
  if (!txn) return txId(tid);

  let label = txId(tid);
  const writes = Object.entries(txn.writes);
  const reads = Object.entries(txn.reads);
  if (writes.length > 0) {
    label += "\n" + writes.map(([k, v]) => `W x${k}:=${v}`).join("\n");
  }
  if (reads.length > 0) {
    label += "\n" +
      reads.map(([k, v]) => `R x${k}==${v === null ? "?" : v}`).join("\n");
  }
  return label;
}

function buildElements(result: TraceResult): Record<string, unknown>[] {
  const els: Record<string, unknown>[] = [];
  const addedEdges = new Set<string>();

  if (!result.sessions) return els;

  // Add nodes
  for (const session of result.sessions) {
    for (const txn of session) {
      if (txn.id.session_id === 0 && txn.id.session_height === 0) continue;
      els.push({
        data: {
          id: txId(txn.id),
          label: txLabel(result.sessions, txn.id),
          sessionId: txn.id.session_id,
          committed: txn.committed,
        },
      });
    }
  }

  // Session-order edges (consecutive transactions in same session)
  for (const session of result.sessions) {
    for (let i = 0; i < session.length - 1; i++) {
      const src = session[i];
      const tgt = session[i + 1];
      if (src.id.session_id === 0) continue;
      const eid = `${txId(src.id)}-so-${txId(tgt.id)}`;
      if (!addedEdges.has(eid)) {
        addedEdges.add(eid);
        els.push({
          data: {
            id: eid,
            source: txId(src.id),
            target: txId(tgt.id),
            label: "SO",
            edgeType: "so",
          },
        });
      }
    }
  }

  // Write-read edges
  if (result.wr_edges) {
    for (const [src, tgt] of result.wr_edges) {
      if (src.session_id === 0) continue;
      const eid = `${txId(src)}-wr-${txId(tgt)}`;
      if (!addedEdges.has(eid)) {
        addedEdges.add(eid);
        els.push({
          data: {
            id: eid,
            source: txId(src),
            target: txId(tgt),
            label: "WR",
            edgeType: "wr",
          },
        });
      }
    }
  }

  // Witness / commit-order edges
  if (result.witness_edges) {
    for (const [src, tgt] of result.witness_edges) {
      if (src.session_id === 0) continue;
      const eid = `${txId(src)}-co-${txId(tgt)}`;
      if (!addedEdges.has(eid)) {
        addedEdges.add(eid);
        els.push({
          data: {
            id: eid,
            source: txId(src),
            target: txId(tgt),
            label: "CO",
            edgeType: "co",
          },
        });
      }
    }
  }

  return els;
}

// -- Graph styles (reads CSS vars from the page) ----------------------------

function getCssVar(name: string): string {
  return getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim();
}

function buildGraphStyle(): Record<string, unknown>[] {
  const bg = getCssVar("--gh-bg-canvas") || "#0d1117";
  const text = getCssVar("--gh-text-primary") || "#e6edf3";
  const muted = getCssVar("--gh-text-secondary") || "#8b949e";
  const border = getCssVar("--gh-border-default") || "#30363d";
  const nodeCommitted = getCssVar("--gh-accent-fg") || "#58a6ff";
  const nodeUncommitted = getCssVar("--gh-danger-fg") || "#f85149";
  const edgeWr = getCssVar("--gh-success-fg") || "#3fb950";
  const edgeCo = getCssVar("--gh-attention-fg") || "#d29922";
  const edgeSo = muted;

  return [
    {
      selector: "node",
      style: {
        label: "data(label)",
        "text-wrap": "wrap",
        "text-valign": "center",
        "text-halign": "center",
        "font-family":
          "ui-monospace, 'Cascadia Code', 'Source Code Pro', Menlo, monospace",
        "font-size": "10px",
        color: text,
        "background-color": bg,
        "border-width": 2,
        "border-color": nodeCommitted,
        width: 60,
        height: 60,
        shape: "ellipse",
        "text-max-width": "120px",
      },
    },
    {
      selector: "node[committed = false]",
      style: {
        "border-color": nodeUncommitted,
        "border-style": "dashed",
      },
    },
    {
      selector: "edge",
      style: {
        "curve-style": "bezier",
        "target-arrow-shape": "triangle",
        "arrow-scale": 0.8,
        label: "data(label)",
        "font-size": "9px",
        color: muted,
        "text-background-color": bg,
        "text-background-opacity": 0.8,
        "text-background-padding": "2px",
        "text-rotation": "autorotate",
        width: 1.5,
        "line-color": border,
        "target-arrow-color": border,
      },
    },
    {
      selector: "edge[edgeType = 'wr']",
      style: {
        "line-color": edgeWr,
        "target-arrow-color": edgeWr,
        color: edgeWr,
      },
    },
    {
      selector: "edge[edgeType = 'co']",
      style: {
        "line-color": edgeCo,
        "target-arrow-color": edgeCo,
        color: edgeCo,
      },
    },
    {
      selector: "edge[edgeType = 'so']",
      style: {
        "line-color": edgeSo,
        "target-arrow-color": edgeSo,
        "line-style": "dashed",
        color: edgeSo,
      },
    },
  ];
}
