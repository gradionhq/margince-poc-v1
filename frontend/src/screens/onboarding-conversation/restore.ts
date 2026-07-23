import type { components } from "../../api/schema";
import type { NarrationEntry, ResumePoint } from "./conversation-types";

// Session restore as a pure decision: server state in, a start plan out.
// The wizard state's `path` field is THE member signal; an existing company
// profile is only the fallback when no state row exists (a returning creator
// has both a state row and a saved company, and must NOT be demoted to the
// member path, which would silently skip the voice act). Recap turns are
// derived here from server facts, never persisted narration, so a reload
// summarizes what happened instead of replaying it.

type OnboardingState = components["schemas"]["OnboardingState"];
type CompanyProfile = components["schemas"]["CompanyProfile"];
type CorpusSummary = components["schemas"]["VoiceCorpusSummary"];

export type VoiceRestoreProbe = Readonly<{
  /** An active or candidate version exists: the voice was actually built. */
  built: boolean;
  /** The server corpus meter; null when no voice profile exists yet. */
  summary: CorpusSummary | null;
}>;

export type RestoreInputs = Readonly<{
  /** GET /onboarding/state; null when nothing was persisted (404). */
  state: OnboardingState | null;
  /** GET /company; null while no human confirmed one (404). */
  profile: CompanyProfile | null;
  /** Voice server truth; null when the probe was not needed (member path,
   * or the journey has not reached the voice act). */
  voice: VoiceRestoreProbe | null;
  /** The OAuth return deep link (#/onboarding/connect/...) lands mid-journey
   * and must reopen the connect act, exactly like the classic coordinator. */
  routeConnect: boolean;
}>;

export type RestorePlan =
  | { kind: "complete" }
  | {
      kind: "start";
      memberPath: boolean;
      companyConfirmed: boolean;
      /** Where RESUME lands; null means the company act is still open. */
      resumeTarget: ResumePoint | null;
      recap: readonly NarrationEntry[];
    };

// The wizard step values that mean the company confirmation already
// happened (the classic coordinator only advances past step "read"/"confirm"
// by persisting the confirmed company).
const confirmedSteps = new Set<OnboardingState["step"]>([
  "voice",
  "results",
  "connect",
]);

function creatorTarget(
  state: OnboardingState,
  voice: VoiceRestoreProbe | null,
): ResumePoint {
  if (state.step === "connect") {
    return "cn.consent";
  }
  if (state.step === "results") {
    return "re.recap";
  }
  // step "voice": the act is still open. A skip recorded early is honored;
  // an existing corpus reopens collection instead of re-inviting.
  if (state.voice_skipped) {
    return "vo.skipped";
  }
  const words = voice?.summary?.total_words ?? 0;
  return words > 0 ? "vo.collecting" : "vo.invite";
}

function recapEntries(
  inputs: RestoreInputs,
  memberPath: boolean,
  target: ResumePoint,
): NarrationEntry[] {
  const { state, profile, voice } = inputs;
  const entries: NarrationEntry[] = [
    { kind: "narration", id: "recap:back", i18nKey: "ob.conv.recap.back" },
  ];
  // The company act's recap: confirmed with the saved name, or the honest
  // "not saved" when the state row claims progress the profile lacks.
  if (profile !== null) {
    entries.push({
      kind: "narration",
      id: "recap:company",
      i18nKey: "ob.conv.recap.company",
      params: { name: profile.display_name },
    });
  } else {
    entries.push({
      kind: "narration",
      id: "recap:company-unsaved",
      i18nKey: "ob.conv.recap.companyUnsaved",
    });
  }
  if (memberPath) {
    return entries;
  }
  // The voice act's recap, only once that act concluded or holds material.
  if (voice?.built === true) {
    entries.push({
      kind: "narration",
      id: "recap:voice-built",
      i18nKey: "ob.conv.recap.voiceBuilt",
    });
  } else if (state?.voice_skipped === true) {
    entries.push({
      kind: "narration",
      id: "recap:voice-skipped",
      i18nKey: "ob.conv.recap.voiceSkipped",
    });
  } else if (target === "vo.collecting") {
    entries.push({
      kind: "narration",
      id: "recap:corpus",
      i18nKey: "ob.conv.recap.corpus",
      params: { words: voice?.summary?.total_words ?? 0 },
    });
  }
  return entries;
}

export function restorePlan(inputs: RestoreInputs): RestorePlan {
  const { state, profile, voice, routeConnect } = inputs;
  if (state?.step === "complete") {
    return { kind: "complete" };
  }
  const memberPath =
    state !== null ? state.path === "member" : profile !== null;
  const companyConfirmed = state !== null && confirmedSteps.has(state.step);
  if (state === null || !companyConfirmed) {
    // Fresh, or the company act is still open (step read/confirm): the act
    // restarts and re-derives its review from the server; no recap yet.
    return {
      kind: "start",
      memberPath,
      companyConfirmed: false,
      resumeTarget: null,
      recap: [],
    };
  }
  const target: ResumePoint =
    memberPath || routeConnect ? "cn.consent" : creatorTarget(state, voice);
  return {
    kind: "start",
    memberPath,
    companyConfirmed: true,
    resumeTarget: target,
    recap: recapEntries(inputs, memberPath, target),
  };
}
