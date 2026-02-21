import { expect, test } from "@playwright/test";

test("app loads with correct title", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await page.goto("/");

  await expect(page).toHaveTitle(/dbcop/);
  expect(errors).toHaveLength(0);
});
