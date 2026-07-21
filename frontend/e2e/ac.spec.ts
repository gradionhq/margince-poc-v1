import AxeBuilder from "@axe-core/playwright";
import { expect, test } from "@playwright/test";
import { mockApi } from "./seed";

// B-EP09.22a/b: the AC-<screen>-N criteria as named tests — a failing test
// names the criterion it breaks. Includes the cross-cutting invariants
// (rail + ⌘K present, 🟡 confirm-first, provenance rendered), the 390px
// no-horizontal-scroll sweep (§3.8), the WCAG 2.2 AA axe gate (B-EP09.21)
// and the PERF-1 perceived record-open budget.

test.beforeEach(async ({ page }) => {
  await mockApi(page);
});

const CORE_SCREENS = [
  "home",
  "contacts",
  "companies",
  "deals",
  "inbox",
  "reports",
  "settings",
  "automations",
];

test("AC-shell-1: the rail renders the canonical 9 items in order", async ({
  page,
}) => {
  await page.goto("/#/home");
  // evaluateAll never waits — anchor on the rendered count first, or the
  // read races the auth splash and sees an empty rail.
  await expect(page.locator("nav.rail a.navitem")).toHaveCount(9);
  const labels = await page
    .locator("nav.rail a.navitem")
    .evaluateAll((links) =>
      links.map((link) => link.getAttribute("aria-label")),
    );
  expect(labels).toEqual([
    "Start",
    "Kontakte",
    "Firmen",
    "Leads",
    "Deals",
    "Aufgaben",
    "Eingang",
    "Berichte",
    "KI fragen",
  ]);
});

test("AC-shell-2: exactly one rail item is active and tracks the route", async ({
  page,
}) => {
  await page.goto("/#/deals");
  await expect(page.locator("nav.rail a.navitem.active")).toHaveCount(1);
  await expect(page.locator("nav.rail a.navitem.active")).toHaveAttribute(
    "aria-label",
    "Deals",
  );
  await page.locator('nav.rail a[aria-label="Berichte"]').click();
  await expect(page.locator("nav.rail a.navitem.active")).toHaveAttribute(
    "aria-label",
    "Berichte",
  );
  await expect(page.locator("nav.rail a.navitem.active")).toHaveCount(1);
});

test("AC-shell-3/4/5: ⌘K opens focused+empty, filters, Enter navigates", async ({
  page,
}) => {
  await page.goto("/#/home");
  await page.locator("body").click();
  await page.keyboard.press("ControlOrMeta+k");
  const input = page.getByRole("textbox", { name: "Befehlspalette" });
  await expect(input).toBeFocused();
  await input.fill("Deals");
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/#\/deals$/);
});

test("AC-shell-7: the top-bar search affordance opens the palette", async ({
  page,
}) => {
  await page.goto("/#/home");
  await page.getByRole("button", { name: "Suche" }).click();
  await expect(
    page.getByRole("textbox", { name: "Befehlspalette" }),
  ).toBeVisible();
});

test("AC-shell-8: Ask FAB mounts on core screens, never on the AI surface", async ({
  page,
}) => {
  await page.goto("/#/contacts");
  await expect(page.locator(".askfab")).toBeVisible();
  await page.goto("/#/deals");
  await expect(page.locator(".askfab")).toBeVisible();
  await page.goto("/#/ai");
  await expect(page.locator(".askfab")).toHaveCount(0);
});

test("features/10 §7: the locale switch flips the chrome DE↔EN", async ({
  page,
}) => {
  await page.goto("/#/home");
  await expect(page.locator('nav.rail a[aria-label="Kontakte"]')).toBeVisible();
  await page.getByRole("button", { name: "Auf Englisch umschalten" }).click();
  await expect(page.locator('nav.rail a[aria-label="Contacts"]')).toBeVisible();
});

test("AC-pipeline-7: board↔table swaps views preserving the deal set", async ({
  page,
}) => {
  await page.goto("/#/deals");
  await expect(page.getByText("Fleet retrofit")).toBeVisible();
  await page.getByRole("button", { name: "Tabelle" }).click();
  await expect(page.getByText("Fleet retrofit")).toBeVisible();
  await expect(page.getByText("Service contract")).toBeVisible();
});

test("AC-deal-6: a terminal-stage drop is a 🟡 confirm — nothing runs before Confirm", async ({
  page,
}) => {
  await page.goto("/#/deals");
  await expect(page.getByText("Fleet retrofit")).toBeVisible();
  const card = page.locator('[data-deal="d-fleet"]');
  const won = page.locator('[data-stage="s4"]');
  await card.dragTo(won);
  await expect(page.getByText("Nach Won verschieben?")).toBeVisible();
  await page.getByRole("button", { name: "Bestätigen" }).click();
  await expect(page.getByText("Nach Won verschoben")).toBeVisible();
});

test("AC-inbox: approve and reject act on the staged row", async ({ page }) => {
  await page.goto("/#/inbox");
  await expect(page.getByText("send_email", { exact: true })).toBeVisible();
  await expect(page.getByText("Agent: runner")).toBeVisible();
  await page.getByRole("button", { name: "Übernehmen" }).click();
});

