// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { cleanup, render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CoreLiquid } from "./margince-core-liquid";

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
function fakeGl(onDraw: () => void = () => {}): WebGLRenderingContext {
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
    uniform2f: () => {},
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

  // Every assertion below counts `drawArrays`, because the cost this component is
  // allowed to have IS its drawn frames: what a reader sees is one sphere either
  // way, and the difference between a Core that costs a fifth of a hero and one
  // that costs a permanent seat is entirely in how often it draws and when it
  // stops. A count is the only thing that can fail when that regresses.
  describe("its draw loop", () => {
    let draws = 0;
    let hidden = false;

    beforeEach(() => {
      draws = 0;
      hidden = false;
      // `document.hidden` is a getter on Document.prototype; redefining it here
      // is what lets a test say "the tab went away" without a real tab.
      Object.defineProperty(document, "hidden", {
        configurable: true,
        get: () => hidden,
      });
      vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockReturnValue(
        fakeGl(() => {
          draws += 1;
        }),
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
    });

    it("spends the frame budget on a timer rather than polling every refresh", () => {
      render(<CoreLiquid state="working" />);
      // A second of animation frames at jsdom's 16ms tick is ~62 callbacks. The
      // old loop drew on a 24fps gate INSIDE a per-refresh rAF chain, so it woke
      // for all 62 to use a third of them; this one asks for a frame only when it
      // intends to draw.
      vi.advanceTimersByTime(1000);
      expect(draws).toBeGreaterThan(0);
      expect(draws).toBeLessThanOrEqual(30);
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
