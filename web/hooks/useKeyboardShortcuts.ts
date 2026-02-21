import { useEffect } from "preact/hooks";

export interface ShortcutHandler {
  onCheck: () => void;
  onToggleTheme: () => void;
  onFormatText: () => void;
  onFormatJson: () => void;
  onShowHelp: () => void;
}

export function useKeyboardShortcuts(handlers: ShortcutHandler) {
  useEffect(() => {
    const handle = (e: KeyboardEvent) => {
      // Ignore if user is typing in an input/textarea (except for shortcuts with Ctrl)
      const target = e.target as HTMLElement;
      const isInput = target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA";

      if (e.ctrlKey && e.key === "Enter") {
        e.preventDefault();
        handlers.onCheck();
        return;
      }

      if (e.ctrlKey && e.key === "d") {
        e.preventDefault();
        handlers.onToggleTheme();
        return;
      }

      if (e.ctrlKey && e.key === "1") {
        e.preventDefault();
        handlers.onFormatText();
        return;
      }

      if (e.ctrlKey && e.key === "2") {
        e.preventDefault();
        handlers.onFormatJson();
        return;
      }

      if (!isInput && e.key === "?") {
        e.preventDefault();
        handlers.onShowHelp();
        return;
      }
    };

    globalThis.addEventListener("keydown", handle);
    return () => globalThis.removeEventListener("keydown", handle);
  }, [handlers]);
}
