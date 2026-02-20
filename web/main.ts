import { check_consistency_trace } from "../wasmlib/dbcop_wasm.js";

// deno-lint-ignore no-explicit-any
declare const cytoscape: any;

// -- Example histories -------------------------------------------------------

const EXAMPLES: Record<string, { history: unknown[]; level: string }> = {
  "write-read": {
    level: "serializable",
    history: [
      [
        {
          events: [{ Write: { variable: 0, version: 1 } }],
          committed: true,
        },
        {
          events: [{ Read: { variable: 0, version: 1 } }],
          committed: true,
        },
      ],
      [
        {
          events: [{ Write: { variable: 0, version: 2 } }],
          committed: true,
        },
      ],
    ],
  },
  "lost-update": {
    level: "snapshot-isolation",
    history: [
      [
        {
          events: [
            { Read: { variable: 0, version: 0 } },
            { Write: { variable: 0, version: 1 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [
            { Read: { variable: 0, version: 0 } },
            { Write: { variable: 0, version: 2 } },
          ],
          committed: true,
        },
      ],
    ],
  },
  serializable: {
    level: "serializable",
    history: [
      [
        {
          events: [
            { Write: { variable: 0, version: 1 } },
            { Write: { variable: 1, version: 1 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [
            { Read: { variable: 0, version: 1 } },
            { Write: { variable: 1, version: 2 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [{ Read: { variable: 1, version: 2 } }],
          committed: true,
        },
      ],
    ],
  },
  "causal-violation": {
    level: "causal",
    history: [
      [
        {
          events: [
            { Write: { variable: 0, version: 1 } },
            { Write: { variable: 1, version: 1 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [
            { Read: { variable: 0, version: 1 } },
            { Write: { variable: 1, version: 2 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [
            { Read: { variable: 1, version: 2 } },
            { Read: { variable: 0, version: 0 } },
          ],
          committed: true,
        },
      ],
    ],
  },
};

// -- Types for WASM result ---------------------------------------------------

interface TransactionId {
  session_id: number;
  session_height: number;
}

interface SessionTransaction {
  id: TransactionId;
  reads: Record<string, number>;
  writes: Record<string, number>;
  committed: boolean;
}

interface TraceResult {
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

// -- Graph state -------------------------------------------------------------

// deno-lint-ignore no-explicit-any
let cy: any = null;

// -- Rendering ---------------------------------------------------------------

function txId(t: TransactionId): string {
  return "S" + t.session_id + "T" + t.session_height;
}

function txLabel(tx: SessionTransaction): string {
  const parts: string[] = [];
  parts.push(txId(tx.id));

  const writes = Object.entries(tx.writes);
  if (writes.length > 0) {
    parts.push(
      "W:" + writes.map(([v, ver]) => "x" + v + "=" + ver).join(","),
    );
  }

  const reads = Object.entries(tx.reads);
  if (reads.length > 0) {
    parts.push(
      "R:" + reads.map(([v, ver]) => "x" + v + "=" + ver).join(","),
    );
  }

  return parts.join("\n");
}

function renderResult(
  result: TraceResult,
  container: HTMLElement,
): void {
  container.innerHTML = "";

  if (typeof result.error === "string") {
    const heading = document.createElement("div");
    heading.className = "result-fail";
    heading.textContent = "ERROR";
    container.appendChild(heading);

    const detail = document.createElement("div");
    detail.className = "result-detail";
    detail.textContent = String(result.error);
    container.appendChild(detail);
    return;
  }

  if (result.ok) {
    const heading = document.createElement("div");
    heading.className = "result-pass";
    heading.textContent = "PASS";
    container.appendChild(heading);

    if (result.witness) {
      const detail = document.createElement("div");
      detail.className = "result-detail";
      const witnessType = Object.keys(result.witness)[0] || "unknown";
      detail.textContent = "Witness: " + witnessType + "\n" +
        JSON.stringify(result.witness, null, 2);
      container.appendChild(detail);
    }
  } else {
    const heading = document.createElement("div");
    heading.className = "result-fail";
    heading.textContent = "FAIL";
    container.appendChild(heading);

    if (result.error) {
      const detail = document.createElement("div");
      detail.className = "result-detail";
      detail.textContent = JSON.stringify(result.error, null, 2);
      container.appendChild(detail);
    }
  }
}

function renderGraph(result: TraceResult): void {
  const container = document.getElementById("graph-container")!;
  const legendEl = document.getElementById("legend")!;

  if (cy) {
    cy.destroy();
    cy = null;
  }
  container.innerHTML = "";
  legendEl.innerHTML = "";

  if (!result.sessions || result.sessions.length === 0) {
    container.textContent = "No graph data available.";
    return;
  }

  // deno-lint-ignore no-explicit-any
  const elements: any[] = [];
  const addedEdges = new Set<string>();
  for (const session of result.sessions) {
    for (const tx of session) {
      if (tx.id.session_id === 0) continue;

      elements.push({
        data: {
          id: txId(tx.id),
          label: txLabel(tx),
          sessionId: tx.id.session_id,
          committed: tx.committed,
        },
      });
    }
  }

  for (const session of result.sessions) {
    for (let i = 0; i < session.length - 1; i++) {
      const from = session[i];
      const to = session[i + 1];
      if (from.id.session_id === 0 || to.id.session_id === 0) continue;
      const edgeId = "so_" + txId(from.id) + "_" + txId(to.id);
      if (!addedEdges.has(edgeId)) {
        addedEdges.add(edgeId);
        elements.push({
          data: {
            id: edgeId,
            source: txId(from.id),
            target: txId(to.id),
            label: "SO",
            edgeType: "so",
          },
        });
      }
    }
  }

  if (result.wr_edges) {
    for (const [from, to] of result.wr_edges) {
      if (from.session_id === 0 || to.session_id === 0) continue;
      const edgeId = "wr_" + txId(from) + "_" + txId(to);
      if (!addedEdges.has(edgeId)) {
        addedEdges.add(edgeId);
        elements.push({
          data: {
            id: edgeId,
            source: txId(from),
            target: txId(to),
            label: "WR",
            edgeType: "wr",
          },
        });
      }
    }
  }

  if (result.witness_edges) {
    for (const [from, to] of result.witness_edges) {
      if (from.session_id === 0 || to.session_id === 0) continue;
      const edgeId = "co_" + txId(from) + "_" + txId(to);
      if (!addedEdges.has(edgeId)) {
        addedEdges.add(edgeId);
        elements.push({
          data: {
            id: edgeId,
            source: txId(from),
            target: txId(to),
            label: "CO",
            edgeType: "co",
          },
        });
      }
    }
  }

  cy = cytoscape({
    container: container,
    elements: elements,
    style: [
      {
        selector: "node",
        style: {
          label: "data(label)",
          "text-wrap": "wrap",
          "text-valign": "center",
          "text-halign": "center",
          "font-size": "10px",
          "font-family": "monospace",
          color: "#e0e0e0",
          "background-color": "#4cc9a0",
          width: 60,
          height: 60,
          "border-width": 2,
          "border-color": "#333",
          "text-outline-color": "#0f0f1a",
          "text-outline-width": 1,
        },
      },
      {
        selector: "node[?committed]",
        style: {
          "background-color": "#4cc9a0",
        },
      },
      {
        selector: "node[committed = false]",
        style: {
          "background-color": "#555",
        },
      },
      {
        selector: "edge",
        style: {
          width: 2,
          "curve-style": "bezier",
          "target-arrow-shape": "triangle",
          "arrow-scale": 1.2,
          label: "data(label)",
          "font-size": "9px",
          "font-family": "monospace",
          color: "#888",
          "text-outline-color": "#0f0f1a",
          "text-outline-width": 1,
          "text-rotation": "autorotate",
        },
      },
      {
        selector: "edge[edgeType = 'wr']",
        style: {
          "line-color": "#4361ee",
          "target-arrow-color": "#4361ee",
        },
      },
      {
        selector: "edge[edgeType = 'co']",
        style: {
          "line-color": "#f7931e",
          "target-arrow-color": "#f7931e",
        },
      },
      {
        selector: "edge[edgeType = 'so']",
        style: {
          "line-color": "#666",
          "target-arrow-color": "#666",
          "line-style": "dashed",
        },
      },
    ],
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

  const legendItems = [
    { color: "#4cc9a0", text: "Committed node" },
    { color: "#555", text: "Uncommitted node" },
    { color: "#4361ee", text: "WR (write-read)" },
    { color: "#f7931e", text: "CO (commit order)" },
    { color: "#666", text: "SO (session order)" },
  ];

  for (const item of legendItems) {
    const div = document.createElement("div");
    div.className = "legend-item";
    const dot = document.createElement("span");
    dot.className = "legend-dot";
    dot.style.backgroundColor = item.color;
    div.appendChild(dot);
    const text = document.createElement("span");
    text.textContent = item.text;
    div.appendChild(text);
    legendEl.appendChild(div);
  }
}

// -- Setup -------------------------------------------------------------------

function setup(): void {
  const historyInput = document.getElementById(
    "history-json",
  ) as HTMLTextAreaElement;
  const levelSelect = document.getElementById(
    "level-select",
  ) as HTMLSelectElement;
  const exampleSelect = document.getElementById(
    "example-select",
  ) as HTMLSelectElement;
  const checkBtn = document.getElementById("check-btn") as HTMLButtonElement;
  const resultOutput = document.getElementById(
    "result-output",
  ) as HTMLDivElement;

  function loadExample(key: string): void {
    const example = EXAMPLES[key];
    if (!example) return;
    historyInput.value = JSON.stringify(example.history, null, 2);
    levelSelect.value = example.level;
  }

  loadExample(exampleSelect.value);

  exampleSelect.addEventListener("change", () => {
    loadExample(exampleSelect.value);
  });

  checkBtn.addEventListener("click", () => {
    const history = historyInput.value;
    const level = levelSelect.value;
    try {
      const raw = check_consistency_trace(history, level);
      const result: TraceResult = JSON.parse(raw);
      renderResult(result, resultOutput);
      renderGraph(result);
    } catch (e) {
      resultOutput.innerHTML = "";
      const heading = document.createElement("div");
      heading.className = "result-fail";
      heading.textContent = "ERROR";
      resultOutput.appendChild(heading);
      const detail = document.createElement("div");
      detail.className = "result-detail";
      detail.textContent = String(e);
      resultOutput.appendChild(detail);
    }
  });
}

setup();