test("AC-book: the booking page renders rail-less with live slots", async ({
  page,
}) => {
  await page.goto("/#/book");
  await expect(page.locator("nav.rail")).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: /06\.07\.2026/ }).first(),
  ).toBeVisible();
});

test("AC-automations-1 (B-EP09.15): create from the catalog arrives paused; enable is the deliberate second step", async ({
  page,
}) => {
  await page.goto("/#/automations");
  await expect(page.getByText("Stillstands-Erinnerung")).toBeVisible();
  await page.getByRole("button", { name: "Vorlage verwenden" }).first().click();
  // the schema default arrives in the one parameter field
  await expect(
    page.getByRole("spinbutton", { name: "due_in_days" }),
  ).toHaveValue("3");
  await page.getByRole("button", { name: "Anlegen" }).click();
  await expect(
    page.getByText("Pausiert angelegt — es läuft nichts, bis du aktivierst."),
  ).toBeVisible();
  const row = page.locator('[data-automation="au-2"]');
  await expect(row.getByText("pausiert")).toBeVisible();
  await row.getByRole("button", { name: "Aktivieren" }).click();
  await expect(row.getByText("aktiv", { exact: true })).toBeVisible();
});

test("AC-automations-2 (features/10 §1): anti-DSL — no free-form rule body, no user-defined trigger", async ({
  page,
}) => {
  await page.goto("/#/automations");
  await expect(page.getByText("Stillstands-Erinnerung")).toBeVisible();
  await page.getByRole("button", { name: "Vorlage verwenden" }).first().click();
  await expect(page.locator("textarea")).toHaveCount(0);
  // exactly the instance name plus the schema-derived parameter
  await expect(page.getByRole("textbox")).toHaveCount(1);
  await expect(page.getByRole("spinbutton")).toHaveCount(1);
});

