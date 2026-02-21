import { expect, test } from "@playwright/test";

test("app loads with correct title", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await page.goto("/");

  await expect(page).toHaveTitle(/dbcop/);
  expect(errors).toHaveLength(0);
});

test("theme toggle switches data-theme and persists on reload", async ({ page }) => {
  await page.goto("/");
  const html = page.locator("html");
  const toggle = page.locator('button[role="switch"]');

  // index.html inline script sets data-theme before app hydrates
  const initial = await html.getAttribute("data-theme");
  expect(initial === "dark" || initial === "light").toBe(true);

  await toggle.click();
  const toggled = initial === "dark" ? "light" : "dark";
  await expect(html).toHaveAttribute("data-theme", toggled);

  await page.reload();
  await expect(page.locator("html")).toHaveAttribute("data-theme", toggled);
});

test("write-read example passes consistency check", async ({ page }) => {
  await page.goto("/");

  await page.locator(".btn-primary.check-btn").click();

  await expect(page.locator(".badge-pass")).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(".result-bar")).toContainText("PASS");
});

test("lost-update example fails consistency check", async ({ page }) => {
  await page.goto("/");

  await page.locator(".editor-panel select").first().selectOption(
    "lost-update",
  );
  await page.locator(".btn-primary.check-btn").click();

  await expect(page.locator(".badge-fail")).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(".result-bar")).toContainText("FAIL");
});

test("JSON format check returns a result", async ({ page }) => {
  await page.goto("/");

  await page.locator(".format-toggle button", { hasText: "JSON" }).click();
  await page.locator(".btn-primary.check-btn").click();

  const badge = page.locator(".badge-pass, .badge-fail");
  await expect(badge).toBeVisible({ timeout: 15_000 });
});

test("selecting different example updates editor content", async ({ page }) => {
  await page.goto("/");
  const textarea = page.locator(".editor-textarea");

  const initial = await textarea.inputValue();

  await page.locator(".editor-panel select").first().selectOption(
    "lost-update",
  );
  const updated = await textarea.inputValue();

  expect(updated).not.toBe(initial);
  expect(updated).toContain("lost update");
});

test("consistency level selector updates selection", async ({ page }) => {
  await page.goto("/");

  const levelSelect = page.locator(".editor-panel select").nth(1);
  await levelSelect.selectOption("causal");
  await expect(levelSelect).toHaveValue("causal");

  await levelSelect.selectOption("prefix");
  await expect(levelSelect).toHaveValue("prefix");
});

test("share button updates URL hash", async ({ page }) => {
  await page.goto("/");

  const hashBefore = await page.evaluate(() => location.hash);
  expect(hashBefore).toBe("");

  await page.locator('button[title="Copy share link"]').click();

  await page.waitForFunction(() => location.hash.length > 1);
  const hashAfter = await page.evaluate(() => location.hash);
  expect(hashAfter.length).toBeGreaterThan(1);
});

test("? key opens shortcut help overlay", async ({ page }) => {
  await page.goto("/");

  // ? shortcut is suppressed when focus is on input/textarea
  await page.locator(".header-brand").click();

  await page.keyboard.press("?");
  await expect(page.locator(".overlay")).toBeVisible();
  await expect(page.locator(".overlay-panel")).toContainText(
    "Keyboard Shortcuts",
  );

  await page.locator('.overlay-panel button[aria-label="Close"]').click();
  await expect(page.locator(".overlay")).not.toBeVisible();
});

test("session cards appear after successful check", async ({ page }) => {
  await page.goto("/");

  await expect(page.locator(".sessions-empty")).toBeVisible();

  await page.locator(".btn-primary.check-btn").click();
  await expect(page.locator(".badge-pass")).toBeVisible({ timeout: 15_000 });

  await expect(page.locator(".session-column").first()).toBeVisible();
  await expect(page.locator(".txn-card").first()).toBeVisible();
});

test("graph panel renders after check", async ({ page }) => {
  await page.goto("/");

  await expect(page.locator(".graph-panel.empty")).toBeVisible();

  await page.locator(".btn-primary.check-btn").click();
  await expect(page.locator(".badge-pass")).toBeVisible({ timeout: 15_000 });

  await expect(page.locator(".graph-container")).toBeVisible();
  await expect(page.locator(".graph-legend")).toBeVisible();
});

test("session builder opens and closes", async ({ page }) => {
  await page.goto("/");

  await expect(
    page.locator('[data-testid="session-builder"]'),
  ).not.toBeVisible();

  await page.locator('button[title="Session builder"]').click();
  const builder = page.locator('[data-testid="session-builder"]');
  await expect(builder).toBeVisible();
  await expect(builder).toContainText("Session Builder");

  await page.locator('button[aria-label="Close builder"]').click();
  await expect(builder).not.toBeVisible();
});
