import { ArrowLeft, ArrowRight } from "lucide-react";
import { useEffect, useState } from "react";
import { Button } from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  type CompanyFieldName,
  type CompanyForm,
  isRequired,
} from "./onboarding";

// The manual company interview: one question at a time, legal identity
// first, required questions gating advance and optional ones skippable.
// The conversational shell hosts it in the artifact panel when the human
// would rather tell than have their website read.

type ManualQuestion = Readonly<{
  field: Exclude<CompanyFieldName, "website">;
  chapter: MessageKey;
  prompt: MessageKey;
  hint: MessageKey;
  multiline?: boolean;
}>;

const MANUAL_QUESTIONS: readonly ManualQuestion[] = [
  {
    field: "legal_name",
    chapter: "ob.manualChapterLegal",
    prompt: "ob.manual.legal_name",
    hint: "ob.manual.legal_nameHint",
  },
  {
    field: "registered_address",
    chapter: "ob.manualChapterLegal",
    prompt: "ob.manual.registered_address",
    hint: "ob.manual.registered_addressHint",
    multiline: true,
  },
  {
    field: "register_vat",
    chapter: "ob.manualChapterLegal",
    prompt: "ob.manual.register_vat",
    hint: "ob.manual.register_vatHint",
  },
  {
    field: "display_name",
    chapter: "ob.manualChapterLegal",
    prompt: "ob.manual.display_name",
    hint: "ob.manual.display_nameHint",
  },
  {
    field: "offer_summary",
    chapter: "ob.manualChapterOffer",
    prompt: "ob.manual.offer_summary",
    hint: "ob.manual.offer_summaryHint",
    multiline: true,
  },
  {
    field: "icp",
    chapter: "ob.manualChapterCustomer",
    prompt: "ob.manual.icp",
    hint: "ob.manual.icpHint",
    multiline: true,
  },
  {
    field: "industry",
    chapter: "ob.manualChapterLegal",
    prompt: "ob.manual.industry",
    hint: "ob.manual.industryHint",
  },
  {
    field: "history",
    chapter: "ob.manualChapterLegal",
    prompt: "ob.manual.history",
    hint: "ob.manual.historyHint",
    multiline: true,
  },
  {
    field: "value_proposition",
    chapter: "ob.manualChapterOffer",
    prompt: "ob.manual.value_proposition",
    hint: "ob.manual.value_propositionHint",
    multiline: true,
  },
  {
    field: "usp",
    chapter: "ob.manualChapterOffer",
    prompt: "ob.manual.usp",
    hint: "ob.manual.uspHint",
    multiline: true,
  },
  {
    field: "buying_center",
    chapter: "ob.manualChapterCustomer",
    prompt: "ob.manual.buying_center",
    hint: "ob.manual.buying_centerHint",
    multiline: true,
  },
  {
    field: "customer_pains",
    chapter: "ob.manualChapterCustomer",
    prompt: "ob.manual.customer_pains",
    hint: "ob.manual.customer_painsHint",
    multiline: true,
  },
  {
    field: "desired_outcomes",
    chapter: "ob.manualChapterCustomer",
    prompt: "ob.manual.desired_outcomes",
    hint: "ob.manual.desired_outcomesHint",
    multiline: true,
  },
  {
    field: "buying_intents",
    chapter: "ob.manualChapterSales",
    prompt: "ob.manual.buying_intents",
    hint: "ob.manual.buying_intentsHint",
    multiline: true,
  },
  {
    field: "common_objections",
    chapter: "ob.manualChapterSales",
    prompt: "ob.manual.common_objections",
    hint: "ob.manual.common_objectionsHint",
    multiline: true,
  },
  {
    field: "sales_motion",
    chapter: "ob.manualChapterSales",
    prompt: "ob.manual.sales_motion",
    hint: "ob.manual.sales_motionHint",
    multiline: true,
  },
];

export function ManualCompanyInterview({
  values,
  setField,
  onPersist,
  onBackToChoice,
  onComplete,
}: Readonly<{
  values: CompanyForm;
  setField: (field: CompanyFieldName, value: string) => void;
  onPersist: () => void;
  onBackToChoice: () => void;
  onComplete: () => void;
}>) {
  const t = useT();
  const [questionIndex, setQuestionIndex] = useState(0);
  const question = MANUAL_QUESTIONS[questionIndex];
  const answerID = `manual-question-${question?.field ?? "unavailable"}-answer`;

  useEffect(() => {
    document.getElementById(answerID)?.focus();
  }, [answerID]);

  if (!question) {
    return null;
  }
  const required = isRequired(question.field);
  const value = values[question.field];
  const last = questionIndex === MANUAL_QUESTIONS.length - 1;
  const advance = () => {
    if (required && value.trim() === "") {
      return;
    }
    onPersist();
    if (last) {
      onComplete();
      return;
    }
    setQuestionIndex((current) => current + 1);
  };
  const back = () => {
    onPersist();
    if (questionIndex === 0) {
      onBackToChoice();
      return;
    }
    setQuestionIndex((current) => current - 1);
  };
  const promptID = `manual-question-${question.field}`;

  return (
    <form
      className="ob-core-dialog ob-manual-question"
      onSubmit={(event) => {
        event.preventDefault();
        advance();
      }}
    >
      <div className="ob-manual-progress">
        <span>{t(question.chapter)}</span>
        <span>
          {questionIndex + 1} / {MANUAL_QUESTIONS.length}
        </span>
      </div>
      <h1 id={promptID}>{t(question.prompt)}</h1>
      <p>{t(question.hint)}</p>
      {question.multiline ? (
        <textarea
          id={answerID}
          className="ob-manual-input ob-manual-textarea"
          aria-labelledby={promptID}
          value={value}
          required={required}
          onChange={(event) => setField(question.field, event.target.value)}
          onBlur={onPersist}
        />
      ) : (
        <input
          id={answerID}
          className="ob-manual-input"
          aria-labelledby={promptID}
          value={value}
          required={required}
          onChange={(event) => setField(question.field, event.target.value)}
          onBlur={onPersist}
        />
      )}
      <div className="ob-manual-actions">
        <button type="button" className="ob-core-link" onClick={back}>
          <ArrowLeft aria-hidden /> {t("ob.back")}
        </button>
        <Button
          variant="primary"
          type="submit"
          disabled={required && value.trim() === ""}
        >
          {last
            ? t("ob.manualReview")
            : !required && value.trim() === ""
              ? t("ob.manualLater")
              : t("ob.manualNext")}
          <ArrowRight aria-hidden />
        </Button>
      </div>
      <small className="ob-manual-required">
        {required ? t("ob.manualRequired") : t("ob.manualOptional")}
      </small>
    </form>
  );
}
