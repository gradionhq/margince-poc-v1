import { describe, expect, it } from "vitest";
import { de } from "./de";
import { en } from "./en";
import { DEFAULT_LOCALE, translate } from "./index";

// B-EP09.16 acceptance: de and en carry the exact same key set (a missing or
// extra key in either fails), placeholders interpolate, and the default
// locale is the A24 mandate (de-DE).

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

  it("the default locale is de (A24: de-DE)", () => {
    expect(DEFAULT_LOCALE).toBe("de");
  });
});
