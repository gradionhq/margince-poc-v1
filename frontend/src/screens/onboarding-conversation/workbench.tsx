import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import type { MarginceCoreState } from "../../design-system/margince-core";
import { MarginceWorkbench } from "../../design-system/margince-workbench";
import { useLocale, useT } from "../../i18n";
import { problemMessage } from "../common";
import { configuredModelLabel } from "../onboarding-read";

// The one workbench shell every conversation act shares: identity, orb,
// runtime transparency bar, and the split conversation/artifact body. Acts
// supply only what differs — presence, status line, runtime, and content.

type AiRunSummary = components["schemas"]["AiRunSummary"];
type AiProfile = components["schemas"]["AiProfile"];

export function ConversationWorkbench({
  core,
  progress,
  status,
  runtime,
  artifact,
  children,
}: Readonly<{
  core: MarginceCoreState;
  progress?: number;
  status: string;
  runtime?: AiRunSummary;
  artifact?: ReactNode;
  children: ReactNode;
}>) {
  const t = useT();
  const { locale } = useLocale();
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
  return (
    <section className="ob-panel ob-read-panel ob-workbench-panel">
      <MarginceWorkbench
        state={core}
        progress={progress}
        eyebrow={t("ob.ai.identity")}
        title={t("ob.ai.role")}
        status={status}
        configured={configuredModelLabel(
          profile.data,
          t("ob.ai.runtimeUnavailable"),
          t,
        )}
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
        }}
        artifact={artifact}
      >
        {children}
      </MarginceWorkbench>
    </section>
  );
}
