// The frontend suite's time budget, derived rather than picked.
//
// Six screen suites failed intermittently with `Test timed out in 5000ms` —
// vitest's default `testTimeout`, which nothing overrode. They passed in
// isolation and failed when the machine was busy, so the same commit produced a
// different verdict run to run (#1144).
//
// It is NOT starvation, and it is not vague slowness either. Measured under
// deliberate load (8 spin loops on 10 cores) with the timeout lifted, the two
// tests observed failing completed in 609ms and 910ms, and the slowest test in
// the whole suite took 3437ms. Nothing is stuck, and nothing is close to five
// seconds of work.
//
// What is actually wrong is arithmetic between two budgets that were never
// compared. Testing Library's `asyncUtilTimeout` and `vi.waitFor`'s timeout both
// default to one second, and this repo overrides neither — so a test built from
// N sequential waits may legitimately spend N seconds waiting without any single
// wait failing. `company-act.test.tsx`'s "re-arms Continue once a skew refetch
// actually lands a NEW hash" chains six of them. Against a five-second ceiling
// that test can fail while every assertion in it is passing, and the failure
// names the test rather than the wait that was slow.
//
// The repo had already found this once and written it down, in the one file that
// hit it hardest — src/screens/company-context.test.tsx pins its own
// `TEST_MS = SETTLE_MS * 4` with the reason spelled out: "it must cover EVERY
// waiter in there: four run in sequence, each bounded by SETTLE_MS. A test whose
// own limit is smaller than the sum lets vitest fire while a waiter still has
// budget, and what surfaces then is an opaque timeout rather than the assertion
// the test was written to make." That is this defect exactly, fixed for one
// file. #1144 is the same arithmetic everywhere it was not fixed, so the ceiling
// belongs here, once, rather than as a per-file constant six more suites would
// have to remember to copy.
//
// That file keeps its own numbers and is deliberately untouched: it overrides
// the per-wait budget as well as the per-test one, and the defect it is still
// carrying (#613) is starvation rather than arithmetic. The ceiling below is
// SMALLER than its 10s-per-waiter budget, so it cannot mask that.
//
// So the ceiling has to clear the longest chain the suite legitimately composes,
// plus the render and act work sitting between those waits.

/** Testing Library's `asyncUtilTimeout` and `vi.waitFor`'s default, neither overridden. */
export const ASYNC_UTIL_TIMEOUT_MS = 1_000;

/**
 * The longest chain of sequential awaited waits in one test. Six, in
 * `src/screens/onboarding-conversation/company-act.test.tsx` — both the
 * "re-arms Continue once a skew refetch actually lands a NEW hash" case and the
 * "re-arms Continue only once a re-check finds the read confirmable" one, with
 * `voice-act.test.tsx` matching them.
 */
export const LONGEST_WAIT_CHAIN = 6;

/**
 * The slowest single test measured under deliberate load, in milliseconds —
 * `read-conclusion.test.tsx`'s "a long multi-snapshot run converges on the
 * review". Used whole as the allowance for the work BETWEEN the waits, which
 * deliberately over-counts: that figure already contains its own waits. An
 * over-estimate here is the safe direction, since the cost of a ceiling that is
 * too high is a slower red, and the cost of one too low is this bug.
 */
export const SLOWEST_MEASURED_TEST_MS = 3_437;

/**
 * The per-test ceiling. Not a round number on purpose: a round one silently
 * disagrees with the bound it has to outlast, and there is no reading of 10s or
 * 15s that explains itself. This one is the sum of the two measurements above,
 * so it moves when they do.
 */
export const TEST_TIMEOUT_MS =
  LONGEST_WAIT_CHAIN * ASYNC_UTIL_TIMEOUT_MS + SLOWEST_MEASURED_TEST_MS;
