import { Check } from "lucide-react";
import type { ReactNode } from "react";
import { useEffect, useRef } from "react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { Callout } from "../../design-system/callout";
import { useT } from "../../i18n";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import { CompanyStep } from "../onboarding-company-form";
import { ManualCompanyInterview } from "../onboarding-manual-interview";

// The company act's work surface: exactly one scene at a time — the pending
// decision, the triage review, the manual interview, or the edit escape
// hatch hosting the classic form. Narration on the left briefly lights the
// row it names, tying speech to evidence.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];

export type ArtifactMode = "dossier" | "edit";

export type FindingHighlight = Readonly<{
  /** The narration entry id that caused the pulse; a new entry re-pulses. */
  key: string;
  ids: readonly string[];
}>;

// Matches the ob-conv-pulse animation length in conversation.css.
const PULSE_MS = 1200;

/** The one look that can turn the state a refusal names into a different one,
 * where there is one — the route forward wherever the refusal is also what
 * holds Continue down. */
export type RefusalRetry = Readonly<{ run: () => void; busy: boolean }>;

/**
 * A confirm the server refused in a way pressing the same button again cannot
 * change, said on the surface that carries the button rather than in the
 * transcript beside it.
 *
 * The sentence arrives already translated: which of the refusals this is, and
 * therefore which words it takes, is the driver's knowledge — this pane only
 * knows where a refusal belongs.
 */
export type ConfirmRefusal = Readonly<{
  message: string;
  retry: RefusalRetry | null;
}>;

type CompanyActArtifactProps = Readonly<{
  mode: ArtifactMode;
  /** The manual interview replaces the dossier until its review begins. */
  manual: boolean;
  /** The triage review card; while set it replaces the dossier as the pane's
   * body — review is work, and the work surface is where work happens. */
  review?: ReactNode;
  read: CompanySiteRead | null;
  draft: CompanyDraft;
  setField: (field: CompanyFieldName, value: string) => void;
  onPickEntity: (entity: LegalEntity) => void;
  selectedFactKeys: readonly string[];
  setSelectedFactKeys: (keys: string[]) => void;
  missingRequired: readonly CompanyFieldName[];
  highlight: FindingHighlight | null;
  onSwitchMode: (mode: ArtifactMode) => void;
  onConfirm: () => void;
  confirmPending: boolean;
  confirmDisabled: boolean;
  saveError: string | null;
  /** A confirm the server refused, if one stands. It renders once, at the head
   * of this pane, for every scene the pane can be showing — the review board,
   * the edit form and the manual interview all press the same Continue.
   * Optional like `review`: a caller mounting this pane to inspect one scene has
   * no refusal to report, and an absent prop must read as "none" rather than as
   * a notice with nothing in it. */
  refusal?: ConfirmRefusal | null;
}>;

export function CompanyActArtifact(props: CompanyActArtifactProps) {
  const t = useT();
  const container = useRef<HTMLDivElement>(null);
  // Mirrors the thread's follow-the-bottom discipline: while the pointer is
  // over the dossier the human owns its scroll position, and a pulse must
  // not yank it.
  const hovered = useRef(false);
  const { highlight } = props;

  // Native listeners (not JSX handlers): the panel stays a static, purely
  // presentational element — hover is an input to the pulse effect, not an
  // interaction of its own.
  useEffect(() => {
    const root = container.current;
    if (!root) {
      return;
    }
    const enter = () => {
      hovered.current = true;
    };
    const leave = () => {
      hovered.current = false;
    };
    root.addEventListener("mouseenter", enter);
    root.addEventListener("mouseleave", leave);
    return () => {
      root.removeEventListener("mouseenter", enter);
      root.removeEventListener("mouseleave", leave);
    };
  }, []);

  useEffect(() => {
    if (!highlight) {
      return;
    }
    const root = container.current;
    if (!root) {
      return;
    }
    // Matching by attribute value (not a built selector) needs no escaping,
    // and jsdom lacks CSS.escape anyway.
    const wanted = new Set(highlight.ids);
    const nodes = [...root.querySelectorAll("[data-finding-id]")].filter(
      (node) => wanted.has(node.getAttribute("data-finding-id") ?? ""),
    );
    for (const node of nodes) {
      node.classList.add("ob-conv-pulse");
    }
    const first = nodes[0];
    if (first !== undefined && !hovered.current) {
      const reduceMotion =
        globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ??
        false;
      // jsdom has no scrollIntoView; in the browser it always exists.
      first.scrollIntoView?.({
        block: "nearest",
        behavior: reduceMotion ? "auto" : "smooth",
      });
    }
    const timer = globalThis.setTimeout(() => {
      for (const node of nodes) {
        node.classList.remove("ob-conv-pulse");
      }
    }, PULSE_MS);
    return () => {
      globalThis.clearTimeout(timer);
      for (const node of nodes) {
        node.classList.remove("ob-conv-pulse");
      }
    };
  }, [highlight]);

  return (
    <div className="mw-review ob-conv-artifact" ref={container}>
      {/* A scene owns its own headline (the prototype's one-surface rule);
          the generic dossier heading would be a second voice above it. */}
      {(props.review == null || props.mode !== "dossier") && (
        <div className="mw-review-heading">
          <span>{t("ob.ai.liveArtifact")}</span>
          <h2>{t("ob.ai.companyKnowledge")}</h2>
          <p>
            {t(
              props.manual
                ? "ob.ai.companyKnowledgeManualBody"
                : "ob.ai.companyKnowledgeBody",
            )}
          </p>
        </div>
      )}
      {props.refusal != null && <RefusalNotice refusal={props.refusal} />}
      <ArtifactBody {...props} />
    </div>
  );
}

