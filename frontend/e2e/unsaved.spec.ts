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

// The installation's own name, edited in the dialog the organization card's
// verb opens.
//
// Chosen over the account page's sign-off because THIS draft is the one that
// outlives its dialog: dismissing the dialog keeps what was typed, so an
// unsaved edit is still unsaved on the way out of the page — which is the state
// the guard exists to notice. A sign-off half-typed into a dialog nobody
// reopened is discarded by design, so it never reaches the guard at all.
const orgName = (page: Page) =>
  page.getByRole("textbox", { name: "Name der Organisation" });

const editOrgName = (page: Page) =>
  page.getByRole("button", { name: "Name der Organisation ändern" });

// Types into the dialog and closes it again, leaving the draft behind on a page
// with nothing open over it — which is what makes the navigation below possible.
const typeAndCloseDialog = async (page: Page, name: string) => {
  await page.goto("/#/settings/admin/general");
  await editOrgName(page).click();
  await orgName(page).fill(name);
  await page.keyboard.press("Escape");
  await expect(page.getByRole("dialog")).toBeHidden();
};

// Selected by destination rather than by name: this link's accessible name is
// the installation's own company, which a fixture is free to change, and what
// the test needs is only that it goes somewhere outside settings.
const leaveSettings = (page: Page) => page.locator('a[href="#/home"]').click();

test("a settings draft holds the page when the reader leaves for another screen", async ({
  page,
}) => {
  await typeAndCloseDialog(page, "Gradion Nord GmbH");

  // Out of settings entirely, which is the move the old guard could not see.
  // The brand link is the way out: while settings is open the rail holds the
  // settings entries, so leaving means the shell's own header.
  await leaveSettings(page);

  const asking = page.getByRole("dialog");
  await expect(asking).toBeVisible();

  // Dismissing the question is the SAFE answer: it keeps the edit and puts the
  // address back, so the reader is where their work is.
  await page.keyboard.press("Escape");
  await expect(asking).toBeHidden();
  await expect(page).toHaveURL(/#\/settings\/admin\/general$/);
  // The draft survived the question: reopening the dialog shows what was typed
  // rather than the value the server still holds.
  await editOrgName(page).click();
  await expect(orgName(page)).toHaveValue("Gradion Nord GmbH");
});

test("discarding leaves for the screen the reader asked for", async ({
  page,
}) => {
  await typeAndCloseDialog(page, "Ein halber Name");
  await leaveSettings(page);

  await page.getByRole("button", { name: /verwerfen/i }).click();
  await expect(page.getByRole("dialog")).toBeHidden();
  await expect(page).toHaveURL(/#\/home$/);
});
