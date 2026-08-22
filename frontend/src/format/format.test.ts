import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  formatDate,
  formatDateTime,
  formatDuration,
  formatMoney,
  formatMoneyOrAbsent,
  MONEY_ABSENT,
} from "./format";

// B-EP09.17/18/19 acceptance: locale changes the RENDERING of the same stored
// value and never the value; de-DE renders decimal-comma / dot-thousands;
// zones are IANA-only; durations are absolute; FX display never computes.

describe("money formatting (B-EP09.17)", () => {
  it("renders the same stored minor units differently per locale, value unchanged", () => {
    const stored = 123_456; // minor units, exactly as the API returns them
    const de = formatMoney(stored, "EUR", "de");
    const en = formatMoney(stored, "EUR", "en");
    expect(de).toBe("1.234,56\u00a0€");
    expect(en).toBe("€1,234.56");
    // formatting is a pure read: re-rendering the same stored value is stable,
    // and the two locales render the one value differently.
    expect(formatMoney(stored, "EUR", "de")).toBe(de);
    expect(de).not.toBe(en);
  });

  it("respects the currency's minor-unit scale", () => {
    // JPY has zero minor digits — 1234 minor units is ¥1,234, not ¥12.34
    expect(formatMoney(1234, "JPY", "en")).toContain("1,234");
  });

  // VND is a zero-decimal currency: formatMoney divides by
  // 10 ** maximumFractionDigits, which resolves to 1 here. Nothing pinned this,
  // and a regression would inflate every Vietnamese money figure 100×.
  it("renders a zero-decimal currency without shifting the decimal point", () => {
    expect(formatMoney(1_284_000, "VND", "vi")).toBe("1.284.000\u00a0₫");
  });

  it("still scales two-decimal currencies under vi", () => {
    expect(formatMoney(128_400, "EUR", "vi")).toBe("1.284,00\u00a0€");
  });
});

// Both halves of a money value are required, and neither absence has a safe
// default: an invented currency is indistinguishable from a real one, and an
// invented zero is a figure the server never sent.
describe("money with a half missing (data-semantics §1)", () => {
  it("renders a dash when the currency is absent, never a guessed one", () => {
    expect(formatMoneyOrAbsent(123_456, null, "en")).toBe(MONEY_ABSENT);
    expect(formatMoneyOrAbsent(123_456, undefined, "en")).toBe(MONEY_ABSENT);
    // The empty string is the shape that reached Intl and threw mid-render.
    expect(formatMoneyOrAbsent(123_456, "", "en")).toBe(MONEY_ABSENT);
  });

  it("renders a dash when the amount is absent, never a zero", () => {
    expect(formatMoneyOrAbsent(null, "EUR", "en")).toBe(MONEY_ABSENT);
    expect(formatMoneyOrAbsent(undefined, "EUR", "en")).toBe(MONEY_ABSENT);
    expect(formatMoneyOrAbsent(null, "EUR", "en")).not.toContain("0");
  });

  // A real zero is a figure the server DID send, and it is not the same claim
  // as an absent one.
  it("renders a stored zero as money", () => {
    expect(formatMoneyOrAbsent(0, "EUR", "en")).toBe("€0.00");
  });

  it("formats exactly as formatMoney once both halves are present", () => {
    expect(formatMoneyOrAbsent(123_456, "EUR", "de")).toBe(
      formatMoney(123_456, "EUR", "de"),
    );
  });
});

describe("date/time formatting (B-EP09.17/19)", () => {
  const instant = "2026-06-04T21:30:00Z";

  it("renders de-DE as dd.mm.yyyy", () => {
    expect(formatDate(instant, "de", "Europe/Berlin")).toBe("04.06.2026");
  });

  it("renders the same UTC instant with the correct zone per purpose", () => {
    // 21:30Z on 4 June is already 5 June in Auckland: the personal-deadline
    // zone and the workspace-reporting zone disagree on the calendar day.
    const userZone = formatDate(instant, "de", "Pacific/Auckland");
    const workspaceZone = formatDate(instant, "de", "Europe/Berlin");
    expect(userZone).toBe("05.06.2026");
    expect(workspaceZone).toBe("04.06.2026");
  });

  it("formats vi dates day-first", () => {
    expect(formatDate("2026-06-24T10:00:00Z", "vi", "Asia/Ho_Chi_Minh")).toBe(
      "24/06/2026",
    );
  });

  it("rejects fixed-offset zones — IANA names only (AC-DS-TZ4)", () => {
    expect(() => formatDate(instant, "de", "+01:00")).toThrow(/IANA/);
    expect(() => formatDateTime(instant, "de", "Etc/GMT-1")).toThrow(/IANA/);
    expect(() => formatDate(instant, "de", "GMT+1")).toThrow(/IANA/);
  });

  it("renders idle spans as absolute durations, not calendar diffs", () => {
    expect(formatDuration(62 * 86_400_000, "en")).toMatch(/62/);
    expect(formatDuration(5 * 3_600_000, "en")).toMatch(/5/);
  });
});

describe("FX display discipline (B-EP09.18)", () => {
  const source = readFileSync(
    join(dirname(fileURLToPath(import.meta.url)), "format.ts"),
    "utf8",
  );
  const explainSource = readFileSync(
    join(
      dirname(fileURLToPath(import.meta.url)),
      "..",
      "design-system",
      "explain.tsx",
    ),
    "utf8",
  );

  it("never issues a live FX call at render time", () => {
    for (const text of [source, explainSource]) {
      expect(text).not.toMatch(/fetch\s*\(|XMLHttpRequest|axios/);
    }
  });

  it("never multiplies native amounts by rates (consumes the IR base_value)", () => {
    // the lineage row fields exist for display; no arithmetic combines them
    expect(explainSource).not.toMatch(/nativeAmountMinor\s*\*|rate\s*\*/);
  });
});

// The ten codes where CLDR and ISO 4217 disagree are the ones that prove
// display and storage share a scale. A stored IQD 1234 is 1.234 dinars to the
// server, and Intl's own count would have rendered it as 1,234.
describe("display scales by the same table the server stores with", () => {
  it.each([
    ["IQD", 1234, "1.234"],
    ["MGA", 1234, "12.34"],
    ["IRR", 1234, "12.34"],
    ["VND", 18_000_000, "18,000,000"],
    ["EUR", 12_345, "123.45"],
  ])("%s %i renders the figure %s", (currency, minor, figure) => {
    // Only the digits are asserted: symbol placement and grouping are Intl's
    // to decide and are not what this pins.
    const rendered = formatMoney(minor, currency, "en").replace(/[^\d.,]/g, "");
    expect(rendered).toBe(figure);
  });
});
