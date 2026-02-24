// Validates history.schema.json is well-formed and examples conform to it.
import Ajv2020 from "ajv/dist/2020.js";

const schema = JSON.parse(
  await Deno.readTextFile(new URL("./history.schema.json", import.meta.url)),
);

// deno-lint-ignore no-explicit-any
const ajv = new (Ajv2020 as any)({ strict: true, allErrors: true });
const validate = ajv.compile(schema);

// Validate built-in examples
const examples: Record<string, unknown[]> = {
  "write-read": [
    [
      { events: [{ Write: { variable: 0, version: 1 } }], committed: true },
      { events: [{ Read: { variable: 0, version: 1 } }], committed: true },
    ],
    [
      { events: [{ Write: { variable: 0, version: 2 } }], committed: true },
    ],
  ],
  "lost-update": [
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
  "uncommitted-read": [
    [
      {
        events: [{ Read: { variable: 0, version: null } }],
        committed: true,
      },
    ],
  ],
};

let failed = false;

for (const [name, history] of Object.entries(examples)) {
  const valid = validate(history);
  if (valid) {
    console.log(`  ${name}: ok`);
  } else {
    console.error(`  ${name}: FAIL`);
    console.error(`    ${ajv.errorsText(validate.errors)}`);
    failed = true;
  }
}

if (failed) {
  console.error("\nSchema validation failed.");
  Deno.exit(1);
} else {
  console.log("\nAll examples conform to schema.");
}