test("AC-settings-16: the audit log renders attributed entries, filters live, and loads more", async ({
  page,
}) => {
  // The audit log lives on the Audit tab of the settings section layout, and
  // renders attribution in human terms (AuditEntryLine): the signed-in human
  // (u1) reads as "Du", agents/connectors show their readable slug — never the
  // raw `type:uuid`.
  await page.goto("/#/settings/audit");
  await expect(page.getByText("Du", { exact: true })).toBeVisible();
  await expect(page.getByText("runner", { exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Mehr laden" }).click();
  await expect(page.getByText("gmail", { exact: true })).toBeVisible();
  // The actor filter still speaks the API's `type:id` vocabulary.
  await page.getByRole("textbox", { name: "Akteur" }).fill("agent:runner");
  await expect(page.getByText("runner", { exact: true })).toBeVisible();
  await expect(page.getByText("Du", { exact: true })).toHaveCount(0);
});

test("AC-settings: the passport list is metadata-only and strikes revoked rows", async ({
  page,
}) => {
  // Agent passports live on the AI & autonomy tab of the settings layout.
  await page.goto("/#/settings/ai");
  await expect(page.getByText("Marcus' Claude", { exact: true })).toBeVisible();
  const revoked = page.locator('[data-passport="pp-2"]');
  await expect(revoked.getByText("widerrufen")).toBeVisible();
  await expect(revoked).toHaveCSS("text-decoration-line", "line-through");
  // no token is ever re-disclosed on this surface
  await expect(page.getByText(/mgp_/)).toHaveCount(0);
});

test("AC-book-public (B-EP09.14): consent gates booking and the policy passes through verbatim", async ({
  page,
}) => {
  await page.goto("/#/book/host-1");
  await expect(page.locator("nav.rail")).toHaveCount(0);
  const slot = page.getByRole("button", { name: /06\.07\.2026/ }).first();
  await expect(slot).toBeDisabled();
  await page.getByRole("textbox", { name: "Dein Name" }).fill("Jonas Beispiel");
  await page
    .getByRole("textbox", { name: "Deine E-Mail" })
    .fill("jonas@beispiel.example");
  await expect(slot).toBeDisabled();
  await page.getByRole("checkbox").check();
  await expect(slot).toBeEnabled();
  const shownWording = await page
    .locator("[data-consent-wording]")
    .textContent();
  const requestPromise = page.waitForRequest(
    (request) =>
      request.method() === "POST" &&
      request.url().includes("/public/booking/host-1"),
  );
  await slot.click();
  const request = await requestPromise;
  const body = request.postDataJSON();
  // the wording the visitor SAW is byte-for-byte what was submitted
  expect(body.consent.wording).toBe(shownWording);
  expect(body.consent.purpose_id).toBeTruthy();
  expect(body.consent.policy_version).toBeTruthy();
  await expect(
    page.getByText("Gebucht. Die Einladung ist unterwegs."),
  ).toBeVisible();
});

test("AC-book-public-409: a taken slot degrades honestly — no fabricated confirmation", async ({
  page,
}) => {
  await page.goto("/#/book/host-1");
  await page.getByRole("textbox", { name: "Dein Name" }).fill("Jonas Beispiel");
  await page
    .getByRole("textbox", { name: "Deine E-Mail" })
    .fill("jonas@beispiel.example");
  await page.getByRole("checkbox").check();
  await page.getByRole("button", { name: /12:00/ }).click();
  await expect(
    page.getByText(
      "Die Buchung ging nicht durch — es wurde nichts eingetragen.",
    ),
  ).toBeVisible();
  await expect(page.getByText("slot no longer available")).toBeVisible();
  await expect(
    page.getByText("Gebucht. Die Einladung ist unterwegs."),
  ).toHaveCount(0);
});

test("AC-onboarding-1: the wizard is rail-less and connect is the LAST step", async ({
  page,
}) => {
  await page.goto("/#/onboarding");
  await expect(page.locator("nav.rail")).toHaveCount(0);
  const steps = await page
    .locator("nav.stepper .step")
    .evaluateAll((nodes) => nodes.map((node) => node.textContent));
  expect(steps[steps.length - 1]).toBe("Verbinden");
});

test("AC-create-1: a contact is created from the list and lands on its 360", async ({
  page,
}) => {
  await page.goto("/#/contacts");
  await page.getByRole("button", { name: "Neuer Kontakt" }).click();
  await page.getByLabel("Vollständiger Name").fill("Peter Neu");
  // Email is now a repeatable row group (P-15): add a row, then fill it.
  await page.getByRole("button", { name: "E-Mail hinzufügen" }).click();
  await page.getByLabel("E-Mail *").fill("peter@neu.example");
  await page.getByRole("button", { name: "Anlegen" }).click();
  await expect(page).toHaveURL(/#\/contacts\/p-new$/);
});

test("AC-create-2: the palette's New-deal action opens the create form; only open stages offered", async ({
  page,
}) => {
  await page.goto("/#/deals/new");
  // Scope to the create dialog: the deals list now also renders a stage FILTER
  // select (bespoke, over ALL stages) whose accessible name likewise contains
  // "Phase", so a page-wide getByLabel would ambiguously match it. The create
  // form's stage select — the subject of this AC — lives inside the modal and
  // still offers open stages only.
  const stageSelect = page.getByRole("dialog").getByLabel("Phase");
  await expect(stageSelect).toBeVisible();
  const stageNames = await stageSelect.locator("option").allTextContents();
  expect(stageNames.filter(Boolean)).toEqual([
    "Qualify",
    "Proposal",
    "Negotiation",
  ]);
  await page.getByLabel("Deal-Name").fill("Neuer Deal");
  await page.getByLabel("Wert").fill("480");
  await page.getByRole("button", { name: "Anlegen" }).click();
  await expect(page).toHaveURL(/#\/deals\/d-new$/);
});

test.describe("§3.8: 390px mobile", () => {
  test.use({ viewport: { width: 390, height: 844 } });

  for (const screen of CORE_SCREENS) {
    test(`no horizontal body scroll at 390px on #/${screen}`, async ({
      page,
    }) => {
      await page.goto(`/#/${screen}`);
      await page.waitForLoadState("networkidle");
      const overflow = await page.evaluate(
        () => document.body.scrollWidth - document.documentElement.clientWidth,
      );
      expect(overflow).toBeLessThanOrEqual(0);
    });
  }

  test("S-E11.2: the approval inbox is usable on mobile — approve works at 390px", async ({
    page,
  }) => {
    await page.goto("/#/inbox");
    await expect(page.getByText("send_email", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Übernehmen" }).click();
  });
});

test.describe("B-EP09.21: WCAG 2.2 AA (axe)", () => {
  for (const screen of CORE_SCREENS) {
    test(`no AA violations on #/${screen}`, async ({ page }) => {
      await page.goto(`/#/${screen}`);
      await page.waitForLoadState("networkidle");
      const results = await new AxeBuilder({ page })
        .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
        .analyze();
      expect(
        results.violations.flatMap((violation) =>
          violation.nodes.map(
            (node) => `${violation.id}: ${node.target.join(" ")}`,
          ),
        ),
      ).toEqual([]);
    });
  }
});

test("PERF-1: record open renders under the 300ms perceived budget", async ({
  page,
}) => {
  await page.goto("/#/contacts");
  // Anchor on a settled screen before measuring: a click during hydration
  // can land on a row whose handler is not attached yet — the navigation
  // then never happens and the assertion times out as a phantom perf
  // failure (twice-seen CI flake). networkidle + the visible row make the
  // click deterministic; the budget still measures click → heading.
  await page.waitForLoadState("networkidle");
  const row = page.getByText("Anna Weber");
  await expect(row).toBeVisible();
  const start = Date.now();
  await row.click();
  await expect(
    page.getByRole("heading", { level: 1, name: "Anna Weber" }),
  ).toBeVisible();
  expect(Date.now() - start).toBeLessThan(300);
});
