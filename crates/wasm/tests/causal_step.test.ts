import {
  check_consistency_step_init,
  check_consistency_step_next,
} from "../../../wasmlib/dbcop_wasm.js";
import { assertEquals } from "@std/assert";

Deno.test("causal steps do not turn commit order into visibility", () => {
  const history = JSON.stringify([
    [
      {
        events: [
          { Write: { variable: 1, version: 46 } },
          { Read: { variable: 2 } },
        ],
        committed: true,
      },
      {
        events: [
          { Read: { variable: 3, version: 56 } },
          { Read: { variable: 1, version: 49 } },
        ],
        committed: true,
      },
    ],
    [
      {
        events: [
          { Write: { variable: 2, version: 48 } },
          { Read: { variable: 1 } },
          { Write: { variable: 1, version: 49 } },
        ],
        committed: true,
      },
      {
        events: [{ Write: { variable: 3, version: 56 } }],
        committed: true,
      },
    ],
  ]);
  type Tid = { session_id: number; session_height: number };
  const edges: [Tid, Tid][] = [];
  let state = JSON.parse(check_consistency_step_init(history, "causal"));
  for (let step = 0; step < 3 && !state.done; step++) {
    state = JSON.parse(check_consistency_step_next(state.session_id));
    edges.push(...state.new_edges);
  }
  assertEquals(state.done, true);
  assertEquals(state.result.ok, true);
  assertEquals(
    edges.some(([from, to]) =>
      from.session_id === 1 && from.session_height === 0 &&
      to.session_id === 2 && to.session_height === 0
    ),
    true,
  );
  assertEquals(edges.some(([, to]) => to.session_id === 0), false);
});
