import { useQuery } from "@tanstack/react-query";
import { useEffect, useReducer, useRef } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { navigate, useRoute } from "../../app/router";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import { problemMessage } from "../common";
import { EMPTY_DRAFT, pickBuiltVersion, useCompany } from "../onboarding";
import { CompanyAct } from "./company-act";
import { ConnectAct } from "./connect-act";
import {
  type ConversationPhase,
  conversationReducer,
  initialConversationState,
} from "./conversation-machine";
import { restorePlan, type VoiceRestoreProbe } from "./restore";
import { ResultsAct } from "./results-act";
import type { WizardPersistInput } from "./use-wizard-state";
import { useWizardStatePersist } from "./use-wizard-state";
import { VoiceAct } from "./voice-act";

// The conversational onboarding shell: one pure machine owns where the
// conversation is, and each act renders inside the shared Margince
// workbench. On mount the shell reads the server truth (wizard state,
// company, voice) and restores through START + RESUME; the wizard state's
// `path` field is THE member signal, with company-exists only the fallback
// when no state row exists. The classic stepper stays the default; this
// screen mounts only behind the flag (see flag.ts).

export { conversationFlagEnabled } from "./flag";

type OnboardingState = components["schemas"]["OnboardingState"];

// The wizard steps whose restore needs the voice server truth (built
// versions, corpus meter); the member path never does.
const voiceProbeSteps = new Set<OnboardingState["step"]>([
  "voice",
  "results",
  "connect",
]);

async function loadWizardState(): Promise<OnboardingState | null> {
  const { data, error, response } = await api.GET("/onboarding/state");
  if (error) {
    if (response.status === 404) {
      return null;
    }
    throw new Error(problemMessage(error));
  }
  return data;
}

// One probe per fact the restore needs: does a built version exist, and
// what does the server corpus meter say right now.
async function probeVoice(): Promise<VoiceRestoreProbe> {
  const list = await api.GET("/voice-profiles");
  if (list.error) {
    throw new Error(problemMessage(list.error));
  }
  const profileId = list.data.data[0]?.id;
  if (profileId === undefined) {
    return { built: false, summary: null };
  }
  const [versions, sources] = await Promise.all([
    api.GET("/voice-profiles/{id}/versions", {
      params: { path: { id: profileId } },
    }),
    api.GET("/voice-profiles/{id}/sources", {
      params: { path: { id: profileId } },
    }),
  ]);
  if (versions.error) {
    throw new Error(problemMessage(versions.error));
  }
  if (sources.error) {
    throw new Error(problemMessage(sources.error));
  }
  return {
    built: pickBuiltVersion(versions.data.data) !== null,
    summary: sources.data.summary,
  };
}

// Live act transitions the server must remember, keyed by the phase pair
// that only a user action (never a restore RESUME out of co.confirmed)
// produces. Classic STEPS indexes: 1 voice, 2 results, 3 connect.
function actCheckpoint(
  prev: ConversationPhase,
  next: ConversationPhase,
  buildSucceeded: boolean,
): Omit<WizardPersistInput, "values"> | null {
  if (prev === "vo.invite" && next === "vo.collecting") {
    return { nextStep: 1, voiceSkipped: false };
  }
  if (
    (prev === "vo.invite" || prev === "vo.collecting") &&
    next === "vo.skipped"
  ) {
    return { nextStep: 2, voiceSkipped: true };
  }
  if (prev === "vo.building" && next === "vo.result" && buildSucceeded) {
    return { nextStep: 2, voiceSkipped: false };
  }
  if ((prev === "vo.result" || prev === "vo.skipped") && next === "re.recap") {
    return { nextStep: 2 };
  }
  if (prev === "re.recap" && next === "cn.consent") {
    return { nextStep: 3 };
  }
  return null;
}

// The welcome gate: restore lookups still in flight, or a load failure with
// one retry that re-runs exactly the lookups that failed.
type RestoreLookup = Readonly<{ isError: boolean; refetch: () => unknown }>;

