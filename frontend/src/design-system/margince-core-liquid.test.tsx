// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CoreLiquid } from "./margince-core-liquid";
import { WINDOW_BLURRED_ATTRIBUTE } from "./window-focus";

// vite.config.ts doesn't enable globals, so @testing-library/react's own
// auto-cleanup never runs here: without this, the rendered CoreLiquid is
// never unmounted and its self-rescheduling requestAnimationFrame render
// loop (stopped only by the effect's own cleanup on unmount) keeps running
// for the rest of the file.
afterEach(cleanup);

// A minimal WebGL stand-in: every call the render effect makes on its way to
// the first frame, none of the actual drawing. jsdom's own getContext is
// stubbed to null globally (WDS-CORE-3's fallback-to-CSS case); this test
// needs the OTHER branch, where a context exists and the shader "compiles",
// so the effect reaches the ResizeObserver line this guards.
// `onResolution` observes the ONE uniform the sphere cannot render without: the
// shader divides by `min(uRes.x, uRes.y)`, so a program that never receives it
// draws an empty canvas rather than an obviously broken one.
function fakeGl(
  onDraw: () => void = () => {},
  onResolution: () => void = () => {},
): WebGLRenderingContext {
  const gl = {
    VERTEX_SHADER: 1,
    FRAGMENT_SHADER: 2,
    COMPILE_STATUS: 3,
    LINK_STATUS: 4,
    ARRAY_BUFFER: 5,
    STATIC_DRAW: 6,
    FLOAT: 7,
    TRIANGLES: 8,
    COLOR_BUFFER_BIT: 9,
    createShader: () => ({}),
    shaderSource: () => {},
    compileShader: () => {},
    getShaderParameter: () => true,
    deleteShader: () => {},
    createProgram: () => ({}),
    attachShader: () => {},
    linkProgram: () => {},
    getProgramParameter: () => true,
    deleteProgram: () => {},
    useProgram: () => {},
    createBuffer: () => ({}),
    bindBuffer: () => {},
    bufferData: () => {},
    deleteBuffer: () => {},
    getAttribLocation: () => 0,
    enableVertexAttribArray: () => {},
    vertexAttribPointer: () => {},
    getUniformLocation: () => ({}),
    uniform1f: () => {},
    uniform2f: onResolution,
    uniform3f: () => {},
    isContextLost: () => false,
    clearColor: () => {},
    clear: () => {},
    drawArrays: onDraw,
    viewport: () => {},
  };
  return gl as unknown as WebGLRenderingContext;
}

