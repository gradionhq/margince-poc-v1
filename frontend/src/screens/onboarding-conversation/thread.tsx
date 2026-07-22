import { useEffect, useRef } from "react";
import { useT } from "../../i18n";
import type { ThreadEntry } from "./conversation-machine";
import {
  NarrationBubble,
  OutcomeCard,
  QuestionCard,
  UserTurn,
} from "./entries";

// The conversation transcript: a polite live region so a screen reader hears
// new turns without stealing focus, auto-scrolled so the newest entry stays
// in view. Question interactivity is delegated upward; the thread itself
// holds no state.

type ConversationThreadProps = Readonly<{
  entries: readonly ThreadEntry[];
  /** The one question still awaiting an answer; older ones render inert. */
  pendingQuestionId: string | null;
  onAnswer: (questionId: string, value: string) => void;
}>;

export function ConversationThread({
  entries,
  pendingQuestionId,
  onAnswer,
}: ConversationThreadProps) {
  const t = useT();
  const end = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (entries.length === 0) return;
    // jsdom has no scrollIntoView; in the browser it always exists.
    end.current?.scrollIntoView?.({ block: "end", behavior: "smooth" });
  }, [entries.length]);

  return (
    <div
      className="ob-conv-thread"
      role="log"
      aria-live="polite"
      aria-label={t("ob.conv.threadLabel")}
    >
      {entries.map((entry) => {
        if (entry.kind === "narration") {
          return <NarrationBubble key={entry.id} entry={entry} />;
        }
        if (entry.kind === "question") {
          return (
            <QuestionCard
              key={entry.id}
              question={entry.question}
              answered={entry.question.id !== pendingQuestionId}
              onAnswer={onAnswer}
            />
          );
        }
        if (entry.kind === "user") {
          return <UserTurn key={entry.id} entry={entry} />;
        }
        return <OutcomeCard key={entry.id} entry={entry} />;
      })}
      <div ref={end} aria-hidden />
    </div>
  );
}
