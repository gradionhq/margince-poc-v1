import { Check, Circle, Sparkles } from "lucide-react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { EvidenceChip, ProvenanceTag } from "../../design-system/trust";
import { useT } from "../../i18n";
import { coldFieldLabel } from "../common";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import { MAX_SELECTED_FACTS } from "../onboarding";
import type { ClarifyAnswer } from "./company-proposal";
import {
  evidencedFields,
  isCompanyField,
  toMachineQuestion,
} from "./company-proposal";
import { NarrationBubble, QuestionCard } from "./entries";

// The in-thread review card: the AI-prepared mapping rendered as field rows
// with honest provenance (site evidence vs the human's own typing), fact
// include-toggles, the remaining server-detected decisions, and ONE explicit
// accept. Evidence-or-omit holds here: a proposal row without a verbatim
// snippet never renders.

type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type ProposalField = components["schemas"]["OnboardingCompanyProposalField"];

type CompanyConfirmCardProps = Readonly<{
  proposal: Proposal;
  draft: CompanyDraft;
  answers: readonly ClarifyAnswer[];
  /** The machine's live in-thread question; the card must not repeat it. */
  pendingQuestionId: string | null;
  selectedFactKeys: readonly string[];
  setSelectedFactKeys: (keys: string[]) => void;
  missingRequired: readonly CompanyFieldName[];
  onAnswerClarify: (clarifyId: string, value: string) => void;
  onAcceptAll: () => void;
  pending: boolean;
  error: string | null;
  onEditDirectly: () => void;
}>;

export function CompanyConfirmCard(props: CompanyConfirmCardProps) {
  const t = useT();
  const fields = evidencedFields(props.proposal.fields);
  const facts = props.proposal.facts ?? [];
  const openQuestions = (props.proposal.open_questions ?? []).filter(
    (question) =>
      question.id !== props.pendingQuestionId &&
      !props.answers.some((answer) => answer.clarifyId === question.id),
  );
  const missingLabels = props.missingRequired
    .map((field) => coldFieldLabel(field, t))
    .join(", ");

  return (
    <section className="ob-conv-confirm">
      <header>
        <Sparkles aria-hidden />
        <h2>{t("ob.conv.review.title")}</h2>
      </header>
      <ul className="ob-conv-confirm-fields">
        {fields.map((field) => (
          <FieldRow key={field.field} field={field} draft={props.draft} />
        ))}
      </ul>
      {openQuestions.length > 0 && (
        <div className="ob-conv-confirm-questions">
          <p>{t("ob.conv.review.openQuestions")}</p>
          {openQuestions.map((question) => (
            <QuestionCard
              key={question.id}
              question={toMachineQuestion(question)}
              onAnswer={props.onAnswerClarify}
            />
          ))}
        </div>
      )}
      {facts.length > 0 && (
        <details className="confirm-facts">
          <summary>
            <span className="seclabel">{t("ob.factsTitle")}</span>
            <span className="facts-count">
              {t("ob.factsSelected", {
                selected: props.selectedFactKeys.length,
                total: facts.length,
              })}
            </span>
          </summary>
          <p className="ob-sub">{t("ob.factsSub")}</p>
          <div className="fact-grid">
            {facts.map((fact) => {
              const selected = props.selectedFactKeys.includes(fact.value_key);
              const selectionFull =
                !selected &&
                props.selectedFactKeys.length >= MAX_SELECTED_FACTS;
              return (
                <button
                  key={`${fact.field}:${fact.value_key}`}
                  type="button"
                  className={`fact-card ${selected ? "selected" : ""}`}
                  aria-pressed={selected}
                  disabled={selectionFull}
                  onClick={() =>
                    props.setSelectedFactKeys(
                      selected
                        ? props.selectedFactKeys.filter(
                            (key) => key !== fact.value_key,
                          )
                        : [...props.selectedFactKeys, fact.value_key],
                    )
                  }
                >
                  <span className="fact-check">
                    {selected ? <Check aria-hidden /> : <Circle aria-hidden />}
                  </span>
                  <span>
                    <b>{coldFieldLabel(fact.field, t)}</b>
                    <span>{fact.value}</span>
                    <small>{fact.evidence_snippet}</small>
                  </span>
                </button>
              );
            })}
          </div>
        </details>
      )}
      {props.missingRequired.length > 0 && (
        <NarrationBubble
          entry={{
            kind: "narration",
            id: "review:missing",
            i18nKey: "ob.conv.review.missing",
            params: { fields: missingLabels },
          }}
        />
      )}
      {props.error && (
        <p className="mw-send-error" role="alert">
          {props.error}
        </p>
      )}
      <div className="ob-conv-confirm-actions">
        <Button
          variant="primary"
          disabled={
            props.pending ||
            props.missingRequired.length > 0 ||
            openQuestions.length > 0
          }
          onClick={props.onAcceptAll}
        >
          {props.pending ? (
            <>
              <span className="ob-spinner" /> {t("ob.s1.saving")}
            </>
          ) : (
            <>
              <Check aria-hidden /> {t("ob.conv.review.acceptAll")}
            </>
          )}
        </Button>
        <Button small variant="ghost" onClick={props.onEditDirectly}>
          {t("ob.conv.review.editDirectly")}
        </Button>
      </div>
    </section>
  );
}

// One reviewed row: the human's current value where the vocabulary knows the
// field, with provenance that flips to typed-by-you the moment they edited
// it. A value the human cleared has nothing left to confirm.
function FieldRow({
  field,
  draft,
}: Readonly<{ field: ProposalField; draft: CompanyDraft }>) {
  const t = useT();
  const name = isCompanyField(field.field, draft.values) ? field.field : null;
  const value = name === null ? field.value : draft.values[name];
  const typed = name !== null && draft.edited.has(name);
  if (value.trim() === "") {
    return null;
  }
  return (
    <li>
      <span className="t-label">{coldFieldLabel(field.field, t)}</span>
      <strong>{value}</strong>
      {typed ? (
        <ProvenanceTag provenance={{ kind: "human" }} />
      ) : (
        <EvidenceChip
          evidence={{
            snippet: field.evidence_snippet,
            source: field.source_url,
          }}
        />
      )}
    </li>
  );
}