describe("CoreLiquid", () => {
  beforeEach(() => {
    // Not held in a typed handle: `getContext` is overloaded per context id,
    // and every spy type that spans those overloads collapses them into one
    // signature that no longer accepts the literal ids.
    vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
      fakeGl(),
    );
    // jsdom's document never has focus, and an unfocused window parks the Core
    // (see window-focus.ts) — so left alone every case here would measure a
    // parked loop and the counts below would stop meaning anything. The default
    // fixture is therefore a window somebody is looking at; the cases about
    // losing focus say so themselves.
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("still paints the shader when the host has WebGL but no ResizeObserver", () => {
    const original = globalThis.ResizeObserver;
    // @ts-expect-error - simulating a host that never defines the global
    globalThis.ResizeObserver = undefined;
    try {
      expect(() => render(<CoreLiquid state="idle" />)).not.toThrow();
      const canvas = document.querySelector("canvas");
      // `.off` is the CSS-fallback rung (WDS-CORE-3); its absence is the
      // signal that the shader path completed instead of aborting mid-setup.
      expect(canvas).not.toHaveClass("off");
    } finally {
      globalThis.ResizeObserver = original;
    }
  });

  // What these count is the component's whole cost: the frames it DRAWS, and the
  // times it WAKES THE MAIN THREAD to decide whether to. A reader sees one sphere
  // either way, so a count is the only thing that can fail when either regresses.
  describe("its draw loop", () => {
    let draws = 0;
    let wakeups = 0;
    let resolutions = 0;
    let hidden = false;

    beforeEach(() => {
      draws = 0;
      wakeups = 0;
      resolutions = 0;
      hidden = false;
      // `document.hidden` is a getter on Document.prototype; redefining it here
      // is what lets a test say "the tab went away" without a real tab.
      Object.defineProperty(document, "hidden", {
        configurable: true,
        get: () => hidden,
      });
      vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
        fakeGl(
          () => {
            draws += 1;
          },
          () => {
            resolutions += 1;
          },
        ),
      );
      vi.useFakeTimers({
        toFake: [
          "requestAnimationFrame",
          "cancelAnimationFrame",
          "setTimeout",
          "clearTimeout",
          "performance",
        ],
      });
      // Counted AFTER the fake timers install, because installing them replaces
      // the global this wraps. Every scheduled frame is one main-thread wakeup,
      // which is the quantity the timer-paced loop exists to reduce — the draw
      // count alone cannot see it, since a per-refresh loop drawing on a 24fps
      // gate produces the same drawn frames from four times the callbacks.
      const scheduled = globalThis.requestAnimationFrame;
      vi.spyOn(globalThis, "requestAnimationFrame").mockImplementation((cb) => {
        wakeups += 1;
        return scheduled(cb);
      });
    });

    afterEach(() => {
      vi.useRealTimers();
      // @ts-expect-error - handing `hidden` back to jsdom's own implementation
      delete document.hidden;
    });

    it("draws ONE frame for a liquid that does not move, then parks", () => {
      // `unavailable` holds time still (SPEED), so every later frame would be the
      // same pixels. A loop that kept running here is invisible in a screenshot
      // and permanent in a profile — this is the assertion that sees it.
      render(<CoreLiquid state="unavailable" />);
      vi.advanceTimersByTime(2000);
      expect(draws).toBe(1);
      // And it asked for exactly the one frame it drew: parking means scheduling
      // nothing, not scheduling a callback that returns early.
      expect(wakeups).toBe(1);
    });

    it("wakes the main thread once per frame it draws, not once per refresh", () => {
      render(<CoreLiquid state="working" />);
      vi.advanceTimersByTime(1000);
      // ~62 animation frames are available in that second at jsdom's 16ms tick.
      // The old loop rescheduled on every one of them and used a third, so its
      // wakeups ran well ahead of its draws; this one asks for a frame only when
      // it intends to draw, so the two counts track each other. Slack of one for
      // the frame in flight when time stopped.
      expect(draws).toBeGreaterThan(0);
      expect(wakeups).toBeLessThanOrEqual(draws + 1);
      // The 24fps ceiling itself, which is the other half of the cost.
      expect(draws).toBeLessThanOrEqual(30);
    });

    it("gives every program its resolution, so a state change cannot blank the sphere", () => {
      // A state change tears the render effect down and builds a NEW program, and
      // a new program has none of its uniforms set. The buffer is usually already
      // the right size at that moment, so a size-changed check that reads the
      // canvas back skips the upload — and the shader divides by uRes, so the
      // sphere renders empty for the rest of the session. One `quiet` → `working`
      // is all it takes.
      const { rerender } = render(<CoreLiquid state="quiet" />);
      vi.advanceTimersByTime(200);
      expect(resolutions).toBe(1);
      const drawnBefore = draws;

      rerender(<CoreLiquid state="working" />);
      vi.advanceTimersByTime(200);
      expect(draws).toBeGreaterThan(drawnBefore);
      expect(resolutions).toBe(2);
    });

    it("stops in a hidden tab and resumes when it is shown again", () => {
      render(<CoreLiquid state="working" />);
      vi.advanceTimersByTime(300);
      const whileVisible = draws;
      expect(whileVisible).toBeGreaterThan(0);

      hidden = true;
      document.dispatchEvent(new Event("visibilitychange"));
      vi.advanceTimersByTime(5000);
      // Nothing at all for five seconds of a tab nobody can see.
      expect(draws).toBe(whileVisible);

      hidden = false;
      document.dispatchEvent(new Event("visibilitychange"));
      vi.advanceTimersByTime(300);
      // And the pause ENDS. A stop with no guaranteed way back is a frozen
      // sphere, which is indistinguishable from a shader that failed.
      expect(draws).toBeGreaterThan(whileVisible);
    });

    it("stops while the window has no focus and resumes when it comes back", () => {
      render(<CoreLiquid state="working" />);
      vi.advanceTimersByTime(300);
      const whileFocused = draws;
      expect(whileFocused).toBeGreaterThan(0);

      window.dispatchEvent(new Event("blur"));
      vi.advanceTimersByTime(5000);
      // A window sitting behind another one is not being watched, and the tab is
      // still "visible" the whole time — so `document.hidden` cannot see this
      // case at all.
      expect(draws).toBe(whileFocused);

      window.dispatchEvent(new Event("focus"));
      vi.advanceTimersByTime(300);
      expect(draws).toBeGreaterThan(whileFocused);
    });

    it("draws its first frame even when it mounts into an unfocused window", () => {
      // The one read of `hasFocus` there is: seeding the state at subscribe time.
      vi.spyOn(document, "hasFocus").mockReturnValue(false);
      render(<CoreLiquid state="working" />);
      vi.advanceTimersByTime(2000);
      // Exactly one frame, and then nothing. An undrawn canvas is blank, and a
      // blank sphere is indistinguishable from a broken one, so the first frame
      // is owed even to a Core nobody is looking at yet; the next two seconds of
      // them are not.
      expect(draws).toBe(1);
      // One rAF for the frame it drew, at most one more for the callback that
      // found nobody watching and parked. A loop still running would be ~48.
      expect(wakeups).toBeLessThanOrEqual(2);

      // `hasFocus` stays mocked false on purpose: the resume must come from the
      // EVENT. A predicate that polled `document.hasFocus()` instead would never
      // see this window come back.
      window.dispatchEvent(new Event("focus"));
      vi.advanceTimersByTime(300);
      expect(draws).toBeGreaterThan(1);
    });

    it("resumes only once BOTH the hidden tab and the lost focus have ended", () => {
      render(<CoreLiquid state="working" />);
      vi.advanceTimersByTime(300);
      const whileWatched = draws;
      expect(whileWatched).toBeGreaterThan(0);

      // Switching to another window's tab is both at once, and the two end
      // separately — a Core that treated either event as "watched again" would
      // start drawing into a tab that is still hidden.
      hidden = true;
      document.dispatchEvent(new Event("visibilitychange"));
      window.dispatchEvent(new Event("blur"));
      vi.advanceTimersByTime(2000);
      expect(draws).toBe(whileWatched);

      hidden = false;
      document.dispatchEvent(new Event("visibilitychange"));
      vi.advanceTimersByTime(2000);
      expect(draws).toBe(whileWatched);

      window.dispatchEvent(new Event("focus"));
      vi.advanceTimersByTime(300);
      expect(draws).toBeGreaterThan(whileWatched);
    });
  });

  /*
   * The stylesheet's half of the same stillness.
   *
   * A paused CSS animation is not observable here — jsdom runs no animations at
   * all — so what a test can honestly pin is the SIGNAL the stylesheet reads:
   * the attribute on the root element, and the fact that a Core leaves none of
   * it behind. The rule that consumes the attribute is pinned against the
   * stylesheet text in margince-core.test.ts.
   */
  describe("its stillness signal", () => {
    it("marks the document while the window has no focus, and unmarks it on return", () => {
      render(<CoreLiquid state="working" />);
      expect(document.documentElement).not.toHaveAttribute(
        WINDOW_BLURRED_ATTRIBUTE,
      );

      window.dispatchEvent(new Event("blur"));
      expect(document.documentElement).toHaveAttribute(
        WINDOW_BLURRED_ATTRIBUTE,
      );

      window.dispatchEvent(new Event("focus"));
      expect(document.documentElement).not.toHaveAttribute(
        WINDOW_BLURRED_ATTRIBUTE,
      );
    });

    it("marks it on the non-GPU rung too, where CSS is the whole Core", () => {
      // WDS-CORE-3's lower rung: no context, so no draw loop is ever built. The
      // breath, the halo and the feed are then the only motion there is, and
      // they are exactly what must stop.
      vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(null);
      render(<CoreLiquid state="working" />);
      expect(document.querySelector("canvas")).toHaveClass("off");

      window.dispatchEvent(new Event("blur"));
      expect(document.documentElement).toHaveAttribute(
        WINDOW_BLURRED_ATTRIBUTE,
      );
    });

    it("leaves nothing behind once the last Core unmounts", () => {
      const { unmount } = render(<CoreLiquid state="working" />);
      window.dispatchEvent(new Event("blur"));
      unmount();

      // The attribute goes with the listeners. Left behind, it would pause the
      // animations of the next Core mounted into a focused window, with no event
      // coming to release them.
      expect(document.documentElement).not.toHaveAttribute(
        WINDOW_BLURRED_ATTRIBUTE,
      );
      window.dispatchEvent(new Event("blur"));
      expect(document.documentElement).not.toHaveAttribute(
        WINDOW_BLURRED_ATTRIBUTE,
      );
    });
  });

  it("still paints the shader when the ResizeObserver constructor throws", () => {
    const original = globalThis.ResizeObserver;
    // A host where the global EXISTS but constructing one fails (some
    // embedded webviews advertise the constructor and then reject it) —
    // the other half of tryCreateResizeObserver's try/catch, distinct from
    // the global-missing case above.
    globalThis.ResizeObserver = class {
      constructor() {
        throw new Error("ResizeObserver is not supported in this host");
      }
    } as unknown as typeof ResizeObserver;
    try {
      expect(() => render(<CoreLiquid state="idle" />)).not.toThrow();
      const canvas = document.querySelector("canvas");
      expect(canvas).not.toHaveClass("off");
    } finally {
      globalThis.ResizeObserver = original;
    }
  });
});
