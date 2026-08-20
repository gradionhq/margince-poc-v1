import userEvent from "@testing-library/user-event";
import { vi } from "vitest";

// A clock that moves only when a test says so, and an interaction driver on the
// same clock.
//
// A suite whose subject IS a deadline — a toast that withdraws itself, a
// debounce that decides when a request goes — cannot let the real clock decide
// when its assertions run. Fake timers answer that, but combining them with an
// awaited `userEvent` interaction deadlocks, and the reason is worth writing
// down once rather than rediscovering per suite:
//
// @testing-library/react configures dom-testing-library's `asyncWrapper` to
// drain the microtask queue by awaiting `setTimeout(resolve, 0)`. It keeps that
// from hanging under mocked timers by advancing the clock alongside it — but
// only `if (typeof jest !== "undefined")`, through `jest.advanceTimersByTime`.
// There is no `jest` in a vitest repo, so the branch never runs, the zero-delay
// timeout never fires, and every awaited interaction hangs until the test times
// out. It reads as a broken component rather than as a missing global.
//
// So the one method it reaches for is provided, and NOT in vitest.setup.ts:
// several suites in this tree reach for `vi.useFakeTimers({ shouldAdvanceTime:
// true })` instead — the other way out of the same deadlock — and their clock
// already moves on its own. Advancing it from inside `asyncWrapper` fires timers
// they never asked to fire, which is a failure in a suite that did nothing
// wrong. Opting in per file keeps that blast radius at the file that wants it.
//
// It is NOT a *.test.* file: the design-system and lint gates skip test files,
// and a helper this many suites lean on should answer to the same rules the app
// does. `src/testing/appharness.tsx` is the same arrangement.

/**
 * Install fake timers for the current test and hand back an interaction driver
 * bound to them.
 *
 * Call it ONCE per test and reuse the instance: a second one starts a second
 * view of the document's input state — what is focused, which buttons are held —
 * and the two then disagree, which shows up as an interaction that silently does
 * nothing. Pair it with `vi.useRealTimers()` in `afterEach`.
 */
export function steppedClock() {
  vi.useFakeTimers();
  Object.assign(globalThis, {
    jest: {
      advanceTimersByTime: (ms: number) => {
        vi.advanceTimersByTime(ms);
      },
    },
  });
  return userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
}
