import { describe, expect, it } from "vitest";
import { minorUnitDigits, toMajorUnits, toMinorUnits } from "./minorunits";

// The defect this module closes: eleven call sites hard-coded 100, so a dong
// price typed as 18,000,000 was stored as 1,800,000,000. The zero-decimal rows
// are the point of the table, not an edge case.
describe("the scale between what a person types and what we store", () => {
  it.each([
    ["EUR", 2],
    ["USD", 2],
    ["GBP", 2],
    ["VND", 0],
    ["JPY", 0],
    ["KRW", 0],
    ["KWD", 3],
    ["BHD", 3],
    ["CLF", 4],
  ])("%s carries %i minor digits", (currency, digits) => {
    expect(minorUnitDigits(currency)).toBe(digits);
  });

  it.each([
    ["EUR", 95_000, 9_500_000],
    ["VND", 18_000_000, 18_000_000],
    ["JPY", 950_000, 950_000],
    ["KWD", 95, 95_000],
  ])("%s: %i typed becomes %i stored", (currency, major, minor) => {
    expect(toMinorUnits(major, currency)).toBe(minor);
  });

  it("round-trips every currency it scales", () => {
    for (const currency of ["EUR", "VND", "JPY", "KWD", "CLF"]) {
      const stored = toMinorUnits(1234, currency);
      expect(toMajorUnits(stored, currency)).toBe(1234);
    }
  });

  it("rounds a typed amount rather than truncating a cent", () => {
    expect(toMinorUnits(12.345, "EUR")).toBe(1235);
    expect(toMinorUnits(12.344, "EUR")).toBe(1234);
  });

  // The cases binary floating point loses. `1.005 * 100` is
  // 100.49999999999999, so a multiply-then-round drops the cent the person
  // typed exactly; shifting the decimal point in the TEXT cannot.
  it.each([
    [1.005, "EUR", 101],
    [8.165, "EUR", 817],
    [1.0005, "KWD", 1001],
    [0.145, "EUR", 15],
  ])(
    "%f %s scales to %i without losing the last unit",
    (major, currency, want) => {
      expect(toMinorUnits(major, currency)).toBe(want);
    },
  );

  // A credit and a charge of the same size must scale to the same magnitude.
  // Math.round sends -0.5 to -0 and 0.5 to 1, which makes them differ by one.
  it("rounds a half away from zero in both directions", () => {
    expect(toMinorUnits(-12.345, "EUR")).toBe(-1235);
    expect(toMinorUnits(12.345, "EUR")).toBe(1235);
    expect(toMinorUnits(-0.005, "EUR")).toBe(-1);
    expect(toMinorUnits(0.005, "EUR")).toBe(1);
  });

  // A pasted overflowing exponent parses to Infinity, which is not NaN.
  //
  // The answer is NaN and NOT zero, which was the first version of this: a
  // caller building a request body writes `amount_minor: 0` for a garbage
  // input, and zero is a perfectly legal price. NaN serialises to `null`, which
  // the nullable money fields accept as "unpriced" and the non-nullable ones
  // refuse — either way the API decides, not a silent default.
  it.each([Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, Number.NaN])(
    "refuses %p rather than sending it to the API",
    (major) => {
      expect(toMinorUnits(major, "EUR")).toBeNaN();
    },
  );

  // Above 2^53 the scaling multiply stops being exact, so a figure would arrive
  // ALTERED rather than refused — the one outcome worse than a rejection.
  it.each([
    [1e15, "EUR"],
    [1e14, "KWD"],
    [Number.MAX_SAFE_INTEGER, "EUR"],
  ])(
    "refuses %p %s, which cannot survive scaling exactly",
    (major, currency) => {
      expect(toMinorUnits(major, currency)).toBeNaN();
    },
  );

  // And the boundary holds on the safe side: a large but exact amount goes.
  it("still scales a large amount that stays exact", () => {
    expect(toMinorUnits(1_000_000_000, "EUR")).toBe(100_000_000_000);
    // The interesting half: a zero-decimal currency needs no scaling, so the
    // largest exact integer there is survives it and must NOT be refused.
    expect(toMinorUnits(Number.MAX_SAFE_INTEGER, "VND")).toBe(
      Number.MAX_SAFE_INTEGER,
    );
  });

  // A code Intl cannot place still has an amount attached to it. Two digits is
  // ISO's own default; throwing would leave the caller storing raw digits.
  it.each(["", "E", "not-a-currency"])(
    "an unusable code %o answers the ISO default rather than throwing",
    (currency) => {
      expect(minorUnitDigits(currency)).toBe(2);
      expect(toMinorUnits(10, currency)).toBe(1000);
    },
  );
});
