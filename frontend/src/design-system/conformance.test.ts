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
  // `dialog` or a `note` (design-system/explain.tsx's popover) has no component
  // to reach for. Such a surface says so in-source where it does it. The exemption reads the role's LITERAL
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

// ---------------------------------------------------------------------------
// Motion, source-wide. Both gates below exist because a reduced-motion promise
// is invisible when it is broken: the reader who asked for less motion is not
// the reader running the app in development, so nothing catches it by eye.
// ---------------------------------------------------------------------------

const cssFiles = files.filter((file) => file.endsWith(".css"));

/**
 * The rules inside every `prefers-reduced-motion: reduce` block of one
 * stylesheet, as (selector, property) pairs with the offset they close at.
 *
 * A hand-rolled scan rather than a parser: these stylesheets are biome-formatted,
 * so a rule is a selector list, `{`, declarations, `}`, and the one nesting that
 * occurs is the media block itself. A parser would be the right answer if this
 * had to survive arbitrary CSS; it has to survive THIS tree, which the gate also
 * keeps formatted.
 */
function reducedMotionRules(
  text: string,
): { selector: string; property: string; endsAt: number }[] {
  const out: { selector: string; property: string; endsAt: number }[] = [];
  const opener = /@media[^{]*prefers-reduced-motion:\s*reduce[^{]*\{/g;
  for (let match = opener.exec(text); match; match = opener.exec(text)) {
    // Walk to the matching close brace of the media block.
    let depth = 1;
    let index = match.index + match[0].length;
    const start = index;
    while (index < text.length && depth > 0) {
      if (text[index] === "{") depth += 1;
      if (text[index] === "}") depth -= 1;
      index += 1;
    }
    const body = text.slice(start, index - 1);
    const rule = /([^{}]+)\{([^{}]*)\}/g;
    for (let inner = rule.exec(body); inner; inner = rule.exec(body)) {
      const selectors = inner[1]
        .replace(/\/\*[\s\S]*?\*\//g, "")
        .split(",")
        .map((one) => one.trim().replace(/\s+/g, " "))
        .filter(Boolean);
      const properties = inner[2]
        .split(";")
        .map((line) => line.split(":")[0].trim())
        .filter((name) => /^[a-z-]+$/.test(name));
      for (const selector of selectors) {
        for (const property of properties) {
          out.push({ selector, property, endsAt: index });
        }
      }
    }
  }
  return out;
}

/**
 * The stylesheet with every at-rule BLOCK blanked to spaces of the same length,
 * so offsets still line up with the original text.
 *
 * Blanking rather than deleting is what lets the two collectors here be compared
 * by position at all. And it has to happen before rules are matched: a regex that
 * skipped media blocks by anchoring on the preceding `}` matched only every
 * OTHER rule, because the brace it anchors on is consumed by the match before it
 * — which silently exempted half of every stylesheet from both gates below.
 */
function withoutAtRuleBlocks(text: string): string {
  const out = text.split("");
  const opener = /@[a-z-]+[^{]*\{/g;
  for (let match = opener.exec(text); match; match = opener.exec(text)) {
    let depth = 1;
    let index = match.index + match[0].length;
    while (index < text.length && depth > 0) {
      if (text[index] === "{") depth += 1;
      if (text[index] === "}") depth -= 1;
      index += 1;
    }
    for (let blank = match.index; blank < index; blank += 1) {
      if (out[blank] !== "\n") {
        out[blank] = " ";
      }
    }
    opener.lastIndex = index;
  }
  return out.join("");
}

/** Every top-level rule in a stylesheet, with where its selector starts. */
function plainRules(
  text: string,
): { selector: string; body: string; at: number }[] {
  const out: { selector: string; body: string; at: number }[] = [];
  const rule = /([^{}]+)\{([^{}]*)\}/g;
  const flat = withoutAtRuleBlocks(text);
  for (let match = rule.exec(flat); match; match = rule.exec(flat)) {
    const selectors = match[1]
      .replace(/\/\*[\s\S]*?\*\//g, "")
      .split(",")
      .map((one) => one.trim().replace(/\s+/g, " "))
      .filter(Boolean);
    for (const selector of selectors) {
      out.push({ selector, body: match[2], at: match.index });
    }
  }
  return out;
}

describe("motion", () => {
  // The defect this pins was live twice at once: `.rail .ws-name` and
  // `.accountsub` each had a reduce rule that a later plain rule of IDENTICAL
  // specificity overrode, because a media query adds none — so the label kept
  // animating its width and the theme flyout kept flying for exactly the readers
  // who had asked them not to. Both stylesheets read as if the promise were kept.
  it("declares every reduced-motion rule after the rule it has to beat", () => {
    for (const file of cssFiles) {
      const text = readFileSync(file, "utf8");
      const plain = plainRules(text);
      for (const { selector, property, endsAt } of reducedMotionRules(text)) {
        const defeated = plain.find(
          (rule) =>
            rule.selector === selector &&
            rule.at > endsAt &&
            new RegExp(`(^|;|\\s)${property}\\s*:`).test(rule.body),
        );
        expect(
          defeated,
          `${relative(frontendRoot, file)}: the reduced-motion rule for \`${selector}\` ` +
            `sets \`${property}\`, and \`${selector}\` sets it again at the same ` +
            `specificity further down the file — document order settles the tie, so ` +
            `the reduced-motion rule loses. Move it after the rule it removes.`,
        ).toBeUndefined();
      }
    }
  });

  // An infinite animation is the one kind that cannot be waited out: a pulse or
  // a shimmer that nobody asked for runs for as long as the surface is on
  // screen. The finite ones are a smaller promise and are tracked separately.
  it("gives every infinite animation a reduced-motion answer", () => {
    for (const file of cssFiles) {
      const text = readFileSync(file, "utf8");
      // Per SELECTOR, not per file: a stylesheet that carries one reduce block
      // and three pulses would otherwise read as covered. The gate asks the
      // question a reader would — is THIS animation answered.
      const covered = new Set(
        reducedMotionRules(text).map((rule) => rule.selector),
      );
      for (const rule of plainRules(text)) {
        if (!/animation[^;]*\binfinite\b/.test(rule.body)) {
          continue;
        }
        // A component may switch its own motion in JS instead; `data-motion` is
        // the tree's convention for that and is pinned by its own suite.
        if (/data-motion/.test(text)) {
          continue;
        }
        // An ANCESTOR counts, and is usually the better answer: the Core hides
        // the whole mote field (`.core-feed { display: none }`) rather than
        // stopping twelve individual particles, which leaves nothing frozen
        // mid-flight.
        const answered = [...covered].some(
          (named) =>
            named === rule.selector || rule.selector.startsWith(`${named} `),
        );
        expect(
          answered,
          `${relative(frontendRoot, file)}: \`${rule.selector}\` runs an infinite ` +
            `animation and no reduced-motion rule names it or an ancestor of it`,
        ).toBe(true);
      }
    }
  });
});

// ---------------------------------------------------------------------------
// The pending vocabulary. Four spellings of "this is loading" grew here before
// they were collapsed into `PendingBody`: three inline-styled bars in
// QueryStates, one silent 32px bar in SurfaceState, five unanimated bone rows
// in ListTable, and a long tail of hand-rolled bars and bare "Loading…" lines.
// They disagreed about the shape and the height, and — the part a reader could
// not see — about whether anything was ANNOUNCED at all: three screens had
// bolted their own visually-hidden line beside a mute placeholder, which is the
// tell that the primitive was missing something rather than that they were
// special.
//
// A fifth spelling is one `<div role="status" aria-busy="true">` away, and it
// looks correct in review. So the gate is that the announcement has exactly one
// home.
// ---------------------------------------------------------------------------

describe("pending states", () => {
  const PENDING_HOME = join("src", "design-system", "atoms.tsx");

  it("announces a pending region from exactly one component", () => {
    const declared = files
      .filter((file) => file.endsWith(".tsx"))
      .filter((file) => !/\.(test|stories)\.tsx$/.test(file))
      // Both static spellings. Matching only the quoted one let
      // `aria-busy={true}` — the same unconditional claim, written the other
      // way — declare a second pending region and pass.
      .filter((file) =>
        /aria-busy=(?:["']true["']|\{\s*true\s*\})/.test(
          readFileSync(file, "utf8"),
        ),
      )
      .map((file) => relative(frontendRoot, file));

    // A control's own busy state is a different fact and reads it from a
    // variable (`aria-busy={busy || undefined}`), so it never matches the
    // literal above — Button and Switch are not exceptions to carve out here,
    // they are simply not pending REGIONS.
    expect(
      declared,
      `a pending region must come from PendingBody (${PENDING_HOME}); ` +
        `these files declare one of their own: ${declared.join(", ")}`,
    ).toEqual([PENDING_HOME]);
  });

  it("keeps the placeholder pulse to one selector", () => {
    // `.lt-bone` was the tell: it painted its own fill and radius and simply
    // forgot the animation, so a table's five placeholder rows were the one
    // mark in the product that did not move — indistinguishable from a list
    // that had answered with five blank rows. It also missed `.skeleton`'s
    // reduced-motion answer, which the gate above can only check for rules that
    // exist.
    //
    // Whether a given box is "a placeholder" is not a question CSS can answer,
    // so this asks the answerable half: the pulse belongs to ONE class, and a
    // second placeholder therefore has to carry that class rather than restate
    // it. Restating it is what drops half the behaviour.
    const wearers = cssFiles.flatMap((file) =>
      plainRules(readFileSync(file, "utf8"))
        .filter((rule) => /animation[^;]*\bds-pulse\b/.test(rule.body))
        .map((rule) => `${relative(frontendRoot, file)} ${rule.selector}`),
    );
    expect(
      wearers,
      "the placeholder pulse has more than one home; a placeholder carries " +
        "`.skeleton` and adds only its own geometry",
    ).toEqual(["src/design-system/atoms.css .skeleton"]);
  });
});

// ---------------------------------------------------------------------------
// The focus ring. Ten spellings of one promise grew here: 2px against 3px,
// `--accent` in most files and a transparent outline plus a glow in three
// others, and two controls where the ring was suppressed and nothing put back —
// which is a WCAG 2.4.7 failure that reads, in a diff, as tidying.
//
// The width and the colour are now tokens; the OFFSET stays per-component,
// because a control clipped by its own container has to draw the ring inside
// itself or lose half of it. So the gate asks about the half that is a promise.
// ---------------------------------------------------------------------------

describe("focus", () => {
  it("draws every focus ring from the one token", () => {
    const spelled = cssFiles.flatMap((file) => {
      if (file.endsWith("tokens.css")) {
        return [];
      }
      // Comments first, or a sentence ABOUT an outline is read as one: the
      // conversation sheet explains at length why it leaves `outline: 0` unset,
      // and the earlier form of this gate reported that paragraph as a finding.
      const text = readFileSync(file, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
      return (
        [...text.matchAll(/outline:\s*([^;]+);/g)]
          .map((match) => match[1].trim())
          // `none` and `0` are the suppression half of the pattern, and legitimate
          // wherever something else draws the ring — a field's own boundary, a
          // wrapper's `:focus-within`. What is not legitimate is spelling a NEW
          // ring by hand.
          .filter((value) => !/^(none|0)$/.test(value))
          .filter((value) => !/var\(--focus-ring(-forced)?\)/.test(value))
          .map((value) => `${relative(frontendRoot, file)}: outline: ${value}`)
      );
    });
    expect(
      spelled,
      "a focus ring reads `outline: var(--focus-ring)`, or " +
        "`var(--focus-ring-forced)` where a glow does the drawing; these spell " +
        "their own width and colour, which is how one promise became ten rules",
    ).toEqual([
      // The outlines in this tree that are NOT focus, and must not read as it.
      // Two say "you may drop here" — dashed, and an outline rather than a
      // border so accepting a drag does not resize the thing being dragged
      // onto. One marks a refused field on the sign-in surface, which is a
      // different fact from where the keyboard is.
      "src/design-system/composed.css: outline: 2px dashed var(--accent)",
      "src/screens/auth.css: outline: 2px solid var(--danger)",
      "src/screens/onboarding-conversation/conversation.css: outline: 2px dashed var(--aiMed)",
    ]);
  });
});

// ---------------------------------------------------------------------------
// Server refusals. `ProblemError` is what tells a refusal the server MEANT
// from a bug worth logging: the global failure sink stays quiet for one and
// `console.error`s anything else, and only a ProblemError carries the RFC-7807
// `details.errors[]` a form needs to put a refusal on the field it is about.
//
// Eleven query and mutation functions threw `new Error(problemMessage(error))`
// instead. Each one flattened the answer to a sentence, so every refused save at
// those sites was ALSO reported as an unexpected error, and the per-field
// assertions the API already sends were unreachable. The wrapper reads as
// careful — it does produce a readable message — which is exactly why a gate is
// the only thing that keeps it out.
// ---------------------------------------------------------------------------

describe("problem details", () => {
  it("throws the server's problem, never a sentence about it", () => {
    const flattened = files
      .filter((file) => /\.tsx?$/.test(file))
      .filter((file) => !/\.(test|stories)\.tsx?$/.test(file))
      .flatMap((file) => {
        const text = readFileSync(file, "utf8").replace(
          /\/\*[\s\S]*?\*\/|\/\/[^\n]*/g,
          "",
        );
        return /throw new Error\(\s*problemMessage\(/.test(text)
          ? [relative(frontendRoot, file)]
          : [];
      });
    expect(
      flattened,
      "`throwProblem(error, t)` keeps the ProblemError: without it the global " +
        "sink logs a server refusal as a bug, and `details.errors[]` — the only " +
        "thing that can put a 422 on the field it is about — is discarded",
    ).toEqual([]);
  });
});