// What the pane says about the press it just refused. `alert`, not `status`:
// the reader pressed Continue and nothing happened, so this interrupts —
// exactly the case the Callout contract reserves it for. The action beside it
// is the one look that can end the state named, where there is one; where
// Continue is blocked it IS the route forward, which is why it is the notice
// that carries it rather than the board's foot bar.
function RefusalNotice({
  refusal,
}: Readonly<{ refusal: ConfirmRefusal }>): ReactNode {
  const t = useT();
  return (
    <Callout
      tone="warn"
      live="alert"
      className="ob-conv-refusal"
      actions={
        refusal.retry === null ? undefined : (
          <Button
            small
            variant="ghost"
            disabled={refusal.retry.busy}
            onClick={refusal.retry.run}
          >
            {t("common.retry")}
          </Button>
        )
      }
    >
      <p>{refusal.message}</p>
    </Callout>
  );
}

// The conversational shell persists at act transitions and on confirm, not
// per keystroke: blur/persist callbacks intentionally store nothing here.
function persistLater(): undefined {
  return undefined;
}

function ArtifactBody(props: CompanyActArtifactProps) {
  const t = useT();
  if (props.review != null && props.mode === "dossier") {
    return props.review;
  }
  if (props.manual && props.mode === "dossier") {
    return (
      <ManualCompanyInterview
        values={props.draft.values}
        setField={props.setField}
        onPersist={persistLater}
        onBackToChoice={() => props.onSwitchMode("edit")}
        onComplete={() => props.onSwitchMode("edit")}
      />
    );
  }
  if (props.mode === "edit") {
    return (
      <>
        <Button
          small
          variant="ghost"
          onClick={() => props.onSwitchMode("dossier")}
        >
          {t("ob.conv.review.backToDossier")}
        </Button>
        <CompanyStep
          embedded
          draft={props.draft}
          setField={props.setField}
          saved={false}
          saveError={props.saveError}
          missingRequired={props.missingRequired}
          read={props.read}
          onPickEntity={props.onPickEntity}
          selectedFactKeys={props.selectedFactKeys}
          setSelectedFactKeys={props.setSelectedFactKeys}
          onFieldBlur={persistLater}
        />
        <div className="mw-confirm-company">
          <p>{t("ob.ai.confirmBoundary")}</p>
          <Button
            variant="primary"
            disabled={props.confirmDisabled || props.confirmPending}
            onClick={props.onConfirm}
          >
            {props.confirmPending ? (
              <>
                <span className="ob-spinner" /> {t("ob.s1.saving")}
              </>
            ) : (
              <>
                <Check aria-hidden /> {t("ob.ai.confirmCompany")}
              </>
            )}
          </Button>
        </div>
      </>
    );
  }
  return <DossierBody {...props} />;
}

// Between scenes there is nothing to stage: before a read there is nothing
// sourced, and a finished read's proposal is still on its way for a beat.
// Both waits say so instead of showing the reader a dossier they are about
// to leave.
function DossierBody(props: CompanyActArtifactProps) {
  const t = useT();
  if (props.read === null) {
    return (
      <>
        <p className="ob-conv-artifact-empty">{t("ob.conv.artifact.empty")}</p>
        <Button
          small
          variant="ghost"
          onClick={() => props.onSwitchMode("edit")}
        >
          {t("ob.conv.review.editDirectly")}
        </Button>
      </>
    );
  }
  return (
    <>
      <div className="ob-state-loading" role="status">
        <span className="ob-spinner" /> {t("ob.restoring")}
      </div>
      <Button small variant="ghost" onClick={() => props.onSwitchMode("edit")}>
        {t("ob.conv.review.editDirectly")}
      </Button>
    </>
  );
}