function RestoreGate({ lookups }: Readonly<{ lookups: RestoreLookup[] }>) {
  const t = useT();
  const failed = lookups.filter((lookup) => lookup.isError);
  return (
    <div className="ob-page ob-conv-page">
      {failed.length > 0 ? (
        <div className="readfail warn" role="alert">
          <p>{t("ob.conv.loadFailed")}</p>
          <Button
            small
            onClick={() => {
              for (const lookup of failed) {
                void lookup.refetch();
              }
            }}
          >
            {t("ob.conv.retry")}
          </Button>
        </div>
      ) : (
        <div className="ob-state-loading" role="status">
          <span className="ob-spinner" /> {t("ob.restoring")}
        </div>
      )}
    </div>
  );
}

export function OnboardingConversationScreen() {
  const route = useRoute();
  const [state, dispatch] = useReducer(
    conversationReducer,
    initialConversationState,
  );
  const { persist } = useWizardStatePersist();
  // GET /company 404s until a human saved one; only a SETTLED lookup may
  // route — a transient error must not send an existing member down the
  // creator flow (nor a returning creator down the member flow).
  const existing = useCompany(true);
  const wizard = useQuery({
    queryKey: ["onboarding-conv-state"],
    queryFn: loadWizardState,
  });
  const voiceNeeded =
    wizard.data != null &&
    wizard.data.path === "creator" &&
    voiceProbeSteps.has(wizard.data.step);
  const voice = useQuery({
    queryKey: ["onboarding-conv-voice"],
    queryFn: probeVoice,
    enabled: voiceNeeded,
  });

  const restored = useRef(false);
  const settled =
    existing.isSuccess && wizard.isSuccess && (!voiceNeeded || voice.isSuccess);
  useEffect(() => {
    if (restored.current || state.act !== "welcome" || !settled) {
      return;
    }
    restored.current = true;
    const plan = restorePlan({
      state: wizard.data ?? null,
      profile: existing.data ?? null,
      voice: voice.data ?? null,
      routeConnect: route.id === "connect",
    });
    if (plan.kind === "complete") {
      navigate({ screen: "home" });
      return;
    }
    dispatch({
      type: "START",
      memberPath: plan.memberPath,
      companyConfirmed: plan.companyConfirmed,
      recap: plan.recap,
    });
    if (plan.companyConfirmed && plan.resumeTarget !== null) {
      dispatch({ type: "RESUME", target: plan.resumeTarget });
    }
  }, [state.act, settled, wizard.data, existing.data, voice.data, route.id]);

  // Act-transition checkpoints: the server remembers where the journey is,
  // so a mid-onboarding reload restores to the right act with recap. Only
  // pairs a live user action produces persist; the restore's RESUME lands
  // from co.confirmed and matches none of them. Best-effort by design: a
  // failed checkpoint never stalls the act (the finish write in the connect
  // act is the one gate that must land before navigation).
  const prevPhase = useRef<ConversationPhase | null>(null);
  useEffect(() => {
    const prev = prevPhase.current;
    prevPhase.current = state.phase;
    if (prev === null || prev === state.phase) {
      return;
    }
    const checkpoint = actCheckpoint(
      prev,
      state.phase,
      state.lastBuildStatus === "succeeded",
    );
    if (checkpoint !== null) {
      void persist({ ...checkpoint, values: EMPTY_DRAFT.values });
    }
  }, [state.phase, state.lastBuildStatus, persist]);

  if (state.act === "welcome") {
    return <RestoreGate lookups={[existing, wizard, voice]} />;
  }

  const voiceBuilt =
    state.lastBuildStatus === "succeeded" || voice.data?.built === true;

  return (
    <div className="ob-page ob-conv-page">
      {state.act === "company" && (
        <CompanyAct
          state={state}
          dispatch={dispatch}
          profile={existing.data ?? null}
          persist={persist}
        />
      )}
      {state.act === "voice" && (
        <VoiceAct
          state={state}
          dispatch={dispatch}
          initialSummary={voice.data?.summary ?? null}
        />
      )}
      {state.act === "results" && (
        <ResultsAct
          state={state}
          dispatch={dispatch}
          profile={existing.data ?? null}
          voiceBuilt={voiceBuilt}
        />
      )}
      {(state.act === "connect" || state.act === "done") && (
        <ConnectAct
          state={state}
          dispatch={dispatch}
          persist={persist}
          outcome={route.id === "connect" ? route.id2 : undefined}
        />
      )}
    </div>
  );
}
