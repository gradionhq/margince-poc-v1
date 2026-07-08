import { describe, expect, it } from "vitest";
import { de } from "./de";
import { en } from "./en";
import { DEFAULT_LOCALE, detectLocale, translate } from "./index";

// B-EP09.16 acceptance: de and en carry the exact same key set (a missing or
// extra key in either fails), placeholders interpolate, and the default
// locale is the A100 mandate (en-GB).

describe("i18n catalogs", () => {
  it("de and en have exact key parity", () => {
    const enKeys = Object.keys(en).sort();
    const deKeys = Object.keys(de).sort();
    expect(deKeys).toEqual(enKeys);
  });

  it("no catalog value is empty", () => {
    for (const [key, value] of [...Object.entries(en), ...Object.entries(de)]) {
      expect(value.trim(), key).not.toBe("");
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
});
