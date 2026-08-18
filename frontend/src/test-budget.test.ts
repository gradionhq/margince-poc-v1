import { describe, expect, it } from "vitest";
import {
  ASYNC_UTIL_TIMEOUT_MS,
  LONGEST_WAIT_CHAIN,
  SLOWEST_MEASURED_TEST_MS,
  TEST_TIMEOUT_MS,
} from "../vitest.budget";

// The budget that governs every other suite here, gated the only way a budget
// can be: against the thing it has to outlast. #1144 was not a slow test — it
// was a per-test ceiling smaller than the sum of the per-wait ceilings the test
// was allowed to spend, so tests failed with every assertion in them passing.
// Nothing in vitest compares those two numbers, which is why one drifted under
// the other unnoticed for as long as it did.
//
// This is arithmetic on constants rather than a timed run on purpose: a guard
// that proves a timeout by actually waiting six seconds costs six seconds on
// every run and still only proves it for the machine it ran on.
describe("the frontend suite's time budget", () => {
  it("clears the longest chain of waits a single test may legitimately compose", () => {
    // Below this the failure mode is not a slow test but a wrong verdict: the
    // waits are each inside their own budget and the test is outside its own.
    const chainBudget = LONGEST_WAIT_CHAIN * ASYNC_UTIL_TIMEOUT_MS;
    expect(TEST_TIMEOUT_MS).toBeGreaterThan(chainBudget);
    // The default this replaced could not, which is the bug in one line.
    expect(chainBudget).toBeGreaterThan(5_000);
  });

  it("leaves room for the work between those waits, not only the waiting", () => {
    // A ceiling that cleared the chain by a millisecond would fail the moment a
    // render between two waits cost anything at all.
    const headroom =
      TEST_TIMEOUT_MS - LONGEST_WAIT_CHAIN * ASYNC_UTIL_TIMEOUT_MS;
    expect(headroom).toBeGreaterThanOrEqual(SLOWEST_MEASURED_TEST_MS);
  });

  it("is the sum of its two measurements and nothing else", () => {
    // The value carries its derivation or it is a number somebody liked. If a
    // longer chain or a slower test is measured, the ceiling moves with it
    // rather than being re-guessed.
    expect(TEST_TIMEOUT_MS).toBe(
      LONGEST_WAIT_CHAIN * ASYNC_UTIL_TIMEOUT_MS + SLOWEST_MEASURED_TEST_MS,
    );
  });
});
