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
