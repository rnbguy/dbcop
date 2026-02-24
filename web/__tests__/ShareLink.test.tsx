import { describe, expect, it } from "vitest";
import { decodeShareState, encodeShareState } from "../hooks/useShareLink.ts";
import type { ConsistencyLevel, InputFormat } from "../types.ts";

interface ShareState {
  text: string;
  level: ConsistencyLevel;
  format: InputFormat;
}

describe("encodeShareState / decodeShareState", () => {
  const sampleState: ShareState = {
    text: '[{"id":1}]',
    level: "causal",
    format: "json",
  };

  it("encodeShareState returns a non-empty string", () => {
    const encoded = encodeShareState(sampleState);
    expect(typeof encoded).toBe("string");
    expect(encoded.length).toBeGreaterThan(0);
  });

  it("decodeShareState roundtrips with encodeShareState", () => {
    const encoded = encodeShareState(sampleState);
    const decoded = decodeShareState(encoded);
    expect(decoded).toEqual(sampleState);
  });

  it("roundtrips with unicode text content", () => {
    const state: ShareState = {
      text: "x := 42; // some data",
      level: "serializable",
      format: "text",
    };
    const decoded = decodeShareState(encodeShareState(state));
    expect(decoded).toEqual(state);
  });

  it("roundtrips all consistency levels", () => {
    const levels: ConsistencyLevel[] = [
      "committed-read",
      "atomic-read",
      "causal",
      "prefix",
      "snapshot-isolation",
      "serializable",
    ];
    for (const level of levels) {
      const state: ShareState = { text: "test", level, format: "json" };
      expect(decodeShareState(encodeShareState(state))).toEqual(state);
    }
  });

  it("decodeShareState returns null for invalid base64", () => {
    expect(decodeShareState("%%%not-valid-base64%%%")).toBeNull();
  });

  it("decodeShareState returns null for valid base64 but invalid JSON", () => {
    const encoded = btoa("not json at all");
    expect(decodeShareState(encoded)).toBeNull();
  });

  it("decodeShareState returns null for JSON missing required fields", () => {
    const encoded = btoa(encodeURIComponent(JSON.stringify({ text: "hi" })));
    expect(decodeShareState(encoded)).toBeNull();
  });

  it("decodeShareState returns null for empty string", () => {
    expect(decodeShareState("")).toBeNull();
  });
});
