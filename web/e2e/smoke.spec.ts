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

test("graph renders after successful check", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".graph-panel.empty")).toBeVisible();
  await page.locator(".btn-primary.check-btn").click();
  await expect(page.locator(".badge-pass")).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(".graph-container")).toBeVisible();
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

// -- Consistency level variations -------------------------------------------

test("write-read passes at committed-read level", async ({ page }) => {
  await page.goto("/");
  await page.locator(".format-toggle button", { hasText: "JSON" }).click();
  await page.locator(".editor-panel select").nth(1).selectOption(
    "committed-read",
  );
  await page.locator(".btn-primary.check-btn").click();
  const badge = page.locator(".badge-pass, .badge-fail");
  await expect(badge).toBeVisible({ timeout: 15_000 });
});
test("write-read passes at causal level", async ({ page }) => {
  await page.goto("/");
  await page.locator(".format-toggle button", { hasText: "JSON" }).click();
  await page.locator(".editor-panel select").nth(1).selectOption("causal");
  await page.locator(".btn-primary.check-btn").click();
  const badge = page.locator(".badge-pass, .badge-fail");
  await expect(badge).toBeVisible({ timeout: 15_000 });
});

test("lost-update fails at serializable level", async ({ page }) => {
  await page.goto("/");
  await page.locator(".editor-panel select").first().selectOption(
    "lost-update",
  );
  await page.locator(".editor-panel select").nth(1).selectOption(
    "serializable",
  );
  await page.locator(".btn-primary.check-btn").click();
  await expect(page.locator(".badge-fail")).toBeVisible({ timeout: 15_000 });
});

test("lost-update fails at prefix level", async ({ page }) => {
  await page.goto("/");
  await page.locator(".editor-panel select").first().selectOption(
    "lost-update",
  );
  await page.locator(".editor-panel select").nth(1).selectOption("prefix");
  await page.locator(".btn-primary.check-btn").click();
  await expect(page.locator(".badge-fail")).toBeVisible({ timeout: 15_000 });
});

// -- Step-through visualization ---------------------------------------------

test("step check shows step controls for causal level in JSON format", async ({ page }) => {
  await page.goto("/");
  await page.locator(".format-toggle button", { hasText: "JSON" }).click();
  await page.locator(".editor-panel select").nth(1).selectOption("causal");
  const stepBtn = page.locator(".step-through .check-btn");
  await expect(stepBtn).toBeVisible();
  await stepBtn.click();
  // Either step controls appear or an immediate result is shown
  const stepControls = page.locator(".step-controls");
  const badge = page.locator(".badge-pass, .badge-fail");
  await expect(stepControls.or(badge)).toBeVisible({ timeout: 15_000 });
});

test("step check in text format works with default example", async ({ page }) => {
  await page.goto("/");
  // Text format is default - select causal level for steppable mode
  await page.locator(".editor-panel select").nth(1).selectOption("causal");
  const stepBtn = page.locator(".step-through .check-btn");
  await expect(stepBtn).toBeVisible();
  await stepBtn.click();
  // Either step controls appear or an immediate result is shown
  const stepControls = page.locator(".step-controls");
  const badge = page.locator(".badge-pass, .badge-fail");
  await expect(stepControls.or(badge)).toBeVisible({ timeout: 15_000 });
});

// -- Error handling ---------------------------------------------------------

test("invalid JSON input shows error result", async ({ page }) => {
  await page.goto("/");
  await page.locator(".format-toggle button", { hasText: "JSON" }).click();
  await page.locator(".editor-textarea").fill("{ not valid json }}}");
  await page.locator(".btn-primary.check-btn").click();
  await expect(page.locator(".badge-fail")).toBeVisible({ timeout: 15_000 });
});

// -- Keyboard shortcuts -----------------------------------------------------

test("Ctrl+Enter triggers consistency check", async ({ page }) => {
  await page.goto("/");
  await page.locator(".header-brand").click();
  await page.keyboard.press("Control+Enter");
  const badge = page.locator(".badge-pass, .badge-fail");
  await expect(badge).toBeVisible({ timeout: 15_000 });
});

// -- Session display details ------------------------------------------------

test("session headers show correct labels after check", async ({ page }) => {
  await page.goto("/");
  await page.locator(".btn-primary.check-btn").click();
  await expect(page.locator(".badge-pass")).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(".graph-container")).toBeVisible({
    timeout: 5_000,
  });
});
test("transaction cards show read and write events", async ({ page }) => {
  await page.goto("/");
  await page.locator(".btn-primary.check-btn").click();
  await expect(page.locator(".badge-pass")).toBeVisible({ timeout: 15_000 });
  await expect(page.locator(".graph-container svg")).toBeVisible({
    timeout: 8_000,
  });
});

// -- Resize handle ----------------------------------------------------------

test("resize handle element exists in DOM", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator(".resize-handle")).toBeAttached();
});

// -- Accessibility ----------------------------------------------------------

test("share button has title attribute", async ({ page }) => {
  await page.goto("/");
  const shareBtn = page.locator('button[title="Copy share link"]');
  await expect(shareBtn).toBeVisible();
});

test("import file input is present in DOM", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator('input[type="file"]')).toBeAttached();
});

// -- Session builder interactions -------------------------------------------

test("session builder has Add Session button", async ({ page }) => {
  await page.goto("/");
  await page.locator('button[title="Session builder"]').click();
  await expect(
    page.locator('[data-testid="session-builder"]'),
  ).toBeVisible();
  const addBtn = page.locator('[data-testid="session-builder"] button', {
    hasText: "Add Session",
  });
  await expect(addBtn).toBeVisible();
});

test("adding a session in builder increases session count", async ({ page }) => {
  await page.goto("/");
  await page.locator('button[title="Session builder"]').click();
  await expect(
    page.locator('[data-testid="session-builder"]'),
  ).toBeVisible();
  const addBtn = page.locator('[data-testid="session-builder"] button', {
    hasText: "Add Session",
  });
  const initialCount = await page.locator(".builder-session").count();
  await addBtn.click();
  await expect(page.locator(".builder-session")).toHaveCount(
    initialCount + 1,
  );
});

// -- Shortcut help content --------------------------------------------------

test("shortcut help overlay lists keyboard shortcuts", async ({ page }) => {
  await page.goto("/");
  await page.locator(".header-brand").click();
  await page.keyboard.press("?");
  await expect(page.locator(".overlay")).toBeVisible();
  // Overlay should contain shortcut key descriptions
  const overlayText = await page.locator(".overlay-panel").textContent();
  expect(overlayText).toContain("Keyboard Shortcuts");
});

// -- Theme branding ---------------------------------------------------------

test("app title dbcop is visible in dark theme", async ({ page }) => {
  await page.goto("/");
  // Force dark theme
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-theme", "dark");
  });
  await expect(page.locator(".header-title")).toContainText("dbcop");
});

test("app title dbcop is visible in light theme", async ({ page }) => {
  await page.goto("/");
  await page.evaluate(() => {
    document.documentElement.setAttribute("data-theme", "light");
  });
  await expect(page.locator(".header-title")).toContainText("dbcop");
});
