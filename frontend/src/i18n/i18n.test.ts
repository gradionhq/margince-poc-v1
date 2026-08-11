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
import { vi as viCatalog } from "./vi";

// Keys whose vi value is identical to en on purpose. Derived by comparing
// every one of the 2846 keys' vi/en values (not guessed): anything identical
// to en that is NOT listed here is a key the translation pass missed, which
// no other check in this file catches. Grouped so a reviewer can tell "brand
// name" from "missed translation" at a glance — an addition to any group
// must be defensible on the same grounds as its neighbours.
const KEPT_IN_ENGLISH = new Set<string>([
  // Pure punctuation layouts: every word in them is a placeholder, so there is
  // nothing to translate and a "translation" could only reorder the slots.
  "today.meeting.headline",
  // Two names and a separator, or a name and an amount: every word is a
  // placeholder and the only literal is punctuation.
  "today.route.headline",
  "today.deal.headline",
  // An em dash standing in for a figure nobody can compute yet. A glyph, not a
  // word — the sentence explaining it is the detail line beside it.
  "co.strip.financeUnknown",
  // A currency figure and nothing else. Vietnamese groups digits the way en
  // does, so the only locale that differs here is de, which does.
  "agent.exampleCost",
  // Brand and provider names: proper nouns, not translated in any locale.
  "connectors.provGmail",
  "connectors.provGcal",
  "connectors.provGraph",
  "connectors.provTelegram",
  "ob.s4.provGoogle",
  "ob.s4.provMicrosoft",
  "ob.conv.connect.linkedinName",
  "co.chip.linkedin",
  "person.page.linkedin",
  "auth.coreProviderAnthropic",
  "auth.coreProviderGemini",
  "auth.coreProviderOllama",
  "auth.coreProviderOpenAI",
  "auth.coreProviderVllm",
  "overlay.userMap.principal.hubspot",
  "overlay.regionEu1",
  "overlay.budgetSources",
  "ob.ai.speaker",
  "ob.ai.speakerName",
  "auth.title",

  // CRM domain nouns kept in English by design (glossary, design.md §6.1):
  // "deal", "pipeline", "timeline" etc. read the same in Vietnamese usage.
  "nav.deals",
  "person.tab.deals",
  "deals.pipeline",
  "deal.fcPipeline",
  "record.timeline",
  "cf.obj.deal",
  "cf.obj.person",
  "cf.obj.lead",
  "co.brief.cite.deal",
  "co.brief.cite.person",
  "quotas.contributing.deal",
  "deals.unit",
  "history.actorAgent",
  "agent.title",

  // The alphabetical sort view: the Vietnamese alphabet also runs A to Z, so
  // the label names the same range in either catalog.
  "list.viewAZ",

  // Endonyms: a locale's own name for itself, identical in every catalog.
  "locale.name.en",
  "locale.name.de",
  "locale.name.vi",

  // Field labels where the English word is also the Vietnamese usage.
  "people.email",
  "create.email",
  "auth.email",
  "person.identity.email",
  "person.action.email",
  "person.memory.email",
  "person.memory.channelEmail",
  "person.rail.email",
  "settings.voice.register.email",
  "product.sku",
  "compose.cc",
  "settings.token",
  "passport.select",

  // Placeholders, examples and other machine-shaped literals: emails,
  // URLs, hostnames — content a translation would corrupt, not prose.
  "auth.emailPlaceholder",
  "users.emailPlaceholder",
  "consumerMail.domainPlaceholder",
  "linkedinImport.profilePlaceholder",
  "ob.conv.linkedin.profilePlaceholder",
  "ob.s4.imapHostPlaceholder",
  "ob.s4.imapEmail",
  "ob.url",
  "ob.urlScheme",
  "ob.s3.exampleProspect",
  "ob.conv.triage.companyWebsite",
  "ob.conv.clarify.question",
  "ob.conv.clarify.optionDetail",
  "create.linkedin",
  "person.enriched.field.linkedin",
  "deepread.skipRobots",
  "ob.live.noValue",
  "quotas.periodRange",

  // Units, version rows and other format-only strings: symbols/abbreviations
  // that do not translate (ms, a version-row template).
  "aicalls.ms",
  "voice.history.versionRow",
  "voice.history.deltaRow",
  "ob.conv.triage.omittedField",
  "ob.rail.tokensUnit",
  "share.ceiling.post",

  // Tab and section labels that are proper nouns in the product.
  "settings.tab.voice",
  "settings.tab.overlay",
  "settings.voice.title",
  "co.decisions.group",
  "partner.role.hosting",

  // Actor labels built on "Agent" and "Connector", which vi carries as
  // loanwords everywhere else in this catalog — translating them only here
  // would make the same actor read as two different things.
  "trust.agentTag",
  "consent.actorAgent",
  "consent.actorConnector",

  // Other cases verified individually against the source.
  "shell.logoAria",
  "ob.conv.connect.scopeMicrosoft",
]);

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

  // An endonym is a language's name in its OWN language, so it is the same
  // string in every catalog: the German switcher says "Tiếng Việt" too. Both
  // loops run over LOCALES — proven above to be exactly the registered
  // catalogs — so a pair compared by hand cannot leave a third catalog free to
  // translate a name it should have carried verbatim. The untranslated-leftover
  // check below cannot stand in for this one: it only flags values EQUAL to
  // English, and a translated endonym differs from English by definition.
  it("every locale has a name key, and names are endonyms shared by all catalogs", () => {
    for (const named of LOCALES) {
      const key = localeNameKey(named);
      const endonym = translate(DEFAULT_LOCALE, key);
      expect(endonym.trim(), key).not.toBe("");
      for (const reader of LOCALES) {
        expect(translate(reader, key), `${reader}: ${key}`).toBe(endonym);
      }
    }
  });

  it("every catalog interpolates {params}", () => {
    for (const locale of LOCALES) {
      const rendered = translate(locale, "trust.agentTag", {
        agent: "capture",
      });
      expect(rendered, locale).toContain("capture");
      expect(rendered, locale).not.toContain("{agent}");
    }
  });

  it("an unknown placeholder is left visible, never silently dropped", () => {
    expect(translate("en", "trust.agentTag", {})).toBe("agent: {agent}");
  });

  it("the default locale is en (A100: en-GB)", () => {
    expect(DEFAULT_LOCALE).toBe("en");
  });

  // Every other check in this file happily accepts a value that is just the
  // English string copied verbatim: key parity, non-empty and placeholder
  // parity all pass on an untranslated leftover. This is the one check that
  // actually proves the vi catalog was translated, not merely typed out.
  it("no vi value is an untranslated copy of en, outside the allowlist", () => {
    const reference: Record<string, string> = en;
    const leftovers = Object.entries(viCatalog)
      .filter(
        ([key, value]) => value === reference[key] && !KEPT_IN_ENGLISH.has(key),
      )
      .map(([key]) => key);
    expect(leftovers, `untranslated keys: ${leftovers.join(", ")}`).toEqual([]);
  });
});

describe("browser-language detection", () => {
  it("picks the first supported language, region-insensitive", () => {
    expect(detectLocale(["en-US"])).toBe("en");
    expect(detectLocale(["de-AT", "en"])).toBe("de");
    expect(detectLocale(["EN-GB"])).toBe("en");
  });

  it("recognises Vietnamese, with or without a region", () => {
    expect(detectLocale(["vi-VN"])).toBe("vi");
    expect(detectLocale(["vi"])).toBe("vi");
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
