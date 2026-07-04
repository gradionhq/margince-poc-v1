import {
  AutonomyDot,
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
  StagedProposal,
} from "../design-system/trust";
import { useT } from "../i18n";

// The design-system verification screen (#/design, not in the rail): renders
// every token and primitive so a human — or the e2e harness — can eyeball the
// system in both themes. Fixture content uses the canonical seed entities.

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

export function DesignScreen() {
  const t = useT();
  return (
    <div className="wrap narrow">
      <h1 className="t-display">{t("app.title")}</h1>
      <p className="t-caption">{t("app.subtitle")}</p>
      <Swatches title={t("section.surfaces")} tokens={surfaceTokens} />
      <Swatches title={t("section.accentAi")} tokens={accentTokens} />
      <Swatches title={t("section.text")} tokens={textTokens} />
      <Swatches title={t("section.status")} tokens={statusTokens} />
      <section style={{ marginTop: 28 }}>
        <h2 className="t-sub">{t("section.typeRamp")}</h2>
        <p className="t-display">{t("type.display")}</p>
        <p className="t-body">{t("type.body")}</p>
        <p className="t-mono">{t("type.mono")}</p>
      </section>
      <TrustShowcase />
    </div>
  );
}

function TrustShowcase() {
  const t = useT();
  return (
    <section style={{ marginTop: 28 }}>
      <h2 className="t-sub">{t("section.trust")}</h2>
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
        <span className="t-caption">
          <AutonomyDot tier="auto" /> {t("autonomy.auto")}
        </span>
        <span className="t-caption">
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
      <h2 className="t-sub">{title}</h2>
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
              className="t-mono"
              style={{ fontSize: 11, marginTop: 4 }}
            >
              --{token}
            </figcaption>
          </figure>
        ))}
      </div>
    </section>
  );
}
