import {
  BookOpenText,
  Check,
  LockKeyhole,
  PenLine,
  ShieldCheck,
} from "lucide-react";
import { type ReactNode, useId } from "react";
import type { components } from "../api/schema";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { useTypeStream } from "../design-system/motion";
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

/**
 * The motion budget, in one place (ADR-0076 Decision 5): the statement reaches
 * its full text within 1200 ms of mount, or renders complete immediately.
 *
 * The speed is DERIVED from the text rather than fixed, and that is the whole
 * reason the budget survives translation. A fixed 34 ms/char (which is what the
 * artifact uses) blows the budget at 36 characters, and the German statement is
 * roughly a quarter longer than the English one, so a constant tuned on English
 * would quietly break for the beachhead's own language. Solve for the budget and
 * the copy can grow without anyone re-checking this number.
 */
const TYPE_START_MS = 90;
const TYPE_BUDGET_MS = 1010;

function typeSpeedFor(text: string): number {
  // Clamped at both ends: a very short statement should not crawl at 26 ms/char,
  // and a very long one should not become an unreadable 3 ms flicker.
  return Math.max(8, Math.min(26, Math.floor(TYPE_BUDGET_MS / text.length)));
}

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
        <LegalFooter />
      </main>
      <IdentityRegion profile={profile} phase={phase} />
    </div>
  );
}

/**
 * The legal line, in the task region and on every outcome (§6.7).
 *
 * It belongs to the task rather than to the identity region, and not by
 * convenience: the identity region's copy is a closed list of four sentence
 * kinds (ADR-0076 Decision 2), and a terms link is none of them.
 *
 * It is the SECOND footer in this region, and the two do not compete: the legal
 * line is region chrome and sits in the task grid's bottom `auto` row as a
 * sibling of `.auth-task-in`, while the locale switcher (`.auth-footer`, in
 * `auth.tsx`) is a control and stays the last child of the card column inside
 * `.auth-task-in`. Different grid rows, so neither can push the other around.
 *
 * **The hrefs are server paths, not app routes.** Both documents have to be
 * readable BEFORE anyone authenticates, so they cannot sit behind the SPA
 * router — the SPA is what a 401 keeps you out of. Nothing serves them yet and
 * they 404: a missing document, which is a content gap rather than a faked
 * capability.
 */
function LegalFooter() {
  const t = useT();
  return (
    <div className="auth-legal">
      {/* Plain text, and the only sentence here. §6.7: it states that ACCESS is
          restricted and nothing about data being safe, encrypted or compliant —
          those are outcome claims the installation's own configuration can
          contradict (VOICE-RULE-7). It must also not read as a control, which is
          why it is not inside the links group. */}
      <p>{t("auth.legalProtected")}</p>
      <span className="auth-legal-links">
        <a href="/legal/terms">{t("auth.legalTerms")}</a>
        <span className="auth-legal-sep" aria-hidden />
        <a href="/legal/privacy">{t("auth.legalPrivacy")}</a>
      </span>
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
 * The four limits are the VOICE-RULE-6 register: architectural guarantees the
 * system enforces, stated absolutely. They are not bullets selling a feature,
 * which is the distinction the July login spec collapsed and ADR-0076 restored.
 *
 * ORDER, top to bottom, and it is the artifact's: the Core, then the system's
 * name, then what it says about itself, then the scope of that, then the limits,
 * then the server-read runtime line. The Core leads because it is the thing that
 * is PRESENT; the copy explains what is present. The previous order opened with
 * the sentence and buried the Core in the middle, which is a paragraph with an
 * illustration rather than a system introducing itself.
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
      <MarginceCoreScene state={coreState(phase)} />

      <div className="auth-identity-copy">
        <p className="auth-kicker" id={identityId}>
          <span className="auth-kicker-dot" aria-hidden />
          {t("auth.coreDisclosure")}
        </p>

        <TypedStatement text={t("auth.coreBoundary")} />

        <p className="auth-scope">{t("auth.coreScope")}</p>

        {/* Four, from the artifact's five, and two of the five did not travel.
            "Enriches records from sources it names" is a capability claim and
            Decision 2 admits only limits. "Switch it off, the CRM still works"
            IS a limit, but it is already the second half of the runtime line
            below when the AI is unconfigured, and that is where it belongs: it
            is a server-read fact about this installation, not a standing
            promise. Saying it twice on one screen weakens both. */}
        <ul className="auth-limits">
          <Limit icon={<LockKeyhole />} text={t("auth.corePermission")} />
          <Limit icon={<BookOpenText />} text={t("auth.coreCites")} />
          <Limit icon={<ShieldCheck />} text={t("auth.coreWaits")} />
          <Limit icon={<PenLine />} text={t("auth.coreMarks")} />
        </ul>
      </div>

      {/* Absent rather than guessed: a runtime line the frontend invented is
          the one thing Decision 2c forbids, so an in-flight or failed probe
          renders nothing. The row reserves its height in CSS so the column
          does not jump when it arrives. */}
      <div className="auth-identity-foot">
        {profile && <RuntimePosture profile={profile} />}
      </div>
    </aside>
  );
}

/**
 * The typed statement, in its own component so that a state update per character
 * re-renders one paragraph rather than the region that also holds the Core.
 *
 * This is a precaution, not a fix for a measured bug, and the distinction is
 * worth recording because the obvious reading is wrong: a stream that looked
 * like 300 ms/char turned out to be Chrome throttling `setTimeout` to 1/second in
 * a tab that was not visible, which `useTypeStream` now handles at the source.
 * The isolation stays because per-tick state next to a WebGL canvas is a bad
 * shape regardless of whether it has bitten yet.
 *
 * Not a heading. The one h1 belongs to the task (§6.4), and this is a paragraph
 * however large it is set. Two details keep it honest:
 *
 *  - the GHOST is an invisible copy of the full sentence holding the final height
 *    open, so the scope line and the limits below never move. A typewriter that
 *    reflows the column under itself is worse than none.
 *  - the sentence reaches assistive tech COMPLETE, through an `.sr-only` span,
 *    because a screen reader must not be fed a partial sentence character by
 *    character. It is a span rather than an `aria-label` on the <p> because a
 *    paragraph has no role that supports being named — biome's a11y lint says so
 *    too.
 *
 * Under reduced motion (or on a hidden tab) `useTypeStream` returns the complete
 * text on its first render and reports `done`, so this is a static paragraph with
 * no caret.
 */
function TypedStatement({ text }: Readonly<{ text: string }>) {
  const { shown, done } = useTypeStream(text, {
    speed: typeSpeedFor(text),
    startDelay: TYPE_START_MS,
  });
  return (
    <p className="auth-statement">
      <span className="sr-only">{text}</span>
      <span className="auth-statement-ghost" aria-hidden>
        {text}
      </span>
      <span className="auth-statement-live" aria-hidden>
        {shown}
        {done ? null : <span className="auth-caret" />}
      </span>
    </p>
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
