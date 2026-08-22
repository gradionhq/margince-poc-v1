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

  // A figure finer than its currency is REFUSED, not rounded.
  //
  // Two reviewers arrived at this from opposite ends and neither could be
  // satisfied by rounding. One reported that 1.005 EUR stored 100 rather than
  // 101 — the cent a person typed, lost to binary. The other reported that
  // 1.004951 EUR stored 101 rather than 100 — a cent invented out of a value
  // below the halfway point. Both are true, and they differ only past the
  // tolerance any rounding has to pick, so any choice of tolerance is wrong for
  // one of them.
  //
  // Refusing is the only answer that never stores a number nobody typed, and it
  // is the rule this tree already reached in documentextraction: "a figure with
  // more decimals than the currency has is a misread, and silently dropping a
  // digit is how an amount becomes wrong by an order of magnitude."
  it.each([
    [1.005, "EUR"],
    [1.004951, "EUR"],
    [0.004951, "EUR"],
    [12.345, "EUR"],
    [1.0005, "KWD"],
    [12.3444951, "KWD"],
    [1.5, "VND"],
  ])("refuses %p %s, which the currency cannot hold", (major, currency) => {
    expect(toMinorUnits(major, currency)).toBeNaN();
  });

  // What the currency CAN hold still scales exactly, and that is the half the
  // string shift exists for: 1.23 * 100 is 122.99999999999999 as a multiply.
  it.each([
    [1.23, "EUR", 123],
    [8.16, "EUR", 816],
    [0.15, "EUR", 15],
    [12.345, "KWD", 12_345],
    [-1.23, "EUR", -123],
  ])("%p %s scales to %i exactly", (major, currency, want) => {
    expect(toMinorUnits(major, currency)).toBe(want);
  });

  // A credit and a charge of the same size reach the same magnitude.
  //
  // These are EXACT at their currency's scale — 0.005 KWD is five fils — so
  // they no longer exercise a rounding at all; over-precise input is refused
  // now rather than rounded. They pin the symmetry, which is the part that
  // matters and which Math.round would break by sending -0.5 to -0 and 0.5 to
  // 1. The binary artefact the half-away rounding still resolves is the one
  // the string shift leaves behind, not a decision about the input.
  it("treats a credit and a charge of one size alike", () => {
    expect(toMinorUnits(-0.005, "KWD")).toBe(-5);
    expect(toMinorUnits(0.005, "KWD")).toBe(5);
    expect(toMinorUnits(-1.23, "EUR")).toBe(-123);
    expect(toMinorUnits(1.23, "EUR")).toBe(123);
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
