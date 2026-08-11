import { expect, test } from "@playwright/test";
import { mockApi } from "./seed";

test("debug overlay person crash", async ({ page }) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(`${e.message} :: ${(e.stack ?? "").split("\n")[1] ?? ""}`));
  const failed: string[] = [];
  page.on("requestfailed", (r) => failed.push(r.url()));
  await mockApi(page, { sor: "overlay" });
  await page.goto("/#/contacts/p-anna");
  await page.waitForTimeout(2500);
  console.log("=== pageerrors: " + JSON.stringify(errors, null, 1));
  console.log("=== requestfailed: " + JSON.stringify(failed.slice(0, 8), null, 1));
  console.log("=== body: " + (await page.locator("body").innerText()).slice(0, 300));
  expect(true).toBe(true);
});
