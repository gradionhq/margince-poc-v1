import type { ReactNode } from "react";
import { useEffect, useRef } from "react";
import { useT } from "../../i18n";
import type { ThreadEntry } from "./conversation-machine";
import type { QuestionSelection } from "./entries";
import {
  ActivityGroup,
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
  /** Local-dismiss for dismissible questions; absent = no escape offered. */
  onDismiss?: (questionId: string) => void;
  /**
   * The act's opening turns, which the machine does not put in its thread but
   * which are still part of the transcript. Inside the log for the same reason
   * as `children`: a greeting mounted as a SIBLING of the scroller cannot
   * shrink and cannot scroll away, so it permanently reduces the transcript
   * window to whatever is left below it.
   */
  lead?: ReactNode;
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

type NarrationEntry = Extract<ThreadEntry, { kind: "narration" }>;

// Progress narration — the machine reporting its own work (pages counted,
// extraction phases, learned fields, build stages, corpus counters) — folds
// into activity groups instead of stacking bubbles. Membership is decided
// by the entry id's stable suffix vocabulary from narration.ts, so a new
// message key never silently becomes "progress" by wording alone.
const PROGRESS_ID =
  /(?::pages|:phase:[a-z]+|:field:[a-z_]+|:stage:[a-z]+|:words|:band:[a-z]+)$/;

function isProgress(entry: ThreadEntry): entry is NarrationEntry {
  return entry.kind === "narration" && PROGRESS_ID.test(entry.id);
}

type RenderItem =
  | { kind: "entry"; entry: ThreadEntry }
  | { kind: "activity"; id: string; entries: NarrationEntry[] };

// Consecutive progress entries become one activity group; everything else
// renders as itself, in order.
function renderItems(entries: readonly ThreadEntry[]): RenderItem[] {
  const items: RenderItem[] = [];
  for (const entry of entries) {
    const last = items.at(-1);
    if (isProgress(entry)) {
      if (last?.kind === "activity") {
        last.entries.push(entry);
      } else {
        items.push({ kind: "activity", id: entry.id, entries: [entry] });
      }
    } else {
      items.push({ kind: "entry", entry });
    }
  }
  return items;
}

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

// The choice a resolved card recorded, read back from the thread itself:
// the answer turn the reducer appended right after (`<seq>:answer:<id>`).
// A dismissal echoes the question's dismiss label; an option answer carries
// the option's label (key or text). A later re-ask of the same id owns the
// answers that follow it, so the scan stops at the next same-id question.
//
// Exported: this is the ONE way to tell a settled question entry from an
// abandoned one — a caller that owns a live surface scene (company-act's
// DecisionScene) needs the same answer to know which thread entries are
// safe to keep rendering as history and which are a superseded re-ask that
// will never resolve.
export function selectionFor(
  entries: readonly ThreadEntry[],
  index: number,
): QuestionSelection | null {
  const entry = entries[index];
  if (entry.kind !== "question") {
    return null;
  }
  const suffix = `:answer:${entry.question.id}`;
  for (let later = index + 1; later < entries.length; later += 1) {
    const candidate = entries[later];
    if (
      candidate.kind === "question" &&
      candidate.question.id === entry.question.id
    ) {
      return null;
    }
    if (candidate.kind !== "user" || !candidate.id.endsWith(suffix)) {
      continue;
    }
    if (
      candidate.i18nKey !== undefined &&
      candidate.i18nKey === entry.question.dismissLabelKey
    ) {
      return { kind: "dismissed" };
    }
    const chosen = entry.question.options.find((option) =>
      candidate.i18nKey !== undefined
        ? option.labelKey === candidate.i18nKey
        : option.label === candidate.text,
    );
    return chosen === undefined
      ? null
      : { kind: "option", value: chosen.value };
  }
  return null;
}

export function ConversationThread({
  entries,
  pendingQuestionId,
  onAnswer,
  onDismiss,
  lead,
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
      {lead}
      {renderItems(entries).map((item) => {
        if (item.kind === "activity") {
          return <ActivityGroup key={item.id} entries={item.entries} />;
        }
        const { entry } = item;
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
          // The full candidate list renders ONLY for the machine's current
          // pending question instance. Every other question entry — settled
          // or superseded — renders nothing here: a settled one's record is
          // the answer turn the reducer already appends right after it
          // (the next entry, a plain UserTurn); a superseded one has no
          // answer coming and nothing to show at all. Reusing that existing
          // turn is the whole fix — there is no second "answered" shape to
          // keep in step with the live card.
          return entry.id === liveQuestionEntryId ? (
            <QuestionCard
              key={entry.id}
              question={entry.question}
              focusFirstOption
              onAnswer={onAnswer}
              onDismiss={onDismiss}
            />
          ) : null;
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
