import { describe, expect, it } from "vitest";
import { render } from "preact";
import { ResultBar } from "../components/ResultBar.tsx";
import type { TraceResult } from "../types.ts";

describe("ResultBar", () => {
  function mount(
    result: TraceResult | null,
    loading = false,
    timedOut = false,
  ) {
    const container = document.createElement("div");
    document.body.appendChild(container);
    render(
      <ResultBar result={result} loading={loading} timedOut={timedOut} />,
      container,
    );
    return container;
  }

  it("shows Ready badge when no result and not loading", () => {
    const c = mount(null);
    expect(c.querySelector(".badge-neutral")?.textContent).toBe("Ready");
    expect(c.textContent).toContain("Select an example");
  });

  it("shows spinner and Checking text when loading", () => {
    const c = mount(null, true);
    expect(c.querySelector(".spinner")).not.toBeNull();
    expect(c.textContent).toContain("Checking...");
  });

  it("shows NP-complete hint when loading and timed out", () => {
    const c = mount(null, true, true);
    expect(c.textContent).toContain("NP-complete");
  });

  it("does not show NP-complete hint when loading but not timed out", () => {
    const c = mount(null, true, false);
    expect(c.textContent).not.toContain("NP-complete");
  });

  it("shows PASS badge when result.ok is true", () => {
    const result: TraceResult = {
      ok: true,
      level: "causal",
      session_count: 3,
      transaction_count: 5,
      witness: { SaturationOrder: [] },
    };
    const c = mount(result);
    expect(c.querySelector(".badge-pass")?.textContent).toBe("PASS");
  });

  it("shows session and transaction counts on PASS", () => {
    const result: TraceResult = {
      ok: true,
      level: "serializable",
      session_count: 2,
      transaction_count: 4,
    };
    const c = mount(result);
    expect(c.textContent).toContain("2 sessions");
    expect(c.textContent).toContain("4");
  });

  it("shows witness type name on PASS", () => {
    const result: TraceResult = {
      ok: true,
      witness: { CommitOrder: [1, 2, 3] },
    };
    const c = mount(result);
    expect(c.textContent).toContain("CommitOrder");
  });

  it("shows OK when PASS but no witness", () => {
    const result: TraceResult = { ok: true };
    const c = mount(result);
    expect(c.textContent).toContain("OK");
  });

  it("shows FAIL badge when result.ok is false", () => {
    const result: TraceResult = {
      ok: false,
      level: "serializable",
      error: { Cycle: { a: 1, b: 2 } },
    };
    const c = mount(result);
    expect(c.querySelector(".badge-fail")?.textContent).toBe("FAIL");
  });

  it("shows error key name on FAIL with object error", () => {
    const result: TraceResult = {
      ok: false,
      error: { Cycle: { a: 1, b: 2 } },
    };
    const c = mount(result);
    expect(c.textContent).toContain("Cycle");
  });

  it("shows error string on FAIL with string error", () => {
    const result: TraceResult = {
      ok: false,
      error: "parse error at line 5",
    };
    const c = mount(result);
    expect(c.textContent).toContain("parse error at line 5");
  });

  it("shows level on FAIL result", () => {
    const result: TraceResult = {
      ok: false,
      level: "prefix",
      error: "invalid",
    };
    const c = mount(result);
    expect(c.textContent).toContain("prefix");
  });
});
