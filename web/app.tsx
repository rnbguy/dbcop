import { useCallback, useMemo, useState } from "preact/hooks";
import type { Theme } from "./components/ThemeToggle.tsx";
import { ThemeToggle } from "./components/ThemeToggle.tsx";
import { EditorPanel } from "./components/EditorPanel.tsx";
import { ResultBar } from "./components/ResultBar.tsx";
import { SessionDisplay } from "./components/SessionDisplay.tsx";
import { GraphPanel } from "./components/GraphPanel.tsx";
import { ShortcutHelp } from "./components/ShortcutHelp.tsx";
import { Toolbar } from "./components/Toolbar.tsx";
import { useWasmCheck } from "./hooks/useWasmCheck.ts";
import {
  type ShortcutHandler,
  useKeyboardShortcuts,
} from "./hooks/useKeyboardShortcuts.ts";
import { useShareLink } from "./hooks/useShareLink.ts";
import type { ConsistencyLevel, InputFormat } from "./types.ts";

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
  const [{ result, loading, timedOut }, { runCheck }] = useWasmCheck();
  const { share, restore } = useShareLink();

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

  // Graph export functions (set by GraphPanel via callback)
  const [graphExport, setGraphExport] = useState<
    {
      exportPng: () => void;
    } | null
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
    runCheck(editorState.text, editorState.level, editorState.format);
  }, [editorState, runCheck]);

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
          graphExportPng={graphExport?.exportPng ?? null}
          graphExportSvg={null}
        />
        <ThemeToggle theme={theme} onToggle={toggleTheme} />
      </header>

      <div class="main-layout">
        <aside class="sidebar">
          <EditorPanel
            onResult={() => {}}
            onLoading={() => {}}
            onStateChange={setEditorState}
            importData={importData}
          />
        </aside>
        <main class="content">
          <ResultBar result={result} loading={loading} timedOut={timedOut} />
          <div class="content-panels">
            <SessionDisplay result={result} />
            <GraphPanel result={result} onExportReady={setGraphExport} />
          </div>
        </main>
      </div>

      <ShortcutHelp open={showHelp} onClose={() => setShowHelp(false)} />
    </div>
  );
}
