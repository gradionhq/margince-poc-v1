import { existsSync, readdirSync, readFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import ts from "typescript";
import { describe, expect, it } from "vitest";

// The two source-wide design gates from B-EP09.1, derived from the tree so a
// new file is enrolled the moment it exists:
//  - exactly three type families (Outfit / DM Sans / JetBrains Mono, §2) — any
//    other font-family fails the build;
//  - every colour reads from a token — literal colours live only in tokens.css.

const frontendRoot = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

function sourceFiles(dir: string): string[] {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) {
      return entry.name === "node_modules" || entry.name === "dist"
        ? []
        : sourceFiles(path);
    }
    return /\.(css|tsx?|html)$/.test(entry.name) ? [path] : [];
  });
}

const files = sourceFiles(join(frontendRoot, "src")).concat(
  join(frontendRoot, "index.html"),
);

const allowedFamilies = new Set([
  "Outfit",
  "DM Sans",
  "JetBrains Mono",
  // stack fallbacks named in the §2 token definitions
  "system-ui",
  "sans-serif",
  "ui-monospace",
  "monospace",
]);

// Every gate below reads — and most of them TypeScript-parse — the whole
// source tree, so what each costs is a function of how much source exists and
// how loaded the runner is. Vitest's 5s per-test default is sized for a unit
// test, not for a repo sweep, and it left no margin: the heaviest leg measures
// ~1.1s locally under the coverage instrumentation `fe-unit` runs with, and
// has already blown the 5s ceiling on a loaded CI runner (reported against a
// job named "vitest + coverage", so it read as a coverage failure). The suite
// budget below is an order of magnitude above that worst observed cost,
// because the tree only grows and because these scans are synchronous file
// I/O — there is no hang for a tight timeout to catch, so a generous one costs
// nothing. Declared on the suite so every gate this file grows inherits it.
const scanBudget = { timeout: 60_000 };

// The value of a JSX attribute when the source states it as a literal string.
// The role gate below needs the value itself, not the initializer's text: a
// substring test for `status` reads `role={role}` as some other role and
// `role="statusbar"` as `status`, wrong in both directions. Anything computed —
// a variable, a call, a template with a substitution — has no value this file
// can read, and returns undefined.
function literalAttributeValue(initializer: ts.Node): string | undefined {
  if (
    ts.isStringLiteral(initializer) ||
    ts.isNoSubstitutionTemplateLiteral(initializer)
  ) {
    return initializer.text;
  }
  if (ts.isJsxExpression(initializer) && initializer.expression !== undefined) {
    return literalAttributeValue(initializer.expression);
  }
  return undefined;
}

