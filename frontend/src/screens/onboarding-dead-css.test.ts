import { readdirSync, readFileSync } from "node:fs";
import { basename, dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Dead CSS reads as intent. A rule with a comment explaining its layout is
// indistinguishable from a live one until somebody greps the sources, so the
// next person to touch a neighbouring rule has to decide whether they are
// about to break something. The onboarding sheets had carried 63 such rules —
// a whole retired wizard (`.sdot`, `.urlbar`, `.wiz-*`), the pre-`fdz`
// file-drop zone (`.dropzone`, `.dz-*`) and six `.ob-core-*` scenes — and
// nothing noticed the sixty-fourth, which turned out to be `.email-callout`,
// dead since the wizard was deleted and hidden from the first version of this
// gate by a prefix it had no business matching.
//
// Both sheets and classes are derived from the tree, so a sheet added to
// onboarding tomorrow is gated the same day it lands, and NOTHING here is
// scoped to a prefix: a gate that only knew `ob-` would have walked straight
// past the dropzone set. The one hand-maintained list is `runtimeClasses`
// below, and it is deliberately short. The sibling gate in
// onboarding-typography.test.ts derives the same sheet list from the same rule.

const here = dirname(fileURLToPath(import.meta.url));
const conversationDir = join(here, "onboarding-conversation");
const srcRoot = join(here, "..");

function gatedStylesheets(): string[] {
  const own = readdirSync(here)
    .filter((name) => name.startsWith("onboarding") && name.endsWith(".css"))
    .map((name) => join(here, name));
  const conversation = readdirSync(conversationDir)
    .filter((name) => name.endsWith(".css"))
    .map((name) => join(conversationDir, name));
  return [...own, ...conversation];
}

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return entry.name === "node_modules" || entry.name === "dist"
        ? []
        : sourceFiles(path);
    }
    if (path === fileURLToPath(import.meta.url)) {
      // This file names dead classes in CODE, not only in prose: the allowlist
      // below is a string literal, so a gate that read itself would vouch for
      // every name it mentions and could never fail.
      return [];
    }
    return /\.(tsx?|html)$/.test(entry.name) ? [path] : [];
  });
}

