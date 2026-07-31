import { describe, expect, it } from "vitest";
import { de } from "../i18n/de";
import { en } from "../i18n/en";
import { TYPE_BUDGET_MS, TYPE_START_MS, typeSpeedFor } from "./auth-core";

// Pins ADR-0076 Decision 5's motion budget: the whole reveal must reach its
// full text within TYPE_BUDGET_MS of mount. The reveal spends TYPE_START_MS
// before the first character and then one gap per REMAINING character, so the
// total is TYPE_START_MS + speed * (text.length - 1) — one fewer gap than
// there are characters. Dividing the full budget by the full length (the bug
// this pins) ignores the lead-in and overran the budget on every string.
function totalRevealMs(text: string): number {
  return TYPE_START_MS + typeSpeedFor(text) * (text.length - 1);
}

describe("typeSpeedFor", () => {
  const strings = [
    "a",
    "hi",
    "a short sentence",
    "a considerably longer sentence than the ones above it",
    en["auth.coreBoundary"],
    de["auth.coreBoundary"],
  ];

  it("keeps the whole reveal inside the budget for a representative set of strings", () => {
    for (const text of strings) {
      expect(
        totalRevealMs(text),
        `"${text}" overran the ${TYPE_BUDGET_MS}ms budget`,
      ).toBeLessThanOrEqual(TYPE_BUDGET_MS);
    }
  });

  // German is the case that matters: it runs about a quarter longer than
  // English and is the beachhead language, so it is asserted on its own
  // rather than folded anonymously into the loop above.
  it("keeps the shipped German boundary statement inside the budget", () => {
    const text = de["auth.coreBoundary"];
    expect(totalRevealMs(text)).toBeLessThanOrEqual(TYPE_BUDGET_MS);
  });

  it("clamps the per-character speed between 12ms and 42ms", () => {
    for (const text of strings) {
      const speed = typeSpeedFor(text);
      expect(speed).toBeGreaterThanOrEqual(12);
      expect(speed).toBeLessThanOrEqual(42);
    }
  });

  // The lower clamp is a floor on readability, not a budget guarantee: at 12
  // ms/gap a string long enough (roughly 156+ characters) still overruns
  // TYPE_BUDGET_MS even though the speed is "correctly" clamped. The real
  // guarantee is that the strings actually shipped today stay comfortably
  // inside the budget, which is what the two tests above pin.
  it("documents that the floor clamp alone does not guarantee the budget for an arbitrarily long string", () => {
    const veryLong = "x".repeat(200);
    expect(typeSpeedFor(veryLong)).toBe(12);
    expect(totalRevealMs(veryLong)).toBeGreaterThan(TYPE_BUDGET_MS);
  });
});
