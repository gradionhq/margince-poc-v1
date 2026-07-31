/** @vitest-environment jsdom */
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { useTypeStream } from "./motion";

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
