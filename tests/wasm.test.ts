import {
  check_consistency,
  check_consistency_trace,
  check_consistency_trace_text,
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

/** Causal violation -- fails causal consistency. */
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

// -- Bug 3 (version-zero -> root) -------------------------------------------

Deno.test("check_consistency_trace: lost update passes causal (x==0 maps to initial state)", () => {
  const result = JSON.parse(check_consistency_trace(LOST_UPDATE, "causal"));
  assertEquals(
    result.ok,
    true,
    `lost update (x==0 reads initial state) should pass causal: ${
      JSON.stringify(result)
    }`,
  );
});

Deno.test("check_consistency_trace_text: version-zero history passes causal", () => {
  const result = JSON.parse(
    check_consistency_trace_text("[x==0 x:=1]\n---\n[x==0 x:=2]\n", "causal"),
  );
  assertEquals(result.ok, true);
  assertEquals(result.level, "causal");
  assertEquals(result.session_count, 2);
});

Deno.test("check_consistency_trace_text: version-zero history fails snapshot-isolation", () => {
  const result = JSON.parse(
    check_consistency_trace_text(
      "[x==0 x:=1]\n---\n[x==0 x:=2]\n",
      "snapshot-isolation",
    ),
  );
  assertEquals(result.ok, false);
  assertEquals(result.level, "snapshot-isolation");
});

// -- Bug 4 (TxCard event order) -------------------------------------------

Deno.test("check_consistency_trace: sessions include events field", () => {
  // SERIALIZABLE_HISTORY has known structure; verify every txn has events array
  const result = JSON.parse(
    check_consistency_trace(SERIALIZABLE_HISTORY, "serializable"),
  );
  assertEquals(result.ok, true);
  for (const session of result.sessions) {
    for (const txn of session) {
      assertEquals(
        Array.isArray(txn.events),
        true,
        "every txn must have events array",
      );
    }
  }
});

Deno.test("check_consistency_trace: events field preserves read-before-write order", () => {
  // [x==0 x:=1]: Read comes before Write in input -- must appear in that order
  const h = JSON.stringify([
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
  const result = JSON.parse(check_consistency_trace(h, "causal"));
  assertEquals(result.ok, true);
  const txn = result.sessions[0][0];
  assertEquals(txn.events.length, 2);
  assertEquals(txn.events[0].type, "R", "first event must be Read");
  assertEquals(txn.events[1].type, "W", "second event must be Write");
  assertEquals(txn.events[0].version, 0);
  assertEquals(txn.events[1].version, 1);
});

Deno.test("check_consistency_trace_text: events field preserves read-before-write order", () => {
  const result = JSON.parse(
    check_consistency_trace_text("[x==0 x:=1]\n---\n[x==0 x:=2]\n", "causal"),
  );
  assertEquals(result.ok, true);
  const txn = result.sessions[0][0];
  assertEquals(Array.isArray(txn.events), true);
  assertEquals(txn.events.length, 2);
  assertEquals(txn.events[0].type, "R", "first event must be Read");
  assertEquals(txn.events[1].type, "W", "second event must be Write");
});
