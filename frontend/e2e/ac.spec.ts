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

test("AC-onboarding-1: onboarding is the rail-less conversational shell", async ({
  page,
}) => {
  // The onboarding wizard/stepper was replaced by the conversational shell
  // (#217): onboarding is a focused, rail-less flow whose journey is a
  // conversation thread, not a stepper.
  await page.goto("/#/onboarding");
  await expect(page.locator("nav.rail")).toHaveCount(0);
  await expect(
    page.getByRole("log", { name: "Einrichtungsgespräch" }),
  ).toBeVisible();
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

// B-EP09.23: the mock-overlay lane — proving the system-of-record mode swap
// end to end against `mockApi(page, { sor: "overlay" })` rather than a real
// HubSpot account. Each test re-seeds on top of the global (native)
// beforeEach — Playwright resolves the most-recently-registered route first,
// so the overlay routes take over for that test only.
test.describe("B-EP09.23: overlay mode", () => {
  test("AC-overlay-1: the mode chip marks an overlay installation (and is absent under the native seed)", async ({
    page,
  }) => {
    // The native seed (this file's global beforeEach) never renders it.
    await page.goto("/#/home");
    await expect(page.locator(".badge-accent")).toHaveCount(0);

    // Same route both times, so a plain goto would be a same-document hash
    // navigation the SPA never reloads for — reload forces the fresh /me
    // read that actually picks up the newly-registered overlay routes.
    await mockApi(page, { sor: "overlay" });
    await page.reload();
    const chip = page.getByRole("link", {
      name: "Diese Installation liest Datensätze aus einem HubSpot-Spiegel statt aus nativen Tabellen. Öffne Einstellungen → Overlay, um die Verbindung zu verwalten.",
    });
    await expect(chip).toBeVisible();
    await expect(chip).toHaveText("Liest aus HubSpot");
    await expect(chip).toHaveAttribute("href", "#/settings/overlay");
  });

  test("AC-overlay-2: the card shows connection, sync rows and budget band", async ({
    page,
  }) => {
    await mockApi(page, { sor: "overlay" });
    await page.goto("/#/settings/overlay");
    await expect(page.getByText("Verbunden", { exact: true })).toBeVisible();
    await expect(page.getByText(/eu1/)).toBeVisible();
    // Per-object sync rows: person + organization landed fresh; deal is still
    // catching up — three distinct rows, not a collapsed summary.
    await expect(page.getByText("person", { exact: true })).toBeVisible();
    await expect(page.getByText("organization", { exact: true })).toBeVisible();
    await expect(page.getByText("deal", { exact: true })).toBeVisible();
    await expect(page.getByText("Aktuell")).toHaveCount(2);
    await expect(page.getByText("Sync ausstehend")).toBeVisible();
    // "Gesund" bands twice: the REST budget window and the per-second Search
    // window, both seeded "ok".
    await expect(page.getByText("Gesund")).toHaveCount(2);
    // The server's own "can't attribute a share" sentinel prints verbatim —
    // never a computed substitute.
    await expect(page.getByText(/~unknown/)).toBeVisible();
  });

  test("AC-overlay-3: an ordinary edit succeeds in overlay mode — the mirror write-back seam accepts it", async ({
    page,
  }) => {
    // Update writes back through the incumbent seam and succeeds
    // (overlay/provider_writes.go) — so the deal 360's Edit affordance
    // renders in overlay too (deals.tsx's DealBadges) and this drives it for
    // real: click Edit, change the name, save, and see the 360 render the
    // saved value — the same click path AC-deal-* exercises in native mode.
    await mockApi(page, { sor: "overlay" });
    await page.goto("/#/deals/d-fleet");
    await page.getByTestId("edit-record").click();
    const name = page.getByLabel("Deal-Name *");
    // Wait for the modal's own prefill to land before typing over it — the
    // form seeds its fields from the fetched record on open, and typing
    // into it before that commits races the prefill, not the write-back
    // this test is about.
    await expect(name).toHaveValue("Fleet retrofit");
    await name.fill("Fleet retrofit — expanded scope");
    await page.getByRole("button", { name: "Speichern" }).click();
    await expect(
      page.getByText("Fleet retrofit — expanded scope"),
    ).toBeVisible();
  });

  test("AC-overlay-4: an unsupported verb explains itself rather than failing", async ({
    page,
  }) => {
    // Every refusable write affordance (advance/edit/merge/promote/
    // disqualify/create/log-activity) is deliberately HIDDEN once the SPA
    // knows it's in overlay mode — so there is no click path to a refused
    // write verb in a freshly-loaded overlay session; a naive "click it and
    // assert the copy" test is unwritable, and forcing one (or reading the
    // raw response body off a direct fetch) would only prove the mock
    // answers 422, not that the SPA does anything with it.
    //
    // The copy exists for exactly one real scenario: the stale-["me"]-cache
    // race. A screen mounts while the installation is still native (its
    // write affordances render, since the overlay gate reads the cached
    // ["me"].system_of_record.mode); another process then flips the
    // installation to overlay server-side. The SPA's own ["me"] read has a
    // 5-minute staleTime and nothing here triggers a refetch of it, so the
    // board still renders as native and the drag is still live — but the
    // request now lands on a server that refuses it. That's reproduced here:
    // load the board under the native seed (global beforeEach), THEN layer
    // the overlay mock on top with no intervening navigation/reload/
    // invalidate, so only the SERVER side (this mock's route table) has
    // flipped — the mounted screen's own state has not.
    await page.goto("/#/deals");
    await expect(page.getByText("Fleet retrofit")).toBeVisible();
    await mockApi(page, { sor: "overlay" });

    // d-fleet (stage s2, "Proposal") → s3 ("Negotiation"), both open-semantic
    // stages: an immediate advance, no confirm modal in the way (AC-deal-6
    // covers the terminal-stage confirm path separately).
    const card = page.locator('[data-deal="d-fleet"]');
    const target = page.locator('[data-stage="s3"]');
    await card.dragTo(target);

    // The board never refetched ["me"] — the Advance affordance was real,
    // still native as far as the SPA knew — but the request the server
    // actually received hit the (now overlay) mock's refused
    // POST /deals/{id}/advance, and the SPA renders the localized refusal
    // (overlay.refused), not the raw sentinel and not a generic failure.
    await expect(
      page.getByText(
        "Beim Lesen aus HubSpot nicht verfügbar — der Spiegel kann diesen Schreibvorgang nicht ausführen.",
      ),
    ).toBeVisible();
    // The deal never actually moved (the mutation errored, so nothing
    // invalidated the deals list) — the card is still in its origin column,
    // not silently accepted into a state the mirror never agreed to.
    await expect(
      page.locator('[data-stage="s2"] [data-deal="d-fleet"]'),
    ).toBeVisible();
  });

  test("overlay mode: an unsupported READ dial (list sort/filter) explains itself rather than failing", async ({
    page,
  }) => {
    // Distinct from AC-overlay-4 above: sort/filter is a refused READ dial
    // (unsupported_in_overlay_mode, compose/overlayread.go), not a refused
    // write verb — a different server code path with its own copy
    // (list.overlayReadOnly / t("overlay.filterUnsupported")), so it gets its
    // own test rather than being folded into "an unsupported verb". The list
    // toolbar never offers the controls in overlay (so the user never gets
    // to click one that can only fail); it explains the gap in place
    // instead. Search and the archived toggle are honestly still served, so
    // only the sort/filter half disappears.
    await mockApi(page, { sor: "overlay" });
    await page.goto("/#/contacts");
    await expect(
      page.getByText("Sortierung und Filter laufen über HubSpot"),
    ).toBeVisible();
    await expect(page.getByRole("combobox", { name: "Sortieren" })).toHaveCount(
      0,
    );
    await expect(page.getByRole("searchbox")).toBeVisible();
    await expect(
      page.getByRole("checkbox", { name: "Archivierte anzeigen" }),
    ).toBeVisible();
  });

  test("AC-overlay-5: sync now reports a queued sweep", async ({ page }) => {
    await mockApi(page, { sor: "overlay" });
    await page.goto("/#/settings/overlay");
    await page.getByRole("button", { name: "Jetzt synchronisieren" }).click();
    await expect(page.getByText(/Abgleich eingereiht/)).toBeVisible();
    // Distinct from the per-object "Backfill abgeschlossen" copy already on
    // this page — this is specifically checking the sweep itself never
    // claims to be finished.
    await expect(page.getByText("Abgleich abgeschlossen")).toHaveCount(0);
  });

  test("AC-overlay-6: disconnect names the purge and the app returns to native", async ({
    page,
  }) => {
    await mockApi(page, { sor: "overlay" });
    await page.goto("/#/settings/overlay");
    // The chip is the only accent badge that is a link; the mapping card on
    // this tab wears the same badge on the row for the signed-in user, so an
    // unqualified `.badge-accent` would be counting two different things.
    const chip = page.locator("a.badge-accent");
    await expect(chip).toBeVisible();
    await page.getByRole("button", { name: "Trennen" }).click();
    await expect(
      page.getByText(
        "Dies löscht die gespiegelten Daten und schaltet den Workspace zurück auf native Datensätze.",
        { exact: false },
      ),
    ).toBeVisible();
    // Two buttons now share the label (the card's own trigger, already
    // clicked, and the modal's confirm) — the modal's is the last in the DOM,
    // the same convention overlay.test.tsx's disconnect test uses.
    const confirms = page.getByRole("button", { name: "Trennen" });
    await confirms.last().click();
    // The whole cache is invalidated on success (/me included) — the chip
    // (driven purely off /me) disappears once the app re-reads native.
    await expect(chip).toHaveCount(0);
    // The connection row survives disconnect (revoked, never deleted —
    // backend/internal/modules/overlay/teardown.go's revokeConnection), so
    // the card's own re-read must show that, not vanish or revert to
    // "active": the revoked badge plus a working Reconnect affordance.
    await expect(page.getByText("Widerrufen")).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Erneut verbinden" }),
    ).toBeVisible();
  });

  test("AC-overlay-8: an admin unmaps a user and maps them back, and each write moves the card", async ({
    page,
  }) => {
    // The whole round trip, not just the request: the seed's mapping handlers
    // mutate their own state, so each assertion below is about what the write
    // DID. A mock answering a bare 200 would let this pass having changed
    // nothing, which is the one way a mapping workflow must not be able to
    // look correct.
    await mockApi(page, { sor: "overlay" });
    await page.goto("/#/settings/overlay");

    // Seeded state: the admin's own seat, matched to a HubSpot owner by email.
    await expect(page.getByText("Über E-Mail zugeordnet")).toBeVisible();

    await page.getByRole("button", { name: "Zuordnung aufheben" }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Zuordnung aufheben" })
      .click();

    // Unmapping is confirm-first and its own standing decision: the row now
    // reports the admin's block, and the email match it replaced is gone.
    await expect(page.getByText("Von Admin aufgehoben")).toBeVisible();
    await expect(page.getByText("Über E-Mail zugeordnet")).toHaveCount(0);

    await page.getByRole("button", { name: "Zuordnen…" }).click();
    await page.getByLabel("HubSpot-Nutzer suchen").fill("Lars");
    await page
      .getByRole("button", { name: "Lars Brandt · lars@brandt.example" })
      .click();

    // Mapping back is the admin's manual override, never a re-derived email
    // match — the card has to say which of the two it is.
    await expect(page.getByText("Manuell gesetzt")).toBeVisible();
    await expect(page.getByText("Von Admin aufgehoben")).toHaveCount(0);
  });

  test("AC-overlay-7: every 360 panel renders its unavailable state, never an error box", async ({
    page,
  }) => {
    await mockApi(page, { sor: "overlay" });
    const unavailable = "In der HubSpot-Ansicht nicht verfügbar";
    const errorBox = "Konnten diese Ansicht nicht laden.";

    // Person 360 (overview tab, the default): timeline, relationship
    // strength, and the related-records context panel each read a native
    // capability the mirror doesn't hold.
    await page.goto("/#/contacts/p-anna");
    await expect(
      page.getByRole("heading", { name: "Anna Weber" }),
    ).toBeVisible();
    await expect(page.getByText(unavailable)).toHaveCount(3);
    await expect(page.getByText(errorBox)).toHaveCount(0);

    // Deal 360: timeline, stakeholders, offers, and the context panel.
    await page.goto("/#/deals/d-fleet");
    await expect(
      page.getByRole("heading", { name: "Fleet retrofit" }),
    ).toBeVisible();
    await expect(page.getByText(unavailable)).toHaveCount(4);
    await expect(page.getByText(errorBox)).toHaveCount(0);

    // Tasks (nav.tasks): a defining `kind=task` filter the mirror can't honor.
    await page.goto("/#/tasks");
    await expect(page.getByText(unavailable)).toHaveCount(1);
    await expect(page.getByText(errorBox)).toHaveCount(0);
  });
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

// ---------------------------------------------------------------------------
// The unauthenticated surface (ADR-0076): §3.8 at two narrow widths, 200% zoom,
// the two-region structure, and axe.
//
// WHY IT NEEDS ITS OWN BLOCK: every sweep above walks CORE_SCREENS, and all of
// those start behind a session — so the one screen a signed-out user actually
// meets was never measured at any width. It is also the only screen the spec
// gives a SECOND region, which makes it the likeliest place a breakpoint
// regression lands and the least likely place anyone notices: nobody resizes the
// login page.
//
// These override the file-level beforeEach with an unauthenticated mock, so the
// app renders login rather than the shell.
// ---------------------------------------------------------------------------
test.describe("ADR-0076: the unauthenticated surface", () => {
  test.beforeEach(async ({ page }) => {
    await mockApi(page, { session: "unauthenticated" });
  });

  // Each entry is a viewport the spec names. 200% zoom is expressed as the CSS
  // viewport it actually produces — a 1280x800 window at 200% presents 640x400 —
  // because that is what the layout sees; deviceScaleFactor would change the
  // pixel ratio and nothing about the breakpoints.
  const NARROW = [
    { label: "390px mobile", width: 390, height: 844 },
    { label: "320px narrow", width: 320, height: 568 },
    { label: "200% zoom", width: 640, height: 400 },
  ] as const;

  for (const { label, width, height } of NARROW) {
    test.describe(label, () => {
      test.use({ viewport: { width, height } });

      test("no horizontal body scroll", async ({ page }) => {
        await page.goto("/");
        await expect(
          page.getByRole("heading", {
            level: 1,
            name: "Bei Margince anmelden",
          }),
        ).toBeVisible();
        const overflow = await page.evaluate(
          () =>
            document.documentElement.scrollWidth -
            document.documentElement.clientWidth,
        );
        expect(overflow).toBeLessThanOrEqual(0);
      });

      // The defect this pins is specific and was live: a fixed, overflow-hidden
      // full-screen surface cannot scroll, so at 320px or under a phone keyboard
      // the submit button is simply unreachable (§13.3).
      test("keeps the primary action reachable", async ({ page }) => {
        await page.goto("/");
        const submit = page.getByRole("button", { name: "Anmelden" });
        await submit.scrollIntoViewIfNeeded();
        await expect(submit).toBeVisible();
        const box = await submit.boundingBox();
        expect(box).not.toBeNull();
        // §6.5/§12: 44px is the target floor, not a rounded-up number.
        expect(Math.round(box?.height ?? 0)).toBeGreaterThanOrEqual(44);
      });

      // A region that has to be hidden to pass a breakpoint has failed the
      // breakpoint (ADR-0076 Decision 6). Both current implementations used to
      // drop two of the three limits with `display: none`, which is what this
      // catches — count AND rendered height, since a count alone passes on a
      // hidden node.
      test("hides no content to fit", async ({ page }) => {
        await page.goto("/");
        const limits = page.locator(".auth-limits li");
        await expect(limits).toHaveCount(3);
        for (let index = 0; index < 3; index += 1) {
          await expect(limits.nth(index)).toBeVisible();
        }
        // The identity region may shrink to the Core and its statement. It may
        // not be removed: the disclosure is the reason it exists.
        await expect(page.locator("aside.auth-identity")).toBeVisible();
      });
    });
  }

  // Stacked below 960px, and the task comes FIRST — visually as well as in the
  // DOM, because at this width there is no second column for `order` to move.
  test("stacks the task region above the identity region below 960px", async ({
    page,
  }) => {
    await page.setViewportSize({ width: 720, height: 900 });
    await page.goto("/");
    await expect(
      page.getByRole("heading", { level: 1, name: "Bei Margince anmelden" }),
    ).toBeVisible();
    const task = await page.locator("main.auth-task").boundingBox();
    const identity = await page.locator("aside.auth-identity").boundingBox();
    expect(task).not.toBeNull();
    expect(identity).not.toBeNull();
    expect(task?.y ?? 0).toBeLessThan(identity?.y ?? 0);
  });

  // §6.4 / §12: one h1, and it is the TASK. A surface whose h1 is the system
  // talking and whose h2 is "sign in" has inverted its own hierarchy — and the
  // identity region's statement is set large enough that promoting it to a
  // heading is a tempting mistake.
  test("has exactly one h1, and it is the task", async ({ page }) => {
    await page.goto("/");
    const headings = page.getByRole("heading", { level: 1 });
    await expect(headings).toHaveCount(1);
    await expect(headings).toHaveText("Bei Margince anmelden");
  });

  // The Core is decoration (WDS-CORE-4): every state it shows is also stated in
  // text by the surface around it, so it must not reach the a11y tree at all.
  test("keeps the Core out of the accessibility tree", async ({ page }) => {
    await page.goto("/");
    const core = page.locator("[data-core-state]");
    await expect(core).toHaveCount(1);
    await expect(core).toHaveAttribute("aria-hidden", "true");
  });

  // §19: no control whose flow does not exist. The mock ships
  // `oidc_providers: []`, which is the honest V1 answer.
  test("offers no identity provider that does not work", async ({ page }) => {
    await page.goto("/");
    await expect(page.getByText(/weiter mit|continue with/i)).toHaveCount(0);
  });

  test("no AA violations on the login screen", async ({ page }) => {
    await page.goto("/");
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
