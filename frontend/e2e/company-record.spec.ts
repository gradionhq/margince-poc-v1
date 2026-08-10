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
  await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
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
    expect(await topOf(page.locator(".co-strip"))).toBeLessThan(
      await topOf(page.locator(".co-tabs")),
    );
  });

  // State A draws six: lifetime, 12 months, open, overdue, median paid after
  // due, and the relationship reading.
  test("the KPI strip has six slots", async ({ page }) => {
    await openCompany(page, POPULATED_ORG as string);
    await expect(page.locator(".co-strip > *")).toHaveCount(6);
  });

  // Overview · People · History · Documents. The page ships
  // Overview/People/Timeline today, so this fails until the tabs are rebuilt.
  test("the tab strip offers overview, people, history and documents", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    await expect(
      page.locator(".co-tabs [role='tab'], .co-tabs button"),
    ).toHaveCount(4);
  });

  // The mockups draw six tiles and the card can build six, but it does not draw
  // six on every account ON PURPOSE: a tile appears only when it has something
  // to say, so an account with no meeting booked and no open signal shows
  // fewer. A fixed count would demand the card invent the missing ones.
  //
  // So this names the two tiles this fixture's data guarantees — an open task
  // and a logged exchange — by their own hook. The rest depend on a booked
  // meeting, a live deal and an open signal, which this account may not have.
  test("Today on this account draws its tiles, including the last exchange", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    // Anchored on the tile's own data hook, never on drawn copy: this suite
    // pins layout, and a translation edit must not turn it red.
    await expect(
      page.locator('.today-tile[data-tile="interaction"]'),
    ).toHaveCount(1);
    await expect(
      page.locator('.today-tile[data-tile="commitment"]'),
    ).toHaveCount(1);
  });

  // AccountBrief's "BEFORE YOU TALK TO THEM" prose block, the NextSteps list
  // and the Ask panel appear in NO mockup. They are the page's biggest visual
  // departure, so their absence is asserted rather than assumed.
  // Anchored on the rendered HEADING rather than a class: NextSteps draws a
  // bare `.card.co-card` with nothing to name it by, so a class assertion here
  // would pass against any page — including one that still renders all three.
  // The strings are the German chrome the suite pins via `locale: de-DE`.
  test("the overview does not render the brief, next steps or the ask panel", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    for (const heading of [
      "Bevor du mit ihnen sprichst",
      "Nächste Schritte",
      "Diesen Account befragen",
    ]) {
      await expect(page.getByRole("heading", { name: heading })).toHaveCount(0);
    }
  });

  // State A's right column: next best actions, health, contacts, signals and
  // recent activity.
  //
  // What is asserted is that the rail EXISTS and sits to the RIGHT of the work
  // column, which is what makes it a rail rather than more page. (The x
  // comparison holds above the 1200px restack, which this suite's viewport
  // pins.)
  //
  // Four rather than five: the advice card draws nothing when the rules found
  // nothing to advise, and health draws nothing on an account it cannot judge.
  test("the right rail carries the account's context beside the work", async ({
    page,
  }) => {
    await openCompany(page, POPULATED_ORG as string);
    const rail = page.locator(".co-rail");
    await expect(rail).toBeVisible();
    expect(await rail.locator("> section").count()).toBeGreaterThanOrEqual(4);

    const railBox = await rail.boundingBox();
    const bodyBox = await page.locator(".co-grid").boundingBox();
    if (!railBox || !bodyBox) {
      throw new Error("rail and grid are visible but one has no box");
    }
    expect(railBox.x).toBeGreaterThan(bodyBox.x);
  });

  // A freshly imported company is lifecycle `unknown` and has nothing on it.
  // It must still render the page's skeleton — the regions carry their own
  // empty states, and a sparse account that drops whole regions is the
  // second way this page stops looking like the mockup.
  test("an imported company keeps the page's shape", async ({ page }) => {
    await openCompany(page, SPARSE_ORG as string);
    await expect(page.locator(".co-strip")).toBeVisible();
    await expect(page.locator(".co-tabs")).toBeVisible();
    expect(await topOf(page.locator(".co-strip"))).toBeLessThan(
      await topOf(page.locator(".co-tabs")),
    );
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
    // first thing a reader lands on.
    expect(
      await px(page.locator("h1").first(), "font-size"),
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
    // ~30px in the mockups. This is the money, and it is the reading the page
    // is opened for — at 16px it is the same size as a form label.
    expect(
      await px(page.locator(".stat-card-value").first(), "font-size"),
    ).toBeGreaterThanOrEqual(24);
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

  test("the header carries the company's attribute chips", async ({ page }) => {
    // Website, LinkedIn, location, industry, size: five pills under the
    // description in both mockups, and the row is absent entirely today.
    expect(
      await page.locator(".record-sub .chip, .co-chips > *").count(),
    ).toBeGreaterThanOrEqual(3);
  });

  test("a card reads as a card against the page behind it", async ({
    page,
  }) => {
    // The mockups set white cards on a light grey page. The border is what
    // makes a card an object; against a near-white background at 1px of very
    // low contrast it stops being visible at all.
    const card = page.locator(".co-grid > .card").first();
    const pageBg = await page.evaluate(
      () => getComputedStyle(document.body).backgroundColor,
    );
    const cardBg = await card.evaluate(
      (element) => getComputedStyle(element).backgroundColor,
    );
    expect(cardBg).not.toBe(pageBg);
  });
});
