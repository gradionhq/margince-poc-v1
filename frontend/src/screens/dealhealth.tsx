import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge } from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { Meter } from "../design-system/readings";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { QueryStates, throwProblem } from "./common";
import "./dealhealth.css";

// The deal's pulse: the health formula as four named factors, each with the
// fact it was read from. The verdict is the server's (at_risk); the card draws
// the parts so a reader can see WHICH part is low and disagree with that.

type Factor = components["schemas"]["DealHealthFactor"];

const FACTOR_LABELS: Record<string, MessageKey> = {
  activity_recency: "pulse.recency",
  stage_velocity: "pulse.velocity",
  engagement: "pulse.engagement",
  commitments: "pulse.commitments",
};

export function DealHealthCard({ dealId }: Readonly<{ dealId: string }>) {
  const t = useT();
  const health = useQuery({
    queryKey: ["deal-health", dealId],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/health", {
        params: { path: { id: dealId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  return (
    <Panel
      title={t("pulse.title")}
      sub={t("pulse.sub")}
      titleAction={
        health.data ? (
          <Badge tone={health.data.at_risk ? "danger" : "success"}>
            {t(health.data.at_risk ? "pulse.atRisk" : "pulse.onTrack")}
          </Badge>
        ) : undefined
      }
    >
      <QueryStates query={health} pendingLines={4}>
        {health.data ? (
          <>
            {(health.data.factors ?? []).map((factor) => (
              <FactorRow key={factor.key} factor={factor} />
            ))}
            <PanelBody>
              <p className="t-small pulse-total">
                {t("pulse.total", {
                  value: Math.round(health.data.health * 100),
                })}
              </p>
            </PanelBody>
          </>
        ) : null}
      </QueryStates>
    </Panel>
  );
}

function FactorRow({ factor }: Readonly<{ factor: Factor }>) {
  const t = useT();
  const label = t(FACTOR_LABELS[factor.key] ?? "pulse.recency");
  return (
    <PanelRow>
      <div className="pulse-factor">
        <Meter
          value={Math.round(factor.value * 100)}
          max={100}
          label={label}
          tone={factor.value < 0.35 ? "danger" : undefined}
          dense
        />
        <p className="t-small pulse-reason">{factor.reason}</p>
      </div>
    </PanelRow>
  );
}
