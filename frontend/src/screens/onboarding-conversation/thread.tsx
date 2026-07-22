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
// in view — but only while the reader is already near the bottom; someone
// reading upthread is never yanked down. Question interactivity is delegated
// upward; the thread itself holds no conversation state.

type ConversationThreadProps = Readonly<{
  entries: readonly ThreadEntry[];
  /** The one question still awaiting an answer; older ones render inert. */
  pendingQuestionId: string | null;
  onAnswer: (questionId: string, value: string) => void;
}>;

// How close (in CSS pixels) to the bottom edge still counts as "following
// the conversation" for auto-scroll purposes.
const FOLLOW_THRESHOLD_PX = 96;

// How long after a programmatic smooth scroll we stop attributing scroll
// events to it, on browsers without the scrollend event.
const PROGRAMMATIC_SCROLL_MS = 700;

// The pending question can share its logical id with an earlier occurrence
// (a re-asked clarify after a re-read); only the LAST matching card is live.
function activeQuestionEntryId(
  entries: readonly ThreadEntry[],
  pendingQuestionId: string | null,
): string | null {
  if (pendingQuestionId === null) return null;
  for (let index = entries.length - 1; index >= 0; index -= 1) {
    const entry = entries[index];
    if (entry.kind === "question" && entry.question.id === pendingQuestionId) {
      return entry.id;
    }
  }
  return null;
}

export function ConversationThread({
  entries,
  pendingQuestionId,
  onAnswer,
}: ConversationThreadProps) {
  const t = useT();
  const log = useRef<HTMLDivElement>(null);
  const end = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  // A programmatic smooth scroll fires intermediate scroll events; while it
  // runs, they must not be read as the user scrolling away.
  const scrollingProgrammatically = useRef(false);

  const lastEntryId = entries.at(-1)?.id;
  useEffect(() => {
    if (lastEntryId === undefined || !following.current) return;
    const reduceMotion =
      globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ??
      false;
    scrollingProgrammatically.current = true;
    // jsdom has no scrollIntoView; in the browser it always exists.
    end.current?.scrollIntoView?.({
      block: "end",
      behavior: reduceMotion ? "auto" : "smooth",
    });
    const settle = () => {
      scrollingProgrammatically.current = false;
    };
    const node = log.current;
    node?.addEventListener("scrollend", settle, { once: true });
    const timer = globalThis.setTimeout(settle, PROGRAMMATIC_SCROLL_MS);
    return () => {
      globalThis.clearTimeout(timer);
      node?.removeEventListener("scrollend", settle);
      settle();
    };
  }, [lastEntryId]);

  const liveQuestionEntryId = activeQuestionEntryId(entries, pendingQuestionId);

  return (
    <div
      ref={log}
      className="ob-conv-thread"
      role="log"
      aria-live="polite"
      aria-label={t("ob.conv.threadLabel")}
      onScroll={() => {
        if (scrollingProgrammatically.current) return;
        const node = log.current;
        if (!node) return;
        following.current =
          node.scrollHeight - node.scrollTop - node.clientHeight <
          FOLLOW_THRESHOLD_PX;
      }}
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
              answered={entry.id !== liveQuestionEntryId}
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
