import { expect, type Locator, type Page, test } from "@playwright/test";

/**
 * The company record page against the mockups in
 * docs/explanation/assets/company-record-page-v2/.
 *
 * This suite exists because "it looks like the mockup" kept being reported from
 * reading the code rather than the page. It asserts REGION ORDER AND PRESENCE,
 * never pixels and never the drawn English strings: the app renders German
 * chrome, and a copy change must not fail a layout suite.
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

  // The mockups draw six tiles. Five is the current build, three is what a
  // partially populated account renders — both are wrong against State D.
  test("Today on this account draws six tiles", async ({ page }) => {
    await openCompany(page, POPULATED_ORG as string);
    await expect(page.locator(".today-tile")).toHaveCount(6);
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

  // State A's right column: next best actions, health, contacts, signals,
  // recent activity.
  test("the right rail renders five cards", async ({ page }) => {
    await openCompany(page, POPULATED_ORG as string);
    await expect(page.locator(".co-rail > section")).toHaveCount(5);
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
