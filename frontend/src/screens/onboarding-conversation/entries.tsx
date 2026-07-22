import type { LucideIcon } from "lucide-react";
import { CircleAlert, CircleCheck, CircleUserRound, Clock } from "lucide-react";
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

export function NarrationBubble({
  entry,
}: Readonly<{ entry: NarrationEntry }>) {
  const t = useT();
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
      <p>
        {t(entry.i18nKey, resolvedParams(t, entry.params, entry.paramKeys))}
      </p>
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
  onAnswer: (questionId: string, value: string) => void;
}>;

export function QuestionCard({
  question,
  answered = false,
  onAnswer,
}: QuestionCardProps) {
  const t = useT();
  return (
    <fieldset className="ob-conv-question" disabled={answered}>
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
