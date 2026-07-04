import { readdirSync, readFileSync } from "node:fs";
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
      if (!file.endsWith(".tsx") || /\.test\.tsx$/.test(file)) {
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

  it("keeps literal colours in tokens.css only — everything else reads a token", () => {
    const literalColour = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch)\(/;
    for (const file of files) {
      // tokens.css is where literals live; the tests pin those literals.
      if (file.endsWith("tokens.css") || /\.test\.tsx?$/.test(file)) {
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
