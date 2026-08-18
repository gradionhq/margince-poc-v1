// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { BEHAVIOUR } from "./margince-core-motion";
import { WINDOW_BLURRED_ATTRIBUTE } from "./window-focus";

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
    expect(stateRules().length).toBeGreaterThanOrEqual(5);
  });

  it("gives every state its own complete triple", () => {
    // A state setting one or two of the three inherits the rest from dormant,
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
    // table moves without a rule here silently wears dormant's colours.
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
    // carries it first: if two states shared an archetype they would be one state
    // wearing two hues, which is exactly what a colour-blind reader would see.
    const motions = Object.values(BEHAVIOUR).map(
      (behaviour) => behaviour.motion,
    );

    expect(new Set(motions).size).toBe(motions.length);
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
 * names: both rules below are about EVERY animated part the file happens to
 * contain, so a part added later is covered by them without being added to a
 * list first.
 *
 * Comments are stripped because a selector is read as the text before a `{`, and
 * this file explains almost every rule it declares.
 */
const sheet = coreCss.replace(/\/\*[\s\S]*?\*\//g, "");

/** Every rule body in the sheet, paired with the selectors it applies to. */
function rules(): ReadonlyArray<{ selectors: string[]; body: string }> {
  return [...sheet.matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((match) => ({
    selectors: match[1].split(",").map((one) => one.trim()),
    body: match[2],
  }));
}

/** The parts that START an animation — `animation: none` switches one off. */
function animatedSelectors(): ReadonlyArray<string> {
  const found = new Set<string>();
  for (const { selectors, body } of rules()) {
    const declared = /animation:\s*([^;]+);/.exec(body)?.[1]?.trim();
    if (declared === undefined || declared.startsWith("none")) {
      continue;
    }
    for (const selector of selectors) {
      found.add(selector);
    }
  }
  return [...found];
}

/** The parts held still while the window is blurred, without that condition. */
function pausedSelectors(): ReadonlyArray<string> {
  const prefix = `:root[${WINDOW_BLURRED_ATTRIBUTE}] `;
  return rules()
    .filter(({ body }) => /animation-play-state:\s*paused/.test(body))
    .flatMap(({ selectors }) => selectors)
    .filter((selector) => selector.startsWith(prefix))
    .map((selector) => selector.slice(prefix.length));
}

describe("the Core's stillness", () => {
  it("holds its position — no part of the Core travels vertically", () => {
    // The sphere breathes in place. A slow vertical drift on top of the breath
    // reads as the page moving rather than as the Core living, and next to copy
    // it is a bug that moves. The feed's `translateX` is the motes arriving,
    // which is the one thing here that travels on purpose.
    //
    // Every spelling of vertical travel, not the one that was written here
    // before: the named function, the 3D form, the `translate` property, and the
    // two-argument shorthand whose SECOND argument is the y — `translate(0, 8px)`
    // moves the Core exactly as far as `translateY(8px)` and would have passed a
    // check that only knew the name.
    // Two, and two is the whole set the STYLESHEET owns now: the sheen and the
    // feed. The ball's own breath moved into the engine, which parks off the same
    // signal this attribute comes from. The floor is here so a rename cannot make
    // the two assertions below vacuous.
    expect(animatedSelectors().length).toBeGreaterThanOrEqual(2);
    expect(sheet).not.toMatch(/translateY|translate3d|translate\s*:/);
    const offsets = [...sheet.matchAll(/\btranslate\(([^)]*)\)/g)].map(
      (match) => (match[1].split(",")[1] ?? "0").trim(),
    );
    expect(offsets.filter((y) => !/^0[a-z%]*$/.test(y))).toEqual([]);
  });

  it("pauses every rhythm it starts while the window has no focus", () => {
    // Nobody is watching a window behind another window. The failure this guards
    // is a part added later and left running there — one rhythm still moving
    // while the rest of the Core is still is worse than all of them moving.
    const paused = pausedSelectors();
    for (const selector of animatedSelectors()) {
      expect(paused, `${selector} must go still with the window`).toContain(
        selector,
      );
    }
  });

  it("pauses them rather than removing them", () => {
    // `animation: none` snaps the sphere to its unanimated size and brightness,
    // so clicking away from the window would jump it. Paused holds the frame it
    // reached, which is what coming back should look like.
    expect(pausedSelectors().length).toBeGreaterThanOrEqual(2);
    // And nothing in the same breath takes the animation away again. Counting the
    // paused selectors alone cannot see that: a rule may declare BOTH, and the
    // shorthand wins whichever order it is written in — the sphere would snap on
    // every blur while this assertion still read green.
    for (const { selectors, body } of rules()) {
      const blurred = selectors.some((selector) =>
        selector.startsWith(`:root[${WINDOW_BLURRED_ATTRIBUTE}]`),
      );
      if (!blurred) {
        continue;
      }
      expect(
        body,
        `${selectors.join(", ")} must pause, not remove`,
      ).not.toMatch(/animation(-name)?\s*:\s*none/);
    }
  });
});
