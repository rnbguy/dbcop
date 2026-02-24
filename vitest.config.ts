import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "happy-dom",
    include: ["web/__tests__/**/*.test.{ts,tsx}"],
    setupFiles: ["web/__tests__/setup.ts"],
  },
  esbuild: {
    jsx: "automatic",
    jsxImportSource: "preact",
  },
});
