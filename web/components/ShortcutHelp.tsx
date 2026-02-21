import { X } from "lucide-preact";
import { SHORTCUTS } from "../constants.ts";

interface Props {
  open: boolean;
  onClose: () => void;
}

export function ShortcutHelp({ open, onClose }: Props) {
  if (!open) return null;

  return (
    <div class="overlay" onClick={onClose}>
      <div
        class="overlay-panel"
        onClick={(e) => e.stopPropagation()}
      >
        <div class="overlay-header">
          <h2>Keyboard Shortcuts</h2>
          <button
            type="button"
            class="btn btn-icon"
            onClick={onClose}
            aria-label="Close"
          >
            <X size={16} />
          </button>
        </div>
        <div class="shortcut-list">
          {SHORTCUTS.map((s) => (
            <div key={s.key} class="shortcut-row">
              <span class="shortcut-keys">
                {s.mod && <kbd>{s.mod}</kbd>}
                {s.mod && " + "}
                <kbd>{s.key}</kbd>
              </span>
              <span class="shortcut-desc">{s.description}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
