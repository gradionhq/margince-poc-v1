import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import {
  formatDate,
  formatDateTime,
  formatDuration,
  formatMoney,
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
    expect(stored).toBe(123_456); // the stored value is untouched
  });

  it("respects the currency's minor-unit scale", () => {
    // JPY has zero minor digits — 1234 minor units is ¥1,234, not ¥12.34
    expect(formatMoney(1234, "JPY", "en")).toContain("1,234");
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
