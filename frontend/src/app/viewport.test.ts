/** @vitest-environment jsdom */
import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PHONE_MAX_WIDTH, usePhoneViewport } from "./viewport";

// The width the chrome has to KNOW rather than merely be laid out by: at phone
// width the sidebar keeps its four destinations and a section moves into the page
// head, which is a decision in TypeScript rather than a layout a stylesheet makes.

afterEach(() => {
  vi.unstubAllGlobals();
});

// One media query, the way a browser hands it over: `matches` now, plus a
// subscription for when it stops being true.
function stubMatchMedia(matches: boolean) {
  const listeners = new Set<() => void>();
  const query = {
    matches,
    media: `(max-width: ${PHONE_MAX_WIDTH}px)`,
    addEventListener: (_: string, listener: () => void) =>
      listeners.add(listener),
    removeEventListener: (_: string, listener: () => void) =>
      listeners.delete(listener),
  };
  vi.stubGlobal("matchMedia", () => query);
  return {
    resizeTo(nowMatches: boolean) {
      query.matches = nowMatches;
      for (const listener of listeners) {
        listener();
      }
    },
    get subscribers() {
      return listeners.size;
    },
  };
}

describe("usePhoneViewport", () => {
  it("answers from the media query the browser gives it", () => {
    stubMatchMedia(true);
    expect(renderHook(() => usePhoneViewport()).result.current).toBe(true);
  });

  it("answers false above the breakpoint", () => {
    stubMatchMedia(false);
    expect(renderHook(() => usePhoneViewport()).result.current).toBe(false);
  });

  // Subscribed, not measured once: a window is resized and a phone is rotated
  // while the app is open, and chrome that read the width at mount would keep the
  // other width's arrangement for the rest of the session.
  it("follows the viewport across a resize, and lets go on unmount", () => {
    const media = stubMatchMedia(true);
    const { result, unmount } = renderHook(() => usePhoneViewport());
    expect(media.subscribers).toBe(1);

    act(() => media.resizeTo(false));
    expect(result.current).toBe(false);

    act(() => media.resizeTo(true));
    expect(result.current).toBe(true);

    unmount();
    expect(media.subscribers).toBe(0);
  });

  // Some embedded contexts have no `matchMedia` at all. A missing media query is
  // a DEFAULT, never an error: the answer is "not a phone", which is the
  // arrangement that works at any width.
  it("defaults to the wide arrangement where matchMedia is absent", () => {
    vi.stubGlobal("matchMedia", undefined);
    expect(renderHook(() => usePhoneViewport()).result.current).toBe(false);
  });
});
