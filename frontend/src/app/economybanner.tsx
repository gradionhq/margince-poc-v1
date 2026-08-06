import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import { Badge, Button } from "../design-system/atoms";
import { useT } from "../i18n";
import { bandTone } from "../screens/aiusage";
import { throwProblem } from "../screens/common";
import { useCan } from "./capability";

export function EconomyBanner() {
  const t = useT();
  // GET /ai/usage gates on automation:update, not on any AI-named object — the
  // budget it reports is the automation runtime's, and the server treats
  // seeing it as an operator concern. Binding this to a more intuitive object
  // would 403 the banner for exactly the roles that are meant to see it.
  const enabled = useCan("automation", "update");
  const previousBand = useRef<string | undefined>(undefined);
  const [occurrence, setOccurrence] = useState(0);
  const [dismissedOccurrence, setDismissedOccurrence] = useState<string | null>(
    null,
  );
  const query = useQuery({
    queryKey: ["ai-usage-band"],
    enabled,
    staleTime: 5 * 60_000,
    queryFn: async () => {
      const today = new Date().toISOString().slice(0, 10);
      const { data, error } = await api.GET("/ai/usage", {
        params: { query: { from: today, to: today } },
      });
      if (error) throwProblem(error);
      if (!data?.budget) throw new Error("malformed AI usage response");
      return data;
    },
  });
  const band = query.data?.budget?.band;
  useEffect(() => {
    if (band !== previousBand.current) {
      previousBand.current = band;
      setOccurrence((value) => value + 1);
    }
  }, [band]);
  const occurrenceKey = band ? `${band}:${occurrence}` : null;
  // The banner is advisory; errors stay on the accountable Settings card.
  if (
    !enabled ||
    query.isError ||
    !band ||
    band === "normal" ||
    dismissedOccurrence === occurrenceKey
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
        gap: "var(--space-3)",
        alignItems: "center",
      }}
    >
      <Badge tone={bandTone(band)}>
        {band === "queued"
          ? t("aibanner.queued")
          : band === "degraded"
            ? t("aibanner.degraded")
            : t("aibanner.unknown")}
      </Badge>
      <a href="#/settings/ai">{t("aibanner.link")}</a>
      <Button
        small
        aria-label={t("aibanner.dismiss")}
        onClick={() => setDismissedOccurrence(occurrenceKey)}
      >
        ×
      </Button>
    </div>
  );
}
