import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "web/e2e",
  timeout: 30_000,
  retries: 0,
  webServer: {
    command:
      "deno run --allow-net --allow-read jsr:@std/http@1/file-server dist/",
    port: 8000,
    reuseExistingServer: true,
  },
  use: {
    baseURL: "http://localhost:8000",
  },
});
