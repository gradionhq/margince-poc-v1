/** @vitest-environment jsdom */
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const here = dirname(fileURLToPath(import.meta.url));

function stripCss(): string {
  return readFileSync(join(here, "statstrip.css"), "utf8");
}

// A slot may be a control, and a strip of filters is what made that true. What
// is held here is the consequence nobody looking at the markup would see: the
// plate CLIPS. It is `overflow: hidden` with a corner radius, because each slot
// draws its dividers outside itself and the plate is what makes the leading ones
// vanish — so anything a slot paints at its own edge is trimmed by that clip,
// and a rectangular selection ring lost its corners to the plate's curve and
// read as a broken box.
//
// Read from the sheet rather than from a rendered element: jsdom computes no
// box-shadow and no radius, so a DOM assertion here could only ever prove the
// class name it already asked for.
describe("statstrip.css draws a selected slot without asking the plate for room", () => {
  it("marks the pressed slot with a tint and an inset line, never a ring", () => {
    const pressed = /\.stat-strip > \[aria-pressed="true"\]\s*\{([^}]*)\}/.exec(
      stripCss(),
    );
    expect(pressed).not.toBeNull();
    const body = pressed?.[1] ?? "";
    // The tint says selected at a glance; the line is what separates it from
    // `--bgHover` for a reader who is also pointing at it.
    expect(body).toMatch(/background:\s*var\(--accentLight\)/);
    expect(body).toMatch(/inset 0 -2px 0 var\(--accent\)/);
    // A ring is exactly what the clip breaks. `inset 0 0 0 <n>px` is its
    // spelling, and it must not come back.
    expect(body).not.toMatch(/inset 0 0 0/);
    // The slot's own dividers still ride outside it — the selected state adds
    // to the plate's rules rather than replacing them.
    expect(body).toMatch(/-1px 0 0 var\(--borderSubtle\)/);
    expect(body).toMatch(/0 -1px 0 var\(--borderSubtle\)/);
  });

  it("gives the corner slots the plate's own curve", () => {
    const css = stripCss();
    // Without this a fill painted in a corner slot squares off the plate's
    // curve, which is visible the moment any slot is pressed or hovered. One
    // pixel tighter than the plate's radius, because the plate's border takes
    // that pixel.
    expect(css).toMatch(
      /\.stat-strip > \*:first-child\s*\{[^}]*border-start-start-radius:\s*calc\(var\(--r-md\) - 1px\)/,
    );
    expect(css).toMatch(
      /\.stat-strip > \*:last-child\s*\{[^}]*border-start-end-radius:\s*calc\(var\(--r-md\) - 1px\)/,
    );
  });
});
