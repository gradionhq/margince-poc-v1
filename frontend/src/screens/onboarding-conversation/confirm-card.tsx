import { Check, Circle, Sparkles } from "lucide-react";
import type { ChangeEvent } from "react";
import { useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import {
  type Evidence,
  EvidenceChip,
  ProvenanceTag,
} from "../../design-system/trust";
import { useLocale, useT } from "../../i18n";
import { coldFieldLabel } from "../common";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import { groundingOf, isMultilineField } from "../onboarding";
import { CapNotice, saveDisabled, useFactSelection } from "../onboarding-facts";
import type { ClarifyAnswer } from "./company-proposal";
import {
  evidencedFields,
  isCompanyField,
  toMachineQuestion,
} from "./company-proposal";
import { NarrationBubble, QuestionCard } from "./entries";

// The in-thread review card, laid out as a triage surface rather than a
// linear wall: a header count of where things stand, what still needs the
// human (the missing-required rows and the open questions) ABOVE the fields
// that are already settled, and the settled fields themselves split into a
// short-value grid and a prose list so neither drowns the other. Evidence-or-
// omit holds throughout: a proposal row without a verbatim snippet never
// renders, and every site-evidenced value keeps its snippet reachable in one
// interaction (the collapsed EvidenceChip's disclosure toggle).

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
  setField: (field: CompanyFieldName, value: string) => void;
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

// A settled field, either grounded in site evidence or typed by the human,
// normalized to one shape so the grid and the prose list can lay it out the
// same way regardless of which proposal it came from.
type ReviewRow = {
  key: string;
  label: string;
  value: string;
  multiline: boolean;
  typed: boolean;
  evidence: Evidence | null;
};

// One evidenced proposal field turned into a review row: the human's current
// value where the vocabulary knows the field, with provenance in precedence
// order — the human's own typing, then the draft's CURRENT grounding (an
// entity pick re-grounds the legal block), then the proposal's own evidence.
// A cleared value has nothing to confirm.
function evidencedRow(
  field: ProposalField,
  draft: CompanyDraft,
  t: ReturnType<typeof useT>,
): ReviewRow | null {
  const name = isCompanyField(field.field, draft.values) ? field.field : null;
  const value = name === null ? field.value : draft.values[name];
  if (value.trim() === "") {
    return null;
  }
  const typed = name !== null && draft.edited.has(name);
  const grounding = name === null ? null : groundingOf(draft, name);
  return {
    key: field.field,
    label: coldFieldLabel(field.field, t),
    value,
    multiline: name !== null && isMultilineField(name),
    typed,
    evidence: typed
      ? null
      : {
          snippet: grounding?.evidence_snippet ?? field.evidence_snippet,
          source: grounding?.source_url ?? field.source_url,
        },
  };
}

// A field the human typed that the evidenced proposal never carried: still
// shown, so Accept all previews everything it is about to save.
function humanRow(
  field: CompanyFieldName,
  draft: CompanyDraft,
  t: ReturnType<typeof useT>,
): ReviewRow {
  return {
    key: field,
    label: coldFieldLabel(field, t),
    value: draft.values[field],
    multiline: isMultilineField(field),
    typed: true,
    evidence: null,
  };
}

// The row's trailing honesty marker: who or what to credit for the value.
// Never both, and a typed row carries no evidence to reach for.
function RowProvenance({ row }: Readonly<{ row: ReviewRow }>) {
  if (row.typed) {
    return <ProvenanceTag provenance={{ kind: "human", self: true }} />;
  }
  if (row.evidence) {
    return <EvidenceChip evidence={row.evidence} collapsed />;
  }
  return null;
}

// A short-value row: label and value on one line, truncated visually with
// the full value as the hover/long-press title, evidence collapsed behind
// its own toggle so the row still reads as one line until asked.
function ShortFieldRow({ row }: Readonly<{ row: ReviewRow }>) {
  return (
    <li>
      <span className="t-label">{row.label}</span>
      <span className="ob-conv-field-value" title={row.value}>
        {row.value}
      </span>
      <RowProvenance row={row} />
    </li>
  );
}

// A prose value reads roughly two lines' worth before it needs a toggle; the
// cut lands on a word boundary rather than mid-word.
const PROSE_PREVIEW_CHARS = 140;

function prosePreview(value: string): string | null {
  if (value.length <= PROSE_PREVIEW_CHARS) {
    return null;
  }
  const cut = value.lastIndexOf(" ", PROSE_PREVIEW_CHARS);
  return `${value.slice(0, cut > 0 ? cut : PROSE_PREVIEW_CHARS).trimEnd()}…`;
}

// A prose row: a short preview stands in for the value, and an explicit
// toggle (not a hover state, so it works by touch and by keyboard) swaps in
// the full text — which stays out of the DOM until asked for, the same
// evidence-or-omit-shaped rule the collapsed EvidenceChip keeps for its own
// snippet.
function ProseFieldRow({ row }: Readonly<{ row: ReviewRow }>) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const preview = prosePreview(row.value);
  return (
    <li className="ob-conv-field-row-prose">
      <span className="t-label">{row.label}</span>
      <p className="ob-conv-field-prose">
        {expanded || preview === null ? row.value : preview}
      </p>
      {preview !== null && (
        <button
          type="button"
          className="ob-conv-field-expand"
          aria-expanded={expanded}
          onClick={() => setExpanded((prev) => !prev)}
        >
          {expanded
            ? t("ob.conv.review.showLess")
            : t("ob.conv.review.showMore")}
        </button>
      )}
      <RowProvenance row={row} />
    </li>
  );
}

