import { Check, ShieldCheck } from "lucide-react";
import { type ReactNode, useId } from "react";
import type { components } from "../api/schema";
import { ThemeToggle } from "../app/theme-toggle";
import {
  MarginceCoreScene,
  type MarginceCoreState,
} from "../design-system/margince-core";
import { useDocumentIntro, useTypeStream } from "../design-system/motion";
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
    // Work in flight, and the only work this surface ever does.
    return "working";
  }
  if (phase === "success") {
    // A finished run settles back to idle: there is no state of its own for
    // "done".
    return "idle";
  }
  if (phase === "error") {
    return "error";
  }
  if (phase === "unavailable") {
    // The installation cannot be reached, which is the same shape of failure as
    // a source the agent cannot get to: nothing is wrong, nothing is reachable,
    // and that is a person's problem to resolve, not the agent's.
    return "warning";
  }
  // idle and quiet both: nothing is running and nothing is staged. The surface
  // is waiting on a person, and the Core does not claim to be listening for
  // them — the agent reads captured activity, it holds no conversation.
  return "idle";
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
 * its full text within 2000 ms of mount, or renders complete immediately.
 *
 * The speed is DERIVED from the text rather than fixed, and that is the whole
 * reason the budget survives translation. A fixed ms/char constant tuned on the
 * English statement quietly breaks for German, which runs roughly a quarter
 * longer — the beachhead's own language. Solve for the budget and the copy can
 * grow without anyone re-checking this number.
 *
 * The budget is deliberately SLOW. A statement about what the system may do with
 * your context is the one sentence on this screen worth reading, and typing it
 * out in a second reads as a loading effect rather than as something being said.
 * The ceiling on the clamp exists so a short translation still types at a
 * readable pace instead of finishing before the eye arrives.
 */
export const TYPE_START_MS = 140;
export const TYPE_BUDGET_MS = 2000;

export function typeSpeedFor(text: string): number {
  // The budget covers the WHOLE reveal, so the lead-in is spent before the
  // first character and what is left is divided between the GAPS, of which
  // there is one fewer than there are characters. Dividing the full budget by
  // the full length overran it by the lead-in plus one interval, which is how
  // the 2000 ms ceiling was missed on every string.
  const gaps = Math.max(1, text.length - 1);
  const perGap = Math.floor((TYPE_BUDGET_MS - TYPE_START_MS) / gaps);
  // Clamped at both ends: a very short statement should not crawl, and a very
  // long one should not become an unreadable flicker.
  return Math.max(12, Math.min(42, perGap));
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
  // The entry choreography belongs to the page load, not to this mount: every
  // animation below — the staggered rows, the typed statement — is gated on it,
  // so a remount renders the surface already arrived. See useDocumentIntro.
  const intro = useDocumentIntro();
  return (
    <div
      className="auth-surface"
      data-auth-phase={phase}
      data-auth-intro={intro ? "play" : "done"}
    >
      {/* A div, NOT a <main>. The frame that hosts this surface already opens a
          <main> (App.tsx's RaillessFrame), and a nested main is invalid HTML that
          puts two "main" entries in a screen reader's landmark rotor — so the
          user is asked to choose between two things claiming to be the one main
          region. The task region's own identity comes from being the first thing
          in the DOM (see the order note above), not from the tag. */}
      <div className="auth-task">
        <div className="auth-task-in">{children}</div>
        <LegalFooter />
      </div>
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
      {/* The bottom row is the surface's chrome row, and the theme control is
          chrome: it changes how this page looks and claims nothing about the
          organization. It sits after the two links, behind a separator, so the
          legal sentence above still reads as a statement and not as a control.
          The row already wraps, so the extra item cannot widen the surface. */}
      <span className="auth-legal-links">
        <a href="/legal/terms">{t("auth.legalTerms")}</a>
        <span className="auth-legal-sep" aria-hidden />
        <a href="/legal/privacy">{t("auth.legalPrivacy")}</a>
        <span className="auth-legal-sep" aria-hidden />
        <ThemeToggle />
      </span>
    </div>
  );
}

