// Validates schemas/history.schema.json structure and verifies
// all JSON examples in web/constants.ts conform to the schema.

const schemaText = await Deno.readTextFile("schemas/history.schema.json");
const schema = JSON.parse(schemaText);

// -- Meta-validation: schema structure ------------------------------------

const requiredDefs = [
  "session",
  "transaction",
  "event",
  "writeEvent",
  "readEvent",
];
const errors: string[] = [];

if (schema.$schema !== "https://json-schema.org/draft/2020-12/schema") {
  errors.push("$schema must be https://json-schema.org/draft/2020-12/schema");
}
if (schema.type !== "array") {
  errors.push("Root type must be 'array' (array of sessions)");
}
for (const def of requiredDefs) {
  if (!schema.$defs?.[def]) {
    errors.push(`Missing required $defs.${def}`);
  }
}

if (errors.length > 0) {
  console.error("Schema meta-validation failed:");
  for (const e of errors) console.error(`  - ${e}`);
  Deno.exit(1);
}

console.log("Schema meta-validation: OK");

// -- Example validation: check JSON_EXAMPLES against schema ---------------

const { JSON_EXAMPLES } = await import("../web/constants.ts");

for (const [name, { json }] of Object.entries(JSON_EXAMPLES)) {
  validateHistory(json as unknown, name);
}

function validateHistory(history: unknown, name: string): void {
  if (!Array.isArray(history)) {
    throw new Error(`${name}: root must be array of sessions`);
  }
  for (let si = 0; si < history.length; si++) {
    const session = history[si];
    if (!Array.isArray(session)) {
      throw new Error(`${name}: sessions[${si}] must be array of transactions`);
    }
    if (session.length === 0) {
      throw new Error(
        `${name}: sessions[${si}] must have at least 1 transaction`,
      );
    }
    for (let ti = 0; ti < session.length; ti++) {
      validateTransaction(session[ti], `${name}.sessions[${si}].txns[${ti}]`);
    }
  }
  console.log(`  example "${name}": OK`);
}

function validateTransaction(txn: unknown, path: string): void {
  if (typeof txn !== "object" || txn === null) {
    throw new Error(`${path}: must be object`);
  }
  const t = txn as Record<string, unknown>;
  if (!Array.isArray(t.events)) {
    throw new Error(`${path}.events: must be array`);
  }
  if (t.events.length === 0) {
    throw new Error(`${path}.events: must have at least 1 event`);
  }
  if (typeof t.committed !== "boolean") {
    throw new Error(`${path}.committed: must be boolean`);
  }
  for (let ei = 0; ei < t.events.length; ei++) {
    validateEvent(t.events[ei], `${path}.events[${ei}]`);
  }
}

function validateEvent(event: unknown, path: string): void {
  if (typeof event !== "object" || event === null) {
    throw new Error(`${path}: must be object`);
  }
  const e = event as Record<string, unknown>;
  if ("Write" in e) {
    const w = e.Write as Record<string, unknown>;
    if (typeof w?.variable !== "number" || typeof w?.version !== "number") {
      throw new Error(`${path}.Write: variable and version must be numbers`);
    }
    if (w.variable < 0 || w.version < 0) {
      throw new Error(`${path}.Write: variable and version must be >= 0`);
    }
  } else if ("Read" in e) {
    const r = e.Read as Record<string, unknown>;
    if (typeof r?.variable !== "number") {
      throw new Error(`${path}.Read: variable must be number`);
    }
    if (r.version !== null && typeof r.version !== "number") {
      throw new Error(`${path}.Read: version must be number or null`);
    }
    if (typeof r.version === "number" && r.version < 0) {
      throw new Error(`${path}.Read: version must be >= 0`);
    }
  } else {
    throw new Error(
      `${path}: must have Write or Read key, got ${Object.keys(e).join(", ")}`,
    );
  }
}

console.log("All schema checks passed.");
