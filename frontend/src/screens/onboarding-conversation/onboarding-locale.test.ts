import { describe, expect, it } from "vitest";
import { LOCALES } from "../../i18n";
import { onboardingLocale } from "./onboarding-locale";

describe("onboarding conversation locale", () => {
  it("passes a prompted locale through untouched", () => {
    expect(onboardingLocale("en")).toBe("en");
    expect(onboardingLocale("de")).toBe("de");
  });

  it("falls back to the contract default for a locale the prompts do not cover", () => {
    // A UI catalog can ship before the prompt library follows. Sending the
    // catalog's code anyway would 422 the reader out of onboarding.
    expect(onboardingLocale("vi")).toBe("en");
  });

  // Derived from LOCALES, so a locale added to the registry is covered here
  // without editing this test — the point being that NO shipped locale can
  // put an unenumerated value on the wire.
  it("maps every shipped locale to a value the contract enumerates", () => {
    for (const locale of LOCALES) {
      expect(["en", "de"], locale).toContain(onboardingLocale(locale));
    }
  });
});
