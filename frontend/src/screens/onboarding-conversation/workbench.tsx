import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { createContext, useContext, useEffect, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { ThemeToggle } from "../../app/theme-toggle";
import type { MarginceCoreState } from "../../design-system/margince-core";
import {
  MarginceWorkbench,
  type WorkbenchStep,
} from "../../design-system/margince-workbench";
import { useLocale, useT } from "../../i18n";
import { throwProblem, useMe } from "../common";
import {
  configuredModelLabel,
  configuredModelSummary,
} from "../onboarding-read";
import type { ConversationState } from "./conversation-types";
import { isDetour, railStops, stopState } from "./rail";

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
        throwProblem(error);
      }
      return data;
    },
    staleTime: Number.POSITIVE_INFINITY,
  });
  return configuredModelLabel(profile.data, t("ob.ai.runtimeUnavailable"), t);
}

// The plain-language sibling of useConfiguredModel(): same cached
// ["ai-profile"] entry, so the exact ids and the count-and-place summary
// can never name a different configuration.
function useConfiguredModelSummary(): string {
  const t = useT();
  const profile = useQuery({
    queryKey: ["ai-profile"],
    queryFn: async (): Promise<AiProfile> => {
      const { data, error } = await api.GET("/ai/profile");
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    staleTime: Number.POSITIVE_INFINITY,
  });
  return configuredModelSummary(profile.data, t("ob.ai.runtimeUnavailable"), t);
}

/**
 * Whether the workbench appearing right now is the FIRST one of this setup.
 *
 * Each act mounts its own shell, so the entrance animation replayed on every act
 * change: the rail, the brand line, the orb and the runtime chip — the parts that
 * are meant to be the stable frame — faded and rose again each time the reader
 * moved forward. The shell cannot remember this itself, because it is exactly
 * what unmounts; the scope sits above the act switch, which does not.
 * What is shared is a mutable box, not a boolean: the answer has to flip when a
 * SHELL appears, and the company act opens on the full-screen gate, so anything
 * keyed on the scope's own mount would already have flipped before the first
 * workbench ever rendered.
 */
const WorkbenchShown = createContext<{ current: boolean }>({ current: false });

export function WorkbenchEntranceScope({
  children,
}: Readonly<{ children: ReactNode }>) {
  const shown = useRef(false);
  return (
    <WorkbenchShown.Provider value={shown}>{children}</WorkbenchShown.Provider>
  );
}

// Frozen per mount in state, not recomputed per render: the shell re-renders on
// every poll, and an entrance that evaluated again would drop its own class
// mid-animation.
function useFirstWorkbench(): boolean {
  const shown = useContext(WorkbenchShown);
  const [first] = useState(() => !shown.current);
  useEffect(() => {
    shown.current = true;
  }, [shown]);
  return first;
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
  const configuredSummary = useConfiguredModelSummary();
  const first = useFirstWorkbench();
  const me = useMe();
  // Built here rather than per act, so four acts cannot drift into four
  // different ideas of where the journey is.
  const stops = railStops(railState.memberPath);
  const steps: WorkbenchStep[] = stops.map((stop) => ({
    label: t(stop.labelKey),
    state: stopState(stop.key, railState),
  }));
  // The sentence the rail's progress line reads: derived from the same
  // stops, so the words and the segments cannot disagree. Before the first
  // stop is current (the read still running) there is no step to name. The
  // clarify detour is not one of those stops, so it gets its own words
  // rather than a slot in a count it does not occupy.
  const current = steps.findIndex((step) => step.state === "now");
  const stepLabel = isDetour(railState)
    ? t("ob.conv.scene.detour")
    : current < 0
      ? undefined
      : t("ob.conv.scene.step", {
          n: current + 1,
          m: steps.length,
          label: steps[current].label,
        });
  return (
    // ob-read-panel is deliberately absent: its centring and decorative glow
    // are for the boxed single-column steps, and both fight a full-viewport
    // two-column surface. ob-workbench-panel stays — entries.tsx resolves the
    // composer through it, so it is a behavioural contract, not just a hook.
    //
    // ob-panel carries nothing but the entrance, so it is worn once: the
    // workspace assembles when it first appears, and thereafter the frame is
    // simply there while the acts change inside it.
    <section className={`ob-workbench-panel${first ? " ob-panel" : ""}`}>
      <MarginceWorkbench
        variant="rail"
        state={core}
        progress={progress}
        eyebrow={t("ob.ai.identity")}
        title={t("ob.ai.role")}
        status={status}
        configured={configured}
        configuredSummary={configuredSummary}
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
          tokensShort: t("ob.rail.tokensUnit"),
        }}
        steps={steps}
        stepLabel={stepLabel}
        artifact={artifact}
        footerLabel={t("ob.rail.spend")}
        person={
          me.data
            ? {
                name: me.data.user.display_name,
                detail: me.data.user.email,
              }
            : undefined
        }
        personAction={
          // Onboarding is railless — no top bar, so the rail's foot carries the
          // one piece of chrome the reader may still want mid-journey.
          <ThemeToggle />
        }
      >
        {children}
      </MarginceWorkbench>
    </section>
  );
}
