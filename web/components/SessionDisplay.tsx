import type { SessionTransaction, TraceResult } from "../types.ts";

interface Props {
  result: TraceResult | null;
}

export function SessionDisplay({ result }: Props) {
  if (!result?.sessions || result.sessions.length === 0) {
    return (
      <div class="sessions-empty empty" aria-live="polite">
        <span>No sessions yet. Run a check to see transaction details.</span>
      </div>
    );
  }

  return (
    <div class="sessions-panel">
      {result.sessions.map((session, si) => {
        // Skip the implicit root session (session_id = 0)
        const firstTxn = session[0];
        if (firstTxn && firstTxn.id.session_id === 0) return null;
        const sid = firstTxn?.id.session_id ?? si;
        return (
          <div key={sid} class="session-column">
            <div class="session-header">Session {sid}</div>
            {session.map((txn) => <TxnCard key={txKey(txn)} txn={txn} />)}
          </div>
        );
      })}
    </div>
  );
}

function TxnCard({ txn }: { txn: SessionTransaction }) {
  const writes = Object.entries(txn.writes);
  const reads = Object.entries(txn.reads);
  return (
    <div class={`txn-card ${txn.committed ? "" : "txn-uncommitted"}`}>
      <div class="txn-header">
        <span class="txn-id mono">
          S{txn.id.session_id}T{txn.id.session_height}
        </span>
        {!txn.committed && <span class="badge badge-fail">uncommitted</span>}
      </div>
      {writes.length > 0 && (
        <div class="txn-events">
          {writes.map(([k, v]) => (
            <div key={`w-${k}`} class="event-row event-write">
              <span class="event-op">W</span>
              <span class="mono">{varLabel(k)} := {v}</span>
            </div>
          ))}
        </div>
      )}
      {reads.length > 0 && (
        <div class="txn-events">
          {reads.map(([k, v]) => (
            <div key={`r-${k}`} class="event-row event-read">
              <span class="event-op">R</span>
              <span class="mono">
                {varLabel(k)} == {v === null ? "?" : v}
              </span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function txKey(txn: SessionTransaction): string {
  return `${txn.id.session_id}-${txn.id.session_height}`;
}

function varLabel(k: string): string {
  const n = parseInt(k, 10);
  if (isNaN(n) || n < 0 || n > 25) return `v${k}`;
  const letters = "xyzabcdefghijklmnopqrstuvw";
  return letters[n];
}