// A class named in PROSE is not a class in use, and this is the difference
// that made the manual sweep necessary: `.dropzone` survived every earlier
// count because one story file's comment happens to say the word. Block
// comments go entirely; a line comment only counts when it owns its line, so
// a `//` inside a URL cannot eat the code beside it.
function code(source: string): string {
  return source
    .replace(/\/\*[\s\S]*?\*\//g, " ")
    .replace(/^[ \t]*\/\/.*$/gm, " ");
}

function declaredClasses(css: string): string[] {
  const rules = css.replace(/\/\*[\s\S]*?\*\//g, "");
  return [
    ...new Set(
      [...rules.matchAll(/\.(-?[A-Za-z_][A-Za-z0-9_-]*)/g)].map((m) => m[1]),
    ),
  ];
}

const sourcePaths = sourceFiles(srcRoot);
const sources = sourcePaths.map((file) => code(readFileSync(file, "utf8")));
const sourceText = sources.join("\n");

/**
 * The code of the ONE source file with this name, or null when there is not
 * exactly one.
 *
 * Ambiguity answers null on purpose: two files sharing a name means the
 * allowlist entry below no longer says which one composes anything, and a gate
 * that picked the first would vouch on the strength of a coin toss.
 */
function codeOfFileNamed(fileName: string): string | null {
  const matches = sourcePaths.filter((path) => basename(path) === fileName);
  const only = matches.length === 1 ? matches[0] : undefined;
  return only ? code(readFileSync(only, "utf8")) : null;
}

// Whole tokens, hyphens included, so `.ob-live` is not answered for by
// `.ob-live-card`. A test that queries `.ob-live-coverage` counts as a use:
// deleting the rule under it would break that test, which makes it live.
const namedInSource = new Set(
  [...sourceText.matchAll(/[A-Za-z][A-Za-z0-9_-]*/g)].map((m) => m[0]),
);

// A name composed at runtime — `` `ob-gate-alert is-${notice.tone}` `` — is
// never a whole token anywhere, so it cannot be found the way every other class
// here is. Harvesting the PREFIXES of template literals from the tree is what a
// first pass did, and it is too wide to hold: ANY template literal anywhere
// donates one, so `.email-callout` — dead since the wizard it belonged to was
// deleted — was vouched for by a React `key` of `` `email-${e.id}` `` on the
// person page, which styles nothing and lives in another screen entirely.
//
// A HEAD written by hand is the same mistake one size smaller. `is-` was
// entered for the three GateNotice tones and silently vouched for four more
// declared families (`is-picked`, `is-absent`, `is-leaving`, `is-argument`),
// none of which needs vouching — every one of them is a whole token in some tsx
// — and would have vouched for any `.is-*` rule anybody deleted the last
// consumer of.
//
// So the classes are ENUMERATED, and every entry carries its own proof:
//
//   `composedAs` is the expression that builds the name, and it has to still be
//   in `composedBy`'s code. Delete or rename the composing site and the entry
//   fails, which is the claim the list is actually making.
//
//   `variants` is the closed set the expression's hole can hold, spelled as the
//   source spells it, and each one has to still be in that same file. A tone
//   dropped from the union stops vouching for its rule the day it goes.
//
// An entry may name a class no sheet declares (`.is-error` takes the base
// `.ob-gate-alert` treatment and adds nothing): what the entry claims is that
// the SITE composes the name, not that a rule exists for it.
const runtimeClasses = [
  {
    composedBy: "onboarding-gate.tsx",
    head: "is-",
    composedAs: `is-\${notice.tone}`,
    variants: ["error", "paused", "resumed"],
  },
] as const;

const composedAtRuntime = new Set(
  runtimeClasses.flatMap((entry) =>
    entry.variants.map((variant) => `${entry.head}${variant}`),
  ),
);

function isReachable(className: string): boolean {
  return namedInSource.has(className) || composedAtRuntime.has(className);
}

describe("the onboarding stylesheets name only elements that exist", () => {
  const sheets = gatedStylesheets();

  it("finds the sheets and the sources it is meant to read", () => {
    // A miswired glob passes every assertion below by inspecting nothing.
    expect(sheets.length).toBeGreaterThan(5);
    expect(sources.length).toBeGreaterThan(100);
    expect(namedInSource.size).toBeGreaterThan(1000);
  });

  it("declares no class the TypeScript sources never name", () => {
    const orphans = sheets.flatMap((sheet) =>
      declaredClasses(readFileSync(sheet, "utf8"))
        .filter((className) => !isReachable(className))
        .map((className) => `${relative(srcRoot, sheet)}: .${className}`),
    );
    // Zero, not a ratchet: the sweep that armed this gate cleared the sheets,
    // so the rule is simply that a rule has an element. Delete the dead one —
    // or, if the class really is composed at runtime, add it to
    // `runtimeClasses` with the expression and the file that compose it.
    expect(orphans.sort()).toEqual([]);
  });

  it("keeps no runtime class whose composing site has stopped composing it", () => {
    // An allowlist is only as honest as its weakest entry, and its weakness is
    // never the class it names — it is the claim that something still builds
    // that name. A gate that only checked whether the CSS was still there
    // passed in the one direction that matters: the rule outliving its last
    // consumer, with the entry still vouching for it.
    const stale = runtimeClasses.flatMap((entry) => {
      const source = codeOfFileNamed(entry.composedBy);
      if (source === null) {
        return [`${entry.composedBy} is not one file under src/`];
      }
      const missing = [
        ...(source.includes(entry.composedAs) ? [] : [entry.composedAs]),
        ...entry.variants.filter((v) => !source.includes(`"${v}"`)),
      ];
      return missing.map((gone) => `${entry.composedBy} no longer has ${gone}`);
    });
    expect(stale).toEqual([]);
  });

  // A deleted rule BODY leaves a selector that still parses and still paints
  // nothing, and a count of selectors cannot see it. This is the arm that can.
  it("leaves no rule without a body behind", () => {
    for (const sheet of sheets) {
      const css = readFileSync(sheet, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
      const hollow = [...css.matchAll(/([^{}]*)\{([^{}]*)\}/g)].filter(
        ([, , body]) => body.trim() === "",
      );
      expect(
        hollow.map(([, selector]) => selector.trim()),
        `${relative(srcRoot, sheet)} has a rule with an empty body — delete the selector too`,
      ).toEqual([]);
    }
  });
});
