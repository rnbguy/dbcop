import {
  check_consistency,
  check_consistency_trace,
} from "../wasmlib/dbcop_wasm.js";
import { assertEquals } from "@std/assert";

// -- Test histories ----------------------------------------------------------
// Format: array of sessions, each session is array of transactions.
// Each transaction has `events` (array of Read/Write) and `committed` (bool).

/** Simple write-read across two sessions (should pass all levels). */
const WRITE_READ = JSON.stringify([
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
]);

/** Multi-session serializable history (3 sessions, chain of writes/reads). */
const SERIALIZABLE_HISTORY = JSON.stringify([
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
]);

/** Lost update anomaly -- fails snapshot-isolation. */
const LOST_UPDATE = JSON.stringify([
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
]);

/** Causal violation -- fails causal consistency (saturation checker; WASM panics on this level currently). */
const _CAUSAL_VIOLATION = JSON.stringify([
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
]);

// -- check_consistency tests -------------------------------------------------

Deno.test("check_consistency: write-read passes serializable", () => {
  const result = JSON.parse(check_consistency(WRITE_READ, "serializable"));
  assertEquals(result.ok, true);
});

Deno.test("check_consistency: serializable history passes serializable", () => {
  const result = JSON.parse(
    check_consistency(SERIALIZABLE_HISTORY, "serializable"),
  );
  assertEquals(result.ok, true);
});

Deno.test("check_consistency: serializable history passes prefix", () => {
  const result = JSON.parse(
    check_consistency(SERIALIZABLE_HISTORY, "prefix"),
  );
  assertEquals(result.ok, true);
});

Deno.test("check_consistency: serializable history passes snapshot-isolation", () => {
  const result = JSON.parse(
    check_consistency(SERIALIZABLE_HISTORY, "snapshot-isolation"),
  );
  assertEquals(result.ok, true);
});

Deno.test("check_consistency: write-read passes prefix", () => {
  const result = JSON.parse(check_consistency(WRITE_READ, "prefix"));
  assertEquals(result.ok, true);
});

Deno.test("check_consistency: write-read passes snapshot-isolation", () => {
  const result = JSON.parse(
    check_consistency(WRITE_READ, "snapshot-isolation"),
  );
  assertEquals(result.ok, true);
});

Deno.test("check_consistency: lost update fails snapshot-isolation", () => {
  const result = JSON.parse(
    check_consistency(LOST_UPDATE, "snapshot-isolation"),
  );
  assertEquals(result.ok, false);
});

Deno.test("check_consistency: invalid level returns error", () => {
  const result = JSON.parse(check_consistency(WRITE_READ, "bad-level"));
  assertEquals(result.ok, false);
});

Deno.test("check_consistency: invalid JSON returns error", () => {
  const result = JSON.parse(check_consistency("not json", "serializable"));
  assertEquals(result.ok, false);
});

Deno.test("check_consistency: empty sessions array passes", () => {
  const result = JSON.parse(check_consistency("[]", "serializable"));
  assertEquals(result.ok, true);
});

// -- check_consistency_trace tests -------------------------------------------

Deno.test("check_consistency_trace: returns sessions and edges on pass", () => {
  const result = JSON.parse(
    check_consistency_trace(SERIALIZABLE_HISTORY, "serializable"),
  );
  assertEquals(result.ok, true);
  assertEquals(Array.isArray(result.sessions), true);
  assertEquals(Array.isArray(result.wr_edges), true);
  assertEquals(typeof result.session_count, "number");
  assertEquals(typeof result.transaction_count, "number");
  assertEquals(result.level, "serializable");
});

Deno.test("check_consistency_trace: returns error on failure", () => {
  const result = JSON.parse(
    check_consistency_trace(LOST_UPDATE, "snapshot-isolation"),
  );
  assertEquals(result.ok, false);
  assertEquals(Array.isArray(result.sessions), true);
});

Deno.test("check_consistency_trace: invalid JSON returns error", () => {
  const result = JSON.parse(
    check_consistency_trace("{bad", "serializable"),
  );
  assertEquals(result.ok, false);
});
