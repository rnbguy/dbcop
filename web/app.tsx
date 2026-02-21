import { useCallback, useMemo, useState } from "preact/hooks";
import type { Theme } from "./components/ThemeToggle.tsx";
import { ThemeToggle } from "./components/ThemeToggle.tsx";
import { EditorPanel } from "./components/EditorPanel.tsx";
import { ResultBar } from "./components/ResultBar.tsx";
import { SessionDisplay } from "./components/SessionDisplay.tsx";
import { GraphPanel } from "./components/GraphPanel.tsx";
import { ShortcutHelp } from "./components/ShortcutHelp.tsx";
import { useWasmCheck } from "./hooks/useWasmCheck.ts";
import {
  type ShortcutHandler,
  useKeyboardShortcuts,
} from "./hooks/useKeyboardShortcuts.ts";
import type { ConsistencyLevel, InputFormat, TraceResult } from "./types.ts";

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
  const [{ result, loading, timedOut }, { runCheck, clear: _clear }] =
    useWasmCheck();

  // Track current editor state for keyboard-triggered check
  const [editorState, setEditorState] = useState<{
    text: string;
    level: ConsistencyLevel;
    format: InputFormat;
  }>({ text: "", level: "serializable", format: "text" });

  const toggleTheme = useCallback(() => {
    setTheme((prev) => {
      const next = prev === "dark" ? "light" : "dark";
      document.documentElement.setAttribute("data-theme", next);
      globalThis.localStorage?.setItem("theme", next);
      return next;
    });
  }, []);

  const handleCheck = useCallback(() => {
    runCheck(editorState.text, editorState.level, editorState.format);
  }, [editorState, runCheck]);

  const handleResult = useCallback(
    (r: TraceResult | null) => {
      if (r) {
        // Direct result from EditorPanel check button
        // (useWasmCheck also stores it, but EditorPanel may call its own check)
      }
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
        <ThemeToggle theme={theme} onToggle={toggleTheme} />
      </header>

      <div class="main-layout">
        <aside class="sidebar">
          <EditorPanel
            onResult={handleResult}
            onLoading={() => {}}
            onStateChange={setEditorState}
          />
        </aside>
        <main class="content">
          <ResultBar result={result} loading={loading} timedOut={timedOut} />
          <div class="content-panels">
            <SessionDisplay result={result} />
            <GraphPanel result={result} />
          </div>
        </main>
      </div>

      <ShortcutHelp open={showHelp} onClose={() => setShowHelp(false)} />
    </div>
  );
}
