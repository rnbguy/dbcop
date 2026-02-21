import { Download, Grid, Link, Share2, Upload } from "lucide-preact";
import { useCallback, useRef, useState } from "preact/hooks";
import type { ConsistencyLevel, InputFormat } from "../types.ts";

interface Props {
  editorState: { text: string; level: ConsistencyLevel; format: InputFormat };
  onImport: (text: string, format: InputFormat) => void;
  onShare: () => string;
  onOpenBuilder: () => void;
  graphExportPng: (() => void) | null;
  graphExportSvg: (() => void) | null;
}

export function Toolbar(
  {
    editorState,
    onImport,
    onShare,
    onOpenBuilder,
    graphExportPng,
    graphExportSvg: _graphExportSvg,
  }: Props,
) {
  const fileRef = useRef<HTMLInputElement>(null);
  const [toast, setToast] = useState("");

  const showToast = useCallback((msg: string) => {
    setToast(msg);
    setTimeout(() => setToast(""), 2000);
  }, []);

  const handleShare = useCallback(() => {
    const url = onShare();
    showToast("Link copied!");
    void url; // already copied in hook
  }, [onShare, showToast]);

  const handleExport = useCallback(() => {
    const ext = editorState.format === "json" ? "json" : "txt";
    const blob = new Blob([editorState.text], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `dbcop-history.${ext}`;
    a.click();
    URL.revokeObjectURL(url);
  }, [editorState]);

  const handleFileSelect = useCallback(
    (e: Event) => {
      const file = (e.target as HTMLInputElement).files?.[0];
      if (!file) return;
      const reader = new FileReader();
      reader.onload = () => {
        const content = reader.result as string;
        const isJson = file.name.endsWith(".json") ||
          content.trimStart().startsWith("[");
        onImport(content, isJson ? "json" : "text");
        showToast(`Imported ${file.name}`);
      };
      reader.readAsText(file);
    },
    [onImport, showToast],
  );

  const handleDrop = useCallback(
    (e: DragEvent) => {
      e.preventDefault();
      const file = e.dataTransfer?.files?.[0];
      if (!file) return;
      const reader = new FileReader();
      reader.onload = () => {
        const content = reader.result as string;
        const isJson = file.name.endsWith(".json") ||
          content.trimStart().startsWith("[");
        onImport(content, isJson ? "json" : "text");
        showToast(`Imported ${file.name}`);
      };
      reader.readAsText(file);
    },
    [onImport, showToast],
  );

  return (
    <div
      class="toolbar"
      role="toolbar"
      aria-label="History actions"
      onDrop={handleDrop}
      onDragOver={(e) => e.preventDefault()}
    >
      <button
        type="button"
        class="btn btn-sm btn-icon"
        onClick={handleShare}
        title="Copy share link"
        aria-label="Copy share link"
      >
        <Link size={14} />
      </button>
      <button
        type="button"
        class="btn btn-sm btn-icon"
        onClick={() => fileRef.current?.click()}
        title="Import file"
        aria-label="Import file"
      >
        <Upload size={14} />
      </button>
      <button
        type="button"
        class="btn btn-sm btn-icon"
        onClick={handleExport}
        title="Export history"
        aria-label="Export history"
      >
        <Download size={14} />
      </button>
      <button
        type="button"
        class="btn btn-sm btn-icon"
        onClick={onOpenBuilder}
        title="Session builder"
        aria-label="Open session builder"
      >
        <Grid size={14} />
      </button>
      {graphExportPng && (
        <button
          type="button"
          class="btn btn-sm btn-icon"
          onClick={graphExportPng}
          title="Export graph as PNG"
          aria-label="Export graph as PNG"
        >
          <Share2 size={14} />
        </button>
      )}
      <input
        ref={fileRef}
        type="file"
        accept=".txt,.json,.history"
        style={{ display: "none" }}
        onChange={handleFileSelect}
      />
      {toast && <span class="toast">{toast}</span>}
    </div>
  );
}
