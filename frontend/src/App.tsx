import { useEffect, useState } from "react";

// Token showcase — the B-EP09.1 verification surface, not a product screen.
// Every colour reads through a custom property; the conformance tests reject
// any literal colour outside tokens.css.

const surfaceTokens = ["bgPage", "bgElevated", "bgCard", "bgHover", "bgRail"];
const accentTokens = [
  "accent",
  "accentLight",
  "accentMed",
  "ai",
  "aiLight",
  "aiMed",
];
const textTokens = [
  "textPrimary",
  "textContent",
  "textSecondary",
  "textTertiary",
  "textMuted",
];
const statusTokens = [
  "online",
  "teal",
  "away",
  "dnd",
  "success",
  "warn",
  "danger",
];

type Theme = "light" | "dark";

export function App() {
  const [theme, setTheme] = useState<Theme>("light");

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  return (
    <main
      style={{ maxWidth: 780, margin: "0 auto", padding: "28px 36px 80px" }}
    >
      <header style={{ display: "flex", alignItems: "baseline", gap: 16 }}>
        <h1
          style={{
            fontFamily: "var(--f-display)",
            fontSize: 22,
            fontWeight: 600,
            color: "var(--textPrimary)",
          }}
        >
          Margince design tokens
        </h1>
        <button
          type="button"
          onClick={() => setTheme(theme === "light" ? "dark" : "light")}
          style={{
            marginLeft: "auto",
            fontFamily: "var(--f-body)",
            fontSize: 13,
            fontWeight: 600,
            padding: "6px 11px",
            borderRadius: "var(--r-sm)",
            border: "1px solid var(--borderSubtle)",
            background: "var(--bgElevated)",
            color: "var(--textSecondary)",
            cursor: "pointer",
          }}
        >
          {theme === "light" ? "Dark" : "Light"} theme
        </button>
      </header>
      <p style={{ fontSize: 13, color: "var(--textTertiary)", marginTop: 4 }}>
        Ledger Green (ADR-0040) — canonical values mirror the spec mockups;
        tests pin them.
      </p>
      <Swatches title="Surfaces" tokens={surfaceTokens} />
      <Swatches title="Accent & AI" tokens={accentTokens} />
      <Swatches title="Text" tokens={textTokens} />
      <Swatches title="Status" tokens={statusTokens} />
      <section style={{ marginTop: 28 }}>
        <h2
          style={{
            fontFamily: "var(--f-body)",
            fontSize: 15,
            fontWeight: 600,
            color: "var(--textPrimary)",
          }}
        >
          Type ramp
        </h2>
        <p
          style={{
            fontFamily: "var(--f-display)",
            fontSize: 22,
            fontWeight: 600,
            color: "var(--textPrimary)",
          }}
        >
          Display — Outfit 600
        </p>
        <p
          style={{
            fontFamily: "var(--f-body)",
            fontSize: 14,
            color: "var(--textContent)",
          }}
        >
          Body — DM Sans 400, the default reading face.
        </p>
        <p
          style={{
            fontFamily: "var(--f-mono)",
            fontSize: 14,
            color: "var(--textContent)",
          }}
        >
          Mono — JetBrains Mono, evidence snippets and IDs.
        </p>
      </section>
    </main>
  );
}

function Swatches({ title, tokens }: { title: string; tokens: string[] }) {
  return (
    <section style={{ marginTop: 28 }}>
      <h2
        style={{
          fontFamily: "var(--f-body)",
          fontSize: 15,
          fontWeight: 600,
          color: "var(--textPrimary)",
        }}
      >
        {title}
      </h2>
      <div
        style={{ display: "flex", flexWrap: "wrap", gap: 10, marginTop: 10 }}
      >
        {tokens.map((token) => (
          <figure key={token} style={{ width: 108 }}>
            <div
              style={{
                height: 56,
                borderRadius: "var(--r-md)",
                background: `var(--${token})`,
                border: "1px solid var(--borderSubtle)",
              }}
            />
            <figcaption
              style={{
                fontFamily: "var(--f-mono)",
                fontSize: 11,
                color: "var(--textSecondary)",
                marginTop: 4,
              }}
            >
              --{token}
            </figcaption>
          </figure>
        ))}
      </div>
    </section>
  );
}
