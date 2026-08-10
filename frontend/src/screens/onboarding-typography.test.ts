import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// The onboarding surface once spelled 42 distinct font sizes, 9 weights, 15
// leadings and 18 trackings — 9px next to 9.5px, 0.71875rem next to 11.5px —
// because the design system tokenised the three FAMILIES and nothing else.
// The tokens exist now (tokens.css), and this is what keeps them the only
// vocabulary: the obligation is derived from the file tree, so a stylesheet
// added to onboarding tomorrow is gated the same day it lands.

const here = dirname(fileURLToPath(import.meta.url));
const conversationDir = join(here, "onboarding-conversation");

function onboardingStylesheets(): string[] {
  const own = readdirSync(here)
    .filter((name) => name.startsWith("onboarding") && name.endsWith(".css"))
    .map((name) => join(here, name));
  const conversation = readdirSync(conversationDir)
    .filter((name) => name.endsWith(".css"))
    .map((name) => join(conversationDir, name));
  return [...own, ...conversation];
}

// Declarations only, and only the ones this rule owns. Comments go first so a
// stylesheet may still quote a pixel value while explaining why a rule exists.
function declarationsOf(css: string, property: RegExp): string[] {
  const code = css.replace(/\/\*[\s\S]*?\*\//g, "");
  return [...code.matchAll(property)].map((match) => match[0]);
}

// The whole value, not a substring of it. Unanchored, `allow` passed anything
// that CONTAINED a token — `calc(var(--fs-body) + 1px)` and
// `var(--fs-body) /* really 15px */ 15px` alike — which is the one thing a
// token gate exists to refuse, so the gate agreed with itself and proved
// nothing.
function declaredValue(declaration: string): string {
  return declaration.slice(declaration.indexOf(":") + 1).trim();
}

const TYPE_RULES: ReadonlyArray<{
  what: string;
  find: RegExp;
  // What a value is allowed to be once the token vocabulary is subtracted.
  allow: RegExp;
}> = [
  {
    what: "font-size",
    find: /font-size\s*:\s*[^;}]+/g,
    allow: /^(?:var\(--fs-[\w-]+\)|inherit)$/,
  },
  {
    what: "font-weight",
    find: /font-weight\s*:\s*[^;}]+/g,
    allow: /^(?:var\(--fw-[\w-]+\)|inherit)$/,
  },
  {
    what: "line-height",
    find: /line-height\s*:\s*[^;}]+/g,
    allow: /^(?:var\(--lh-[\w-]+\)|inherit)$/,
  },
  {
    what: "letter-spacing",
    find: /letter-spacing\s*:\s*[^;}]+/g,
    allow: /^(?:var\(--tracking-[\w-]+\)|inherit|normal)$/,
  },
];

describe("the onboarding type scale", () => {
  const sheets = onboardingStylesheets();

  it("finds the stylesheets it is meant to gate", () => {
    // A miswired glob would pass every assertion below by inspecting nothing.
    expect(sheets.length).toBeGreaterThan(5);
  });

  for (const { what, find, allow } of TYPE_RULES) {
    it(`spells every ${what} as a token`, () => {
      for (const sheet of sheets) {
        const css = readFileSync(sheet, "utf8");
        for (const declaration of declarationsOf(css, find)) {
          expect(
            allow.test(declaredValue(declaration)),
            `${sheet.split("/").slice(-2).join("/")}: "${declaration.trim()}" — use the ${what} tokens from tokens.css`,
          ).toBe(true);
        }
      }
    });
  }

  // The `font:` shorthand carries family, size, weight and leading at once, so
  // a raw one slips past all four checks above.
  //
  // Subtracting the tokens and requiring nothing to remain, rather than looking
  // for digits: half the shorthand's vocabulary is keywords, so a digit hunt
  // waved through `font: italic small-caps bold large/normal serif` — six raw
  // values and not a numeral among them.
  it("spells every font shorthand as tokens", () => {
    for (const sheet of sheets) {
      const css = readFileSync(sheet, "utf8");
      for (const declaration of declarationsOf(css, /\bfont\s*:\s*[^;}]+/g)) {
        const value = declaredValue(declaration);
        const leftovers = value
          .replace(/var\(--(?:f|fs|fw|lh)-[\w-]+\)/g, "")
          .replace(/[\s/]/g, "");
        expect(
          value === "inherit" || leftovers === "",
          `${sheet.split("/").slice(-2).join("/")}: "${declaration.trim()}" — use the type tokens from tokens.css`,
        ).toBe(true);
      }
    }
  });

  // Monospace is for a string a reader compares character by character: a
  // model id, a URL, an address, a registry number, a token count. It was the
  // most-used family on this surface at 71 declarations, two thirds of them
  // over translated prose. The cap is a ratchet, not a target.
  it("keeps monospace to the identifiers that earn it", () => {
    const mono = sheets
      .map((sheet) => readFileSync(sheet, "utf8").split("--f-mono").length - 1)
      .reduce((total, count) => total + count, 0);
    expect(mono).toBeLessThanOrEqual(15);
  });
});
