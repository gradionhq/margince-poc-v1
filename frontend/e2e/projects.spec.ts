import { expect, test } from "@playwright/test";
import { mockApi } from "./seed";

/**
 * A project from birth to close, the way a rep works it: created from the
 * Projects list, attached to a deal on the deal's own form, moved into
 * delivery by the deal's win rather than by hand, accumulating the timeline
 * rows filed under it, advanced by hand, and closed with the reason the
 * close demands. German chrome, as the app renders it (playwright.config.ts
 * pins de-DE).
 *
 * The mock remembers every write and enforces If-Match (e2e/projectmock.ts),
 * so each step below reads back the state the previous step wrote.
 */

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

async function choose(
  page: import("@playwright/test").Page,
  combobox: import("@playwright/test").Locator,
  option: string | RegExp,
) {
  await combobox.click();
  await page.getByRole("option", { name: option }).click();
}

test("a project is created, a deal is attached, the win starts delivery, the timeline fills, and the close records its reason", async ({
  page,
}) => {
  // 1. Create. The list carries the seeded project; the new one joins it.
  await page.goto("/#/projects");
  await expect(
    page.getByRole("link", { name: /^Flottenumbau Brandt/ }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Neues Projekt" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("textbox", { name: "Projektname" }).fill("Brandt ERP");
  await dialog.getByRole("textbox", { name: "Kürzel" }).fill("BRANDT-ERP");
  await choose(
    page,
    dialog.getByRole("combobox", { name: "Unternehmen" }),
    "Brandt Automotive GmbH",
  );
  await dialog.getByRole("button", { name: "Anlegen" }).click();

  // The create lands on the project's own page, born in Initiative with the
  // birth row in its history and nothing filed under it yet.
  await expect(page).toHaveURL(/#\/projects\/pr-new-1$/);
  await expect(
    page.getByRole("heading", { level: 1, name: "Brandt ERP" }),
  ).toBeVisible();
  const phase = page.getByRole("group", { name: "Phase" });
  // The current phase is the one step rendered as text, not as a button.
  const current = phase.locator('[aria-current="step"]');
  await expect(current).toHaveText("Initiative");
  await expect(page.getByText("Gestartet in Initiative")).toBeVisible();
  await expect(
    page.getByText("Unter diesem Projekt ist noch nichts abgelegt", {
      exact: false,
    }),
  ).toBeVisible();

  // 2. Attach a deal on the deal's own form — the project picker offers the
  // company's live projects by name.
  await page.goto("/#/deals/d-fleet");
  await page.getByRole("button", { name: "Deal bearbeiten" }).click();
  await choose(
    page,
    dialog.getByRole("combobox", { name: "Projekt" }),
    "Brandt ERP",
  );
  const patched = page.waitForRequest(
    (request) =>
      request.url().endsWith("/v1/deals/d-fleet") &&
      request.method() === "PATCH",
  );
  await dialog.getByRole("button", { name: "Speichern" }).click();
  expect((await patched).postDataJSON().project_id).toBe("pr-new-1");
  // The deal header now carries the project as a chip linking to its page.
  await expect(page.getByTestId("deal-project")).toHaveText("Brandt ERP");

  // 3. Log something on the deal, then win it. The note is filed under the
  // deal, not the project — the project timeline picks it up in step 5.
  await page.getByLabel(/^Betreff/).fill("Kickoff mit Brandt IT");
  await page.getByRole("button", { name: "Erfassen" }).click();
  const timeline = page.getByRole("region", { name: "Verlauf" });
  await expect(timeline.getByText("Kickoff mit Brandt IT")).toBeVisible();

  await page
    .getByRole("group", { name: "Phase" })
    .getByRole("button", { name: "Won" })
    .click();
  await expect(page.getByText("Nach Won verschieben?")).toBeVisible();
  const won = page.waitForRequest(
    (request) =>
      request.url().endsWith("/v1/deals/d-fleet/advance") &&
      request.method() === "POST",
  );
  await page.getByRole("button", { name: "Bestätigen" }).click();
  await won;

  // 4. The win moved the project into delivery — nobody pressed a phase.
  await page.getByTestId("deal-project").click();
  await expect(page).toHaveURL(/#\/projects\/pr-new-1$/);
  await expect(current).toHaveText("In Umsetzung");
  await expect(page.getByText("Initiative → In Umsetzung")).toBeVisible();
  // And the deal is listed on the project with its won status.
  await expect(
    page.getByRole("button", { name: "Fleet retrofit" }),
  ).toBeVisible();

  // 5. The timeline accumulates what is filed under the project: relink the
  // deal's note to the project and it appears here with the coverage count.
  await page.goto("/#/deals/d-fleet");
  await timeline.getByRole("button", { name: "Neu verknüpfen" }).click();
  await dialog
    .getByRole("searchbox", {
      name: "Person, Organisation, Deal oder Lead suchen",
    })
    .fill("Brandt ERP");
  await dialog.getByRole("button", { name: "Brandt ERP" }).click();
  await dialog.getByRole("button", { name: "Neu verknüpfen" }).click();
  await expect(dialog).toBeHidden();

  // A hash change keeps the document, and with it the 360 read cached a
  // moment ago; reload so the page reads the relinked state afresh.
  await page.goto("/#/projects/pr-new-1");
  await page.reload();
  await expect(timeline.getByText("Kickoff mit Brandt IT")).toBeVisible();
  await expect(page.getByText(/^1 zugeordnet ·/)).toBeVisible();

  // 6. Advance by hand: a non-closing move takes an optional reason and
  // lands in the history with it.
  await phase.getByRole("button", { name: "Im Vertrieb" }).click();
  await expect(
    page.getByRole("heading", { name: "Wechsel zu Im Vertrieb" }),
  ).toBeVisible();
  await page
    .getByTestId("project-advance-reason")
    .fill("Erweiterung wird neu verhandelt.");
  await page.getByRole("button", { name: "Wechseln" }).click();
  await expect(current).toHaveText("Im Vertrieb");
  await expect(page.getByText("In Umsetzung → Im Vertrieb")).toBeVisible();
  await expect(
    page.getByText("Erweiterung wird neu verhandelt.", { exact: false }),
  ).toBeVisible();

  // 7. Close with a reason. The close button stays disabled until a reason
  // is typed, and the reason is what the history shows afterwards.
  await phase.getByRole("button", { name: "Abgeschlossen" }).click();
  await expect(
    page.getByRole("heading", { name: "Wechsel zu Abgeschlossen" }),
  ).toBeVisible();
  const close = page.getByRole("button", { name: "Projekt abschließen" });
  await expect(close).toBeDisabled();
  await page
    .getByTestId("project-advance-reason")
    .fill("Go-live abgenommen, Hypercare an den Support übergeben.");
  const closed = page.waitForRequest(
    (request) =>
      request.url().endsWith("/v1/projects/pr-new-1/advance") &&
      request.method() === "POST",
  );
  await close.click();
  const body = (await closed).postDataJSON();
  expect(body.to_phase).toBe("closed");
  expect(body.reason).toBe(
    "Go-live abgenommen, Hypercare an den Support übergeben.",
  );
  await expect(current).toHaveText("Abgeschlossen");
  await expect(page.getByText("Im Vertrieb → Abgeschlossen")).toBeVisible();
  await expect(
    page.getByText("Go-live abgenommen, Hypercare an den Support übergeben.", {
      exact: false,
    }),
  ).toBeVisible();
});
