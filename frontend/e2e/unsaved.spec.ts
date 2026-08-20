import { expect, test } from "@playwright/test";
import { mockApi } from "./seed";

/**
 * A draft survives the reader leaving the page it is on.
 *
 * Asserted end to end because the thing under test is where the guard SITS. It
 * is rendered above the routed screen (App.tsx), and the reason is only visible
 * across a whole navigation: a guard installed inside the settings screen
 * unmounted with that screen, so it caught a move from one settings entry to the
 * next and lost the draft without a word the moment the reader clicked anything
 * in the sidebar. A unit test of the guard cannot tell those two arrangements
 * apart — both hold content when the address changes — so the proof has to be a
 * real route change in a real shell.
 */

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

type Page = import("@playwright/test").Page;

const signature = (page: Page) =>
  page.getByRole("textbox", { name: /Grußformel/ });

// Selected by destination rather than by name: this link's accessible name is
// the installation's own company, which a fixture is free to change, and what
// the test needs is only that it goes somewhere outside settings.
const leaveSettings = (page: Page) => page.locator('a[href="#/home"]').click();

test("a settings draft holds the page when the reader leaves for another screen", async ({
  page,
}) => {
  await page.goto("/#/settings/account");
  await signature(page).fill("Mit freundlichen Grüßen, Lena");

  // Out of settings entirely, which is the move the old guard could not see.
  // The brand link is the way out: while settings is open the rail holds the
  // settings entries, so leaving means the shell's own header.
  await leaveSettings(page);

  const asking = page.getByRole("dialog");
  await expect(asking).toBeVisible();
  // The draft is STILL ON SCREEN. A guard that shows the next page and asks
  // afterwards has already taken the work away.
  await expect(signature(page)).toHaveValue("Mit freundlichen Grüßen, Lena");

  // Dismissing the question is the SAFE answer: it keeps the edit and puts the
  // address back, so the reader is where their work is.
  await page.keyboard.press("Escape");
  await expect(asking).toBeHidden();
  await expect(page).toHaveURL(/#\/settings\/account$/);
  await expect(signature(page)).toHaveValue("Mit freundlichen Grüßen, Lena");
});

test("discarding leaves for the screen the reader asked for", async ({
  page,
}) => {
  await page.goto("/#/settings/account");
  await signature(page).fill("Ein halber Satz");
  await leaveSettings(page);

  await page.getByRole("button", { name: /verwerfen/i }).click();
  await expect(page.getByRole("dialog")).toBeHidden();
  await expect(page).toHaveURL(/#\/home$/);
});
