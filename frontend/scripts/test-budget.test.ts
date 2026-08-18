import { globSync } from "node:fs";
import { describe, expect, it } from "vitest";
import viteConfig from "../vite.config";
import {
  MAX_DEFAULT_WAITER_BUDGET_MS,
  TEST_TIMEOUT_MS,
} from "../vitest.budget";
import { budgetsIn } from "./test-budget";

// The budget that governs every other suite here, gated the only way a budget
// can be: against the thing it has to outlast. #1144 was not a slow test — it
// was a per-test ceiling smaller than the sum of the per-waiter budgets the
// test was allowed to spend, so tests failed with every waiter in them still
// inside its own budget. Nothing in vitest compares those two numbers, which is
// why one drifted under the other unnoticed for as long as it did.
//
// Read from the syntax tree rather than run: a guard that proved the ceiling by
// actually waiting eleven seconds would cost eleven seconds every run and still
// only prove it for the machine it ran on. This costs about a third of a second
// and proves it for every test in the tree.

/**
 * `company-context.test.tsx`'s two `clickRefresh` cases spend 30s of waiter
 * budget under a ceiling of 10437ms, and they are LEFT that way on purpose.
 * That file carries #613 — a panel that does not render even with a 10s waiter,
 * which is starvation rather than this arithmetic — and giving those two tests a
 * ceiling that covers their waiters would turn its fast red into a slow one and
 * bury the defect being investigated. Named here, with the reason, rather than
 * left to look like an oversight: this is the one place the invariant below does
 * not hold, and it does not hold deliberately.
 */
const EXEMPT = new Map([
  [
    "src/screens/company-context.test.tsx",
    "#613 — starvation, deliberately left fast-red",
  ],
]);

const budgets = globSync("src/**/*.test.ts?(x)")
  .flatMap((file) => budgetsIn(file, TEST_TIMEOUT_MS))
  .filter((budget) => !EXEMPT.has(budget.file));

describe("the frontend suite's time budget", () => {
  it("is the ceiling vitest actually applies", () => {
    // Everything below reasons about TEST_TIMEOUT_MS. If the config stops
    // reading it, all of it becomes true about a number nothing uses — and
    // #1144 is back with this suite still green.
    expect(viteConfig.test?.testTimeout).toBe(TEST_TIMEOUT_MS);
  });

  it("covers every waiter budget the tests under it compose", () => {
    // The invariant, per test rather than in aggregate: a test may spend the
    // sum of its waiters' budgets without any one of them failing, so its own
    // ceiling has to be at least that. A test that needs longer raises its
    // own — vitest takes it as the third argument to `it` — and several here
    // already do.
    const over = budgets
      .filter((budget) => budget.waiterBudgetMs > budget.ceilingMs)
      .map(
        (budget) =>
          `${budget.file}:${budget.line} spends ${budget.waiterBudgetMs}ms of waiter budget under a ${budget.ceilingMs}ms ceiling — "${budget.name}"`,
      );
    expect(over).toEqual([]);
  });

  it("is derived from the widest budget it has to cover, not chosen", () => {
    // The ceiling is only honest if MAX_DEFAULT_WAITER_BUDGET_MS is still the
    // real maximum. Re-measured here from the same trees, so a suite that
    // grows a longer chain moves the measurement instead of silently sitting
    // closer to the edge.
    const widest = budgets
      .filter((budget) => budget.ceilingMs === TEST_TIMEOUT_MS)
      .reduce((max, budget) => Math.max(max, budget.waiterBudgetMs), 0);
    expect(widest).toBe(MAX_DEFAULT_WAITER_BUDGET_MS);
  });

  it("reads a tree it can actually parse", () => {
    // Every assertion above is an absence-assertion over `budgets`, and an
    // absence-assertion passes for free over an empty list — a glob that
    // stopped matching, or a parser that returned nothing, would look exactly
    // like a clean tree.
    expect(budgets.length).toBeGreaterThan(2_000);
    expect(budgets.some((budget) => budget.waiterBudgetMs > 0)).toBe(true);
  });
});
