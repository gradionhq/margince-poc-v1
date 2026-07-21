import { BookOpenText, Check, LockKeyhole, ShieldCheck } from "lucide-react";
import { type ReactNode, useId } from "react";
import type { components } from "../api/schema";
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
        <MarginceCore profile={profile} />
      </main>
    </div>
  );
}

export function MarginceCore({
  profile,
}: Readonly<{ profile?: AssistantProfile }>) {
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

      <CoreScene />

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

function CoreScene() {
  return (
    <div className="auth-core-scene" aria-hidden="true">
      <div className="auth-core-glow" />
      <div className="auth-core-orbit auth-core-orbit-context">
        <span className="auth-core-node auth-core-node-a" />
        <span className="auth-core-node auth-core-node-b" />
      </div>
      <div className="auth-core-orbit auth-core-orbit-evidence">
        <span className="auth-core-node auth-core-node-c" />
        <span className="auth-core-node auth-core-node-d" />
      </div>
      <div className="auth-core-orbit auth-core-orbit-approval">
        <span className="auth-core-gate" />
      </div>
      <span className="auth-core-thread auth-core-thread-a" />
      <span className="auth-core-thread auth-core-thread-b" />
      <span className="auth-core-thread auth-core-thread-c" />
      <div className="auth-core-mark-shell">
        <MarginceMark />
      </div>
    </div>
  );
}

function MarginceMark() {
  return (
    <svg viewBox="0 0 299 230" role="presentation" focusable="false">
      <path
        className="auth-core-mark-soft"
        d="M141.688 223.911V212.017C141.688 210.362 142.722 209.259 143.239 208.914L160.821 191.849C166.613 186.47 172.198 193.4 172.198 197.02V223.911C172.198 228.048 168.061 229.427 165.993 229.599H147.376C143.239 229.599 141.86 225.807 141.688 223.911Z"
      />
      <path
        className="auth-core-mark-mid"
        d="M191.312 223.907V164.954C191.312 163.299 192.347 162.196 192.864 161.852L210.446 144.786C216.238 139.408 221.823 146.338 221.823 149.957V223.907C221.823 228.044 217.686 229.423 215.618 229.595H197.001C192.864 229.595 191.485 225.803 191.312 223.907Z"
      />
      <path
        className="auth-core-mark-soft"
        d="M241 223.886V112.704C241 111.049 242.034 109.946 242.551 109.602L260.134 92.5361C265.926 87.1579 271.511 94.0875 271.511 97.7074V223.886C271.511 228.023 267.374 229.402 265.305 229.574H246.688C242.551 229.574 241.172 225.782 241 223.886Z"
      />
      <path d="M0 29.4771V213.06C0 232.09 40.8535 237.882 40.8535 212.025V94.636C40.8535 90.9127 44.9906 91.5196 46.0249 92.5675C72.2263 119.114 125.974 173.344 131.352 177.895C136.73 182.445 142.556 179.791 144.797 177.895C187.202 135.49 272.219 50.369 273.046 49.128C273.874 47.887 275.115 48.611 275.632 49.128C278.735 52.403 285.147 59.057 285.975 59.471C293.732 65.159 298.386 59.643 298.386 55.851V9.826C298.386 0 286.492 0 280.803 0H235.296C228.573 0 228.573 8.274 230.124 9.826C233.917 13.963 241.812 22.444 243.053 23.271C244.294 24.098 244.259 24.995 244.087 25.34C210.301 58.264 144.797 116.356 142.729 118.424C140.66 120.493 138.419 119.286 137.557 118.424L31.028 16.032C15.721 0.724 0 20.169 0 29.477Z" />
    </svg>
  );
}
