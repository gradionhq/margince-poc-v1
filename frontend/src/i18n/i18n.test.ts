import { readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
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
// every key's vi and en value (not guessed): anything identical
// to en that is NOT listed here is a key the translation pass missed, which
// no other check in this file catches. Grouped so a reviewer can tell "brand
// name" from "missed translation" at a glance — an addition to any group
// must be defensible on the same grounds as its neighbours.
const KEPT_IN_ENGLISH = new Set<string>([
  // The product name of the buyer surface, and a title that is only the
  // deal's own name in a placeholder.
  "room.card.title",
  "persondealrooms.title",
  "dealbrief.room",
  "dealmeeting.untitled",
  "room.create.defaultTitle",
  // Pure punctuation layouts: every word in them is a placeholder, so there is
  // nothing to translate and a "translation" could only reorder the slots.
  "today.exchange.subjectWhen",
  // Two names and a separator: every word is a placeholder and the only
  // literal is punctuation.
  "today.route.headline",
  // Two phase names and an arrow: the same shape.
  "project.history.moved",
  // An em dash standing in for a figure nobody can compute yet. A glyph, not a
  // word — the sentence explaining it is the detail line beside it.
  "co.strip.financeUnknown",
  // Brand and provider names: proper nouns, not translated in any locale.
  "connectors.provGmail",
  "connectors.provGcal",
  "connectors.provGraph",
  "connectors.provTelegram",
  "ob.s4.provGoogle",
  "ob.s4.provMicrosoft",
  "ob.conv.connect.linkedinName",
  // "Lead" is the loanword vi uses for the record (see nav.leads).
  "dedupe.kindLead",
  // Employee-count bands: digits and an en dash, the same in every locale.
  "lead.signal.employees.1-10",
  "lead.signal.employees.11-50",
  "lead.signal.employees.51-200",
  "lead.signal.employees.201+",
  // "Email" is the Vietnamese word for email. de has "E-Mail" and differs;
  // vi does not, and inventing a difference would name the transport
  // something no Vietnamese speaker calls it.
  "person.composer.transportEmail",
  // The same proper noun as connectors.provGmail and its neighbours, one
  // surface over.
  "provider.profile.linkedin",
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

  // The RFC 5322 header name. "Bcc" is the field's identity in every mail
  // client in every locale — a translated label would name a field the
  // recipient's own client does not call that, and the placeholder beside it
  // is what carries the meaning.
  "person.composer.bcc",

  // CRM domain nouns kept in English by design (glossary, design.md §6.1):
  // "deal", "pipeline", "timeline" etc. read the same in Vietnamese usage.
  "nav.deals",
  "tab.deals",
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
  "restricted.kind.email",
  "timeline.filters.kind.email",
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
  "consumerMail.baselinePlaceholder",
  "linkedinImport.profilePlaceholder",
  "ob.conv.linkedin.profilePlaceholder",
  "ob.s4.imapHostPlaceholder",
  "ob.s4.imapEmail",
  "ob.url",
  "ob.urlScheme",
  "ob.conv.triage.companyWebsite",
  "ob.conv.clarify.question",
  "ob.conv.clarify.optionDetail",
  "create.linkedin",
  "person.enriched.field.linkedin",
  "deepread.skipRobots",
  "quotas.periodRange",

  // Units, version rows and other format-only strings: symbols/abbreviations
  // that do not translate (ms, a version-row template).
  "aicalls.ms",
  "voice.history.versionRow",
  "voice.history.deltaRow",
  "ob.conv.triage.omittedField",
  "ob.rail.tokensUnit",
  "share.ceiling.post",

  // Tab and section labels that are proper nouns in the product. The settings
  // ENTRY for the voice surface is no longer one of them — it was renamed off
  // the feature name to "Voice", which vi translates.
  "settings.voice.title",
  "co.decisions.group",
  "partner.role.hosting",

  // Actor labels built on "Agent" and "Connector", which vi carries as
  // loanwords everywhere else in this catalog — translating them only here
  // would make the same actor read as two different things.
  "trust.agentTag",
  "consent.actorAgent",
  "consent.actorConnector",
  "users.agentSeat",
  // Same reason, one level up: vi carries "AI" as the loanword throughout this
  // catalog, so spelling out "trí tuệ nhân tạo" on the settings entry alone
  // would make one subject read as two.
  "settings.tab.ai",
  // "Lead" is the loanword in both de and vi — every other lead key in this
  // catalog leaves it untranslated, and the marker on the record page names
  // the same object those keys do.
  "lead.marker",

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
    expect(translate("en", "trust.agentTag", {})).toBe("Automated by {agent}");
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

  // The allowlist above is the one hand-written list in this file, and a key
  // deleted from the catalogs leaves its entry behind silently — an exemption
  // for a string that no longer exists, which the next reader has to research
  // before they can tell it is stale.
  it("the untranslated-copy allowlist names only keys that still exist", () => {
    const stale = [...KEPT_IN_ENGLISH].filter((key) => !(key in en));
    expect(stale, `allowlist entries with no key: ${stale.join(", ")}`).toEqual(
      [],
    );
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

// The two guards below read the source tree rather than a written list of
// what is allowed, for the reason every fitness function here does: a list
// records one moment's answer and then drifts, while the tree is the answer.

const SRC_ROOT = dirname(dirname(fileURLToPath(import.meta.url)));

const CATALOG_FILES = new Set(
  ["en.ts", "de.ts", "vi.ts"].map((file) => join(SRC_ROOT, "i18n", file)),
);

/**
 * Every file that can RENDER a key: the whole source tree minus the catalogs
 * themselves and minus the tests.
 *
 * Tests are excluded deliberately. A key a test names but no screen renders is
 * exactly the dead weight the orphan check looks for, and counting tests would
 * also let this file's own allowlist vouch for keys nothing displays.
 */
function renderingFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) {
      return renderingFiles(path);
    }
    const rendersKeys =
      /\.tsx?$/.test(entry) &&
      !/\.test\.tsx?$/.test(entry) &&
      !CATALOG_FILES.has(path);
    return rendersKeys ? [path] : [];
  });
}

const RENDERING_SOURCE = renderingFiles(SRC_ROOT).map((path) =>
  readFileSync(path, "utf8"),
);

// A key written out in full, in any of the three quote styles. The character
// class is the alphabet the catalogs actually use: the enum-shaped keys carry
// ':' (lead.factor.manual:employees), '-' and '+' (the employee bands).
const QUOTED_LITERAL = /["'`]([A-Za-z0-9_.:+-]+)["'`]/g;

// The stem of a key built at runtime — t(`ob.readStatus.${status}`). Whatever
// follows the stem is a value this file cannot see, so every key under it
// counts as rendered: the alternative is a guard that tells the next person to
// delete a string a screen is displaying.
const TEMPLATE_STEM = /`([A-Za-z0-9_.]*)\$\{/g;

function renderedLiterals(): Set<string> {
  const literals = new Set<string>();
  for (const source of RENDERING_SOURCE) {
    for (const [, literal] of source.matchAll(QUOTED_LITERAL)) {
      literals.add(literal);
    }
  }
  return literals;
}

function renderedStems(): string[] {
  const stems = new Set<string>();
  for (const source of RENDERING_SOURCE) {
    for (const [, stem] of source.matchAll(TEMPLATE_STEM)) {
      // A dot is what makes a stem a key stem. Without one the template is a
      // class name, a URL or a message, and treating it as a stem would vouch
      // for the entire catalog.
      if (stem.includes(".")) {
        stems.add(stem);
      }
    }
  }
  return [...stems];
}

describe("catalog keys against the surfaces that render them", () => {
  it("every key is rendered by a source file, literally or under a stem", () => {
    const literals = renderedLiterals();
    const stems = renderedStems();
    const orphans = Object.keys(en).filter(
      (key) =>
        !literals.has(key) && !stems.some((stem) => key.startsWith(stem)),
    );
    expect(
      orphans,
      `keys translated three times and rendered nowhere: ${orphans.join(", ")}`,
    ).toEqual([]);
  });
});

/*
 * The tenant is called "organization" to a reader (A107/ADR-0061): one
 * installation serves one organization, and "workspace" is the internal
 * boundary the schema and the RBAC code use. A second noun for one thing reads
 * as two concepts the product does not have.
 *
 * Matched against VALUES only. Key names keep the internal spelling on purpose
 * — jobs.workspaceKinds, extUnits.workspace.*, captureActivity.scope.workspace
 * — because a key is an identifier the screens import, not copy anybody reads.
 *
 * "Google Workspace" is the one legitimate reading of the word: a product name
 * rather than the tenant, so the guard drops it before it looks.
 */
const TENANT_MISNOMER = /workspace|arbeitsbereich|không gian làm việc/i;
const PRODUCT_NAME = /Google Workspace/g;

describe("the product's word for the tenant", () => {
  it("no catalog calls the organization a workspace", () => {
    for (const [locale, catalog] of Object.entries(catalogs)) {
      for (const [key, value] of Object.entries(catalog)) {
        expect(
          TENANT_MISNOMER.test(value.replace(PRODUCT_NAME, "")),
          `${locale}: ${key} — "${value}" says workspace where the product says organization`,
        ).toBe(false);
      }
    }
  });
});
