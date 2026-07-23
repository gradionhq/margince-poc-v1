import type { ReactNode } from "react";
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
  /**
   * Conversation content that lives OUTSIDE the machine's thread (chat
   * replies, review cards, act controls) but must share the same scroll
   * region and screen-reader log — a second scroll container inside the
   * conversation would fight this one's follow-the-bottom behaviour.
   */
  children?: ReactNode;
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
  children,
}: ConversationThreadProps) {
  const t = useT();
  const log = useRef<HTMLDivElement>(null);
  const end = useRef<HTMLDivElement>(null);
  const following = useRef(true);
  // Entries already present when the thread mounted (a restored recap, an
  // act switch) render instantly; only narration that ARRIVES live reveals
  // word by word. Membership is fixed at mount — an entry that revealed once
  // keeps its reveal markup, so a re-render never snaps it to plain text.
  const preRendered = useRef<ReadonlySet<string> | null>(null);
  if (preRendered.current === null) {
    preRendered.current = new Set(entries.map((entry) => entry.id));
  }
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
      onWheel={(event) => {
        // Deliberate upward intent breaks the follow even mid smooth-scroll:
        // the programmatic-scroll window must not eat the user's escape.
        if (event.deltaY < 0) {
          following.current = false;
        }
      }}
      onTouchMove={() => {
        // A touch drag during the programmatic window is the user taking
        // over; the next scroll event re-evaluates follow honestly.
        scrollingProgrammatically.current = false;
      }}
    >
      {entries.map((entry) => {
        if (entry.kind === "narration") {
          return (
            <NarrationBubble
              key={entry.id}
              entry={entry}
              reveal={!preRendered.current?.has(entry.id)}
            />
          );
        }
        if (entry.kind === "question") {
          return (
            <QuestionCard
              key={entry.id}
              question={entry.question}
              answered={entry.id !== liveQuestionEntryId}
              focusFirstOption={entry.id === liveQuestionEntryId}
              onAnswer={onAnswer}
            />
          );
        }
        if (entry.kind === "user") {
          return <UserTurn key={entry.id} entry={entry} />;
        }
        return <OutcomeCard key={entry.id} entry={entry} />;
      })}
      {children}
      <div ref={end} aria-hidden />
    </div>
  );
}