describe("design-system conformance gates (B-EP09.1)", scanBudget, () => {
  it("uses only the three §2 type families", () => {
    for (const file of files) {
      const text = readFileSync(file, "utf8");
      for (const [, families] of text.matchAll(
        /font-family\s*:\s*([^;}"']+)|fontFamily\s*:\s*"([^"]+)"/g,
      )) {
        for (const family of (families ?? "").split(",")) {
          const name = family.trim().replace(/^["']|["']$/g, "");
          if (name === "" || name.startsWith("var(")) {
            continue;
          }
          expect(
            allowedFamilies.has(name),
            `${relative(frontendRoot, file)}: font-family "${name}" is outside the three-family rule (§2)`,
          ).toBe(true);
        }
      }
    }
  });

  // B-EP09.16: no inline user-facing copy — every string the user reads comes
  // from the i18n catalogs. The walk covers JSX text nodes and the attributes
  // that reach the user (aria-label, title, placeholder, alt); fixture data
  // passed as props and non-alphabetic glyphs are not copy.
  it("has no hard-coded user-facing copy outside the i18n catalogs", () => {
    const userFacingAttrs = new Set([
      "aria-label",
      "title",
      "placeholder",
      "alt",
    ]);
    const hasWords = (text: string) => /[A-Za-z]{2,}/.test(text);
    const violations: string[] = [];

    for (const file of files) {
      // Stories (like tests) are catalog fixtures, not shipped UI: their demo
      // copy is deliberately literal — they still stay subject to the emoji and
      // colour-purity checks below, only this i18n-copy rule exempts them.
      //
      // Plus exactly ONE component, named rather than pattern-matched:
      // mcp-apps/story-hosts.tsx is Storybook-only scaffolding whose single
      // string tells a DEVELOPER to run `pnpm build` before the document story
      // can render. No user ever reads it, and it cannot carry a .stories.tsx
      // name because Storybook would then load it as a story module and fail on
      // the component exports. Keep this a NAMED file — widening it to a pattern
      // is how real drift gets in beside it.
      if (
        !file.endsWith(".tsx") ||
        /\.test\.tsx$/.test(file) ||
        /\.stories\.tsx$/.test(file) ||
        file.endsWith("mcp-apps/story-hosts.tsx")
      ) {
        continue;
      }
      const source = ts.createSourceFile(
        file,
        readFileSync(file, "utf8"),
        ts.ScriptTarget.ES2022,
        true,
        ts.ScriptKind.TSX,
      );
      const visit = (node: ts.Node) => {
        if (ts.isJsxText(node) && hasWords(node.text)) {
          const { line } = source.getLineAndCharacterOfPosition(
            node.getStart(),
          );
          violations.push(
            `${relative(frontendRoot, file)}:${line + 1} JSX text "${node.text.trim()}"`,
          );
        }
        if (
          ts.isJsxAttribute(node) &&
          userFacingAttrs.has(node.name.getText()) &&
          node.initializer &&
          ts.isStringLiteral(node.initializer) &&
          hasWords(node.initializer.text)
        ) {
          const { line } = source.getLineAndCharacterOfPosition(
            node.getStart(),
          );
          violations.push(
            `${relative(frontendRoot, file)}:${line + 1} ${node.name.getText()}="${node.initializer.text}"`,
          );
        }
        ts.forEachChild(node, visit);
      };
      visit(source);
    }
    expect(violations, violations.join("\n")).toEqual([]);
  });

  // B-EP09.20 (Lucide-only glyphs) + B-EP09.8 (offline honesty): UI glyphs
  // come from lucide-react — the sanctioned 🟢/🟡 autonomy semantics render
  // through the .dot token component, so NO emoji may appear in any source
  // string or JSX text. The service worker never caches or fabricates /v1.
  it("uses no emoji glyphs in source strings — Lucide only (§2b)", () => {
    const emoji = /[\u{1F300}-\u{1FAFF}\u{2600}-\u{27BF}]/u;
    const violations: string[] = [];
    for (const file of files) {
      if (
        !/\.(tsx|ts)$/.test(file) ||
        /\.test\.tsx?$/.test(file) ||
        file.endsWith(".d.ts")
      ) {
        continue;
      }
      const source = ts.createSourceFile(
        file,
        readFileSync(file, "utf8"),
        ts.ScriptTarget.ES2022,
        true,
        file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS,
      );
      const visit = (node: ts.Node) => {
        const isText =
          ts.isStringLiteral(node) ||
          ts.isNoSubstitutionTemplateLiteral(node) ||
          ts.isJsxText(node);
        if (isText && emoji.test(node.text)) {
          violations.push(
            `${relative(frontendRoot, file)}: "${node.text.trim()}"`,
          );
        }
        ts.forEachChild(node, visit);
      };
      visit(source);
    }
    expect(violations, violations.join("\n")).toEqual([]);
  });

  // No service worker, in both halves: no script to install and no call that
  // would install one. The previous worker cached the app shell cache-first
  // under a cache name that never changed between builds, so a browser that
  // loaded the app once kept serving that build's index.html — and the
  // content-hashed bundle it named — past every deploy after it. A worker is
  // the only thing that can answer a request from Cache Storage, so the honest
  // gate is that the app ships none.
  it("ships no service worker, and registers none", () => {
    expect(existsSync(join(frontendRoot, "public", "sw.js"))).toBe(false);
    const main = readFileSync(join(frontendRoot, "src", "main.tsx"), "utf8");
    expect(main).not.toMatch(/serviceWorker\.register\(/);
  });

  it("the web-app manifest is valid and complete for installability", () => {
    const manifest = JSON.parse(
      readFileSync(
        join(frontendRoot, "public", "manifest.webmanifest"),
        "utf8",
      ),
    );
    expect(manifest.name).toBe("Margince");
    expect(manifest.start_url).toBe("/");
    expect(manifest.display).toBe("standalone");
    expect(manifest.icons.length).toBeGreaterThanOrEqual(1);
  });

  // One stylesheet per class namespace.
  //
  // Two sheets declaring the same class collide at equal specificity, and the
  // winner is whichever one the bundler injected last — an ordering no source
  // file states and no import expresses. So one sheet's `margin: 4px` silently
  // beats the other's `margin: 20px`, and the symptom surfaces as spacing that is
  // wrong on one screen and right on its sibling.
  //
  // Unreadable by inspection: a duplicate declaration is not a syntax error and
  // both files are correct on their own. Hence a gate over the tree rather than a
  // rule someone has to remember while editing either sheet.
  it("declares each screen's class namespace in exactly one stylesheet", () => {
    const namespaces = [
      { prefix: "auth-", home: "screens/auth.css" },
      // Registered the day dedupe.css was created, because this namespace is
      // what the rule was written about: the whole `dedupe-*` block lived in
      // onboarding.css, a sheet the screen that draws it never imports, so it
      // was styled by accident in the app and not at all in an isolated render.
      { prefix: "dedupe-", home: "screens/dedupe.css" },
    ];
    const violations: string[] = [];
    for (const file of files) {
      if (!file.endsWith(".css")) {
        continue;
      }
      const path = relative(frontendRoot, file).replace(/\\/g, "/");
      const text = readFileSync(file, "utf8");
      // Selectors only: a `.auth-shell` inside a comment is a cross-reference,
      // which is exactly how the two onboarding sheets cite this surface.
      const declarations = text.replace(/\/\*[\s\S]*?\*\//g, "");
      for (const { prefix, home } of namespaces) {
        if (path.endsWith(home)) {
          continue;
        }
        for (const [selector] of declarations.matchAll(
          new RegExp(`\\.${prefix}[\\w-]+`, "g"),
        )) {
          violations.push(
            `${path}: declares ${selector} — the ${prefix}* namespace belongs to ${home}`,
          );
        }
      }
    }
    expect(violations, violations.join("\n")).toEqual([]);
  });

  // One spelling of the button. `Button` (design-system/atoms.tsx) is what
  // emits `btn` — a `className` that spells the base class itself is a
  // hand-rolled copy of it, and a copy is frozen at the day it was written: the
  // width floor, the focus ring, the icon sizing and the shared control height
  // all landed on Button and reached none of the ten copies this gate was
  // written to clear.
  //
  // The rule is deliberately narrow so it states its own exception. It matches
  // the `btn` BASE token only — a `.btn-*` modifier in a STYLESHEET is how the
  // variants are declared, and a component class that merely ends in `btn`
  // (`iconbtn`, `lt-btn`) is a different control. And it matches every element
  // EXCEPT an anchor: `Button` renders a `<button>`, so a link that looks like
  // a button (screens/client.tsx's "create a lead" href) has no component to
  // reach for and is legitimately styled by hand.
  it("renders every button through Button — no hand-rolled btn classes", () => {
    const violations: string[] = [];
    for (const file of files) {
      // atoms.tsx is Button's own file: it is where `btn` is minted.
      if (!file.endsWith(".tsx") || file.endsWith("design-system/atoms.tsx")) {
        continue;
      }
      const source = ts.createSourceFile(
        file,
        readFileSync(file, "utf8"),
        ts.ScriptTarget.ES2022,
        true,
        ts.ScriptKind.TSX,
      );
      const visit = (node: ts.Node) => {
        if (ts.isJsxAttribute(node) && node.name.getText() === "className") {
          const element = node.parent.parent;
          const tag = element.tagName.getText();
          // Every literal fragment the className can evaluate to, so a
          // conditional or an interpolated class list is read too.
          const fragments: string[] = [];
          const collect = (child: ts.Node) => {
            if (
              ts.isStringLiteral(child) ||
              ts.isTemplateLiteralToken(child) ||
              ts.isJsxText(child)
            ) {
              fragments.push(child.text);
            }
            ts.forEachChild(child, collect);
          };
          if (node.initializer) {
            collect(node.initializer);
          }
          const handRolled = fragments.some((fragment) =>
            fragment.split(/\s+/).includes("btn"),
          );
          if (handRolled && tag !== "a") {
            const { line } = source.getLineAndCharacterOfPosition(
              node.getStart(),
            );
            violations.push(
              `${relative(frontendRoot, file)}:${line + 1} <${tag}> spells the btn class by hand — import Button from design-system/atoms`,
            );
          }
        }
        ts.forEachChild(node, visit);
      };
      visit(source);
    }
    expect(violations, violations.join("\n")).toEqual([]);
  });

  // The card equivalent of the button rule above, and it exists for the same
  // reason: `Card` owns five chrome values — elevated ground, a subtle border,
  // the 12px radius, one padding, and the inset variant — and a surface that
  // spells `card` by hand keeps whichever of the five were true the day it was
  // written. Thirteen sites had drifted that way across the public booking page,
  // the extension client, the preference centre, the OAuth consent screen, Home
  // and one of two adjacent skeletons on the company record — where the OTHER
  // skeleton, forty lines up, was a real Card.
  //
  // Narrow, so it states its own exception. It matches the `card` and
  // `card-inset` BASE tokens only: a component class that merely contains the
  // word (`auth-card`, `staging-card`, `digest-card`, `co-card`, `dedupe-card`)
  // is a different surface, exactly as `iconbtn` is a different control. And it
  // spares an element that declares a role `Card` cannot express: the component
  // admits `role="status"` and nothing else, on purpose — a card must not be
  // able to claim it is a modal — so a surface that has to announce itself as a
  // `dialog` (app/fab.tsx's anchored panel) or a `note`
  // (design-system/explain.tsx's popover) has no component to reach for. Both
  // say so in-source where they do it. The exemption reads the role's LITERAL
  // value and compares it exactly to `status`. A role the source computes
  // (`role={role}`) is NOT an exemption: the gate cannot know what it evaluates
  // to, so it asks rather than assumes — an unreadable role that waved the card
  // through would be the one surface nobody was checking.
  it("renders every card through Card — no hand-rolled card classes", () => {
    const violations: string[] = [];
    for (const file of files) {
      // atoms.tsx is Card's own file: it is where `card` is minted.
      if (!file.endsWith(".tsx") || file.endsWith("design-system/atoms.tsx")) {
        continue;
      }
      const source = ts.createSourceFile(
        file,
        readFileSync(file, "utf8"),
        ts.ScriptTarget.ES2022,
        true,
        ts.ScriptKind.TSX,
      );
      const visit = (node: ts.Node) => {
        if (ts.isJsxAttribute(node) && node.name.getText() === "className") {
          const element = node.parent.parent;
          const tag = element.tagName.getText();
          const fragments: string[] = [];
          const collect = (child: ts.Node) => {
            if (
              ts.isStringLiteral(child) ||
              ts.isTemplateLiteralToken(child) ||
              ts.isJsxText(child)
            ) {
              fragments.push(child.text);
            }
            ts.forEachChild(child, collect);
          };
          if (node.initializer) {
            collect(node.initializer);
          }
          const handRolled = fragments.some((fragment) => {
            const tokens = fragment.split(/\s+/);
            return tokens.includes("card") || tokens.includes("card-inset");
          });
          const declaresOtherRole = node.parent.properties.some((property) => {
            if (
              !ts.isJsxAttribute(property) ||
              property.name.getText() !== "role" ||
              property.initializer === undefined
            ) {
              return false;
            }
            const role = literalAttributeValue(property.initializer);
            return role !== undefined && role !== "status";
          });
          if (handRolled && !declaresOtherRole) {
            const { line } = source.getLineAndCharacterOfPosition(
              node.getStart(),
            );
            violations.push(
              `${relative(frontendRoot, file)}:${line + 1} <${tag}> spells the card class by hand — import Card from design-system/atoms`,
            );
          }
        }
        ts.forEachChild(node, visit);
      };
      visit(source);
    }
    expect(violations, violations.join("\n")).toEqual([]);
  });

  it("keeps literal colours in tokens.css only — everything else reads a token", () => {
    const literalColour = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch)\(/;
    for (const file of files) {
      // tokens.css is where literals live (tests pin them); index.html's
      // meta theme-color cannot read a CSS custom property.
      //
      // provider-mark.tsx is the one component exemption, and it is a NAMED
      // file rather than a widened pattern on purpose: it carries Google's and
      // Microsoft's own sign-in marks. Another company's colours are not ours
      // to tokenise, and a provider mark rendered in Ledger Green is a wrong
      // mark. The same single entry is in scripts/check-ds-purity.sh, so
      // neither arm of this gate can be satisfied without the other.
      if (
        file.endsWith("tokens.css") ||
        file.endsWith("index.html") ||
        file.endsWith("provider-mark.tsx") ||
        /\.test\.tsx?$/.test(file)
      ) {
        continue;
      }
      const text = readFileSync(file, "utf8");
      for (const [index, line] of text.split("\n").entries()) {
        expect(
          literalColour.test(line),
          `${relative(frontendRoot, file)}:${index + 1} hard-codes a colour — read it from a token`,
        ).toBe(false);
      }
    }
  });
});
