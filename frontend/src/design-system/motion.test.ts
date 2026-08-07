/** @vitest-environment jsdom */
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { INTRO_MS, useDocumentIntro, useTypeStream } from "./motion";

// Pins the two hidden-tab paths together: a stream that STARTS hidden and one
// that goes hidden MID-STREAM both land on the complete string with `done`
// true. Chrome throttles setTimeout to about one call per second in a
// background tab, so a stream left running there would still be typing at a
// viewer who returns a minute later — the fix subscribes to
// `visibilitychange` instead of reading `document.hidden` once at mount.

let originalHidden: PropertyDescriptor | undefined;

function setHidden(hidden: boolean) {
  Object.defineProperty(document, "hidden", {
    configurable: true,
    get: () => hidden,
  });
}

beforeEach(() => {
  originalHidden = Object.getOwnPropertyDescriptor(document, "hidden");
});

afterEach(() => {
  // `hidden` is normally an accessor inherited from Document.prototype, so a
  // suite that never touched it has no OWN property to restore — put the
  // instance back to that state rather than leaving the stub behind.
  if (originalHidden) {
    Object.defineProperty(document, "hidden", originalHidden);
  } else {
    Reflect.deleteProperty(document, "hidden");
  }
  vi.useRealTimers();
});

describe("useTypeStream", () => {
  it("jumps to the complete text when the tab is hidden mid-stream, without the throttle running it out", () => {
    setHidden(false);
    vi.useFakeTimers();

    const text = "the quick brown fox jumps";
    const { result } = renderHook(() => useTypeStream(text, { speed: 20 }));

    act(() => {
      vi.advanceTimersByTime(20 * 4);
    });

    // A partial reveal while visible: a strict, non-empty prefix.
    const partial = result.current.shown;
    expect(partial.length).toBeGreaterThan(0);
    expect(partial.length).toBeLessThan(text.length);
    expect(text.startsWith(partial)).toBe(true);
    expect(result.current.done).toBe(false);

    // Flip visible -> hidden mid-stream and let the listener fire. No further
    // timer advance follows this: the proof is that the jump to complete
    // happens on the visibilitychange itself, not because the throttled loop
    // was given enough time to finish on its own.
    setHidden(true);
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });

    expect(result.current.shown).toBe(text);
    expect(result.current.done).toBe(true);
  });

  it("renders the complete text immediately when the stream starts on a hidden tab", () => {
    setHidden(true);

    const text = "already finished by the time anyone looked";
    const { result } = renderHook(() => useTypeStream(text, { speed: 20 }));

    expect(result.current.shown).toBe(text);
    expect(result.current.done).toBe(true);
  });
});

// An entry animation belongs to the page load. A React remount is not one, and
// this is the difference the hook exists to hold: a surface that keys its intro
// to the mount replays the whole choreography every time a query settles or a
// parent re-branches, which reads to the person watching as the page reloading
// under them.
describe("useDocumentIntro", () => {
  beforeEach(() => {
    // A fresh document per case: the deadline lives on the document element, which
    // one test file shares across all of its cases.
    delete document.documentElement.dataset.marginceIntroUntil;
    vi.useFakeTimers();
  });

  it("plays for the first mount of a document", () => {
    const { result } = renderHook(() => useDocumentIntro());

    expect(result.current).toBe(true);
  });

  it("does not play for a mount that arrives after the intro has run", () => {
    const first = renderHook(() => useDocumentIntro());
    act(() => {
      vi.advanceTimersByTime(INTRO_MS);
    });
    first.unmount();

    const later = renderHook(() => useDocumentIntro());

    expect(later.result.current).toBe(false);
  });

  it("holds the deadline the FIRST mount set, however often the surface remounts", () => {
    // The hole this closes: a countdown owned by a mount is cancelled by its
    // unmount, so a surface that rebuilds every second would restart the window
    // each time and replay its intro indefinitely. The deadline is an instant, and
    // an instant cannot be pushed out.
    for (let elapsed = 0; elapsed < INTRO_MS; elapsed += INTRO_MS / 4) {
      const mount = renderHook(() => useDocumentIntro());
      expect(mount.result.current).toBe(true);
      mount.unmount();
      act(() => {
        vi.advanceTimersByTime(INTRO_MS / 4);
      });
    }

    expect(renderHook(() => useDocumentIntro()).result.current).toBe(false);
  });

  it("still plays for a remount that lands while the intro is mid-flight", () => {
    // React's development double-mount, and it is why the rule is a deadline
    // rather than "one mount gets it": the second mount lands milliseconds after
    // the first, so an intro nobody has seen yet must still be playing.
    const first = renderHook(() => useDocumentIntro());
    act(() => {
      vi.advanceTimersByTime(INTRO_MS / 4);
    });
    first.unmount();

    const second = renderHook(() => useDocumentIntro());

    expect(second.result.current).toBe(true);
  });

  it("keeps playing for the mount that owns an intro already under way", () => {
    // The hook decides once and holds the answer: a surface must not lose its
    // animation halfway through because the deadline passed mid-sequence.
    const { result } = renderHook(() => useDocumentIntro());
    act(() => {
      vi.advanceTimersByTime(INTRO_MS * 2);
    });

    expect(result.current).toBe(true);
    // And the document carries the deadline, which is what the next mount reads.
    expect(
      Number(document.documentElement.dataset.marginceIntroUntil),
    ).toBeGreaterThan(0);
  });
});
