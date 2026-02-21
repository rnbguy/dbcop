import {
  check_consistency_trace,
  check_consistency_trace_text,
  tokenize_history,
} from "../wasmlib/dbcop_wasm.js";

// deno-lint-ignore no-explicit-any
declare const cytoscape: any;

// -- Types ------------------------------------------------------------------

interface TransactionId {
  session_id: number;
  session_height: number;
}

interface SessionTransaction {
  id: TransactionId;
  reads: Record<string, number | null>;
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

interface HighlightToken {
  kind: string;
  start: number;
  end: number;
  text: string;
}

// -- Example histories (text DSL) -------------------------------------------

const TEXT_EXAMPLES: Record<string, { text: string; level: string }> = {
  "write-read": {
    level: "serializable",
    text: `// session 1: write then read
[x:=1] [x==1]
---
// session 2: concurrent write
[x:=2]`,
  },
  "lost-update": {
    level: "snapshot-isolation",
    text: `// session 1: read-then-write
[x==0 x:=1]
---
// session 2: read-then-write (lost update)
[x==0 x:=2]`,
  },
  serializable: {
    level: "serializable",
    text: `// session 1: write both
[x:=1 y:=1]
---
// session 2: read x, write y
[x==1 y:=2]
---
// session 3: read y
[y==2]`,
  },
  "causal-violation": {
    level: "causal",
    text: `// session 1: write x and y
[x:=1 y:=1]
---
// session 2: sees x, writes y
[x==1 y:=2]
---
// session 3: sees y:=2 but not x:=1 (causal violation)
[y==2 x==?]`,
  },
};

const JSON_EXAMPLES: Record<string, { json: unknown[]; level: string }> = {
  "write-read": {
    level: "serializable",
    json: [
      [
        { events: [{ Write: { variable: 0, version: 1 } }], committed: true },
        { events: [{ Read: { variable: 0, version: 1 } }], committed: true },
      ],
      [
        { events: [{ Write: { variable: 0, version: 2 } }], committed: true },
      ],
    ],
  },
  "lost-update": {
    level: "snapshot-isolation",
    json: [
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
    json: [
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
    json: [
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

// -- State ------------------------------------------------------------------

let currentFormat: "text" | "json" = "text";
// deno-lint-ignore no-explicit-any
let cy: any = null;

// -- Theme ------------------------------------------------------------------

function getThemeColors() {
  const s = getComputedStyle(document.documentElement);
  return {
    bg: s.getPropertyValue("--bg").trim(),
    text: s.getPropertyValue("--text").trim(),
    muted: s.getPropertyValue("--muted").trim(),
    border: s.getPropertyValue("--border").trim(),
    nodeCommitted: s.getPropertyValue("--node-committed").trim(),
    nodeUncommitted: s.getPropertyValue("--node-uncommitted").trim(),
    edgeWr: s.getPropertyValue("--edge-wr").trim(),
    edgeCo: s.getPropertyValue("--edge-co").trim(),
    edgeSo: s.getPropertyValue("--edge-so").trim(),
  };
}

function applyTheme(theme: "dark" | "light") {
  if (theme === "light") {
    document.documentElement.dataset.theme = "light";
  } else {
    delete document.documentElement.dataset.theme;
  }
  localStorage.setItem("theme", theme);

  const btn = document.getElementById("theme-toggle")!;
  btn.innerHTML = theme === "dark" ? "&#9790;" : "&#9728;";

  // Re-theme cytoscape if active
  if (cy) {
    reThemeCytoscape();
  }
}

function initTheme() {
  const saved = localStorage.getItem("theme");
  if (saved === "light" || saved === "dark") {
    applyTheme(saved);
  } else if (
    globalThis.matchMedia &&
    globalThis.matchMedia("(prefers-color-scheme: light)").matches
  ) {
    applyTheme("light");
  } else {
    applyTheme("dark");
  }
}

function toggleTheme() {
  const current = document.documentElement.dataset.theme === "light"
    ? "light"
    : "dark";
  applyTheme(current === "dark" ? "light" : "dark");
}

// -- Helpers ----------------------------------------------------------------

function txId(t: TransactionId): string {
  return "S" + t.session_id + "T" + t.session_height;
}

function txLabel(tx: SessionTransaction): string {
  const parts: string[] = [];
  parts.push(txId(tx.id));

  const writes = Object.entries(tx.writes);
  if (writes.length > 0) {
    parts.push(
      "W:" + writes.map(([v, ver]) => v + ":=" + ver).join(", "),
    );
  }

  const reads = Object.entries(tx.reads);
  if (reads.length > 0) {
    parts.push(
      "R:" +
        reads
          .map(([v, ver]) => v + "==" + (ver === null ? "?" : ver))
          .join(", "),
    );
  }

  return parts.join("\n");
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;");
}

// -- Syntax highlighting ----------------------------------------------------

function highlightText(text: string, overlay: HTMLElement) {
  if (currentFormat !== "text") {
    overlay.innerHTML = "";
    return;
  }

  try {
    const raw = tokenize_history(text);
    const tokens: HighlightToken[] = JSON.parse(raw);
    let html = "";
    let pos = 0;

    for (const tok of tokens) {
      // Gap between tokens (shouldn't happen often)
      if (tok.start > pos) {
        html += escapeHtml(text.slice(pos, tok.start));
      }

      const escaped = escapeHtml(tok.text);
      const cls = tokenClass(tok.kind);
      if (cls) {
        html += `<span class="${cls}">${escaped}</span>`;
      } else {
        html += escaped;
      }
      pos = tok.end;
    }

    // Remaining text after last token
    if (pos < text.length) {
      html += escapeHtml(text.slice(pos));
    }

    overlay.innerHTML = html;
  } catch {
    overlay.innerHTML = "";
  }
}

function tokenClass(kind: string): string | null {
  switch (kind) {
    case "Comment":
      return "syn-comment";
    case "BracketOpen":
    case "BracketClose":
      return "syn-bracket";
    case "ColonEquals":
    case "DoubleEquals":
      return "syn-operator";
    case "Ident":
      return "syn-variable";
    case "Integer":
      return "syn-number";
    case "Dash":
      return "syn-separator";
    case "Bang":
      return "syn-bang";
    case "QuestionMark":
      return "syn-question";
    default:
      return null;
  }
}

// -- Session visualization --------------------------------------------------

function renderSessions(result: TraceResult) {
  const container = document.getElementById("sessions-display")!;
  container.innerHTML = "";

  if (!result.sessions || result.sessions.length === 0) {
    container.innerHTML =
      '<span style="color:var(--muted);font-size:0.85rem">No session data.</span>';
    return;
  }

  for (let si = 0; si < result.sessions.length; si++) {
    const session = result.sessions[si];
    // Skip the "init" session (session_id === 0)
    if (session.length > 0 && session[0].id.session_id === 0) continue;

    const col = document.createElement("div");
    col.className = "session-column";

    const header = document.createElement("div");
    header.className = "session-header";
    header.textContent = "Session " + (si + 1);
    col.appendChild(header);

    for (const tx of session) {
      const card = document.createElement("div");
      card.className = "txn-card" + (tx.committed ? "" : " uncommitted");

      const cardHeader = document.createElement("div");
      cardHeader.className = "txn-card-header";
      cardHeader.textContent = txId(tx.id) +
        (tx.committed ? "" : " (uncommitted)");
      card.appendChild(cardHeader);

      // Render events: writes first, then reads
      for (const [variable, version] of Object.entries(tx.writes)) {
        const row = document.createElement("div");
        row.className = "event-row write";
        row.innerHTML =
          `<span class="event-var">${escapeHtml(variable)}</span>` +
          `<span class="event-op">:=</span>` +
          `<span class="event-val">${escapeHtml(String(version))}</span>`;
        card.appendChild(row);
      }
      for (const [variable, version] of Object.entries(tx.reads)) {
        const row = document.createElement("div");
        row.className = "event-row read";
        row.innerHTML =
          `<span class="event-var">${escapeHtml(variable)}</span>` +
          `<span class="event-op">==</span>` +
          `<span class="event-val">${
            escapeHtml(version === null ? "?" : String(version))
          }</span>`;
        card.appendChild(row);
      }

      col.appendChild(card);
    }

    container.appendChild(col);
  }
}

// -- Result rendering -------------------------------------------------------

function renderResult(result: TraceResult, container: HTMLElement) {
  container.innerHTML = "";

  if (typeof result.error === "string" && !result.sessions) {
    // Parse/input error
    const badge = document.createElement("span");
    badge.className = "result-badge error";
    badge.textContent = "ERROR";
    container.appendChild(badge);

    const detail = document.createElement("span");
    detail.className = "result-detail";
    detail.textContent = String(result.error);
    container.appendChild(detail);
    return;
  }

  if (result.ok) {
    const badge = document.createElement("span");
    badge.className = "result-badge pass";
    badge.textContent = "PASS";
    container.appendChild(badge);

    if (result.level) {
      const detail = document.createElement("span");
      detail.className = "result-detail";
      const witnessType = result.witness
        ? Object.keys(result.witness)[0] || ""
        : "";
      detail.textContent = result.level +
        (witnessType ? " (" + witnessType + ")" : "");
      container.appendChild(detail);
    }
  } else {
    const badge = document.createElement("span");
    badge.className = "result-badge fail";
    badge.textContent = "FAIL";
    container.appendChild(badge);

    if (result.level) {
      const detail = document.createElement("span");
      detail.className = "result-detail";
      detail.textContent = result.level;
      container.appendChild(detail);
    }
  }
}

// -- Graph rendering --------------------------------------------------------

function buildGraphStyle() {
  const c = getThemeColors();
  return [
    {
      selector: "node",
      style: {
        label: "data(label)",
        "text-wrap": "wrap",
        "text-valign": "center",
        "text-halign": "center",
        "font-size": "10px",
        "font-family": "monospace",
        color: c.text,
        "background-color": c.nodeCommitted,
        width: 60,
        height: 60,
        "border-width": 2,
        "border-color": c.border,
        "text-outline-color": c.bg,
        "text-outline-width": 1,
      },
    },
    {
      selector: "node[?committed]",
      style: { "background-color": c.nodeCommitted },
    },
    {
      selector: "node[committed = false]",
      style: { "background-color": c.nodeUncommitted },
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
        color: c.muted,
        "text-outline-color": c.bg,
        "text-outline-width": 1,
        "text-rotation": "autorotate",
      },
    },
    {
      selector: "edge[edgeType = 'wr']",
      style: {
        "line-color": c.edgeWr,
        "target-arrow-color": c.edgeWr,
      },
    },
    {
      selector: "edge[edgeType = 'co']",
      style: {
        "line-color": c.edgeCo,
        "target-arrow-color": c.edgeCo,
      },
    },
    {
      selector: "edge[edgeType = 'so']",
      style: {
        "line-color": c.edgeSo,
        "target-arrow-color": c.edgeSo,
        "line-style": "dashed",
      },
    },
  ];
}

function reThemeCytoscape() {
  if (!cy) return;
  cy.style().fromJson(buildGraphStyle()).update();
}

function renderGraph(result: TraceResult) {
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

  // Session order edges
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

  // Write-read edges
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

  // Commit order / witness edges
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
    style: buildGraphStyle(),
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

  // Legend
  const c = getThemeColors();
  const legendItems = [
    { color: c.nodeCommitted, text: "Committed" },
    { color: c.nodeUncommitted, text: "Uncommitted" },
    { color: c.edgeWr, text: "WR (write-read)" },
    { color: c.edgeCo, text: "CO (commit order)" },
    { color: c.edgeSo, text: "SO (session order)" },
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

// -- Setup ------------------------------------------------------------------

function setup() {
  const input = document.getElementById(
    "history-input",
  ) as HTMLTextAreaElement;
  const overlay = document.getElementById(
    "highlight-overlay",
  ) as HTMLDivElement;
  const levelSelect = document.getElementById(
    "level-select",
  ) as HTMLSelectElement;
  const exampleSelect = document.getElementById(
    "example-select",
  ) as HTMLSelectElement;
  const checkBtn = document.getElementById("check-btn") as HTMLButtonElement;
  const resultBar = document.getElementById("result-bar") as HTMLDivElement;
  const fmtText = document.getElementById("fmt-text") as HTMLButtonElement;
  const fmtJson = document.getElementById("fmt-json") as HTMLButtonElement;
  const themeToggle = document.getElementById(
    "theme-toggle",
  ) as HTMLButtonElement;

  // -- Theme
  initTheme();
  themeToggle.addEventListener("click", toggleTheme);

  // -- Format toggle
  function setFormat(fmt: "text" | "json") {
    currentFormat = fmt;
    fmtText.classList.toggle("active", fmt === "text");
    fmtJson.classList.toggle("active", fmt === "json");
    input.placeholder = fmt === "text"
      ? "Enter history in compact text format..."
      : "Enter history as JSON array...";
    loadExample(exampleSelect.value);
  }

  fmtText.addEventListener("click", () => setFormat("text"));
  fmtJson.addEventListener("click", () => setFormat("json"));

  // -- Examples
  function loadExample(key: string) {
    if (currentFormat === "text") {
      const ex = TEXT_EXAMPLES[key];
      if (!ex) return;
      input.value = ex.text;
      levelSelect.value = ex.level;
      highlightText(input.value, overlay);
    } else {
      const ex = JSON_EXAMPLES[key];
      if (!ex) return;
      input.value = JSON.stringify(ex.json, null, 2);
      levelSelect.value = ex.level;
      overlay.innerHTML = "";
    }
  }

  loadExample(exampleSelect.value);

  exampleSelect.addEventListener("change", () => {
    loadExample(exampleSelect.value);
  });

  // -- Syntax highlighting on input (text mode)
  input.addEventListener("input", () => {
    highlightText(input.value, overlay);
  });

  // Sync scroll between textarea and overlay
  input.addEventListener("scroll", () => {
    overlay.scrollTop = input.scrollTop;
    overlay.scrollLeft = input.scrollLeft;
  });

  // -- Check button
  checkBtn.addEventListener("click", () => {
    const value = input.value;
    const level = levelSelect.value;

    try {
      let raw: string;
      if (currentFormat === "text") {
        raw = check_consistency_trace_text(value, level);
      } else {
        raw = check_consistency_trace(value, level);
      }
      const result: TraceResult = JSON.parse(raw);
      renderResult(result, resultBar);
      renderSessions(result);
      renderGraph(result);
    } catch (e) {
      resultBar.innerHTML = "";
      const badge = document.createElement("span");
      badge.className = "result-badge error";
      badge.textContent = "ERROR";
      resultBar.appendChild(badge);
      const detail = document.createElement("span");
      detail.className = "result-detail";
      detail.textContent = String(e);
      resultBar.appendChild(detail);
    }
  });
}

setup();
