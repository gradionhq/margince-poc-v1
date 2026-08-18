import { expect, test } from "@playwright/test";
import { mockApi } from "./seed";

/**
 * The lead surface end to end (ADR-0118/A169, ADR-0119/A170): the list names
 * the owner and opens the LEAD's own page (never the person's), the page can
 * be worked (a note logged against the lead), and promotion says what it will
 * do before it does it. German chrome, as the app renders it.
 */

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

test("AC-leads-list: a row names its owner and opens the lead's own page", async ({
  page,
}) => {
  await page.goto("/#/leads");
  const row = page.getByRole("row", { name: /Jonas Petersen/ });
  await expect(row).toBeVisible();
  // The owner column answers "whose lead is this" — the same column the
  // people and company lists carry, never "typed by a person".
  await expect(row).toContainText("Lena Fischer");
  // Exactly the name cell: since rows became selectable, every row also holds
  // an sr-only "<name> auswählen" label for its checkbox, and a substring
  // match resolves to both.
  await row.getByText("Jonas Petersen", { exact: true }).click();
  await expect(page).toHaveURL(/#\/leads\/l-1$/);
  await expect(
    page.getByRole("heading", { level: 1, name: "Jonas Petersen" }),
  ).toBeVisible();
});

test("AC-leaddetail-work: a note is logged against the lead itself", async ({
  page,
}) => {
  const posted = page.waitForRequest(
    (request) =>
      request.url().endsWith("/v1/activities") && request.method() === "POST",
  );
  await page.goto("/#/leads/l-1");
  // The composer is inline on the lead page (ADR-0118/A169), not behind a
  // button: working the lead is the page's job.
  await page.getByLabel("Betreff *").fill("Rückruf vereinbart");
  await page.getByRole("button", { name: "Erfassen" }).click();
  const request = await posted;
  const body = request.postDataJSON();
  expect(body.subject).toBe("Rückruf vereinbart");
  // The link is the LEAD, not a person: this is what activity_link's lead arm
  // (migration 0038) exists for.
  expect(body.links).toEqual([{ entity_type: "lead", entity_id: "l-1" }]);
});

test("AC-leaddetail-promote: the confirm step says what promotion will do, then lands on the contact", async ({
  page,
}) => {
  await page.goto("/#/leads/l-1");
  await page.getByRole("button", { name: "Zum Kontakt machen" }).click();
  await expect(
    page.getByText("Die Übernahme legt einen neuen Kontakt an."),
  ).toBeVisible();
  await page.getByRole("button", { name: "Übernehmen" }).click();
  await expect(page).toHaveURL(/#\/contacts\/p-new$/);
});
