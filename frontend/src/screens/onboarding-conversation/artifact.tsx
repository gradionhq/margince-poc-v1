import { Check } from "lucide-react";
import { useEffect, useRef } from "react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import { CompanyStep } from "../onboarding-company-form";
import { ManualCompanyInterview } from "../onboarding-manual-interview";
import { ReadEvidence } from "../onboarding-read";

// The right panel of the company act: a living dossier of what the read
// grounded, an edit escape hatch hosting the classic form, and — on the
// manual path — the interview questions. Narration on the left briefly
// lights the dossier card it names, tying speech to evidence.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];

export type ArtifactMode = "dossier" | "edit";

export type FindingHighlight = Readonly<{
  /** The narration entry id that caused the pulse; a new entry re-pulses. */
  key: string;
  ids: readonly string[];
}>;

const PULSE_MS = 1600;

type CompanyActArtifactProps = Readonly<{
  mode: ArtifactMode;
  /** The manual interview replaces the dossier until its review begins. */
  manual: boolean;
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
}>;

export function CompanyActArtifact(props: CompanyActArtifactProps) {
  const t = useT();
  const container = useRef<HTMLDivElement>(null);
  const { highlight } = props;

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
      <ArtifactBody {...props} />
    </div>
  );
}

// The conversational shell persists at act transitions and on confirm, not
// per keystroke: blur/persist callbacks intentionally store nothing here.
function persistLater(): undefined {
  return undefined;
}

function ArtifactBody(props: CompanyActArtifactProps) {
  const t = useT();
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
  return (
    <>
      {props.read ? (
        <ReadEvidence read={props.read} />
      ) : (
        <p className="ob-conv-artifact-empty">{t("ob.conv.artifact.empty")}</p>
      )}
      <Button small variant="ghost" onClick={() => props.onSwitchMode("edit")}>
        {t("ob.conv.review.editDirectly")}
      </Button>
    </>
  );
}
