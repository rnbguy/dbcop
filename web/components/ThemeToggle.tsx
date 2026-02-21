import { Moon, Sun } from "lucide-preact";

export type Theme = "dark" | "light";

interface Props {
  theme: Theme;
  onToggle: () => void;
}

export function ThemeToggle({ theme, onToggle }: Props) {
  const isDark = theme === "dark";
  return (
    <div class="theme-toggle">
      <Moon size={12} />
      <button
        class="toggle-track"
        role="switch"
        aria-checked={!isDark}
        aria-label={isDark ? "Switch to light mode" : "Switch to dark mode"}
        onClick={onToggle}
        type="button"
      >
        <span class={`toggle-thumb ${isDark ? "" : "toggle-thumb--light"}`} />
      </button>
      <Sun size={12} />
    </div>
  );
}
