import { expect, type Locator, type Page, test } from "@playwright/test";

/**
 * The company record page against the mockups in
 * docs/explanation/assets/company-record-page-v2/.
 *
 * This suite exists because "it looks like the mockup" kept being reported from
 * reading the code rather than the page. TWO describes, and the split matters:
 *
 *   - page shape — which regions exist and in what order;
 *   - visual weight — how big and how prominent they are.
 *
 * Shape alone is not the requirement, and asserting only shape is how a page
 * with the right skeleton at half the mockup's scale passed every check while
 * looking nothing like it. The second describe reads computed styles.
 *
 * Never pixel-equality and never the drawn English strings: the app renders
 * German chrome, so a copy change must not fail a layout suite, and a font
 * substitution must not fail a scale one. The visual assertions are FLOORS —
 * they say "at least this prominent", which is the claim the mockup makes.
 *
 * It runs against a LIVE stack (BASE_URL + a real company id), because the two
 * states that have to look right — a populated account and a freshly imported
 * one — are data states, not mock fixtures. Without BASE_URL the whole suite
 * skips itself loudly rather than passing on an app it never loaded.
 */

const BASE_URL = process.env.BASE_URL;
// The two cases from the plan: an account with deals, activities and a
// dossier, and one that arrived by import with nothing on it yet. Both must
// look right, and they fail differently — the populated one by missing
// regions, the sparse one by showing regions that should be absent.
const POPULATED_ORG = process.env.E2E_ORG_POPULATED;
const SPARSE_ORG = process.env.E2E_ORG_SPARSE;

// A live run is opt-in, but a HALF-configured one is a mistake rather than a
// choice: skipping silently there is exactly the failure this suite was built
// to stop, so it fails instead and says which variable is missing.
if (BASE_URL && !(POPULATED_ORG && SPARSE_ORG)) {
  throw new Error(
    "BASE_URL is set, so this suite runs live — it also needs E2E_ORG_POPULATED and E2E_ORG_SPARSE (company uuids on that stack).",
  );
}

test.skip(
  !BASE_URL,
  "company-record runs against a live stack: set BASE_URL, E2E_ORG_POPULATED and E2E_ORG_SPARSE (see make e2e-company).",
);

const SHOTS = process.env.E2E_SHOT_DIR ?? "/tmp/e2e-company";

// The readings row, by the test id StateStrip hands its StatStrip. Not by class:
// the plate is the shared primitive's (design-system/statstrip.tsx), so a class
// selector here would either name the primitive — and match the person record's
// row too — or name a screen class that only exists to be selected. The test id
// names THIS row on THIS page, which is what these assertions are about.
const STRIP = '[data-testid="company-strip"]';

/**
 * Sign in as the dev bootstrap admin, unless the session is already up.
 *
 * A dev stack that has been logged into already redirects /#/login straight to
 * the app, so the form is ABSENT on the happy path — waiting for a navigation
 * that never happens is a 30s timeout, not a login failure. The form is filled
 * only when it is actually rendered.
 */
async function signIn(page: Page) {
  await page.goto("/#/login", { waitUntil: "networkidle" });
  const email = page
    .locator('input[type="email"], input[name="email"]')
    .first();
  if ((await email.count()) === 0) {
    return;
  }
  await email.fill(process.env.E2E_EMAIL ?? "admin@demo.test");
  await page
    .locator('input[type="password"], input[name="password"]')
    .first()
    .fill(process.env.E2E_PASSWORD ?? "demo-password-123");
  await page.locator('button[type="submit"]').first().click();
  // The nav is what "signed in" looks like, and it is what every assertion
  // below depends on — anchoring here rather than on a URL change means the
  // wait describes the state the tests need.
  await expect(page.locator("nav.rail").first()).toBeVisible();
}

/**
 * Open a company and wait for the 360 to settle.
 *
 * The page is a composite of independent reads, so `networkidle` alone can land
 * while cards are still empty. Anchoring on the heading means the assertions
 * below describe a rendered page rather than a racing one.
 */
async function openCompany(page: Page, orgId: string) {
  await page.goto(`/#/companies/${orgId}`, { waitUntil: "networkidle" });
  // The RECORD's own identity block. On a record route the shell's page head
  // shows only the trail back and no heading, and that trail renders from the
  // router before any company read returns — so anchoring there would say the
  // page is settled while every card below it is still empty.
  await expect(page.locator(".record-head h1")).toBeVisible();
}

