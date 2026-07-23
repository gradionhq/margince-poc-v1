import type { LucideIcon } from "lucide-react";
import { CircleAlert, CircleCheck, CircleUserRound, Clock } from "lucide-react";
import { Fragment, useEffect, useRef } from "react";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import type { MessageKey } from "../../i18n/en";
import type {
  ConversationQuestion,
  OutcomeTone,
  ThreadEntry,
} from "./conversation-machine";
import "./conversation.css";

// Presentational pieces for the conversation thread. Copy always resolves
// through the i18n catalogs; server-derived values (option labels, params)
// arrive as data and render verbatim, while paramKeys are translated here.

type NarrationEntry = Extract<ThreadEntry, { kind: "narration" }>;
type UserEntry = Extract<ThreadEntry, { kind: "user" }>;
type OutcomeEntry = Extract<ThreadEntry, { kind: "outcome" }>;

type Translate = ReturnType<typeof useT>;

function resolvedParams(
  t: Translate,
  params: Record<string, string | number> | undefined,
  paramKeys: Record<string, MessageKey> | undefined,
): Record<string, string | number> {
  const translated = Object.fromEntries(
    Object.entries(paramKeys ?? {}).map(([name, key]) => [name, t(key)]),
  );
  return { ...params, ...translated };
}

// Word-by-word reveal for narration that arrives LIVE — speech gets a beat,
// factual cards (questions, outcomes, user turns) never do. The animated copy
// is presentation only (aria-hidden, per-word spans); the full sentence rides
// along visually hidden so assistive tech and text queries always see one
// coherent string. The stagger shrinks with word count so a long sentence
// finishes inside the same cap as a short one; prefers-reduced-motion
// collapses the animation entirely (conversation.css).

const REVEAL_WORD_STEP_MS = 90;
const REVEAL_TOTAL_CAP_MS = 1200;

export function RevealText({ text }: Readonly<{ text: string }>) {
  const words = text.split(/\s+/).filter((word) => word !== "");
  const step = Math.min(
    REVEAL_WORD_STEP_MS,
    REVEAL_TOTAL_CAP_MS / Math.max(1, words.length),
  );
  // Repeated words need distinct keys; an occurrence counter keeps them
  // stable without keying on the array index.
  const seen = new Map<string, number>();
  return (
    <>
      <span className="ob-conv-reveal-source">{text}</span>
      <span className="ob-conv-reveal" aria-hidden>
        {words.map((word, position) => {
          const occurrence = (seen.get(word) ?? 0) + 1;
          seen.set(word, occurrence);
          return (
            <Fragment key={`${word}:${occurrence}`}>
              <span
                style={{ animationDelay: `${Math.round(position * step)}ms` }}
              >
                {word}
              </span>{" "}
            </Fragment>
          );
        })}
      </span>
    </>
  );
}

export function NarrationBubble({
  entry,
  reveal = false,
}: Readonly<{ entry: NarrationEntry; reveal?: boolean }>) {
  const t = useT();
  const text = t(
    entry.i18nKey,
    resolvedParams(t, entry.params, entry.paramKeys),
  );
  return (
    <div
      className="ob-conv-narration"
      data-finding-ids={entry.findingIds?.join(" ")}
    >
      <span
        className="ob-conv-speaker"
        role="img"
        aria-label={t("ob.ai.speakerName")}
      >
        <span aria-hidden>{t("ob.ai.speaker")}</span>
      </span>
      <p>{reveal ? <RevealText text={text} /> : text}</p>
    </div>
  );
}

export function UserTurn({ entry }: Readonly<{ entry: UserEntry }>) {
  const t = useT();
  return (
    <div className="ob-conv-user">
      <p>{entry.i18nKey ? t(entry.i18nKey, entry.params) : entry.text}</p>
      <CircleUserRound aria-hidden />
    </div>
  );
}

const outcomeIcons: Record<OutcomeTone, LucideIcon> = {
  success: CircleCheck,
  deferred: Clock,
  failure: CircleAlert,
};

// No live-region role here: the surrounding thread is the announcing log,
// and a nested one would double-announce every outcome.
export function OutcomeCard({ entry }: Readonly<{ entry: OutcomeEntry }>) {
  const t = useT();
  const Icon = outcomeIcons[entry.tone];
  return (
    <div className="ob-conv-outcome" data-tone={entry.tone}>
      <Icon aria-hidden />
      <p>{t(entry.i18nKey, entry.params)}</p>
    </div>
  );
}

type QuestionCardProps = Readonly<{
  question: ConversationQuestion;
  /** Set after the question is answered; options stay visible but inert. */
  answered?: boolean;
  /** The card is the one live question: keyboard focus moves to its first
   * option — unless the human is mid-thought in a text field. */
  focusFirstOption?: boolean;
  onAnswer: (questionId: string, value: string) => void;
}>;

export function QuestionCard({
  question,
  answered = false,
  focusFirstOption = false,
  onAnswer,
}: QuestionCardProps) {
  const t = useT();
  const card = useRef<HTMLFieldSetElement>(null);

  useEffect(() => {
    if (!focusFirstOption || answered) {
      return;
    }
    const button = card.current?.querySelector("button");
    if (button == null) {
      return;
    }
    // Never steal focus from someone typing: any focused text field wins,
    // and a composer still holding a draft keeps its claim even unfocused.
    const active = button.ownerDocument.activeElement;
    if (
      active instanceof HTMLTextAreaElement ||
      active instanceof HTMLInputElement
    ) {
      return;
    }
    const composer = button
      .closest(".ob-workbench-panel")
      ?.querySelector<HTMLTextAreaElement>(".mw-composer textarea");
    if (composer != null && composer.value !== "") {
      return;
    }
    button.focus();
  }, [focusFirstOption, answered]);

  return (
    <fieldset ref={card} className="ob-conv-question" disabled={answered}>
      <legend>{t(question.i18nKey, question.params)}</legend>
      <div className="ob-conv-options">
        {question.options.map((option) => (
          <Button
            key={option.value}
            small
            className="ob-conv-option"
            onClick={() => onAnswer(question.id, option.value)}
          >
            <span>
              {option.labelKey
                ? t(option.labelKey, option.params)
                : option.label}
            </span>
            {option.detailKey && (
              <small>{t(option.detailKey, option.params)}</small>
            )}
          </Button>
        ))}
      </div>
    </fieldset>
  );
}
