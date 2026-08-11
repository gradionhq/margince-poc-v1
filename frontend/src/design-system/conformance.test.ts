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

describe("design-system conformance gates (B-EP09.1)", () => {
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
    const namespaces = [{ prefix: "auth-", home: "screens/auth.css" }];
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