export function CompanyConfirmCard(props: CompanyConfirmCardProps) {
  const t = useT();
  const { locale } = useLocale();
  const fields = evidencedFields(props.proposal.fields);
  const facts = props.proposal.facts ?? [];
  // The contract ceiling on `selected_fact_keys` is the selection model's to
  // enforce, wherever a fact is picked: this card's toggles and the fact table's
  // checkboxes write the same key list, so they refuse on the same terms.
  const factSelection = useFactSelection(
    facts,
    props.selectedFactKeys,
    props.setSelectedFactKeys,
  );
  const openQuestions = (props.proposal.open_questions ?? []).filter(
    (question) =>
      question.id !== props.pendingQuestionId &&
      !props.answers.some((answer) => answer.clarifyId === question.id),
  );
  const humanFields = humanOnlyRows(
    props.draft,
    new Set(fields.map((field) => field.field)),
  );
  const rows: ReviewRow[] = [
    ...fields
      .map((field) => evidencedRow(field, props.draft, t))
      .filter((row): row is ReviewRow => row !== null),
    ...humanFields.map((field) => humanRow(field, props.draft, t)),
  ];
  const shortRows = rows.filter((row) => !row.multiline);
  const proseRows = rows.filter((row) => row.multiline);
  // The triage summary's own vocabulary: "grounded" is every settled row
  // (site evidence or the human's own typing), "needing" is what still
  // blocks Accept all — the two together are the total the card is tracking.
  const groundedCount = rows.length;
  const needingCount = props.missingRequired.length;
  const totalCount = groundedCount + needingCount;
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
      <p className="ob-conv-review-summary">
        {openQuestions.length > 0
          ? t("ob.conv.review.summaryWithQuestions", {
              total: totalCount,
              grounded: groundedCount,
              needing: needingCount,
              openQuestions: openQuestions.length,
            })
          : t("ob.conv.review.summary", {
              total: totalCount,
              grounded: groundedCount,
              needing: needingCount,
            })}
      </p>
      {props.missingRequired.length > 0 && (
        <>
          <NarrationBubble
            entry={{
              kind: "narration",
              id: "review:missing",
              i18nKey: "ob.conv.review.missing",
              params: { fields: missingLabels },
            }}
          />
          <MissingRequiredFields
            fields={props.missingRequired}
            draft={props.draft}
            setField={props.setField}
          />
        </>
      )}
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
      {shortRows.length > 0 && (
        <ul className="ob-conv-confirm-fields ob-conv-confirm-fields-grid">
          {shortRows.map((row) => (
            <ShortFieldRow key={row.key} row={row} />
          ))}
        </ul>
      )}
      {proseRows.length > 0 && (
        <ul className="ob-conv-confirm-fields ob-conv-confirm-fields-prose">
          {proseRows.map((row) => (
            <ProseFieldRow key={row.key} row={row} />
          ))}
        </ul>
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
          {/* The artifact panel beside this thread draws the same ceiling and
              owns announcing it; a second live region on the same boundary
              would read the sentence twice. */}
          <CapNotice atCap={factSelection.atCap} locale={locale} live={false} />
          <div className="fact-grid">
            {facts.map((fact) => {
              const selected = factSelection.isSelected(fact);
              return (
                <button
                  key={`${fact.field}:${fact.value_key}`}
                  type="button"
                  className={`fact-card ${selected ? "selected" : ""}`}
                  aria-pressed={selected}
                  disabled={saveDisabled(factSelection, selected)}
                  onClick={() => factSelection.toggle(fact)}
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

// The narration names what is still missing; these rows let the human fill
// it right where they read that, instead of leaving the thread for the whole
// form. Writing through props.setField lands the value in the same draft the
// evidenced rows above read from, so a filled row drops out of
// missingRequired on the next render with no separate save step.
function MissingRequiredFields({
  fields,
  draft,
  setField,
}: Readonly<{
  fields: readonly CompanyFieldName[];
  draft: CompanyDraft;
  setField: (field: CompanyFieldName, value: string) => void;
}>) {
  const t = useT();
  const block = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const control = block.current?.querySelector<
      HTMLInputElement | HTMLTextAreaElement
    >("input, textarea");
    if (control == null) {
      return;
    }
    // Never steal focus from someone typing: any focused text field wins,
    // and a composer still holding a draft keeps its claim even unfocused.
    const active = control.ownerDocument.activeElement;
    if (
      active instanceof HTMLTextAreaElement ||
      active instanceof HTMLInputElement
    ) {
      return;
    }
    const composer = control
      .closest(".ob-workbench-panel")
      ?.querySelector<HTMLTextAreaElement>(".mw-composer textarea");
    if (composer != null && composer.value !== "") {
      return;
    }
    control.focus();
    // Focus lands once when the block first appears; it must not jump the
    // human back to the first row every time filling one shrinks the list.
  }, []);

  return (
    <div className="ob-conv-confirm-missing" ref={block}>
      {fields.map((field) => {
        const id = `confirm-missing-${field}`;
        const value = draft.values[field];
        const onChange = (
          event: ChangeEvent<HTMLInputElement | HTMLTextAreaElement>,
        ) => setField(field, event.target.value);
        return (
          <div key={field} className="ob-conv-confirm-missing-row">
            <label className="t-label" htmlFor={id}>
              {coldFieldLabel(field, t)}
            </label>
            {isMultilineField(field) ? (
              <textarea id={id} value={value} onChange={onChange} />
            ) : (
              <input id={id} value={value} onChange={onChange} />
            )}
          </div>
        );
      })}
    </div>
  );
}
