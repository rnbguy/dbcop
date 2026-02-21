import type { ConsistencyLevel, InputFormat } from "./types.ts";

// -- Consistency levels -----------------------------------------------------

export const CONSISTENCY_LEVELS: {
  value: ConsistencyLevel;
  label: string;
}[] = [
  { value: "committed-read", label: "Committed Read" },
  { value: "atomic-read", label: "Atomic Read" },
  { value: "causal", label: "Causal" },
  { value: "prefix", label: "Prefix" },
  { value: "snapshot-isolation", label: "Snapshot Isolation" },
  { value: "serializable", label: "Serializable" },
];

// -- Example histories (text DSL) -------------------------------------------

export const TEXT_EXAMPLES: Record<
  string,
  { text: string; level: ConsistencyLevel }
> = {
  "write-read": {
    level: "serializable",
    text: `// session 1: write then read
[x:=1] [x==1]
---
// session 2: concurrent write
[x:=2]`,
  },
  "lost-update": {
    level: "snapshot-isolation",
    text: `// session 1: read-then-write
[x==0 x:=1]
---
// session 2: read-then-write (lost update)
[x==0 x:=2]`,
  },
  serializable: {
    level: "serializable",
    text: `// session 1: write both
[x:=1 y:=1]
---
// session 2: read x, write y
[x==1 y:=2]
---
// session 3: read y
[y==2]`,
  },
  "causal-violation": {
    level: "causal",
    text: `// session 1: write x and y
[x:=1 y:=1]
---
// session 2: sees x, writes y
[x==1 y:=2]
---
// session 3: sees y:=2 but not x:=1 (causal violation)
[y==2 x==?]`,
  },
};

// -- Example histories (JSON) -----------------------------------------------

export const JSON_EXAMPLES: Record<
  string,
  { json: unknown[]; level: ConsistencyLevel }
> = {
  "write-read": {
    level: "serializable",
    json: [
      [
        { events: [{ Write: { variable: 0, version: 1 } }], committed: true },
        { events: [{ Read: { variable: 0, version: 1 } }], committed: true },
      ],
      [
        { events: [{ Write: { variable: 0, version: 2 } }], committed: true },
      ],
    ],
  },
  "lost-update": {
    level: "snapshot-isolation",
    json: [
      [
        {
          events: [
            { Read: { variable: 0, version: 0 } },
            { Write: { variable: 0, version: 1 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [
            { Read: { variable: 0, version: 0 } },
            { Write: { variable: 0, version: 2 } },
          ],
          committed: true,
        },
      ],
    ],
  },
  serializable: {
    level: "serializable",
    json: [
      [
        {
          events: [
            { Write: { variable: 0, version: 1 } },
            { Write: { variable: 1, version: 1 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [
            { Read: { variable: 0, version: 1 } },
            { Write: { variable: 1, version: 2 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [{ Read: { variable: 1, version: 2 } }],
          committed: true,
        },
      ],
    ],
  },
  "causal-violation": {
    level: "causal",
    json: [
      [
        {
          events: [
            { Write: { variable: 0, version: 1 } },
            { Write: { variable: 1, version: 1 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [
            { Read: { variable: 0, version: 1 } },
            { Write: { variable: 1, version: 2 } },
          ],
          committed: true,
        },
      ],
      [
        {
          events: [
            { Read: { variable: 1, version: 2 } },
            { Read: { variable: 0, version: 0 } },
          ],
          committed: true,
        },
      ],
    ],
  },
};

// -- Example keys -----------------------------------------------------------

export const EXAMPLE_KEYS = Object.keys(TEXT_EXAMPLES);

// -- Default values ---------------------------------------------------------

export const DEFAULT_FORMAT: InputFormat = "text";
export const DEFAULT_LEVEL: ConsistencyLevel = "serializable";
export const DEFAULT_EXAMPLE = "write-read";

// -- Keyboard shortcuts -----------------------------------------------------

export const SHORTCUTS: { key: string; mod: string; description: string }[] = [
  { key: "Enter", mod: "Ctrl", description: "Run check" },
  { key: "d", mod: "Ctrl", description: "Toggle dark/light theme" },
  { key: "1", mod: "Ctrl", description: "Switch to text format" },
  { key: "2", mod: "Ctrl", description: "Switch to JSON format" },
  { key: "?", mod: "", description: "Show keyboard shortcuts" },
];
