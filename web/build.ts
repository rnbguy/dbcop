import { copy, ensureDir } from "@std/fs";
import { denoPlugins } from "@luca/esbuild-deno-loader";

const dist = "dist";
await ensureDir(dist);

await copy("web/index.html", `${dist}/index.html`, { overwrite: true });
await copy("web/style.css", `${dist}/style.css`, { overwrite: true });
await copy("web/theme.css", `${dist}/theme.css`, { overwrite: true });
await copy("wasmlib", `${dist}/wasmlib`, { overwrite: true });

// Download viz-standalone.js (pinned to 3.24.0) for browser graph rendering.
const vizUrl =
  "https://cdn.jsdelivr.net/npm/@viz-js/viz@3.24.0/dist/viz-global.js";
const vizResp = await fetch(vizUrl);
if (!vizResp.ok) {
  throw new Error(`Failed to fetch viz-standalone.js: ${vizResp.status}`);
}
await Deno.writeTextFile(`${dist}/viz-standalone.js`, await vizResp.text());

// Patch dist/wasmlib/dbcop_wasm.js for browser compatibility.
// The Deno-native 'import * as wasm from "./dbcop_wasm.wasm"' syntax
// is not supported in Chrome stable. Replace with WebAssembly.instantiateStreaming.
const wasmJsPath = `${dist}/wasmlib/dbcop_wasm.js`;
let wasmJs = await Deno.readTextFile(wasmJsPath);
const wasmImportLine = 'import * as wasm from "./dbcop_wasm.wasm";';
const browserCompatLines = [
  'import * as __wb_imports from "./dbcop_wasm.internal.js";',
  'const __wb_resp = fetch(new URL("./dbcop_wasm.wasm", import.meta.url));',
  "const wasm = await (async () => {",
  "  const r = await __wb_resp;",
  '  const ct = r.headers.get("content-type") ?? "";',
  '  const imports = { "./dbcop_wasm.internal.js": __wb_imports };',
  '  return ct.startsWith("application/wasm")',
  "    ? (await WebAssembly.instantiateStreaming(r, imports)).instance.exports",
  "    : (await WebAssembly.instantiate(await r.arrayBuffer(), imports)).instance.exports;",
  "})();",
];
wasmJs = wasmJs.replace(wasmImportLine, browserCompatLines.join("\n"));
await Deno.writeTextFile(wasmJsPath, wasmJs);

let html = await Deno.readTextFile(`${dist}/index.html`);
html = html.replace('src="main.ts"', 'src="main.js"');
await Deno.writeTextFile(`${dist}/index.html`, html);

const esbuild = await import("esbuild");
await esbuild.default.build({
  entryPoints: ["web/main.tsx"],
  bundle: true,
  format: "esm",
  platform: "browser",
  external: ["../wasmlib/dbcop_wasm.js"],
  outfile: `${dist}/main.js`,
  minify: true,
  jsx: "automatic",
  jsxImportSource: "preact",
  plugins: [...denoPlugins()],
});
await esbuild.default.stop();

let js = await Deno.readTextFile(`${dist}/main.js`);
js = js.replaceAll("../wasmlib/dbcop_wasm.js", "./wasmlib/dbcop_wasm.js");
await Deno.writeTextFile(`${dist}/main.js`, js);

console.log("Build complete: dist/");
