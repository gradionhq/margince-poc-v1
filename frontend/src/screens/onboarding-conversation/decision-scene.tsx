import { Check } from "lucide-react";
import { useId, useState } from "react";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import type { ConversationQuestion } from "./conversation-machine";

// A decision as the whole work surface: the question is the headline, the
// candidates are cards, and the answer is chosen THEN confirmed — a decision
// this size deserves a look at one's own choice before it goes on record.
// The dismiss escape stays subordinate: dashed, at the list's foot, and it
// resolves immediately because "I will set it myself" needs no second look.

/**
 * What the read knows about one candidate, beyond its name. Every part is
 * optional because the read prints only what the page printed: a candidate
 * with no registry number renders none rather than an empty slot, and one
 * with no verbatim quote offers no evidence toggle at all.
 */
export type CandidateFacts = Readonly<{
  /** The reading-size line under the name (a registered address). */
  meta?: string;
  /** The mono detail line (registry / VAT number). */
  mono?: string;
  /** The verbatim quote and the page it was read from. */
  snippet?: string;
  source?: string;
}>;

export function DecisionScene({
  question,
  onAnswer,
  onDismiss,
  factsOf,
}: Readonly<{
  question: ConversationQuestion;
  onAnswer: (questionId: string, value: string) => void;
  onDismiss?: (questionId: string) => void;
  /** The read's own detail for a candidate, keyed by the option value. */
  factsOf?: (value: string) => CandidateFacts | null;
}>) {
  const t = useT();
  const group = useId();
  const headline = useId();
  const [picked, setPicked] = useState("");
  return (
    <div className="ob-scene ob-decision">
      <div className="ob-decision-head">
        <div>
          <p className="ob-scene-eyebrow">{t("ob.conv.scene.detour")}</p>
          <h2 id={headline}>{t(question.i18nKey, question.params)}</h2>
          <p className="ob-scene-sub">{t("ob.conv.scene.decisionSub")}</p>
        </div>
        <span className="ob-decision-count">
          {t("ob.conv.scene.candidates", { count: question.options.length })}
        </span>
      </div>
      <div
        role="radiogroup"
        aria-labelledby={headline}
        className="ob-decision-options"
      >
        {question.options.map((option) => (
          <CandidateCard
            key={option.value}
            group={group}
            value={option.value}
            label={
              option.labelKey ? t(option.labelKey, option.params) : option.label
            }
            detail={
              option.detailKey ? t(option.detailKey, option.params) : undefined
            }
            facts={factsOf?.(option.value) ?? null}
            picked={picked === option.value}
            onPick={() => setPicked(option.value)}
          />
        ))}
        {question.dismissLabelKey !== undefined && onDismiss !== undefined && (
          <button
            type="button"
            className="ob-decision-skip"
            onClick={() => onDismiss(question.id)}
          >
            <span aria-hidden>+</span> {t(question.dismissLabelKey)}
          </button>
        )}
      </div>
      <div className="ob-decision-acts">
        <Button
          variant="primary"
          disabled={picked === ""}
          onClick={() => {
            // The attribute keeps the pointer out; this keeps a programmatic
            // click from answering with a choice nobody made.
            if (picked !== "") {
              onAnswer(question.id, picked);
            }
          }}
        >
          {t("ob.conv.scene.continue")}
        </Button>
      </div>
    </div>
  );
}

// One candidate: the choice row (disc, name, the read's detail lines) with
// the evidence toggle on the right, and the quote itself revealed under it.
// The toggle sits OUTSIDE the label, because a control inside a label also
// activates the radio the label is for.
function CandidateCard({
  group,
  value,
  label,
  detail,
  facts,
  picked,
  onPick,
}: Readonly<{
  group: string;
  value: string;
  label: string;
  detail?: string;
  facts: CandidateFacts | null;
  picked: boolean;
  onPick: () => void;
}>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const panel = useId();
  const hasEvidence = facts?.snippet !== undefined && facts.snippet !== "";
  return (
    <div className={`ob-decision-card${picked ? " is-picked" : ""}`}>
      <div className="ob-decision-row">
        <label>
          <input
            type="radio"
            name={group}
            value={value}
            checked={picked}
            onChange={onPick}
          />
          <span className="ob-decision-disc" aria-hidden>
            {picked && <Check />}
          </span>
          <span className="ob-decision-body">
            <b>{label}</b>
            {facts?.meta !== undefined && facts.meta !== "" && (
              <span>{facts.meta}</span>
            )}
            {((facts?.mono !== undefined && facts.mono !== "") ||
              (detail !== undefined && detail !== "")) && (
              <small>{facts?.mono || detail}</small>
            )}
          </span>
        </label>
        {hasEvidence && (
          <button
            type="button"
            className="ob-decision-toggle"
            aria-expanded={open}
            aria-controls={panel}
            onClick={() => setOpen((prev) => !prev)}
          >
            {t(open ? "ob.conv.scene.hideEvidence" : "ob.conv.scene.evidence")}
          </button>
        )}
      </div>
      {hasEvidence && open && (
        <div className="ob-decision-proof" id={panel}>
          <p className="ob-decision-proof-head">{t("ob.conv.scene.whyThis")}</p>
          <blockquote>{facts?.snippet}</blockquote>
          {facts?.source !== undefined && facts.source !== "" && (
            <>
              <p className="ob-decision-proof-head">
                {t("ob.conv.scene.foundOn")}
              </p>
              <span className="ob-decision-path">{pathOf(facts.source)}</span>
            </>
          )}
        </div>
      )}
    </div>
  );
}

// The page a quote was read from, as the path chip the reader recognises.
// A malformed URL degrades to the raw string rather than throwing.
function pathOf(source: string): string {
  try {
    const url = new URL(source);
    return `${url.host}${url.pathname}`;
  } catch {
    return source;
  }
}