/**
 * The identity region: the system introducing itself, in its own voice.
 *
 * ORDER, top to bottom, and every row of it is load-bearing: the Core, the
 * disclosure, the greeting, what the system is for, what it does, the one
 * promise, the handover, then the server-read runtime line. The Core leads
 * because it is the thing that is PRESENT; the copy explains what is present. An
 * earlier order opened with a sentence and buried the Core in the middle, which
 * is a paragraph with an illustration rather than a system introducing itself.
 *
 * The five copy rows are ONE paragraph somebody is saying, not a list of claims,
 * so they are read in sequence or not at all — the greeting means nothing after
 * the promise. That is why they are five sibling paragraphs in a fixed order
 * rather than a collection something could reorder or filter.
 *
 * Two bounds this region used to hold and deliberately no longer does: it
 * admitted only limits on the system's own behaviour plus server-read facts
 * about the installation, so there was no greeting; and it carried no copy the
 * task depended on, so there was no handover. The handover line's whole job is
 * to point at the form in the other half of the screen, which is why it is the
 * last thing said.
 *
 * STILL NO CONTROLS. That is what keeps the region from competing with the form,
 * and it is structural rather than a matter of taste.
 */
export function IdentityRegion({
  profile,
  phase,
}: Readonly<{ profile?: AssistantProfile; phase: AuthPhase }>) {
  const t = useT();
  const identityId = useId();
  return (
    /*
     * The column is a plain wrapper and the ASIDE is the region — the split is
     * what lets the mobile layout put the Core above the form and the words below
     * it. Below 960 the wrapper becomes `display: contents`, so the Core and the
     * aside become rows of the surface grid and the task can sit between them. A
     * wrapper with no role can dissolve like that; the aside cannot, because a
     * landmark that stops being a box stops being reliably reported.
     *
     * The Core moving out of the aside costs nothing semantically: it is
     * decoration (WDS-CORE-4, `aria-hidden`), and every state it shows is also
     * stated in words inside the region.
     */
    <div className="auth-identity-col">
      <MarginceCoreScene state={coreState(phase)} />

      <aside className="auth-identity" aria-labelledby={identityId}>
        <div className="auth-identity-copy">
          <p className="auth-kicker" id={identityId}>
            {t("auth.coreDisclosure")}
          </p>

          {/* The greeting is what gets typed, and it is the only row that does:
              a system saying its own name as it arrives is what the motion is
              ABOUT. Everything under it fades up complete, so a reader who looks
              down mid-reveal finds finished sentences rather than four
              paragraphs assembling themselves. */}
          <TypedStatement text={t("auth.coreGreeting")} />

          <p className="auth-purpose">{t("auth.corePurpose")}</p>

          <p className="auth-scope">{t("auth.coreWork")}</p>

          {/* The badge marks the promise as the one absolute on this screen, and
              it is the treatment the region's older list of limits carried,
              because this sentence is that same register. A paragraph rather
              than a one-item list: a <ul> of one tells a screen reader there is
              a list to walk when there is a sentence to read. */}
          <p className="auth-promise">
            <span className="auth-promise-icon" aria-hidden>
              <ShieldCheck />
            </span>
            {t("auth.corePromise")}
          </p>

          <p className="auth-handover">{t("auth.coreHandover")}</p>
        </div>

        {/* Absent rather than guessed: a runtime line the frontend invented is
            the one thing Decision 2c forbids, so an in-flight or failed probe
            renders nothing. The row reserves its height in CSS so the column
            does not jump when it arrives. */}
        <div className="auth-identity-foot">
          {profile && <RuntimePosture profile={profile} />}
        </div>
      </aside>
    </div>
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
  // Typed once per page load, exactly like the fades around it: a statement
  // that retypes itself under a reader who has already read it says the page
  // reloaded, and nothing did. A remount lands on the finished sentence.
  const intro = useDocumentIntro();
  const stream = useTypeStream(text, {
    speed: typeSpeedFor(text),
    startDelay: TYPE_START_MS,
    enabled: intro,
  });
  const shown = intro ? stream.shown : text;
  const done = intro ? stream.done : true;
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
