import { copy, ensureDir } from "@std/fs";

const dist = "dist";
await ensureDir(dist);

await copy("web/index.html", `${dist}/index.html`, { overwrite: true });
await copy("web/style.css", `${dist}/style.css`, { overwrite: true });
await copy("wasmlib", `${dist}/wasmlib`, { overwrite: true });

let html = await Deno.readTextFile(`${dist}/index.html`);
html = html.replace('src="main.ts"', 'src="main.js"');
await Deno.writeTextFile(`${dist}/index.html`, html);

const esbuild = await import("esbuild");
await esbuild.default.build({
  entryPoints: ["web/main.ts"],
  bundle: true,
  format: "esm",
  platform: "browser",
  external: ["../wasmlib/dbcop_wasm.js"],
  outfile: `${dist}/main.js`,
  minify: true,
});
await esbuild.default.stop();

let js = await Deno.readTextFile(`${dist}/main.js`);
js = js.replaceAll("../wasmlib/dbcop_wasm.js", "./wasmlib/dbcop_wasm.js");
await Deno.writeTextFile(`${dist}/main.js`, js);

console.log("Build complete: dist/");
