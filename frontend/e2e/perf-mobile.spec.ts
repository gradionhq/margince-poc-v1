import { expect, type Page, test } from "@playwright/test";
import { mockApi } from "./seed";

/**
 * MOBILE-AC-2: record open p95 < 300 ms perceived, on a throttled Fast-3G
 * profile at 390px. The BUDGET is PERF-1's and is single-homed there;
 * MOBILE-PARAM-2 pins only the condition it has to hold under.
 *
 * This is the throttled half of PERF-1's perceived budget. The unthrottled half
 * already exists as `PERF-1: record open renders under the 300ms perceived
 * budget` in ac.spec.ts, and it stays there — this file does not restate it on
 * a fast link, it measures the same interaction on a slow one.
 *
 * Run it with `make bench-mobile`. It is not collected by `pnpm e2e`.
 */

// Chrome DevTools' own Fast-3G preset. Named constants rather than inline
// arithmetic because these are the numbers MOBILE-PARAM-2 refers to by name,
// and a reader has to be able to check them against the profile they claim.
const FAST_3G = {
  downloadThroughput: (1.6 * 1024 * 1024) / 8, // 1.6 Mbit/s
  uploadThroughput: (750 * 1024) / 8, // 750 kbit/s
  latency: 562.5, // ms round trip
};

const SAMPLES = 20;
const PERCEIVED_BUDGET_MS = 300;

/**
 * Throttle the link the way a phone experiences it.
 *
 * TWO mechanisms, because either alone would measure a lie here. CDP throttling
 * shapes real network traffic — the bundle, the fonts — but the seed fixture is
 * mocked at the network edge, and a fulfilled route never touches the transport
 * CDP is shaping. So the API's round-trip cost has to be paid explicitly.
 *
 * The delay route is registered AFTER mockApi so it matches FIRST (Playwright
 * runs handlers in reverse registration order), waits out one round trip, and
 * then falls back to the seed mock rather than answering in its place.
 */
async function throttle(page: Page) {
  const session = await page.context().newCDPSession(page);
  await session.send("Network.enable");
  await session.send("Network.emulateNetworkConditions", {
    offline: false,
    ...FAST_3G,
  });
  await page.route("**/v1/**", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, FAST_3G.latency));
    await route.fallback();
  });
}

/** Nearest-rank p95, the same method the Go harness reports — so a value here
 * is a latency that actually happened rather than an interpolation between two
 * that did not. */
function p95(samples: number[]): number {
  const sorted = [...samples].sort((a, b) => a - b);
  const rank = Math.ceil(sorted.length * 0.95) - 1;
  return sorted[Math.min(Math.max(rank, 0), sorted.length - 1)];
}

test("MOBILE-AC-2: record open holds the 300ms perceived budget on Fast-3G at 390px", async ({
  page,
}) => {
  await mockApi(page);
  await throttle(page);

  const samples: number[] = [];
  for (let i = 0; i < SAMPLES; i++) {
    await page.goto("/#/contacts");
    // Anchor on a settled screen before measuring, for the reason ac.spec.ts
    // records: a click during hydration lands on a row whose handler is not
    // attached, the navigation never happens, and the assertion times out as a
    // phantom perf failure. Under throttling this is likelier, not less likely.
    await page.waitForLoadState("networkidle");
    const row = page.getByText("Anna Weber");
    await expect(row).toBeVisible();

    const start = Date.now();
    await row.click();
    // The record's OWN header, not the shell's: the head shows only the trail
    // on a record route and renders from the router before any record read
    // returns, so waiting on it would measure routing rather than the open.
    await expect(page.locator(".record-head h1")).toHaveText("Anna Weber");
    samples.push(Date.now() - start);
  }

  const measured = p95(samples);
  console.log(
    `perfbench [fast-3g/390px]: record_open_perceived p95=${measured}ms ` +
      `(budget ${PERCEIVED_BUDGET_MS}ms, ${SAMPLES} samples)`,
  );
  expect(measured).toBeLessThan(PERCEIVED_BUDGET_MS);
});
