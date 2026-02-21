import { useCallback } from "preact/hooks";
import type { ConsistencyLevel, InputFormat } from "../types.ts";

interface ShareState {
  text: string;
  level: ConsistencyLevel;
  format: InputFormat;
}

export function encodeShareState(state: ShareState): string {
  const json = JSON.stringify(state);
  return btoa(encodeURIComponent(json));
}

export function decodeShareState(hash: string): ShareState | null {
  try {
    const json = decodeURIComponent(atob(hash));
    const parsed = JSON.parse(json);
    if (
      parsed && typeof parsed.text === "string" && parsed.level && parsed.format
    ) {
      return parsed as ShareState;
    }
  } catch {
    // Invalid share link, ignore
  }
  return null;
}

export function useShareLink() {
  const share = useCallback((state: ShareState) => {
    const encoded = encodeShareState(state);
    const url = new URL(globalThis.location.href);
    url.hash = encoded;
    globalThis.history.replaceState(null, "", url.toString());
    navigator.clipboard?.writeText(url.toString());
    return url.toString();
  }, []);

  const restore = useCallback((): ShareState | null => {
    const hash = globalThis.location.hash.slice(1);
    if (!hash) return null;
    return decodeShareState(hash);
  }, []);

  return { share, restore };
}
