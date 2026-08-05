// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { coreBufferSize } from "./margince-core-liquid";

// The Core is the product's identity, not a status light. It is ALWAYS the brand
// green: a sphere that turns amber or red reads as the brand changing character,
// and a grey one reads as switched off. State is carried in the rhythm — how fast
// it breathes — and the condition itself is stated in words beside every Core
// (the workbench status line, the read theatre's phase, the gate's notice) and on
// a small status dot that IS allowed the danger and success hues.
//
// Derived from the stylesheet rather than from a list, so a state added later
// cannot quietly reintroduce a colour: the assertion is about every
// `[data-core-state]` rule the file contains, whatever they turn out to be.

const here = dirname(fileURLToPath(import.meta.url));
const coreCss = readFileSync(join(here, "margince-core.css"), "utf8");

/** Every `.core[data-core-state="…"]` rule block in the sheet, body included. */
function stateRules(): ReadonlyArray<{ selector: string; body: string }> {
  const rules: Array<{ selector: string; body: string }> = [];
  const pattern = /([^{}]*\[data-core-state[^{}]*)\{([^{}]*)\}/g;
  for (const match of coreCss.matchAll(pattern)) {
    rules.push({ selector: match[1].trim(), body: match[2] });
  }
  return rules;
}

describe("the Core's colour", () => {
  it("has state rules to check, so the check cannot pass by finding nothing", () => {
    // The whole suite is vacuous if the pattern stops matching — a rename of the
    // attribute would otherwise turn every assertion below green.
    expect(stateRules().length).toBeGreaterThanOrEqual(5);
  });

  it("is never set by a state: no state rule declares a tint", () => {
    const offenders = stateRules()
      .filter(({ body }) => /--coreTint\s*:/.test(body))
      .map(({ selector }) => selector);

    expect(offenders).toEqual([]);
  });

  it("is never re-weighted by a state either", () => {
    // --coreTintMix is how hard the shader pulls toward the tint. A state that
    // raised it would deepen the same green into a different-looking material,
    // which is the same defect wearing the accent's name.
    const offenders = stateRules()
      .filter(({ body }) => /--coreTintMix\s*:/.test(body))
      .map(({ selector }) => selector);

    expect(offenders).toEqual([]);
  });

  it("resolves to the brand accent, at the glass weight", () => {
    // The one place the tint IS set: the primitive's own defaults.
    const root = /\.core\s*\{([\s\S]*?)\n\}/.exec(coreCss);
    expect(root).not.toBeNull();
    const body = root?.[1] ?? "";
    expect(body).toMatch(/--coreTint:\s*var\(--accent\)\s*;/);
    expect(body).toMatch(/--coreTintMix:\s*0\.22\s*;/);
  });

  it("keeps the status hues out of the sheet entirely", () => {
    // Not just unused by the state rules — absent, so no future rule can reach
    // for one without this failing.
    for (const token of [
      "--success",
      "--warn",
      "--danger",
      "--textTertiary",
      "--textMuted",
    ]) {
      expect(coreCss).not.toContain(`var(${token})`);
    }
  });

  it("builds every colour in the liquid out of the tint uniform", () => {
    // Derived from the shader rather than from a list of stop names, because the
    // failure this guards against is someone ADDING a stop. A new one that hard
    // -codes its rgb would be invisible to a test that only checked the three
    // that existed when it was written — and the emissive stops are the tempting
    // place to do it, since an emitter wants more chroma than the tint carries.
    const shader = readFileSync(join(here, "margince-core-liquid.tsx"), "utf8");
    const stops = [...shader.matchAll(/vec3\s+(c[A-Z]\w*)\s*=([^;]+);/g)];
    expect(stops.length).toBeGreaterThanOrEqual(3);
    for (const [, name, definition] of stops) {
      expect(definition, `${name} must be built from the tint`).toContain(
        "uTintMix",
      );
    }
  });

  it("still distinguishes the states by rhythm", () => {
    // Colour is gone, so the beat is the whole vocabulary: if these collapsed to
    // one value the Core would stop saying anything at all.
    const beats = new Set(
      [...coreCss.matchAll(/--coreBeat:\s*([^;]+);/g)].map((m) => m[1].trim()),
    );
    expect(beats.size).toBeGreaterThanOrEqual(5);
  });
});

describe("the Core's render buffer", () => {
  it("follows the displayed size, so a big Core gets a real interior", () => {
    // The whole point of deriving it: a 126px workbench orb and a 172px hero
    // must not share one resolution, because the fixed 80 they used to share was
    // the ceiling on how fine the liquid's threads could be.
    expect(coreBufferSize(126)).toBeLessThan(coreBufferSize(172));
  });

  it("never asks for more fragments than the sphere can show", () => {
    // A caller that sizes a Core to fill a page must not buy a 900px buffer for
    // a subject that is blurred glass.
    expect(coreBufferSize(2000)).toBe(160);
    expect(coreBufferSize(400)).toBe(160);
  });

  it("never drops below the size where filaments hold together", () => {
    // Under this the ridge band aliases into sparkle: the threads stop reading
    // as threads and start reading as noise on the glass.
    expect(coreBufferSize(20)).toBe(96);
    expect(coreBufferSize(96)).toBe(96);
  });

  it("survives being asked before the first layout", () => {
    // `clientWidth` is 0 until the element is laid out, and NaN is what a torn
    // -down node hands back. Neither may produce a 0×0 canvas — that renders as
    // nothing, which is indistinguishable from a broken Core.
    for (const bad of [0, -1, Number.NaN, Number.POSITIVE_INFINITY]) {
      expect(coreBufferSize(bad)).toBeGreaterThanOrEqual(96);
      expect(coreBufferSize(bad)).toBeLessThanOrEqual(160);
    }
  });
});