/** The vertical position of a region, for order assertions. */
async function topOf(locator: Locator): Promise<number> {
  await expect(locator).toBeVisible();
  const box = await locator.boundingBox();
  if (!box) {
    throw new Error("region is visible but has no box — cannot order it");
  }
  return box.y;
}

test.describe("company record — the mockup's page shape", () => {
  test.beforeEach(async ({ page }) => {
    await signIn(page);
  });

  // State D and State A both put the money strip directly under the header and
  // the tab strip below it. Today the page renders the tabs first, so this is
  // the assertion that fails until the topology is fixed.
  test("the KPI strip sits above the tab strip", async ({ page }) => {
    await openCompany(page, POPULATED_ORG as string);
    expect(await topOf(page.locator(STRIP))).toBeLessThan(
      await topOf(page.locator(".co-tabs")),
    );
  });

  // The strip carries the account's STANDING state — what is true until
  // something changes it. What is true TODAY is the daily brief's, so the
  // whose-move and worth-knowing readings are not here to be counted.
  //
  // FIVE slots on every account, whatever the fixture holds: the lifecycle
  // decides which fifth reading it is (a close date or the money) and never how
  // many there are, and a slot with no reading says so rather than vanishing.
  // The count is asserted because StatStrip derives the row's fold from it — a
  // row that sometimes carried six would fold at a different width on two
  // accounts of the same product.
  //
  // Then the row must READ as one row — the regression this strip actually had
  // was two slots drawn at a different font size than the rest — so every slot
  // is checked for a label AND a value, and every value against the SAME
  // computed size. A row of readings is a scale the eye sweeps; a slot that
  // sizes itself to its own content breaks the sweep.
  test("every KPI slot carries a label and a value, all at one size", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    const slots = page.locator(`${STRIP} > *`);
    // Waited for, not counted straight away: a bare count() resolves against
    // whatever is mounted at that instant, which on a composite read is an
    // empty strip. The floor is the stage and relationship readings, which no
    // account lacks.
    await expect(slots.nth(1)).toBeVisible();
    await expect(slots).toHaveCount(5);
    const count = await slots.count();
    const sizes = new Set<string>();
    for (let index = 0; index < count; index++) {
      const slot = slots.nth(index);
      await expect(slot.locator(".stat-card-label")).not.toBeEmpty();
      await expect(slot.locator(".stat-card-value")).not.toBeEmpty();
      sizes.add(
        await slot
          .locator(".stat-card-value")
          .evaluate((element) => getComputedStyle(element).fontSize),
      );
    }
    expect(sizes.size).toBe(1);
  });

  // Overview · People · Deals · Tasks · History · Documents · Profile, plus
  // Partner for an account that has a partner programme (companyTabsFor in
  // organizations.tsx gates it on relationship_types, not on a fixed count) —
  // so the expectation follows the fixture's own data rather than assuming
  // either shape.
  test("the tab strip offers every tab, plus partner only where the account has one", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    const isPartnerAccount = await page.evaluate(async () => {
      const id = location.hash.split("/").pop();
      const response = await fetch(`/v1/organizations/${id}`, {
        headers: { accept: "application/json" },
      });
      const org = await response.json();
      return Boolean(org?.relationship_types?.includes("partner"));
    });
    await expect(page.locator(".co-tabs button")).toHaveCount(
      isPartnerAccount ? 8 : 7,
    );
  });

  // "Today on this account" and "Worth doing next" merged into one daily
  // brief (companytoday.tsx), which LEADS the overview column. The account
  // brief ("The account, in short") follows it in the same column.
  //
  // Anchored on the rendered HEADING rather than a class: NextSteps draws a
  // bare `.card.co-card` with nothing to name it by, so a class assertion here
  // would pass against any page — including one that still renders it.
  // The strings are the German chrome the suite pins via `locale: de-DE`.
  test("the overview renders the account brief, but not next steps or the ask panel", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    await expect(
      page.getByRole("heading", { name: "Der Account, kurz gefasst" }),
    ).toHaveCount(1);
    for (const heading of ["Nächste Schritte", "Diesen Account befragen"]) {
      await expect(page.getByRole("heading", { name: heading })).toHaveCount(0);
    }
  });

  // The brief asks for a move on the account, so it belongs to the tab a
  // reader opens to be told what to do — not to every tab. Someone who has
  // gone to People or Documents has already chosen what to read, and the
  // readings above the tab bar are what stays with them there.
  test("the daily brief leads the overview, and is not drawn on the other tabs", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    const brief = page.getByRole("heading", {
      name: "Heute bei diesem Account",
    });
    await expect(brief).toBeVisible();

    await page.getByRole("button", { name: "Personen" }).click();
    await expect(brief).toHaveCount(0);
  });

  // State A's left column (RecordView's `rail` slot, aria-label "Profile"):
  // the account's own fields, then health, people, signals and how it is
  // filed, drawn as ONE panel rather than a stack of separate cards.
  //
  // What is asserted is structural, not textual: the panel's own presence,
  // and that it carries a fixed number of named sections (a non-empty
  // <summary> on each collapsible slice), never the copy inside them. The
  // app renders German chrome, and a copy change must not fail a layout
  // suite. The rail sitting to the LEFT of the overview stack is the other
  // half of the claim, the account's context beside the work rather than
  // under it (the x comparison holds above the 1200px restack, which this
  // suite's viewport pins).
  //
  // Four, always: Health, People, Signals and Lists & tags each name their
  // own slice whether or not they have a row to draw today, unlike the
  // stacked cards this replaced, which could disappear along with their data.
  test("the left rail carries the account's context as one panel of named sections", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    const rail = page.locator(".co-rail");
    await expect(rail).toBeVisible();

    expect(await rail.locator("> .panel").count()).toBe(1);
    const sections = rail.locator("> .panel > details");
    expect(await sections.count()).toBe(4);
    for (const summary of await sections.locator("> summary").all()) {
      expect((await summary.textContent())?.trim()).not.toBe("");
    }

    const railBox = await rail.boundingBox();
    const bodyBox = await page.locator(".co-overview-stack").boundingBox();
    if (!railBox || !bodyBox) {
      throw new Error("rail and stack are visible but one has no box");
    }
    expect(railBox.x).toBeLessThan(bodyBox.x);
  });

  // A freshly imported company is lifecycle `unknown` and has nothing on it.
  // It must still render the page's skeleton — the regions carry their own
  // empty states, and a sparse account that drops whole regions is the
  // second way this page stops looking like the mockup.
  test("an imported company keeps the page's shape", async ({ page }) => {
    await openCompany(page, SPARSE_ORG as string);
    await expect(page.locator(STRIP)).toBeVisible();
    await expect(page.locator(".co-tabs")).toBeVisible();
    expect(await topOf(page.locator(STRIP))).toBeLessThan(
      await topOf(page.locator(".co-tabs")),
    );
    // Five readings here too, on an account that can answer almost none of
    // them: each slot says which reading it has none of. A shorter row would be
    // the sparse account quietly dropping part of the page again.
    await expect(page.locator(`${STRIP} > *`)).toHaveCount(5);
  });

  // Not an assertion — the artifact a human compares against the PNGs. It is
  // written outside the repo (E2E_SHOT_DIR) because a screenshot is session
  // debris, not product.
  //
  // The assertions above this point are all STRUCTURE: which regions exist and
  // in what order. A page can satisfy every one of them and still look nothing
  // like the mockup, because none of them reads a colour, a size or a weight —
  // which is exactly what happened. The block below closes that hole.
  test("capture both states for eyeball comparison", async ({ page }) => {
    for (const [name, org] of [
      ["populated", POPULATED_ORG],
      ["sparse", SPARSE_ORG],
    ] as const) {
      await openCompany(page, org as string);
      // openCompany waits for the h1, which the router paints before the
      // composite read returns. Shooting there photographs skeletons and —
      // because a section reading the shared view cannot tell "still loading"
      // from "failed" — two rail sections mid-flash of the same wording a real
      // outage shows. A capture whose whole purpose is eyeball comparison must
      // wait for the page it is comparing. The strip's second slot comes from
      // that same composite read, so its arrival is the proxy for it.
      await expect(page.locator(`${STRIP} > *`).nth(1)).toBeVisible();
      await page.screenshot({
        path: `${SHOTS}/company-${name}.png`,
        fullPage: true,
      });
    }
  });
});

