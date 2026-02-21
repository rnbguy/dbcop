import { Check, Minus, Plus, X } from "lucide-preact";
import { useEffect, useRef, useState } from "preact/hooks";

const STORAGE_KEY = "dbcop-builder-state";

interface BuilderEvent {
  type: "read" | "write";
  variable: number;
  version: number | null;
}

interface BuilderTxn {
  id: string;
  events: BuilderEvent[];
  committed: boolean;
}

interface BuilderSession {
  id: string;
  txns: BuilderTxn[];
}

interface WasmWriteEvent {
  Write: { variable: number; version: number };
}

interface WasmReadEvent {
  Read: { variable: number; version: number | null };
}

type WasmEvent = WasmWriteEvent | WasmReadEvent;

type WasmTxn = {
  events: WasmEvent[];
  committed: boolean;
};

type WasmHistory = WasmTxn[][];

interface EdgeLine {
  id: string;
  x1: number;
  y1: number;
  x2: number;
  y2: number;
}

interface Props {
  open: boolean;
  onClose: () => void;
  onExport: (json: WasmHistory) => void;
}

function uniqueId(): string {
  return String(Date.now() + Math.random());
}

function parseNumber(value: string): number {
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed < 0) {
    return 0;
  }
  return Math.floor(parsed);
}

function parseVersion(value: string): number | null {
  if (value.trim() === "") {
    return null;
  }
  return parseNumber(value);
}

function defaultEvent(): BuilderEvent {
  return { type: "write", variable: 0, version: 0 };
}

function defaultTxn(): BuilderTxn {
  return {
    id: uniqueId(),
    events: [defaultEvent()],
    committed: true,
  };
}

function loadSessions(): BuilderSession[] {
  const raw = globalThis.localStorage?.getItem(STORAGE_KEY);
  if (!raw) {
    return [];
  }

  try {
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) {
      return [];
    }
    return parsed as BuilderSession[];
  } catch (error) {
    console.warn("Failed to parse saved builder state", error);
    return [];
  }
}

function toWasmJson(sessions: BuilderSession[]): WasmHistory {
  return sessions.map((session) =>
    session.txns.map((txn) => ({
      events: txn.events.map((event) =>
        event.type === "write"
          ? {
            Write: {
              variable: event.variable,
              version: event.version ?? 0,
            },
          }
          : {
            Read: {
              variable: event.variable,
              version: event.version,
            },
          }
      ),
      committed: txn.committed,
    }))
  );
}

