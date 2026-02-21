export type Theme = "dark" | "light";

interface ThemeToggleProps {
  theme: Theme;
  onToggle: () => void;
}

function MoonIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="currentColor"
      aria-hidden="true"
    >
      <path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
    </svg>
  );
}

function SunIcon() {
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width="12"
      height="12"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="2"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="4" fill="currentColor" stroke="none" />
      <line x1="12" y1="2" x2="12" y2="5" />
      <line x1="12" y1="19" x2="12" y2="22" />
      <line x1="4.22" y1="4.22" x2="6.34" y2="6.34" />
      <line x1="17.66" y1="17.66" x2="19.78" y2="19.78" />
      <line x1="2" y1="12" x2="5" y2="12" />
      <line x1="19" y1="12" x2="22" y2="12" />
      <line x1="6.34" y1="17.66" x2="4.22" y2="19.78" />
      <line x1="19.78" y1="4.22" x2="17.66" y2="6.34" />
    </svg>
  );
}

export function ThemeToggle({ theme, onToggle }: ThemeToggleProps) {
  const isDark = theme === "dark";
  return (
    <div class="theme-toggle">
      <MoonIcon />
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
      <SunIcon />
    </div>
  );
}
