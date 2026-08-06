import { describe, expect, it } from "vitest";
import { en } from "./en";
import {
  catalogs,
  DEFAULT_LOCALE,
  detectLocale,
  LOCALES,
  localeNameKey,
  translate,
} from "./index";

// Every invariant below derives from `catalogs`, so a locale added to the
// registry is covered without editing this file. That is the point: a
// hand-maintained locale list is a list that drifts.

function placeholders(message: string): string[] {
  return [...message.matchAll(/\{(\w+)\}/g)].map((match) => match[1]).sort();
}

describe("i18n catalogs", () => {
  it("LOCALES lists exactly the registered catalogs", () => {
    expect([...LOCALES].sort()).toEqual(Object.keys(catalogs).sort());
  });

  it("every catalog has exact key parity with en", () => {
    const expected = Object.keys(en).sort();
    for (const [locale, catalog] of Object.entries(catalogs)) {
      expect(Object.keys(catalog).sort(), locale).toEqual(expected);
    }
  });

  it("no catalog value is empty", () => {
    for (const [locale, catalog] of Object.entries(catalogs)) {
      for (const [key, value] of Object.entries(catalog)) {
        expect(value.trim(), `${locale}: ${key}`).not.toBe("");
      }
    }
  });

  // A translation that drops {count} passes key parity, passes the non-empty
  // check, compiles, and ships a label with a hole in it. Nothing else catches it.
  it("every catalog carries the same placeholders as en", () => {
    const reference: Record<string, string> = en;
    for (const [locale, catalog] of Object.entries(catalogs)) {
      for (const [key, value] of Object.entries(catalog)) {
        expect(placeholders(value), `${locale}: ${key}`).toEqual(
          placeholders(reference[key]),
        );
      }
    }
  });

  it("every locale has a name key, and names are endonyms shared by all catalogs", () => {
    for (const locale of LOCALES) {
      const key = localeNameKey(locale);
      expect(translate("en", key)).toBe(translate("de", key));
      expect(translate("en", key).trim()).not.toBe("");
    }
  });

  it("both locales interpolate {params}", () => {
    expect(translate("en", "trust.agentTag", { agent: "capture" })).toBe(
      "agent: capture",
    );
    expect(translate("de", "trust.agentTag", { agent: "capture" })).toBe(
      "Agent: capture",
    );
  });

  it("an unknown placeholder is left visible, never silently dropped", () => {
    expect(translate("en", "trust.agentTag", {})).toBe("agent: {agent}");
  });

  it("the default locale is en (A100: en-GB)", () => {
    expect(DEFAULT_LOCALE).toBe("en");
  });
});

describe("browser-language detection", () => {
  it("picks the first supported language, region-insensitive", () => {
    expect(detectLocale(["en-US"])).toBe("en");
    expect(detectLocale(["de-AT", "en"])).toBe("de");
    expect(detectLocale(["EN-GB"])).toBe("en");
  });

  it("skips unsupported languages to the first one we ship", () => {
    expect(detectLocale(["fr-FR", "es", "en-US"])).toBe("en");
  });

  it("falls back to the A100 default when nothing matches or the list is empty", () => {
    expect(detectLocale(["fr", "ja"])).toBe(DEFAULT_LOCALE);
    expect(detectLocale([])).toBe(DEFAULT_LOCALE);
  });

  it("never matches an inherited Object property", () => {
    expect(detectLocale(["constructor"])).toBe(DEFAULT_LOCALE);
    expect(detectLocale(["toString"])).toBe(DEFAULT_LOCALE);
  });
});
