import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import { ChevronDown, Play } from "lucide-preact";
import type { ConsistencyLevel, InputFormat, TraceResult } from "../types.ts";
import {
  CONSISTENCY_LEVELS,
  DEFAULT_EXAMPLE,
  DEFAULT_FORMAT,
  DEFAULT_LEVEL,
  EXAMPLE_KEYS,
  JSON_EXAMPLES,
  TEXT_EXAMPLES,
} from "../constants.ts";

interface Props {
  onResult: (result: TraceResult | null) => void;
  onLoading: (loading: boolean) => void;
  onStateChange?: (
    state: { text: string; level: ConsistencyLevel; format: InputFormat },
  ) => void;
  importData?: { text: string; format: InputFormat } | null;
}

export function EditorPanel(
  { onResult, onLoading, onStateChange, importData }: Props,
) {
  const [format, setFormat] = useState<InputFormat>(DEFAULT_FORMAT);
  const [level, setLevel] = useState<ConsistencyLevel>(DEFAULT_LEVEL);
  const [example, setExample] = useState(DEFAULT_EXAMPLE);
  const [text, setText] = useState(TEXT_EXAMPLES[DEFAULT_EXAMPLE].text);
  const [highlightHtml, setHighlightHtml] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const overlayRef = useRef<HTMLDivElement>(null);

  // Notify parent of editor state changes
  useEffect(() => {
    onStateChange?.({ text, level, format });
  }, [text, level, format, onStateChange]);

  const loadExample = useCallback(
    (key: string) => {
      setExample(key);
      if (format === "text") {
        const ex = TEXT_EXAMPLES[key];
        if (ex) {
          setText(ex.text);
          setLevel(ex.level);
        }
      } else {
        const ex = JSON_EXAMPLES[key];
        if (ex) {
          setText(JSON.stringify(ex.json, null, 2));
          setLevel(ex.level);
        }
      }
    },
    [format],
  );

  const switchFormat = useCallback(
    (f: InputFormat) => {
      setFormat(f);
      if (f === "text") {
        const ex = TEXT_EXAMPLES[example];
        if (ex) {
          setText(ex.text);
          setLevel(ex.level);
        }
      } else {
        const ex = JSON_EXAMPLES[example];
        if (ex) {
          setText(JSON.stringify(ex.json, null, 2));
          setLevel(ex.level);
        }
      }
    },
    [example],
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
      onLoading(false);
    }
  }, [format, level, text, onResult, onLoading]);

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
            value={text}
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
          <label class="field-label">Consistency Level</label>
          <select
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
          <button
            type="button"
            class="btn btn-primary check-btn"
            onClick={runCheck}
          >
            <Play size={14} /> Check
          </button>
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
      <div
        class="section-header"
        onClick={() => setOpen(!open)}
        role="button"
        tabIndex={0}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") setOpen(!open);
        }}
      >
        <span class="section-title">{title}</span>
        <ChevronDown
          size={14}
          class={`section-chevron ${open ? "" : "section-chevron--collapsed"}`}
        />
      </div>
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
