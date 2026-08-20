import { beforeEach, vi } from "vitest";

// Node ≥23 ships its own global Web Storage: a `localStorage` getter that
// yields undefined unless the process was started with --localstorage-file.
// Because the key already exists on the Node global, vitest's populateGlobal
// keeps it instead of copying jsdom's Storage onto the test global (only keys
// on vitest's own KEYS allowlist override an existing global, and the storage
// keys are not on it). Tests in the jsdom environment then see undefined —
// while a runtime without the Node global (Node 22, today's CI) gets jsdom's
// working Storage and passes. Rebind the real jsdom Storage whenever the test
// global disagrees with the jsdom window, so both runtimes behave like CI.
const jsdomHost: { jsdom?: { window?: Record<string, unknown> } } = globalThis;
const jsdomWindow = jsdomHost.jsdom?.window;

if (jsdomWindow) {
  for (const key of ["localStorage", "sessionStorage"]) {
    const testGlobal: Record<string, unknown> = globalThis;
    if (testGlobal[key] !== jsdomWindow[key]) {
      Object.defineProperty(globalThis, key, {
        get: () => jsdomWindow[key],
        configurable: true,
      });
    }
  }
}

// The two DOM stubs below are guarded on there BEING a DOM: this setup file runs
// for every suite, and most of them are node-environment (jsdom is opted into
// per file with `@vitest-environment jsdom`). Unguarded, they threw
// "window is not defined" at setup time and took 20 unrelated suites with them.
if (typeof window !== "undefined") {
  // jsdom ships no matchMedia, and every motion-aware component asks it for
  // prefers-reduced-motion on first render. Default to "no preference" so the
  // animated path is what the tests exercise; a test that wants the reduced path
  // overrides this per case.
  if (!window.matchMedia) {
    window.matchMedia = ((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addEventListener: () => {},
      removeEventListener: () => {},
      addListener: () => {},
      removeListener: () => {},
      dispatchEvent: () => false,
    })) as typeof window.matchMedia;
  }

  // jsdom ships no ResizeObserver, and the list table watches its own body so
  // the frozen column's edge shadow follows a resized column. A stub that never
  // fires is the honest stand-in: the component measures once on mount either
  // way, which is what the tests assert on.
  if (!window.ResizeObserver) {
    window.ResizeObserver = class {
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof window.ResizeObserver;
  }

  // The Margince Core draws its liquid on a WebGL canvas; jsdom has no GL
  // context. Returning null is the same signal a browser without WebGL gives,
  // and the Core has a REQUIRED CSS rendering of every state for exactly that
  // case (WDS-CORE-3) — so this stub is what makes the suite exercise the
  // fallback rung of the ladder rather than the shader.
  //
  // Assigned UNCONDITIONALLY, and that is the fix rather than the style choice:
  // jsdom DOES define getContext, as a method that throws "Not implemented". An
  // `if (!…)` guard therefore never fires, and every render of a screen carrying
  // the Core prints a twelve-line jsdom stack to stderr — noise that trains a
  // reader to ignore test output, which is where the next real error hides.
  //
  // Re-applied before EVERY case, not once at setup: a suite whose `afterEach`
  // calls `vi.restoreAllMocks()` (auth.test.tsx does) hands getContext back to
  // jsdom after its first case, and every later render brings the stack trace
  // back. The install at setup time covers a render that happens while a test
  // file is still being imported, before any hook has run.
  const stubCanvasContext = () => {
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
  };

  stubCanvasContext();
  beforeEach(stubCanvasContext);
}

// The calendar-drift lane: run the whole suite as if it were N days from now.
//
// A test must not depend on the real clock. The half of that rule a grep can
// hold is small — "an absolute date in a file that never pins the clock" matches
// most of this suite's fixtures, nearly all of them harmless — because a date
// only misleads when the COMPONENT compares it to now to decide a state, and
// nothing static separates those from the dates a component merely formats. So
// the check is a second RUN: shift the clock, require the same verdict.
//
// Only the no-argument Date and Date.now move. Timers stay real, because a
// suite-wide vi.useFakeTimers would change what every async test is waiting for
// and report its own breakage as calendar drift. A test that pins its own clock
// overrides this and is unaffected, which is correct: it is already immune to
// what this looks for.
//
// `make fe-clock-drift` runs it; docs/reference/make-targets.md says why it runs
// daily on main rather than on a pull request.
const skewRequest = process.env.FE_CLOCK_SKEW_DAYS ?? "";
if (skewRequest !== "") {
  const days = Number(skewRequest);
  // A gate that cannot arm must FAIL, not run the ordinary suite and report
  // green. Number("") is 0 and Number("later") is NaN, so a typo or a stray
  // quote would otherwise shift no clock at all — and a lane that silently
  // checks nothing is the same colour as one that checked everything, which is
  // the confusion this lane was built to remove.
  if (!Number.isFinite(days) || days === 0) {
    throw new Error(
      `FE_CLOCK_SKEW_DAYS=${skewRequest} is not a non-zero number of days: the clock-drift lane ` +
        "would run the ordinary suite and report green over a check that never happened",
    );
  }
  const skewMs = days * 24 * 60 * 60 * 1000;
  const RealDate = globalThis.Date;
  class SkewedDate extends RealDate {
    constructor(...args: ConstructorParameters<typeof Date>) {
      if (args.length === 0) {
        super(RealDate.now() + skewMs);
        return;
      }
      super(...args);
    }
    static now(): number {
      return RealDate.now() + skewMs;
    }
  }
  globalThis.Date = SkewedDate as DateConstructor;
  // The instrument proves itself before any test runs. Everything this lane
  // reports rests on the clock actually having moved, and that is one
  // assignment away from being true of nothing.
  const shifted = Date.now() - RealDate.now();
  if (Math.abs(shifted - skewMs) > 1000) {
    throw new Error(
      `the clock-drift skew did not take: Date.now() is ${shifted}ms ahead, want ${skewMs}ms`,
    );
  }
}
