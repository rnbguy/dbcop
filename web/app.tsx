import { useState } from "preact/hooks";
import type { Theme } from "./components/ThemeToggle.tsx";
import { ThemeToggle } from "./components/ThemeToggle.tsx";
import { EditorPanel } from "./components/EditorPanel.tsx";
import { ResultBar } from "./components/ResultBar.tsx";
import { SessionDisplay } from "./components/SessionDisplay.tsx";
import { GraphPanel } from "./components/GraphPanel.tsx";
import type { TraceResult } from "./types.ts";

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
  const [result, setResult] = useState<TraceResult | null>(null);
  const [loading, setLoading] = useState(false);

  const toggleTheme = () => {
    const next = theme === "dark" ? "light" : "dark";
    setTheme(next);
    document.documentElement.setAttribute("data-theme", next);
    globalThis.localStorage?.setItem("theme", next);
  };

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
          <EditorPanel onResult={setResult} onLoading={setLoading} />
        </aside>
        <main class="content">
          <ResultBar result={result} loading={loading} />
          <div class="content-panels">
            <SessionDisplay result={result} />
            <GraphPanel result={result} />
          </div>
        </main>
      </div>
    </div>
  );
}
