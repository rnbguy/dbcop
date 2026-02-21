import { useEffect, useState } from "preact/hooks";
import { ThemeToggle } from "./components/ThemeToggle.tsx";

type Theme = "dark" | "light";

function getInitialTheme(): Theme {
  try {
    const stored = localStorage.getItem("theme");
    if (stored === "dark" || stored === "light") return stored;
    if (globalThis.matchMedia("(prefers-color-scheme: light)").matches) {
      return "light";
    }
  } catch (_) {
    // ignore
  }
  return "dark";
}

export function App() {
  const [theme, setTheme] = useState<Theme>("dark");

  useEffect(() => {
    const initial = getInitialTheme();
    setTheme(initial);
    document.documentElement.setAttribute("data-theme", initial);
  }, []);

  const toggleTheme = () => {
    const next: Theme = theme === "dark" ? "light" : "dark";
    setTheme(next);
    try {
      localStorage.setItem("theme", next);
    } catch (_) {
      // ignore
    }
    document.documentElement.setAttribute("data-theme", next);
  };

  return (
    <div class="app">
      <header class="header">
        <div class="header-brand">
          <span class="header-logo">dbcop</span>
          <span class="header-sep">/</span>
          <span class="header-tagline">consistency checker</span>
        </div>
        <div class="header-actions">
          <ThemeToggle theme={theme} onToggle={toggleTheme} />
        </div>
      </header>
      <div class="main-layout">
        <aside class="sidebar">
          {/* EditorPanel -- added in T6 */}
          <div class="panel-placeholder">Editor loading...</div>
        </aside>
        <main class="content">
          {/* ResultBar + SessionDisplay + GraphPanel -- added in T7, T8 */}
          <div class="panel-placeholder">Results loading...</div>
        </main>
      </div>
    </div>
  );
}
