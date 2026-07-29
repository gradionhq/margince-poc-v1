import { BookOpenText, Check, LockKeyhole, ShieldCheck } from "lucide-react";
import { type ReactNode, useId } from "react";
import type { components } from "../api/schema";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";

/**
 * The unauthenticated surface: two regions (ADR-0076 Decision 1).
 *
 * **The task region is first in the DOM**, and that is the whole reason this
 * component owns the frame rather than each screen writing its own grid.
 * Visually the identity region sits on the left at wide widths; in reading
 * order, keyboard order, and the narrow stack the task comes first. `order` on
 * the grid children buys that, and it is the kind of thing that gets "tidied
 * away" by someone reordering JSX to match the picture — `auth.test.tsx` asserts
 * the DOM order for exactly that reason.
 *
 * Every one of the surface's four outcomes renders in this frame (§4) — sign-in,
 * password reset, connection problem, installation unavailable — so a reviewer
 * sees the same screen shape whatever went wrong.
 */
export type AssistantProfile = components["schemas"]["AssistantProfile"];
export type AuthPhase =
  | "idle"
  | "signing-in"
  | "success"
  | "error"
  | "quiet"
  | "unavailable";

function coreState(phase: AuthPhase): MarginceCoreState {
  if (phase === "signing-in") {
    return "working";
  }
  if (phase === "idle") {
    // Waiting on the user, and saying so. `idle` would be honest too, but the
    // surface IS asking for something, and `listening` is the state that means
    // that in the closed vocabulary.
    return "listening";
  }
  return phase;
}

const providerKeys: Record<AssistantProfile["providers"][number], MessageKey> =
  {
    anthropic: "auth.coreProviderAnthropic",
    gemini: "auth.coreProviderGemini",
    ollama: "auth.coreProviderOllama",
    openai: "auth.coreProviderOpenAI",
    openai_compatible: "auth.coreProviderCompatible",
    vllm: "auth.coreProviderVllm",
  };

const modeKeys: Record<AssistantProfile["inference_mode"], MessageKey> = {
  cloud: "auth.coreModeCloud",
  local: "auth.coreModeLocal",
  hybrid: "auth.coreModeHybrid",
  none: "auth.coreModeNone",
  development: "auth.coreModeDevelopment",
};

export function AuthExperience({
  children,
  profile,
  phase,
}: Readonly<{
  children: ReactNode;
  profile?: AssistantProfile;
  phase: AuthPhase;
}>) {
  return (
    <div className="auth-surface" data-auth-phase={phase}>
      <main className="auth-task">
        <div className="auth-task-in">{children}</div>
      </main>
      <IdentityRegion profile={profile} phase={phase} />
    </div>
  );
}

/**
 * The identity region (ADR-0076 Decision 2).
 *
 * Everything here is one of exactly four kinds of sentence, and the list is
 * closed: the system's presence and name, a limit on its own behaviour in the
 * first person, a server-read fact about this installation, nothing else. The
 * test is one line — *a sentence that would still be true and still desirable on
 * a marketing page is out of bounds*.
 *
 * The three limits are the VOICE-RULE-6 register: architectural guarantees the
 * system enforces, stated absolutely. They are not bullets selling a feature,
 * which is the distinction the July login spec collapsed and ADR-0076 restored.
 *
 * NO CONTROLS AND NO COPY THE TASK DEPENDS ON. That is what stops this region
 * competing with the form, and it is structural rather than a matter of taste.
 */
export function IdentityRegion({
  profile,
  phase,
}: Readonly<{ profile?: AssistantProfile; phase: AuthPhase }>) {
  const t = useT();
  const identityId = useId();
  return (
    <aside className="auth-identity" aria-labelledby={identityId}>
      <div className="auth-identity-top">
        <p className="auth-kicker" id={identityId}>
          <span className="auth-kicker-dot" aria-hidden />
          {t("auth.coreDisclosure")}
        </p>
        {/* Not a heading. The one h1 belongs to the task (§6.4), and this is a
            paragraph however large it is set. */}
        <p className="auth-statement">{t("auth.coreBoundary")}</p>
      </div>

      <MarginceCoreScene state={coreState(phase)} />

      <div className="auth-identity-foot">
        {/* Absent rather than guessed: a runtime line the frontend invented is
            the one thing Decision 2c forbids, so an in-flight or failed probe
            renders nothing. The row reserves its height in CSS so the column
            does not jump when it arrives. */}
        {profile && <RuntimePosture profile={profile} />}
        <ul className="auth-limits">
          <Limit icon={<LockKeyhole />} text={t("auth.corePermission")} />
          <Limit icon={<BookOpenText />} text={t("auth.coreCites")} />
          <Limit icon={<ShieldCheck />} text={t("auth.coreWaits")} />
        </ul>
      </div>
    </aside>
  );
}

function RuntimePosture({ profile }: Readonly<{ profile: AssistantProfile }>) {
  const t = useT();
  if (profile.state === "unconfigured") {
    return (
      <div className="auth-runtime">
        <span className="auth-runtime-state">{t("auth.coreUnconfigured")}</span>
        <span>{t("auth.coreStillWorks")}</span>
      </div>
    );
  }
  if (profile.state === "development") {
    return (
      <div className="auth-runtime">
        <span className="auth-runtime-state">{t("auth.coreDevelopment")}</span>
        <span>{t(modeKeys[profile.inference_mode])}</span>
      </div>
    );
  }
  const providers = profile.providers
    .map((provider) => t(providerKeys[provider]))
    .join(" + ");
  return (
    <div className="auth-runtime">
      <span className="auth-runtime-state">
        <Check aria-hidden /> {t("auth.coreConfigured")}
      </span>
      <span>
        {[providers, t(modeKeys[profile.inference_mode])]
          .filter(Boolean)
          .join(" · ")}
      </span>
    </div>
  );
}

function Limit({ icon, text }: Readonly<{ icon: ReactNode; text: string }>) {
  return (
    <li>
      <span className="auth-limit-icon" aria-hidden>
        {icon}
      </span>
      {text}
    </li>
  );
}
