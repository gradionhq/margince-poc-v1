import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import type { MarginceCoreState } from "../../design-system/margince-core";
import {
  MarginceWorkbench,
  type WorkbenchStep,
} from "../../design-system/margince-workbench";
import { useLocale, useT } from "../../i18n";
import { problemMessage } from "../common";
import { configuredModelLabel } from "../onboarding-read";
import type { ConversationState } from "./conversation-types";
import { railStops, stopState } from "./rail";

// The one workbench shell every conversation act shares: identity, orb,
// runtime transparency bar, and the split conversation/artifact body. Acts
// supply only what differs — presence, status line, runtime, and content.

type AiRunSummary = components["schemas"]["AiRunSummary"];
type AiProfile = components["schemas"]["AiProfile"];

// The detailed AI profile, and the label every onboarding surface names the
// configured model with. One hook so the gate, the read theatre and the
// workbench cannot disagree about what is answering — and one ["ai-profile"]
// cache entry, so naming it in three places still costs one request.
export function useConfiguredModel(): string {
  const t = useT();
  const profile = useQuery({
    queryKey: ["ai-profile"],
    queryFn: async (): Promise<AiProfile> => {
      const { data, error } = await api.GET("/ai/profile");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    staleTime: Number.POSITIVE_INFINITY,
  });
  return configuredModelLabel(profile.data, t("ob.ai.runtimeUnavailable"), t);
}

export function ConversationWorkbench({
  core,
  progress,
  status,
  runtime,
  railState,
  artifact,
  children,
}: Readonly<{
  core: MarginceCoreState;
  progress?: number;
  status: string;
  runtime?: AiRunSummary;
  railState: ConversationState;
  artifact?: ReactNode;
  children: ReactNode;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const configured = useConfiguredModel();
  // Built here rather than per act, so four acts cannot drift into four
  // different ideas of where the journey is.
  const steps: WorkbenchStep[] = railStops(railState.memberPath).map(
    (stop) => ({
      label: t(stop.labelKey),
      state: stopState(stop.key, railState),
    }),
  );
  return (
    // ob-read-panel is deliberately absent: its centring and decorative glow
    // are for the boxed single-column steps, and both fight a full-viewport
    // two-column surface. ob-workbench-panel stays — entries.tsx resolves the
    // composer through it, so it is a behavioural contract, not just a hook.
    <section className="ob-panel ob-workbench-panel">
      <MarginceWorkbench
        state={core}
        progress={progress}
        eyebrow={t("ob.ai.identity")}
        title={t("ob.ai.role")}
        status={status}
        configured={configured}
        locale={locale}
        runtime={runtime}
        runtimeLabels={{
          configured: t("ob.ai.configured"),
          used: t("ob.ai.modelsUsed"),
          route: t("ob.ai.route"),
          calls: t("ob.ai.calls"),
          tokens: t("ob.ai.tokens"),
          latency: t("ob.ai.latency"),
          estimatedCost: t("ob.ai.estimatedCost"),
          partial: t("ob.ai.partialEstimate"),
          awaiting: t("ob.ai.awaitingModel"),
          unavailable: t("ob.ai.notAvailableYet"),
          chip: t("ob.ai.runtimeChip"),
          answering: t("ob.ai.answeringNow"),
          scope: t("ob.ai.runScope"),
        }}
        steps={steps}
        artifact={artifact}
      >
        {children}
      </MarginceWorkbench>
    </section>
  );
}
