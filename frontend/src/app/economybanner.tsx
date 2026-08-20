import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import { Badge, Button } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
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
    // The band decides the tone: a queued workspace is being refused work
    // right now, where a degraded one is still serving and only warning.
    <Callout
      className="appbanner"
      tone={band === "queued" ? "danger" : "warn"}
      live="status"
      actions={
        <>
          <a href="#/settings/admin/ai">{t("aibanner.link")}</a>
          <Button
            small
            aria-label={t("aibanner.dismiss")}
            onClick={() => setDismissedOccurrence(occurrenceKey)}
          >
            <X aria-hidden size={14} />
          </Button>
        </>
      }
    >
      <Badge tone={bandTone(band)}>
        {band === "queued"
          ? t("aibanner.queued")
          : band === "degraded"
            ? t("aibanner.degraded")
            : t("aibanner.unknown")}
      </Badge>
    </Callout>
  );
}
