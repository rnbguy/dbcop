import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "preact/hooks";
import type { Theme } from "./components/ThemeToggle.tsx";
import { ThemeToggle } from "./components/ThemeToggle.tsx";
import { EditorPanel } from "./components/EditorPanel.tsx";
import { ResultBar } from "./components/ResultBar.tsx";
import { GraphPanel } from "./components/GraphPanel.tsx";
import { ShortcutHelp } from "./components/ShortcutHelp.tsx";
import { SessionBuilder } from "./components/SessionBuilder.tsx";
import { StepThrough } from "./components/StepThrough.tsx";
import { Toolbar } from "./components/Toolbar.tsx";
import { useWasmCheck } from "./hooks/useWasmCheck.ts";
import {
  type ShortcutHandler,
  useKeyboardShortcuts,
} from "./hooks/useKeyboardShortcuts.ts";
import { useShareLink } from "./hooks/useShareLink.ts";
import type {
  ConsistencyLevel,
  InputFormat,
  TraceResult,
  TransactionId,
} from "./types.ts";

function getInitialTheme(): Theme {
  const stored = globalThis.localStorage?.getItem("theme");
  if (stored === "light" || stored === "dark") return stored;
  if (globalThis.matchMedia?.("(prefers-color-scheme: light)").matches) {
    return "light";
  }
  return "dark";
}

export function App() {
  const [theme, setTheme] = useState<Theme>(getInitialTheme);
  const [showHelp, setShowHelp] = useState(false);
  const [showBuilder, setShowBuilder] = useState(false);
  const [{ result, loading, timedOut }, { runCheck }] = useWasmCheck();
  const { share, restore } = useShareLink();
  const [displayResult, setDisplayResult] = useState<TraceResult | null>(null);
  const [displayLoading, setDisplayLoading] = useState(false);
  const [displayTimedOut, setDisplayTimedOut] = useState(false);

  // Sidebar resize state
  const [sidebarWidth, setSidebarWidth] = useState(340);
  const resizeRef = useRef<HTMLDivElement>(null);

  const handleResizeStart = useCallback((e: MouseEvent) => {
    e.preventDefault();
    const startX = e.clientX;
    const startWidth = sidebarWidth;
    const handle = resizeRef.current;
    if (handle) handle.classList.add("dragging");

    const onMouseMove = (ev: MouseEvent) => {
      const delta = ev.clientX - startX;
      const next = Math.min(520, Math.max(220, startWidth + delta));
      setSidebarWidth(next);
    };

    const onMouseUp = () => {
      document.removeEventListener("mousemove", onMouseMove);
      document.removeEventListener("mouseup", onMouseUp);
      if (handle) handle.classList.remove("dragging");
    };

    document.addEventListener("mousemove", onMouseMove);
    document.addEventListener("mouseup", onMouseUp);
  }, [sidebarWidth]);

  // Track current editor state for keyboard-triggered check and share
  const [editorState, setEditorState] = useState<{
    text: string;
    level: ConsistencyLevel;
    format: InputFormat;
  }>(() => {
    const restored = restore();
    if (restored) return restored;
    return { text: "", level: "serializable", format: "text" };
  });

  const [highlightEdges, setHighlightEdges] = useState<
    ((edges: [TransactionId, TransactionId][]) => void) | null
  >(null);

  // Import handler (receives text from file drop/select)
  const [importData, setImportData] = useState<
    {
      text: string;
      format: InputFormat;
    } | null
  >(null);

  const toggleTheme = useCallback(() => {
    setTheme((prev) => {
      const next = prev === "dark" ? "light" : "dark";
      document.documentElement.setAttribute("data-theme", next);
      globalThis.localStorage?.setItem("theme", next);
      return next;
    });
  }, []);

  const handleCheck = useCallback(() => {
    setDisplayLoading(true);
    setDisplayResult(null);
    setDisplayTimedOut(false);
    runCheck(editorState.text, editorState.level, editorState.format);
  }, [editorState, runCheck]);

  useEffect(() => {
    setDisplayLoading(loading);
    setDisplayTimedOut(timedOut);
    if (result !== null || !loading) {
      setDisplayResult(result);
    }
  }, [loading, result, timedOut]);

  const handleShare = useCallback(() => {
    return share(editorState);
  }, [editorState, share]);

  const handleImport = useCallback(
    (text: string, format: InputFormat) => {
      setImportData({ text, format });
      setEditorState((s) => ({ ...s, text, format }));
    },
    [],
  );

  const shortcuts: ShortcutHandler = useMemo(() => ({
    onCheck: handleCheck,
    onToggleTheme: toggleTheme,
    onFormatText: () =>
      setEditorState((s) => ({ ...s, format: "text" as const })),
    onFormatJson: () =>
      setEditorState((s) => ({ ...s, format: "json" as const })),
    onShowHelp: () => setShowHelp((v) => !v),
  }), [handleCheck, toggleTheme]);

  useKeyboardShortcuts(shortcuts);

  return (
    <div class="app">
      <header class="header">
        <div class="header-brand">
          <h1 class="header-title">dbcop</h1>
          <span class="header-subtitle">consistency checker</span>
        </div>
        <Toolbar
          editorState={editorState}
          onImport={handleImport}
          onShare={handleShare}
          onOpenBuilder={() => setShowBuilder(true)}
        />
        <ThemeToggle theme={theme} onToggle={toggleTheme} />
      </header>

      <div class="main-layout">
        <aside class="sidebar" style={{ width: sidebarWidth + "px" }}>
          <EditorPanel
            onResult={(next) => {
              setDisplayResult(next);
              if (next) setDisplayTimedOut(false);
            }}
            onLoading={(next) => {
              setDisplayLoading(next);
              if (next) {
                setDisplayTimedOut(false);
                setDisplayResult(null);
              }
            }}
            checkControls={
              <StepThrough
                input={editorState.text}
                level={editorState.level}
                format={editorState.format}
                graphRef={highlightEdges ? { highlightEdges } : null}
                onResult={(next) => {
                  setDisplayResult(next);
                  setDisplayLoading(false);
                  setDisplayTimedOut(false);
                }}
              />
            }
            onStateChange={setEditorState}
            importData={importData}
          />
        </aside>
        <div
          class="resize-handle"
          ref={resizeRef}
          onMouseDown={handleResizeStart}
        />
        <main class="content">
          <ResultBar
            result={displayResult}
            loading={displayLoading}
            timedOut={displayTimedOut}
          />
          <div class="content-panels">
            <GraphPanel
              result={displayResult}
              onHighlightReady={setHighlightEdges}
            />
          </div>
        </main>
      </div>

      <ShortcutHelp open={showHelp} onClose={() => setShowHelp(false)} />
      <SessionBuilder
        open={showBuilder}
        onClose={() => setShowBuilder(false)}
        onExport={(json) => {
          handleImport(JSON.stringify(json, null, 2), "json");
          setShowBuilder(false);
        }}
      />
    </div>
  );
}
