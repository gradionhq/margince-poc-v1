import { BookOpenText, Check, LockKeyhole, ShieldCheck } from "lucide-react";
import { type ReactNode, useId } from "react";
import type { components } from "../api/schema";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";

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
    <div className="auth-page">
      <main className="auth-experience" data-auth-phase={phase}>
        <section className="auth-column">{children}</section>
        <MarginceCore profile={profile} phase={phase} />
      </main>
    </div>
  );
}

export function MarginceCore({
  profile,
  phase,
}: Readonly<{ profile?: AssistantProfile; phase: AuthPhase }>) {
  const t = useT();
  const identityId = useId();
  return (
    <aside className="auth-core" aria-labelledby={identityId}>
      <div className="auth-core-copy">
        <p className="auth-core-kicker" id={identityId}>
          <span className="auth-core-identity-dot" aria-hidden />
          {t("auth.coreDisclosure")}
        </p>
        <p className="auth-core-statement">{t("auth.coreBoundary")}</p>
      </div>

      <MarginceCoreScene state={coreState(phase)} />

      <div className="auth-core-meta">
        {profile && <RuntimeProfile profile={profile} />}
        <ul className="auth-core-trust">
          <TrustFact icon={<LockKeyhole />} text={t("auth.corePermission")} />
          <TrustFact icon={<BookOpenText />} text={t("auth.coreCites")} />
          <TrustFact icon={<ShieldCheck />} text={t("auth.coreWaits")} />
        </ul>
      </div>
    </aside>
  );
}

function RuntimeProfile({ profile }: Readonly<{ profile: AssistantProfile }>) {
  const t = useT();
  if (profile.state === "unconfigured") {
    return (
      <div className="auth-core-runtime">
        <span className="auth-core-runtime-state">
          {t("auth.coreUnconfigured")}
        </span>
        <span>{t("auth.coreStillWorks")}</span>
      </div>
    );
  }
  if (profile.state === "development") {
    return (
      <div className="auth-core-runtime">
        <span className="auth-core-runtime-state">
          {t("auth.coreDevelopment")}
        </span>
        <span>{t(modeKeys[profile.inference_mode])}</span>
      </div>
    );
  }
  const providers = profile.providers
    .map((provider) => t(providerKeys[provider]))
    .join(" + ");
  return (
    <div className="auth-core-runtime">
      <span className="auth-core-runtime-state">
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

function TrustFact({
  icon,
  text,
}: Readonly<{ icon: ReactNode; text: string }>) {
  return (
    <li>
      <span className="auth-core-trust-icon" aria-hidden>
        {icon}
      </span>
      {text}
    </li>
  );
}
