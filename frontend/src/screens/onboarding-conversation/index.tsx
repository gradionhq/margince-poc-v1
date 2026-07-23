import { useQuery } from "@tanstack/react-query";
import type { Dispatch } from "react";
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
  type ConversationEvent,
  type ConversationPhase,
  type ConversationState,
  conversationReducer,
  initialConversationState,
} from "./conversation-machine";
import { restorePlan, type VoiceRestoreProbe } from "./restore";
import { ResultsAct } from "./results-act";
import type { WizardPersistInput } from "./use-wizard-state";
import { useWizardStatePersist } from "./use-wizard-state";
import { VoiceAct } from "./voice-act";

// The conversational onboarding shell — THE onboarding experience: one pure
// machine owns where the conversation is, and each act renders inside the
// shared Margince workbench. On mount the shell reads the server truth
// (wizard state, company, voice) and restores through START + RESUME; the
// wizard state's `path` field is THE member signal, with company-exists only
// the fallback when no state row exists.

type OnboardingState = components["schemas"]["OnboardingState"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

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

// The restore lookups and the one START/RESUME dispatch, as a hook: the
// server truth (wizard state, company, voice, persisted read) is read once,
// and only a SETTLED set of lookups may route — a transient error must not
// send an existing member down the creator flow (nor a returning creator
// down the member flow).
function useRestore(
  state: ConversationState,
  dispatch: Dispatch<ConversationEvent>,
  routeConnect: boolean,
) {
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
  // The persisted read is only worth fetching while the company act is
  // still open: a reload must reattach a running or finished read instead
  // of stranding the user's work behind a fresh intro.
  const persistedReadId =
    wizard.data != null &&
    (wizard.data.step === "read" || wizard.data.step === "confirm")
      ? (wizard.data.site_read_id ?? null)
      : null;
  const persistedRead = useQuery({
    queryKey: ["onboarding-conv-read", persistedReadId],
    enabled: persistedReadId !== null,
    queryFn: async (): Promise<CompanySiteRead | null> => {
      const { data, error, response } = await api.GET(
        "/company/site-reads/{readId}",
        { params: { path: { readId: persistedReadId ?? "" } } },
      );
      if (error) {
        // A read the server no longer serves is not a restore failure; the
        // company act simply reopens fresh.
        if (response.status === 404) {
          return null;
        }
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const restored = useRef(false);
  const settled =
    existing.isSuccess &&
    wizard.isSuccess &&
    (!voiceNeeded || voice.isSuccess) &&
    (persistedReadId === null || persistedRead.isSuccess);
  useEffect(() => {
    if (restored.current || state.act !== "welcome" || !settled) {
      return;
    }
    restored.current = true;
    const plan = restorePlan({
      state: wizard.data ?? null,
      profile: existing.data ?? null,
      voice: voice.data ?? null,
      read: persistedRead.data ?? null,
      routeConnect,
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
    // Reattaching happens through the ordinary machine event, so legality
    // and correlation hold exactly as for a live read.
    if (plan.adoptRead !== null) {
      dispatch({ type: "READ_STARTED", readId: plan.adoptRead.id });
    }
    if (plan.companyConfirmed && plan.resumeTarget !== null) {
      dispatch({ type: "RESUME", target: plan.resumeTarget });
    }
  }, [
    state.act,
    settled,
    wizard.data,
    existing.data,
    voice.data,
    persistedRead.data,
    routeConnect,
    dispatch,
  ]);

  return {
    existing,
    voice,
    persistedRead,
    lookups: [existing, wizard, voice, persistedRead],
  };
}

export function OnboardingConversationScreen() {
  const route = useRoute();
  const [state, dispatch] = useReducer(
    conversationReducer,
    initialConversationState,
  );
  const { persist } = useWizardStatePersist();
  const { existing, voice, persistedRead, lookups } = useRestore(
    state,
    dispatch,
    route.id === "connect",
  );

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
    return <RestoreGate lookups={lookups} />;
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
          adoptedRead={
            persistedRead.data != null &&
            persistedRead.data.id === state.activeReadId
              ? persistedRead.data
              : null
          }
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
