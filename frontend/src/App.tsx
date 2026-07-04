import { useEffect, useState } from "react";
import {
  AutonomyDot,
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
  StagedProposal,
} from "./design-system/trust";
import { useT } from "./i18n";

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
  const t = useT();
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
          {t("app.title")}
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
          {theme === "light" ? t("theme.toDark") : t("theme.toLight")}
        </button>
      </header>
      <p style={{ fontSize: 13, color: "var(--textTertiary)", marginTop: 4 }}>
        {t("app.subtitle")}
      </p>
      <Swatches title={t("section.surfaces")} tokens={surfaceTokens} />
      <Swatches title={t("section.accentAi")} tokens={accentTokens} />
      <Swatches title={t("section.text")} tokens={textTokens} />
      <Swatches title={t("section.status")} tokens={statusTokens} />
      <section style={{ marginTop: 28 }}>
        <h2
          style={{
            fontFamily: "var(--f-body)",
            fontSize: 15,
            fontWeight: 600,
            color: "var(--textPrimary)",
          }}
        >
          {t("section.typeRamp")}
        </h2>
        <p
          style={{
            fontFamily: "var(--f-display)",
            fontSize: 22,
            fontWeight: 600,
            color: "var(--textPrimary)",
          }}
        >
          {t("type.display")}
        </p>
        <p
          style={{
            fontFamily: "var(--f-body)",
            fontSize: 14,
            color: "var(--textContent)",
          }}
        >
          {t("type.body")}
        </p>
        <p
          style={{
            fontFamily: "var(--f-mono)",
            fontSize: 14,
            color: "var(--textContent)",
          }}
        >
          {t("type.mono")}
        </p>
      </section>
      <TrustShowcase />
    </main>
  );
}

function TrustShowcase() {
  const t = useT();
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
        {t("section.trust")}
      </h2>
      <div
        style={{
          display: "flex",
          flexWrap: "wrap",
          alignItems: "center",
          gap: 12,
          marginTop: 10,
        }}
      >
        <EvidenceChip
          evidence={{
            snippet: "…budget approved in Q3…",
            source: "email 12 Jun",
          }}
        />
        <ConfidenceMeter level="high" />
        <ConfidenceMeter level="med" />
        <ConfidenceMeter level="low" />
        <ProvenanceTag provenance={{ kind: "agent", agent: "capture" }} />
        <ProvenanceTag provenance={{ kind: "human" }} />
        <span style={{ fontSize: 13, color: "var(--textSecondary)" }}>
          <AutonomyDot tier="auto" /> {t("autonomy.auto")}
        </span>
        <span style={{ fontSize: 13, color: "var(--textSecondary)" }}>
          <AutonomyDot tier="confirm" /> {t("autonomy.confirm")}
        </span>
      </div>
      <div style={{ marginTop: 14, maxWidth: 460 }}>
        <StagedProposal
          proposal={{
            description: "Set Brandt Automotive's deal value",
            value: "€48.000",
            agent: "capture",
            confidence: "med",
            evidence: {
              snippet: "…offer of 48k as discussed…",
              source: "email 12 Jun",
            },
          }}
        />
      </div>
    </section>
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
