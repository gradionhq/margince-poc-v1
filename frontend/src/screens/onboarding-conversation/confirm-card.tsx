import { Check, Circle, Sparkles } from "lucide-react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { EvidenceChip, ProvenanceTag } from "../../design-system/trust";
import { useT } from "../../i18n";
import { coldFieldLabel } from "../common";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import { groundingOf, MAX_SELECTED_FACTS } from "../onboarding";
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
type Comparison = components["schemas"]["CompanySiteReadComparison"];

type CompanyConfirmCardProps = Readonly<{
  proposal: Proposal;
  draft: CompanyDraft;
  answers: readonly ClarifyAnswer[];
  /** The site-read comparisons, so a conflict question's dismiss action is
   * labeled as keeping the human's value (the server still gets its
   * keep_current resolution). */
  comparisons: readonly Comparison[];
  /** The machine's live in-thread question; the card must not repeat it. */
  pendingQuestionId: string | null;
  selectedFactKeys: readonly string[];
  setSelectedFactKeys: (keys: string[]) => void;
  missingRequired: readonly CompanyFieldName[];
  onAnswerClarify: (clarifyId: string, value: string) => void;
  onDismissClarify: (clarifyId: string) => void;
  onAcceptAll: () => void;
  pending: boolean;
  /** A clarify authorization is still in flight; accepting must wait for it. */
  authorizing: boolean;
  error: string | null;
  onEditDirectly: () => void;
}>;

// Everything Accept all will save is shown: fields the human typed that the
// evidenced proposal does not carry get their own typed-by-you rows.
function humanOnlyRows(
  draft: CompanyDraft,
  shown: ReadonlySet<string>,
): CompanyFieldName[] {
  return [...draft.edited].filter(
    (field) => !shown.has(field) && draft.values[field].trim() !== "",
  );
}

export function CompanyConfirmCard(props: CompanyConfirmCardProps) {
  const t = useT();
  const fields = evidencedFields(props.proposal.fields);
  const facts = props.proposal.facts ?? [];
  const openQuestions = (props.proposal.open_questions ?? []).filter(
    (question) =>
      question.id !== props.pendingQuestionId &&
      !props.answers.some((answer) => answer.clarifyId === question.id),
  );
  const humanRows = humanOnlyRows(
    props.draft,
    new Set(fields.map((field) => field.field)),
  );
  // Dismissed questions are named honestly: nothing was written, the field
  // stays the human's to edit — never silently swallowed.
  const dismissedLabels = props.answers
    .filter((answer) => answer.dismissed === true)
    .map((answer) => coldFieldLabel(answer.field, t))
    .join(", ");
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
        {humanRows.map((field) => (
          <li key={field}>
            <span className="t-label">{coldFieldLabel(field, t)}</span>
            <strong>{props.draft.values[field]}</strong>
            <ProvenanceTag provenance={{ kind: "human" }} />
          </li>
        ))}
      </ul>
      {openQuestions.length > 0 && (
        <div className="ob-conv-confirm-questions">
          <p>{t("ob.conv.review.openQuestions")}</p>
          {openQuestions.map((question) => (
            <QuestionCard
              key={question.id}
              question={toMachineQuestion(question, props.comparisons)}
              onAnswer={props.onAnswerClarify}
              onDismiss={props.onDismissClarify}
            />
          ))}
        </div>
      )}
      {dismissedLabels !== "" && (
        <div className="ob-conv-confirm-skipped">
          <p>{t("ob.conv.review.skipped", { fields: dismissedLabels })}</p>
          <Button small variant="ghost" onClick={props.onEditDirectly}>
            {t("ob.conv.review.editDirectly")}
          </Button>
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
        // A failed save speaks as Margince, not as a bare server string
        // floating in the card; the safe problem detail rides as a param.
        <div role="alert">
          <NarrationBubble
            entry={{
              kind: "narration",
              id: "review:confirm-failed",
              i18nKey: "ob.conv.review.confirmFailed",
              params: { detail: props.error },
            }}
          />
        </div>
      )}
      <div className="ob-conv-confirm-actions">
        <Button
          variant="primary"
          disabled={
            props.pending ||
            props.authorizing ||
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
        <Button
          small
          variant="ghost"
          disabled={props.pending}
          onClick={props.onEditDirectly}
        >
          {t("ob.conv.review.editDirectly")}
        </Button>
      </div>
    </section>
  );
}

// One reviewed row: the human's current value where the vocabulary knows the
// field, with provenance in precedence order — the human's own typing, then
// the draft's CURRENT grounding (an entity pick re-grounds the legal block),
// then the proposal's own evidence. A cleared value has nothing to confirm.
function FieldRow({
  field,
  draft,
}: Readonly<{ field: ProposalField; draft: CompanyDraft }>) {
  const t = useT();
  const name = isCompanyField(field.field, draft.values) ? field.field : null;
  const value = name === null ? field.value : draft.values[name];
  const typed = name !== null && draft.edited.has(name);
  const grounding = name === null ? null : groundingOf(draft, name);
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
            snippet: grounding?.evidence_snippet ?? field.evidence_snippet,
            source: grounding?.source_url ?? field.source_url,
          }}
        />
      )}
    </li>
  );
}
