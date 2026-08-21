// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { BEHAVIOUR } from "./margince-core-motion";

// The Core's material is a three-colour triple per state — --coreC1 (the light
// end), --coreC2 (the body) and --coreC3 (the second dot tone) — and every other
// value in the stylesheet is mixed from those three. That is what these cases
// hold: a state changes exactly the triple, the triple is built from tokens, and
// the vocabulary the stylesheet paints is the same one the motion table moves.
//
// State carrying colour is the design here rather than a drift, and the guard
// that matters is the other direction: colour may not REPLACE motion. Two states
// that differ only in hue are two states a reader cannot tell apart in a
// screenshot, cannot tell apart at 34px, and cannot tell apart at all if they are
// colour-blind — so every state owns a distinct movement, asserted below off the
// motion table rather than off a list written by hand.
//
// Derived from the stylesheet, so a state added later is covered without being
// added to a fixture: the assertions are about every `[data-core-state]` rule the
// file contains, whatever they turn out to be.

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

describe("the Core's material", () => {
  it("has state rules to check, so the check cannot pass by finding nothing", () => {
    // The whole suite is vacuous if the pattern stops matching — a rename of the
    // attribute would otherwise turn every assertion below green.
    expect(stateRules().length).toBeGreaterThanOrEqual(4);
  });

  it("gives every state its own complete triple", () => {
    // A state setting one or two of the three inherits the rest from idle,
    // which is how a half-themed state ends up with a red body and a green glow.
    for (const { selector, body } of stateRules()) {
      if (!/--coreC1\s*:/.test(body)) {
        // A rule that sets no colour at all is a rule about something else — the
        // feed, say — and is covered by its own cases.
        continue;
      }
      for (const stop of ["--coreC1", "--coreC2", "--coreC3"]) {
        expect(body, `${selector} must declare ${stop}`).toMatch(
          new RegExp(`${stop}\\s*:`),
        );
      }
    }
  });

  it("builds every triple out of tokens, never a literal", () => {
    // check-ds-purity greps for hex and rgb() and would catch those. It cannot
    // catch a triple built from another COMPONENT's token, which is the way this
    // primitive would quietly start following the wrong palette.
    for (const { selector, body } of stateRules()) {
      for (const [, stop, value] of body.matchAll(
        /(--coreC[123])\s*:\s*([^;]+);/g,
      )) {
        expect(value, `${selector} ${stop} must read a token`).toMatch(
          /var\(--(orb[A-Z]\w*|accent)\)/,
        );
      }
    }
  });

  it("paints the same vocabulary the motion table moves", () => {
    // Two lists that must not drift: a state the stylesheet colours but the
    // motion table does not move is a state that does not exist, and one the
    // table moves without a rule here silently wears idle's colours.
    const painted = new Set(
      [...coreCss.matchAll(/\[data-core-state="([\w-]+)"\]/g)].map(
        (match) => match[1],
      ),
    );
    for (const state of painted) {
      expect(Object.keys(BEHAVIOUR), `${state} is painted`).toContain(state);
    }
  });

  it("gives every state a movement of its own", () => {
    // The load-bearing one. Colour is allowed to carry state here BECAUSE motion
    // carries it first: if two states shared a signature they would be one state
    // wearing two hues, which is exactly what a colour-blind reader would see.
    const signatures = Object.values(BEHAVIOUR).map((behaviour) =>
      JSON.stringify([
        behaviour.level,
        behaviour.speed,
        behaviour.pulse,
        behaviour.ingest,
      ]),
    );

    expect(new Set(signatures).size).toBe(signatures.length);
  });

  it("keeps the status hues out of the sheet entirely", () => {
    // The orb's amber and red are its OWN (--orbAmber, --orbRed), not the chrome
    // tokens a notice or a destructive button paints with: those are tuned for
    // text and fills on a surface, and they go muddy as a 34px glowing ball.
    // Absent rather than merely unused, so no future rule can reach for one.
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
});

/*
 * The Core's stillness, derived from the sheet rather than from a list of class
 * names.
 *
 * Comments are stripped because a selector is read as the text before a `{`, and
 * this file explains almost every rule it declares.
 */
const sheet = coreCss.replace(/\/\*[\s\S]*?\*\//g, "");

describe("the Core's stillness", () => {
  it("declares no animation of its own, and no part travels vertically", () => {
    // Everything that moves is drawn by the engine, which parks off the
    // window-focus signal (window-focus.ts). A CSS animation declared here
    // would keep running in a blurred window, since nothing in this sheet
    // pauses one — so the sheet stays free of the property entirely, and the
    // static dress it paints holds still rather than drifting under it.
    //
    // The vertical-travel check covers every spelling: the named function, the
    // 3D form, the `translate` property, and the two-argument shorthand whose
    // SECOND argument is the y — `translate(0, 8px)` moves a part exactly as
    // far as `translateY(8px)` and would pass a check that only knew the name.
    expect(sheet).not.toMatch(/animation(-name)?\s*:/);
    expect(sheet).not.toMatch(/translateY|translate3d|translate\s*:/);
    const offsets = [...sheet.matchAll(/\btranslate\(([^)]*)\)/g)].map(
      (match) => (match[1].split(",")[1] ?? "0").trim(),
    );
    expect(offsets.filter((y) => !/^0[a-z%]*$/.test(y))).toEqual([]);
  });
});
