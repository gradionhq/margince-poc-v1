import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Dead CSS reads as intent. A rule with a comment explaining its layout is
// indistinguishable from a live one until somebody greps the sources, so the
// next person to touch a neighbouring rule has to decide whether they are
// about to break something. The onboarding sheets had carried 63 such rules —
// a whole retired wizard (`.sdot`, `.urlbar`, `.wiz-*`), the pre-`fdz`
// file-drop zone (`.dropzone`, `.dz-*`) and six `.ob-core-*` scenes — and
// nothing noticed the sixty-fourth.
//
// Both sides are derived from the tree, so a sheet added to onboarding
// tomorrow is gated the same day it lands, and NOTHING here is scoped to a
// prefix: a gate that only knew `ob-` would have walked straight past the
// dropzone set. The sibling gate in onboarding-typography.test.ts derives the
// same sheet list from the same rule.

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

const sources = sourceFiles(srcRoot).map((file) =>
  code(readFileSync(file, "utf8")),
);
const sourceText = sources.join("\n");

// Whole tokens, hyphens included, so `.ob-live` is not answered for by
// `.ob-live-card`. A test that queries `.ob-live-coverage` counts as a use:
// deleting the rule under it would break that test, which makes it live.
const namedInSource = new Set(
  [...sourceText.matchAll(/[A-Za-z][A-Za-z0-9_-]*/g)].map((m) => m[0]),
);

// A name composed at runtime — `` `ob-triage-row-${tier}` `` — is never a
// whole token anywhere. What IS in the source is the literal head of the
// template, so a declared class that continues one of those heads counts as
// reachable. Without this the gate fails on every variant class in the tree;
// with it, a family head is as wide as the family, which is the trade a
// template makes on the caller's behalf.
const runtimeFamilies = [
  ...new Set(
    [...sourceText.matchAll(/([A-Za-z][A-Za-z0-9_-]*-)\$\{/g)].map((m) => m[1]),
  ),
];

function isReachable(className: string): boolean {
  return (
    namedInSource.has(className) ||
    runtimeFamilies.some((family) => className.startsWith(family))
  );
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
    // or, if the class really is composed at runtime, give the template head a
    // trailing hyphen so the family is visible from the source.
    expect(orphans.sort()).toEqual([]);
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
