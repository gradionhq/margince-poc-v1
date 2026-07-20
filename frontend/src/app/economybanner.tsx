import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import { Badge, Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { bandTone } from "../screens/aiusage";
import {
  canConfigureAutomations,
  problemMessage,
  useMe,
} from "../screens/common";

export function EconomyBanner() {
  const t = useT();
  const me = useMe();
  const enabled = canConfigureAutomations(me.data?.roles);
  const [dismissedBand, setDismissedBand] = useState<string | null>(null);
  const query = useQuery({
    queryKey: ["ai-usage-band"],
    enabled,
    staleTime: 5 * 60_000,
    queryFn: async () => {
      const today = new Date().toISOString().slice(0, 10);
      const { data, error } = await api.GET("/ai/usage", {
        params: { query: { from: today, to: today } },
      });
      if (error) throw new Error(problemMessage(error));
      if (!data?.budget) throw new Error("malformed AI usage response");
      return data;
    },
  });
  const band = query.data?.budget?.band;
  // The banner is advisory; errors stay on the accountable Settings card.
  if (
    !enabled ||
    query.isError ||
    !band ||
    band === "normal" ||
    dismissedBand === band
  ) {
    return null;
  }
  return (
    <div
      role="status"
      className="card card-inset"
      style={{
        borderRadius: 0,
        display: "flex",
        gap: 10,
        alignItems: "center",
      }}
    >
      <Badge tone={bandTone(band)}>
        {band === "queued" ? t("aibanner.queued") : t("aibanner.degraded")}
      </Badge>
      <a href="#/settings/ai">{t("aibanner.link")}</a>
      <Button
        small
        aria-label={t("aibanner.dismiss")}
        onClick={() => setDismissedBand(band)}
      >
        ×
      </Button>
    </div>
  );
}
