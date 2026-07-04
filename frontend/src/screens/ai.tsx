import { useState } from "react";
import { ASK_QUERY_KEY } from "../app/palette";
import { Kbd, SectionHeader } from "../design-system/atoms";
import { AutonomyDot } from "../design-system/trust";
import { useT } from "../i18n";

// Ask AI (B-EP09.12c, 03b): the BYO-agent surface. Agents connect over MCP
// with a passport; this surface states the two-tier contract honestly —
// 🟢 read/draft executes, 🟡 write/send stages into the approval inbox —
// and never pretends a chat backend exists before one is connected.

export function AskAiScreen() {
  const t = useT();
  const [query] = useState(() => {
    const stored = sessionStorage.getItem(ASK_QUERY_KEY);
    sessionStorage.removeItem(ASK_QUERY_KEY);
    return stored;
  });

  return (
    <div className="wrap narrow">
      <SectionHeader title={t("nav.ai")} sub={t("ai.sub")} />
      {query && (
        <div className="card" style={{ marginBottom: 14 }}>
          <p className="t-label">{t("ai.fromPalette")}</p>
          <p className="t-mono" style={{ marginTop: 4 }}>
            {query}
          </p>
        </div>
      )}
      <div className="card" style={{ marginBottom: 14 }}>
        <SectionHeader title={t("ai.tiers")} />
        <ul
          style={{
            listStyle: "none",
            display: "flex",
            flexDirection: "column",
            gap: 8,
          }}
        >
          <li>
            <AutonomyDot tier="auto" /> <strong>{t("ai.tierGreen")}</strong>{" "}
            <span className="t-caption">{t("ai.tierGreenDetail")}</span>
          </li>
          <li>
            <AutonomyDot tier="confirm" /> <strong>{t("ai.tierYellow")}</strong>{" "}
            <span className="t-caption">{t("ai.tierYellowDetail")}</span>
          </li>
        </ul>
      </div>
      <div className="card card-inset">
        <SectionHeader title={t("ai.connect")} />
        <p className="t-caption">{t("ai.connectDetail")}</p>
        <p className="t-caption" style={{ marginTop: 8 }}>
          {t("ai.paletteHint")} <Kbd>⌘K</Kbd>
        </p>
      </div>
    </div>
  );
}
