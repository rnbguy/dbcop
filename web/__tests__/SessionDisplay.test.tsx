import { describe, expect, it } from "vitest";
import { render } from "preact";
import { SessionDisplay } from "../components/SessionDisplay.tsx";
import type { TraceResult } from "../types.ts";

describe("SessionDisplay", () => {
  function mount(result: TraceResult | null) {
    const container = document.createElement("div");
    document.body.appendChild(container);
    render(<SessionDisplay result={result} />, container);
    return container;
  }

  it("renders empty state when result is null", () => {
    const c = mount(null);
    expect(c.querySelector(".sessions-empty")).not.toBeNull();
    expect(c.textContent).toContain("No sessions yet");
  });

  it("renders empty state when sessions array is empty", () => {
    const c = mount({ ok: true, sessions: [] });
    expect(c.querySelector(".sessions-empty")).not.toBeNull();
  });

  const mockResult: TraceResult = {
    ok: true,
    level: "causal",
    session_count: 2,
    transaction_count: 3,
    sessions: [
      [
        {
          id: { session_id: 0, session_height: 0 },
          reads: {},
          writes: {},
          committed: true,
        },
      ],

      [
        {
          id: { session_id: 1, session_height: 0 },
          reads: { "0": 1 },
          writes: {},
          committed: true,
        },
        {
          id: { session_id: 1, session_height: 1 },
          reads: {},
          writes: { "0": 2 },
          committed: true,
        },
      ],

      [
        {
          id: { session_id: 2, session_height: 0 },
          reads: {},
          writes: { "1": 3 },
          committed: false,
        },
      ],
    ],
    witness_edges: [],
    wr_edges: [],
  };

  it("renders without throwing with mock data", () => {
    expect(() => mount(mockResult)).not.toThrow();
  });

  it("renders the sessions panel container", () => {
    const c = mount(mockResult);
    expect(c.querySelector(".sessions-panel")).not.toBeNull();
  });

  it("skips root session (session_id=0)", () => {
    const c = mount(mockResult);

    const columns = c.querySelectorAll(".session-column");
    expect(columns.length).toBe(2);
  });

  it("renders correct session headers", () => {
    const c = mount(mockResult);
    const headers = c.querySelectorAll(".session-header");
    expect(headers[0]?.textContent).toBe("Session 1");
    expect(headers[1]?.textContent).toBe("Session 2");
  });

  it("renders transaction cards for each transaction", () => {
    const c = mount(mockResult);
    const cards = c.querySelectorAll(".txn-card");

    expect(cards.length).toBe(3);
  });

  it("renders transaction IDs in mono format", () => {
    const c = mount(mockResult);
    const ids = c.querySelectorAll(".txn-id");
    expect(ids[0]?.textContent).toBe("S1T0");
    expect(ids[1]?.textContent).toBe("S1T1");
    expect(ids[2]?.textContent).toBe("S2T0");
  });

  it("shows uncommitted badge for uncommitted transactions", () => {
    const c = mount(mockResult);
    const uncommittedBadges = c.querySelectorAll(".badge-fail");
    expect(uncommittedBadges.length).toBe(1);
    expect(uncommittedBadges[0]?.textContent).toBe("uncommitted");
  });

  it("applies txn-uncommitted class to uncommitted transactions", () => {
    const c = mount(mockResult);
    const uncommitted = c.querySelectorAll(".txn-uncommitted");
    expect(uncommitted.length).toBe(1);
  });

  it("renders write events with W marker", () => {
    const c = mount(mockResult);
    const writeOps = c.querySelectorAll(".event-write .event-op");

    expect(writeOps.length).toBe(2);
    expect(writeOps[0]?.textContent).toBe("W");
  });

  it("renders read events with R marker", () => {
    const c = mount(mockResult);
    const readOps = c.querySelectorAll(".event-read .event-op");

    expect(readOps.length).toBe(1);
    expect(readOps[0]?.textContent).toBe("R");
  });
});
