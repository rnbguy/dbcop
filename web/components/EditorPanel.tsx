import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ChevronDown, Play } from "lucide-preact";
import type { ConsistencyLevel, InputFormat, TraceResult } from "../types.ts";
import {
  CONSISTENCY_LEVELS,
  DEFAULT_EXAMPLE,
  DEFAULT_FORMAT,
  DEFAULT_LEVEL,
  EXAMPLE_KEYS,
  TEXT_EXAMPLES,
} from "../constants.ts";

const CHECK_TIMEOUT_MS = 10_000;

interface Props {
  onResult: (result: TraceResult | null) => void;
  onLoading: (loading: boolean) => void;
  onTimedOut?: (timedOut: boolean) => void;
  onRegisterCheck?: (fn: () => void) => void;
  checkControls?: preact.ComponentChildren;
  onStateChange?: (
    state: { text: string; level: ConsistencyLevel; format: InputFormat },
  ) => void;
  importData?: { text: string; format: InputFormat } | null;
}

export function EditorPanel(
  {
    onResult,
    onLoading,
    onTimedOut,
    onRegisterCheck,
    checkControls,
    onStateChange,
    importData,
  }: Props,
) {
  const [format, setFormat] = useState<InputFormat>(DEFAULT_FORMAT);
  const [level, setLevel] = useState<ConsistencyLevel>(DEFAULT_LEVEL);
  const [example, setExample] = useState(DEFAULT_EXAMPLE);
  const [text, setText] = useState(TEXT_EXAMPLES[DEFAULT_EXAMPLE].text);
  // Use uncontrolled textarea to avoid cursor resets from async re-renders
  const setTextareaValue = useCallback((newText: string) => {
    setText(newText);
    if (textareaRef.current) {
      textareaRef.current.value = newText;
    }
  }, []);
  const [highlightHtml, setHighlightHtml] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const overlayRef = useRef<HTMLDivElement>(null);

  // Notify parent of editor state changes
  useEffect(() => {
    onStateChange?.({ text, level, format });
  }, [text, level, format, onStateChange]);

  const loadExample = useCallback(
    async (key: string) => {
      setExample(key);
      const ex = TEXT_EXAMPLES[key];
      if (!ex) return;
      if (format === "text") {
        setTextareaValue(ex.text);
        setLevel(ex.level);
      } else {
        // Derive JSON from text dynamically
        try {
          const wasm = await import("../wasm.ts");
          const normalized = ex.text.endsWith("\n") ? ex.text : ex.text + "\n";
          const jsonStr = wasm.text_to_json_sessions(normalized);
          setTextareaValue(jsonStr);
          setLevel(ex.level);
        } catch {
          setTextareaValue(ex.text);
          setLevel(ex.level);
        }
      }
    },
    [format, setTextareaValue],
  );

  const switchFormat = useCallback(
    async (f: InputFormat) => {
      setFormat(f);
      const ex = TEXT_EXAMPLES[example];
      if (!ex) return;
      if (f === "json") {
        // Switching to JSON
        if (text === ex.text) {
          // User hasn't modified the example -- convert example text to JSON
          try {
            const wasm = await import("../wasm.ts");
            const normalized = ex.text.endsWith("\n")
              ? ex.text
              : ex.text + "\n";
            const jsonStr = wasm.text_to_json_sessions(normalized);
            setTextareaValue(jsonStr);
            setLevel(ex.level);
          } catch {
            setTextareaValue(ex.text);
            setLevel(ex.level);
          }
        } else {
          // User has custom text -- try to convert it
          try {
            const wasm = await import("../wasm.ts");
            const normalized = text.endsWith("\n") ? text : text + "\n";
            const jsonStr = wasm.text_to_json_sessions(normalized);
            if (!jsonStr.startsWith('{"error"')) {
              setTextareaValue(jsonStr);
            }
          } catch {
            // Keep text as-is
          }
        }
      } else {
        // Switching to Text -- no json-to-text conversion exists
        // If current content matches example's JSON, restore example text
        try {
          const wasm = await import("../wasm.ts");
          const normalized = ex.text.endsWith("\n") ? ex.text : ex.text + "\n";
          const exampleJson = wasm.text_to_json_sessions(normalized);
          if (text === exampleJson) {
            setTextareaValue(ex.text);
            setLevel(ex.level);
            return;
          }
        } catch {
          // Fall through
        }
        // Custom content -- keep as-is
      }
    },
    [example, text, setTextareaValue],
  );

  // Apply imported data
  useEffect(() => {
    if (importData) {
      setText(importData.text);
      setFormat(importData.format);
    }
  }, [importData]);

  // Syntax highlighting for text format
  useEffect(() => {
    if (format !== "text") {
      setHighlightHtml("");
      return;
    }
    import("../wasm.ts").then((wasm) => {
      try {
        const tokens = JSON.parse(wasm.tokenize_history(text));
        let html = "";
        for (const tok of tokens) {
          const cls = tokenClass(tok.kind);
          const escaped = escapeHtml(tok.text);
          html += cls ? `<span class="${cls}">${escaped}</span>` : escaped;
        }
        setHighlightHtml(html);
      } catch {
        setHighlightHtml(escapeHtml(text));
      }
    }).catch(() => {
      setHighlightHtml(escapeHtml(text));
    });
  }, [text, format]);

  const handleScroll = () => {
    if (textareaRef.current && overlayRef.current) {
      overlayRef.current.scrollTop = textareaRef.current.scrollTop;
      overlayRef.current.scrollLeft = textareaRef.current.scrollLeft;
    }
  };

  const runCheck = useCallback(async () => {
    onLoading(true);
    onResult(null);
    onTimedOut?.(false);

    const timeoutId = setTimeout(() => {
      onTimedOut?.(true);
    }, CHECK_TIMEOUT_MS);

    try {
      const wasm = await import("../wasm.ts");
      let resultJson: string;
      if (format === "text") {
        // Parser requires trailing newline on every line
        const normalized = text.endsWith("\n") ? text : text + "\n";
        resultJson = wasm.check_consistency_trace_text(normalized, level);
      } else {
        resultJson = wasm.check_consistency_trace(text, level);
      }
      const result: TraceResult = JSON.parse(resultJson);
      onResult(result);
    } catch (err) {
      onResult({
        ok: false,
        error: err instanceof Error ? err.message : String(err),
      });
    } finally {
      clearTimeout(timeoutId);
      onTimedOut?.(false);
      onLoading(false);
    }
  }, [format, level, text, onResult, onLoading, onTimedOut]);

  // Expose runCheck to parent for keyboard shortcut
  useEffect(() => {
    onRegisterCheck?.(runCheck);
  }, [runCheck, onRegisterCheck]);

  return (
    <div class="editor-panel">
      <Section title="Example" defaultOpen>
        <div class="editor-field">
          <select
            value={example}
            onChange={(e) => loadExample((e.target as HTMLSelectElement).value)}
          >
            {EXAMPLE_KEYS.map((key) => (
              <option key={key} value={key}>{key}</option>
            ))}
          </select>
        </div>
      </Section>

      <Section title="Input" defaultOpen>
        <div class="editor-field">
          <div class="format-toggle">
            <button
              type="button"
              class={`btn btn-sm ${format === "text" ? "btn-primary" : ""}`}
              onClick={() => switchFormat("text")}
            >
              Text
            </button>
            <button
              type="button"
              class={`btn btn-sm ${format === "json" ? "btn-primary" : ""}`}
              onClick={() => switchFormat("json")}
            >
              JSON
            </button>
          </div>
        </div>
        <div class="editor-wrap">
          <textarea
            ref={textareaRef}
            class="editor-textarea mono"
            defaultValue={text}
            onInput={(e) => setText((e.target as HTMLTextAreaElement).value)}
            onScroll={handleScroll}
            spellcheck={false}
          />
          {format === "text" && highlightHtml && (
            <div
              ref={overlayRef}
              class="editor-highlight mono"
              dangerouslySetInnerHTML={{ __html: highlightHtml + "\n" }}
            />
          )}
        </div>
      </Section>

      <Section title="Check" defaultOpen>
        <div class="editor-field">
          <label class="field-label" for="consistency-level-select">
            Consistency Level
          </label>
          <select
            id="consistency-level-select"
            value={level}
            onChange={(e) =>
              setLevel(
                (e.target as HTMLSelectElement).value as ConsistencyLevel,
              )}
          >
            {CONSISTENCY_LEVELS.map(({ value, label }) => (
              <option key={value} value={value}>{label}</option>
            ))}
          </select>
        </div>
        <div class="editor-field">
          <div class="check-actions">
            <button
              type="button"
              class="btn btn-primary check-btn"
              onClick={runCheck}
            >
              <Play size={14} /> Check
            </button>
            {checkControls}
          </div>
        </div>
      </Section>
    </div>
  );
}

// -- Collapsible section (uses lucide ChevronDown) --------------------------

function Section(
  { title, defaultOpen = true, children }: {
    title: string;
    defaultOpen?: boolean;
    children: preact.ComponentChildren;
  },
) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div class="section">
      <button
        type="button"
        class="section-header"
        onClick={() => setOpen(!open)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") setOpen(!open);
        }}
      >
        <span class="section-title">{title}</span>
        <ChevronDown
          size={14}
          class={`section-chevron ${open ? "" : "section-chevron--collapsed"}`}
        />
      </button>
      {open && <div class="section-body">{children}</div>}
    </div>
  );
}

// -- Helpers ----------------------------------------------------------------

function tokenClass(kind: string): string {
  const map: Record<string, string> = {
    Comment: "syn-comment",
    BracketOpen: "syn-bracket",
    BracketClose: "syn-bracket",
    ColonEquals: "syn-write-op",
    DoubleEquals: "syn-read-op",
    Ident: "syn-variable",
    Integer: "syn-number",
    Dash: "syn-separator",
    Bang: "syn-bang",
    QuestionMark: "syn-question",
  };
  return map[kind] ?? "";
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(
    />/g,
    "&gt;",
  );
}
