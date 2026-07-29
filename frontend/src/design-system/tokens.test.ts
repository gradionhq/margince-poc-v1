import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Pins the light-mode token layer to the canonical Ledger-Green values from the
// spec's design/mockups/app.css :root (design-language §2, ADR-0040). A value
// drifting from the design source of truth — or sliding back to Gradion orange
// or Dispact warm-stone — fails the build.

const here = dirname(fileURLToPath(import.meta.url));
const tokensCss = readFileSync(join(here, "tokens.css"), "utf8");

// Values verbatim from the mockups; comparison normalizes case, whitespace and
// a leading zero before a decimal point so formatting is free but values are not.
const canonical: Record<string, string> = {
  "--bgPage": "#FBFCFB",
  "--bgElevated": "#ffffff",
  "--bgCard": "#EEF1F0",
  "--bgHover": "#F3F6F4",
  "--accent": "#0B7A53",
  "--accentLight": "rgba(11,122,83,.09)",
  "--accentMed": "rgba(11,122,83,.17)",
  "--textPrimary": "#15201B",
  "--textContent": "#36433D",
  "--textSecondary": "#68756E",
  "--textTertiary": "#9AA6A0",
  "--textMuted": "#CBD2CD",
  "--textMeta": "#5E6C65",
  "--textOnAccent": "#fff",
  "--borderSubtle": "#E5E9E7",
  "--borderStrong": "#D2D8D4",
  "--online": "#22c55e",
  "--teal": "#0E7490",
  "--tealLight": "rgba(14,116,144,.1)",
  "--away": "#fbbf24",
  "--dnd": "#ef4444",
  "--bgRail": "#13231D",
  "--ai": "#5B61D6",
  "--aiLight": "rgba(91,97,214,.08)",
  "--aiMed": "rgba(91,97,214,.30)",
  "--aiText": "#3F45B0",
  "--success": "#15803d",
  "--successBg": "rgba(34,197,94,.12)",
  "--warn": "#92400e",
  "--warnBg": "rgba(251,191,36,.16)",
  "--warnBorder": "rgba(251,191,36,.45)",
  "--danger": "#b91c1c",
  "--dangerBg": "rgba(239,68,68,.1)",
  "--r-sm": "8px",
  "--r-md": "12px",
  "--r-lg": "20px",
  "--r-full": "9999px",
  "--f-display": '"Outfit",system-ui,sans-serif',
  "--f-body": '"DM Sans",system-ui,sans-serif',
  "--f-mono": '"JetBrains Mono",ui-monospace,monospace',
};

function normalize(value: string): string {
  return value
    .toLowerCase()
    .replace(/\s+/g, "")
    .replace(/(^|[^0-9])0\./g, "$1.")
    .replace(/(\.[0-9]*?)0+([^0-9]|$)/g, "$1$2");
}

function parseBlock(css: string, selector: string): Record<string, string> {
  const match = css.match(
    new RegExp(`${selector.replace(/[[\]"=]/g, "\\$&")}\\s*\\{([^}]*)\\}`),
  );
  if (!match) {
    throw new Error(`tokens.css has no ${selector} block`);
  }
  const props: Record<string, string> = {};
  for (const [, name, value] of match[1].matchAll(
    /(--[\w-]+)\s*:\s*([^;]+);/g,
  )) {
    props[name] = value.trim();
  }
  return props;
}

describe("Ledger-Green token layer (B-EP09.1)", () => {
  const light = parseBlock(tokensCss, ":root");

  it("exports every canonical §2 token with the exact mockup value", () => {
    for (const [name, want] of Object.entries(canonical)) {
      expect(light[name], `${name} missing from :root`).toBeDefined();
      expect(normalize(light[name]), name).toBe(normalize(want));
    }
  });

  it("is Ledger Green — not Gradion orange, not Dispact warm-stone", () => {
    expect(normalize(light["--accent"])).toBe("#0b7a53");
    expect(normalize(light["--bgRail"])).toBe("#13231d");
    const all = normalize(Object.values(light).join(" "));
    expect(all).not.toContain("#ff6b00"); // Gradion orange
  });

  it("keeps brand emerald and success grass-green tonally distinct (§2)", () => {
    expect(normalize(light["--accent"])).not.toBe(normalize(light["--online"]));
  });

  // The material overlays are the ONLY non-canon literals in this file, and they
  // are here because check-ds-purity.sh excludes tokens.css and nothing else.
  // Pinned so a later "tidy-up" cannot quietly turn them into brand colours.
  it("ships the two material overlays as pure white and pure black", () => {
    expect(normalize(light["--overlayLight"])).toBe("#ffffff");
    expect(normalize(light["--overlayDark"])).toBe("#000000");
  });

  describe("dark palette (data-theme toggle)", () => {
    const dark = parseBlock(tokensCss, '[data-theme="dark"]');

    it("lightens the accent toward #16A34A (ADR-0040)", () => {
      expect(normalize(dark["--accent"])).toBe("#16a34a");
    });

    it("overrides only tokens the light theme defines — no orphan knobs", () => {
      for (const name of Object.keys(dark)) {
        expect(light[name], `${name} exists only in dark`).toBeDefined();
      }
    });

    it("keeps the rail on the shared ink-green field (§2b: the rail is not themed)", () => {
      expect(dark["--bgRail"]).toBeUndefined();
    });
  });
});

// brand.css is the derived layer, and "derived" is the whole guarantee: a literal
// there would be a brand colour the spec has never seen, following neither the
// dark-theme accent lift nor any future palette change. Comments are stripped
// first — the file's own header quotes the accent lift's two hex values, and
// documenting the canon is not inventing a colour.
describe("the derived brand layer", () => {
  const brandCss = readFileSync(join(here, "brand.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );

  it("invents no colour — every value derives from a canonical token", () => {
    const literal = /#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch)\(/;
    for (const [index, line] of brandCss.split("\n").entries()) {
      expect(
        literal.test(line),
        `brand.css line ${index + 1} carries a colour literal — derive it from a token`,
      ).toBe(false);
    }
  });

  it("declares every derived token as a color-mix of a token", () => {
    const declarations = [...brandCss.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)];
    expect(declarations.length).toBeGreaterThan(0);
    for (const [, name, value] of declarations) {
      expect(value, `${name} is not derived`).toMatch(/color-mix\(/);
      expect(value, `${name} mixes no token`).toMatch(/var\(--/);
    }
  });
});
