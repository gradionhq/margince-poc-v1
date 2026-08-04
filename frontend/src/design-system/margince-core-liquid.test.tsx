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
function fakeGl(): WebGLRenderingContext {
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
    drawArrays: () => {},
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
});