export function SessionBuilder({ open, onClose, onExport }: Props) {
  const [sessions, setSessions] = useState<BuilderSession[]>(loadSessions);
  const [edges, setEdges] = useState<EdgeLine[]>([]);
  const sessionsRef = useRef<HTMLDivElement>(null);
  const cardRefs = useRef<Record<string, HTMLDivElement | null>>({});

  useEffect(() => {
    globalThis.localStorage?.setItem(STORAGE_KEY, JSON.stringify(sessions));
  }, [sessions]);

  useEffect(() => {
    if (!open) {
      setEdges([]);
      return;
    }

    const root = sessionsRef.current;
    if (!root) {
      setEdges([]);
      return;
    }

    const frame = requestAnimationFrame(() => {
      const nextEdges: EdgeLine[] = [];
      const rootBox = root.getBoundingClientRect();

      for (
        let targetSessionIndex = 0;
        targetSessionIndex < sessions.length;
        targetSessionIndex++
      ) {
        const targetSession = sessions[targetSessionIndex];
        for (const targetTxn of targetSession.txns) {
          const targetCard = cardRefs.current[targetTxn.id];
          if (!targetCard) continue;

          for (const event of targetTxn.events) {
            if (event.type !== "read" || event.version === null) continue;

            let sourceTxnId: string | null = null;

            for (
              let sourceSessionIndex = 0;
              sourceSessionIndex < sessions.length;
              sourceSessionIndex++
            ) {
              if (sourceSessionIndex === targetSessionIndex) continue;
              const sourceSession = sessions[sourceSessionIndex];

              for (const sourceTxn of sourceSession.txns) {
                const match = sourceTxn.events.some((sourceEvent) =>
                  sourceEvent.type === "write" &&
                  sourceEvent.variable === event.variable &&
                  sourceEvent.version === event.version
                );

                if (match) {
                  sourceTxnId = sourceTxn.id;
                  break;
                }
              }

              if (sourceTxnId) break;
            }

            if (!sourceTxnId) continue;
            const sourceCard = cardRefs.current[sourceTxnId];
            if (!sourceCard) continue;

            const sourceBox = sourceCard.getBoundingClientRect();
            const targetBox = targetCard.getBoundingClientRect();
            nextEdges.push({
              id:
                `${sourceTxnId}-${targetTxn.id}-${event.variable}-${event.version}`,
              x1: sourceBox.right - rootBox.left,
              y1: sourceBox.top + sourceBox.height / 2 - rootBox.top,
              x2: targetBox.left - rootBox.left,
              y2: targetBox.top + targetBox.height / 2 - rootBox.top,
            });
          }
        }
      }

      setEdges(nextEdges);
    });

    return () => cancelAnimationFrame(frame);
  }, [open, sessions]);

  if (!open) return null;

  const addSession = () => {
    setSessions((prev) => [...prev, { id: uniqueId(), txns: [defaultTxn()] }]);
  };

  const removeSession = (sessionId: string) => {
    setSessions((prev) => prev.filter((session) => session.id !== sessionId));
  };

  const addTxn = (sessionId: string) => {
    setSessions((prev) =>
      prev.map((session) =>
        session.id === sessionId
          ? { ...session, txns: [...session.txns, defaultTxn()] }
          : session
      )
    );
  };

  const removeTxn = (sessionId: string, txnId: string) => {
    setSessions((prev) =>
      prev.map((session) =>
        session.id === sessionId
          ? { ...session, txns: session.txns.filter((txn) => txn.id !== txnId) }
          : session
      )
    );
  };

  const updateTxn = (
    sessionId: string,
    txnId: string,
    updater: (txn: BuilderTxn) => BuilderTxn,
  ) => {
    setSessions((prev) =>
      prev.map((session) =>
        session.id === sessionId
          ? {
            ...session,
            txns: session.txns.map((txn) =>
              txn.id === txnId ? updater(txn) : txn
            ),
          }
          : session
      )
    );
  };

  const addEvent = (sessionId: string, txnId: string) => {
    updateTxn(sessionId, txnId, (txn) => ({
      ...txn,
      events: [...txn.events, defaultEvent()],
    }));
  };

  const removeEvent = (
    sessionId: string,
    txnId: string,
    eventIndex: number,
  ) => {
    updateTxn(sessionId, txnId, (txn) => ({
      ...txn,
      events: txn.events.filter((_, index) => index !== eventIndex),
    }));
  };

  const updateEvent = (
    sessionId: string,
    txnId: string,
    eventIndex: number,
    updater: (event: BuilderEvent) => BuilderEvent,
  ) => {
    updateTxn(sessionId, txnId, (txn) => ({
      ...txn,
      events: txn.events.map((event, index) =>
        index === eventIndex ? updater(event) : event
      ),
    }));
  };

  const exportJson = () => {
    onExport(toWasmJson(sessions));
  };

  return (
    <div class="builder-backdrop">
      <button
        type="button"
        class="builder-backdrop-dismiss"
        onClick={onClose}
        aria-label="Close session builder"
      />
      <div
        class={`builder-panel ${open ? "open" : ""}`}
        data-testid="session-builder"
      >
        <div class="builder-header">
          <h2>Session Builder</h2>
          <div class="builder-header-actions">
            <button
              type="button"
              class="btn btn-sm"
              data-testid="builder-export-json"
              onClick={exportJson}
            >
              Export as JSON
            </button>
            <button
              type="button"
              class="btn btn-sm btn-icon"
              onClick={onClose}
              aria-label="Close builder"
            >
              <X size={14} />
            </button>
          </div>
        </div>

        <div class="builder-sessions" ref={sessionsRef}>
          <svg class="builder-edges" aria-hidden="true" focusable="false">
            {edges.map((edge) => (
              <line
                key={edge.id}
                x1={edge.x1}
                y1={edge.y1}
                x2={edge.x2}
                y2={edge.y2}
                stroke="var(--gh-edge-wr)"
                stroke-width="2"
              />
            ))}
          </svg>

          {sessions.map((session, sessionIndex) => (
            <div key={session.id} class="builder-session">
              <div class="builder-session-header">
                <span class="mono">S{sessionIndex + 1}</span>
                <button
                  type="button"
                  class="btn btn-sm btn-icon"
                  onClick={() =>
                    removeSession(session.id)}
                  aria-label="Delete session"
                  title="Delete session"
                >
                  <Minus size={12} />
                </button>
              </div>

              {session.txns.map((txn, txnIndex) => (
                <div
                  key={txn.id}
                  class="builder-txn"
                  ref={(element) => {
                    cardRefs.current[txn.id] = element;
                  }}
                >
                  <div class="builder-txn-header">
                    <span class="mono">T{txnIndex + 1}</span>
                    <button
                      type="button"
                      class="btn btn-sm btn-icon"
                      onClick={() => removeTxn(session.id, txn.id)}
                      aria-label="Delete transaction"
                      title="Delete transaction"
                    >
                      <Minus size={12} />
                    </button>
                  </div>

                  {txn.events.map((event, eventIndex) => (
                    <div
                      key={`${txn.id}-${eventIndex}`}
                      class="builder-event-row"
                    >
                      <select
                        value={event.type}
                        onChange={(evt) => {
                          const nextType = evt.currentTarget.value as
                            | "read"
                            | "write";
                          updateEvent(
                            session.id,
                            txn.id,
                            eventIndex,
                            (current) => ({
                              ...current,
                              type: nextType,
                              version: current.version ?? 0,
                            }),
                          );
                        }}
                      >
                        <option value="read">Read</option>
                        <option value="write">Write</option>
                      </select>
                      <input
                        type="number"
                        min="0"
                        value={String(event.variable)}
                        onInput={(evt) => {
                          const variable = parseNumber(evt.currentTarget.value);
                          updateEvent(
                            session.id,
                            txn.id,
                            eventIndex,
                            (current) => ({
                              ...current,
                              variable,
                            }),
                          );
                        }}
                        title="Variable"
                      />
                      <input
                        type="number"
                        min="0"
                        value={event.version === null
                          ? ""
                          : String(event.version)}
                        onInput={(evt) => {
                          const version = parseVersion(evt.currentTarget.value);
                          updateEvent(
                            session.id,
                            txn.id,
                            eventIndex,
                            (current) => ({
                              ...current,
                              version:
                                current.type === "write" && version === null
                                  ? 0
                                  : version,
                            }),
                          );
                        }}
                        placeholder={event.type === "read" ? "?" : "0"}
                        title="Version"
                      />
                      <button
                        type="button"
                        class="btn btn-sm btn-icon"
                        onClick={() =>
                          removeEvent(session.id, txn.id, eventIndex)}
                        aria-label="Delete event"
                        title="Delete event"
                      >
                        <Minus size={12} />
                      </button>
                    </div>
                  ))}

                  <div class="builder-txn-actions">
                    <label class="builder-committed-toggle">
                      <input
                        type="checkbox"
                        checked={txn.committed}
                        onChange={(evt) => {
                          const committed = evt.currentTarget.checked;
                          updateTxn(session.id, txn.id, (current) => ({
                            ...current,
                            committed,
                          }));
                        }}
                      />
                      <span>
                        <Check size={12} /> committed
                      </span>
                    </label>
                    <button
                      type="button"
                      class="btn btn-sm"
                      onClick={() =>
                        addEvent(session.id, txn.id)}
                    >
                      <Plus size={12} /> Add Event
                    </button>
                  </div>
                </div>
              ))}

              <button
                type="button"
                class="btn btn-sm"
                onClick={() => addTxn(session.id)}
              >
                <Plus size={12} /> Add Txn
              </button>
            </div>
          ))}

          <button type="button" class="btn btn-sm" onClick={addSession}>
            <Plus size={12} /> Add Session
          </button>
        </div>
      </div>
    </div>
  );
}
