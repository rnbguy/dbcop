import { describe, expect, it, vi } from "vitest";
import { render } from "preact";

// Mock WASM module -- not available in happy-dom.
// EditorPanel dynamically imports this.
vi.mock("../wasm.ts", () => ({
  check_consistency_trace: () => JSON.stringify({ ok: true, sessions: [] }),
  check_consistency_trace_text: () =>
    JSON.stringify({ ok: true, sessions: [] }),
  tokenize_history: () => "[]",
}));

// Stub Viz global (loaded via CDN in production)
vi.stubGlobal("Viz", {
  instance: () =>
    Promise.resolve({
      renderSVGElement: () =>
        document.createElementNS("http://www.w3.org/2000/svg", "svg"),
    }),
});

const { App } = await import("../app.tsx");

describe("App", () => {
  it("mounts without throwing", () => {
    const container = document.createElement("div");
    document.body.appendChild(container);
    expect(() => render(<App />, container)).not.toThrow();
  });
});