/**
 * The mockup's TYPOGRAPHIC SCALE, asserted as computed styles.
 *
 * The structural suite above passes on a page rendering at half the mockup's
 * size, because a count does not know how big anything is. These are the
 * numbers a reader actually sees: the account's name, its logo, the money in
 * the KPI strip, and the control that says where the account stands.
 *
 * Floors rather than exact values. A design that lands at 42px where the
 * mockup drew 40 is right; one that lands at 22 is the dense admin-tool
 * rendering these exist to catch. Pixel equality would fail on a font
 * substitution and tell nobody anything.
 */
test.describe("company record — the mockup's visual weight", () => {
  test.beforeEach(async ({ page }) => {
    await signIn(page);
    await openCompany(page, POPULATED_ORG as string);
  });

  const px = async (locator: Locator, prop: string): Promise<number> => {
    await expect(locator).toBeVisible();
    const value = await locator.evaluate(
      (element, name) => getComputedStyle(element).getPropertyValue(name),
      prop,
    );
    return Number.parseFloat(value);
  };

  test("the account's name leads the page", async ({ page }) => {
    // ~40px in both mockups. It is the largest text on the record and the
    // first thing a reader lands on. Measured on the record header's own
    // heading: it is the page's h1, and the page head above it carries the
    // trail back at a deliberately quiet 13px.
    expect(
      await px(page.locator(".record-head h1"), "font-size"),
    ).toBeGreaterThanOrEqual(30);
  });

  test("the company's mark is a logo, not a favicon", async ({ page }) => {
    // ~110px square in the mockups, beside a name at ~40px. At 44 it reads as
    // a list-row avatar that wandered onto a record page.
    const box = await page
      .locator(".record-head .avatar")
      .first()
      .boundingBox();
    if (!box) {
      throw new Error("the header avatar has no box");
    }
    expect(box.width).toBeGreaterThanOrEqual(72);
  });

  test("the KPI figures read as the headline numbers they are", async ({
    page,
  }) => {
    // ONE size for every slot, and it belongs to the primitive now
    // (design-system/statstrip.css `.stat-strip .stat-card-value`): some slots
    // carry a figure and some a sentence ("typically 4 days early"), and a slot
    // sized to its own content would stop the row reading as one comparison.
    // The shared clamp floors at 13px, and at 1280px (this suite's pinned
    // viewport) it sits AT that floor: the bar is "clearly bigger than a label",
    // not "exactly what the clamp happens to compute".
    const size = await px(
      page.locator(`${STRIP} .stat-card-value`).first(),
      "font-size",
    );
    expect(size).toBeGreaterThanOrEqual(13);
    // And it must still LEAD its label — the stronger claim, that every
    // slot's value shares this exact size with every other slot, is what the
    // strip's "every KPI slot carries a label and a value, all at one size"
    // test above pins; a fixed ratio here cannot also express that.
    const label = await px(
      page.locator(`${STRIP} .stat-card-label`).first(),
      "font-size",
    );
    expect(size).toBeGreaterThan(label);
  });

  test("the lifecycle control is a control, not a tag", async ({ page }) => {
    // A filled button of ~190x48 in State A, sitting beside the name. The
    // 75x22 pale chip reads as metadata a reader cannot act on.
    const box = await page
      .locator(".co-standing .badge, .co-standing button")
      .first()
      .boundingBox();
    if (!box) {
      throw new Error("the lifecycle control has no box");
    }
    expect(box.height).toBeGreaterThanOrEqual(32);
  });

  // Website, LinkedIn, location, industry, size: five pills under the
  // description in both mockups. Each is drawn only where the field is
  // recorded — a chip invented for an empty column would state a fact the
  // record does not hold — so this asserts the row renders what the fixture
  // HAS, rather than a fixed count the data may not support.
  test("the header carries the company's attribute chips", async ({ page }) => {
    const recorded = await page.evaluate(async () => {
      const id = location.hash.split("/").pop();
      const response = await fetch(`/v1/organizations/${id}`, {
        headers: { accept: "application/json" },
      });
      const org = await response.json();
      return ["industry", "address_city", "linkedin_url"].filter((field) =>
        Boolean(org?.[field]),
      ).length;
    });
    expect(
      await page.locator(".record-sub .chip").count(),
    ).toBeGreaterThanOrEqual(recorded);
  });

  test("a card reads as a card against the page behind it", async ({
    page,
  }) => {
    // The mockups set white cards on a light grey page. The border is what
    // makes a card an object; against a near-white background at 1px of very
    // low contrast it stops being visible at all.
    const card = page.locator(".co-overview-stack > .panel").first();
    const pageBg = await page.evaluate(
      () => getComputedStyle(document.body).backgroundColor,
    );
    const cardBg = await card.evaluate(
      (element) => getComputedStyle(element).backgroundColor,
    );
    expect(cardBg).not.toBe(pageBg);
  });
});
