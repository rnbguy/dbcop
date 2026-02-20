import { check_consistency_trace } from "../wasmlib/dbcop_wasm.js";

// Example: 2 sessions, 3 transactions total.
// Session 0: T0 writes x0=1, T1 reads x0=1.
// Session 1: T0 writes x0=2.
// Format: Vec<Vec<Transaction>> where Transaction = {events, committed}.
const EXAMPLE_HISTORY = JSON.stringify([
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

function setup(): void {
  const historyInput = document.getElementById(
    "history-json",
  ) as HTMLTextAreaElement;
  const levelSelect = document.getElementById(
    "level-select",
  ) as HTMLSelectElement;
  const checkBtn = document.getElementById("check-btn") as HTMLButtonElement;
  const resultOutput = document.getElementById(
    "result-output",
  ) as HTMLPreElement;

  historyInput.value = EXAMPLE_HISTORY;

  checkBtn.addEventListener("click", () => {
    const history = historyInput.value;
    const level = levelSelect.value;
    try {
      const result = check_consistency_trace(history, level);
      const parsed = JSON.parse(result);
      resultOutput.textContent = JSON.stringify(parsed, null, 2);
    } catch (e) {
      resultOutput.textContent = "Error: " + String(e);
    }
  });
}

setup();
