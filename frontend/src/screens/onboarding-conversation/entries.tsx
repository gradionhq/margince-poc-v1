import type { LucideIcon } from "lucide-react";
import {
  ChevronDown,
  CircleAlert,
  CircleCheck,
  CircleUserRound,
  Clock,
} from "lucide-react";
import { Fragment, useEffect, useRef, useState } from "react";
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

// Matches the pulse animation length in conversation.css.
const JUMP_PULSE_MS = 1600;

// The contract a collapsed row opts into: dispatched at the exact element
// carrying `data-finding-id` before this function looks for anything to
// focus inside it, so a row that renders its control only once expanded
// (confirm-card.tsx's FieldRow) gets the chance to open first. A row that
// never listens — settled rows, a person or fact entry with nothing to
// edit — simply ignores it, and the fallback below still focuses the row
// itself.
export const FINDING_EXPAND_EVENT = "ob:expand-finding";

// How long this function is willing to wait for a row's control to appear
// after asking it to expand, before it gives up and focuses the row
// instead — long enough for a state update and re-render, never so long
// the jump feels stuck.
const EXPAND_WAIT_MS = 400;

// The jump's actual destination: the field's own input or textarea if the
// row carries one (a click on a to-do means "let me fill this in", so the
// caret belongs in the control, not merely on the row that contains it),
// falling back to the row itself for anything with nothing to type into.
function focusableControl(
  node: Element,
): HTMLInputElement | HTMLTextAreaElement | null {
  if (node instanceof HTMLInputElement || node instanceof HTMLTextAreaElement) {
    return node;
  }
  return node.querySelector<HTMLInputElement | HTMLTextAreaElement>(
    "input, textarea",
  );
}

// Focuses the node's control the moment one exists — immediately if the row
// was already open, otherwise as soon as the expand request above causes one
// to mount. A MutationObserver rather than a fixed delay: a state update's
// timing is never guaranteed, and guessing a delay either flashes focus too
// early (nothing there yet) or leaves the human waiting past the render
// that already finished.
function focusWhenReady(node: Element): void {
  const control = focusableControl(node);
  if (control) {
    control.focus({ preventScroll: true });
    return;
  }
  if (typeof MutationObserver === "undefined") {
    // jsdom without a MutationObserver polyfill (none of this repo's tests
    // need one); in the browser it always exists.
    if (node instanceof HTMLElement) {
      node.focus({ preventScroll: true });
    }
    return;
  }
  const timeout = globalThis.setTimeout(() => {
    observer.disconnect();
    // The row never grew a control — it does not listen for the expand
    // request, or has none to offer — so the row itself is the honest
    // landing spot, exactly as before this function existed.
    if (node instanceof HTMLElement) {
      node.focus({ preventScroll: true });
    }
  }, EXPAND_WAIT_MS);
  const observer = new MutationObserver(() => {
    const found = focusableControl(node);
    if (found) {
      observer.disconnect();
      globalThis.clearTimeout(timeout);
      found.focus({ preventScroll: true });
    }
  });
  observer.observe(node, { childList: true, subtree: true });
}

/**
 * Scroll to, expand, briefly light, and focus the surface row(s) a
 * narration or a rail chip names — the "links him to the field" contract.
 * A rail control that only scrolled would leave a keyboard or screen-reader
 * user exactly where they clicked, and a collapsed row that only scrolled
 * into view would leave them focused on nothing typeable at all: the target
 * becomes where they ARE, ready to type, not just what they can see.
 * Matching is by attribute value scan (no built selector), the same choice
 * artifact.tsx documents: it needs no escaping and jsdom lacks CSS.escape.
 */
export function jumpToFindings(ids: readonly string[]): void {
  const wanted = new Set(ids);
  const nodes = [...document.querySelectorAll("[data-finding-id]")].filter(
    (node) => wanted.has(node.getAttribute("data-finding-id") ?? ""),
  );
  const first = nodes[0];
  if (first === undefined) {
    return;
  }
  first.dispatchEvent(new CustomEvent(FINDING_EXPAND_EVENT));
  const reduceMotion =
    globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ??
    false;
  // jsdom has no scrollIntoView; in the browser it always exists.
  first.scrollIntoView?.({
    block: "center",
    behavior: reduceMotion ? "auto" : "smooth",
  });
  focusWhenReady(first);
  for (const node of nodes) {
    node.classList.add("ob-conv-pulse");
  }
  globalThis.setTimeout(() => {
    for (const node of nodes) {
      node.classList.remove("ob-conv-pulse");
    }
  }, JUMP_PULSE_MS);
}

/**
 * A run of progress narration, folded the way a working agent's activity
 * log folds: one line showing the LATEST step and the count, the full list
 * one press away. Progress is what the AI did — it must be visible as
 * motion and auditable on demand, but it is not a message anyone owes a
 * reply to, so it does not stack bubbles. A step that names fields is a
 * button that jumps to and lights them.
 */
export function ActivityGroup({
  entries,
}: Readonly<{ entries: readonly NarrationEntry[] }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  const latest = entries.at(-1);
  if (latest === undefined) {
    return null;
  }
  const textOf = (entry: NarrationEntry) =>
    t(entry.i18nKey, resolvedParams(t, entry.params, entry.paramKeys));
  return (
    <div className="ob-conv-activity">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((prev) => !prev)}
      >
        <i aria-hidden />
        <span>{textOf(latest)}</span>
        <b>{t("ob.conv.activity.steps", { count: entries.length })}</b>
        <ChevronDown aria-hidden />
      </button>
      {open && (
        <ul>
          {entries.map((entry) =>
            entry.findingIds !== undefined && entry.findingIds.length > 0 ? (
              <li key={entry.id}>
                <button
                  type="button"
                  onClick={() => jumpToFindings(entry.findingIds ?? [])}
                >
                  {textOf(entry)}
                </button>
              </li>
            ) : (
              <li key={entry.id}>{textOf(entry)}</li>
            ),
          )}
        </ul>
      )}
    </div>
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
      <p>
        {reveal ? <RevealText text={text} /> : text}
        {entry.findingIds !== undefined && entry.findingIds.length > 0 && (
          // The attention contract: a message that names fields carries the
          // jump that shows and lights them on the surface.
          <button
            type="button"
            className="ob-conv-jump"
            onClick={() => jumpToFindings(entry.findingIds ?? [])}
          >
            {t("ob.conv.showField")}
          </button>
        )}
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

/** What a resolved question recorded: the chosen option, or the dismissal.
 * Read back from the thread's own answer turn — see `selectionFor` in
 * thread.ts — never from a live card, which no longer carries either shape
 * once answered (see QuestionCard below). */
export type QuestionSelection =
  | { kind: "option"; value: string }
  | { kind: "dismissed" };

type QuestionCardProps = Readonly<{
  question: ConversationQuestion;
  /** The card is the one live question: keyboard focus moves to its first
   * option — unless the human is mid-thought in a text field. */
  focusFirstOption?: boolean;
  onAnswer: (questionId: string, value: string) => void;
  /** The local-dismiss escape; rendered only when the question carries a
   * dismiss label AND the surface wires a handler. */
  onDismiss?: (questionId: string) => void;
}>;

// The full candidate list and its dismiss escape — genuinely live only: a
// resolved question is never re-rendered as this card. The rail keeps the
// choice as the one-line answer turn the machine already appends beside it
// (UserTurn); the review's own open-questions list drops a question from
// its feed the moment it is answered. Neither caller has a use for
// re-showing rejected candidates, so there is no "answered" shape here to
// drift from either.
export function QuestionCard({
  question,
  focusFirstOption = false,
  onAnswer,
  onDismiss,
}: QuestionCardProps) {
  const t = useT();
  const card = useRef<HTMLFieldSetElement>(null);

  useEffect(() => {
    if (!focusFirstOption) {
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
  }, [focusFirstOption]);

  return (
    <fieldset ref={card} className="ob-conv-question">
      <legend>{t(question.i18nKey, question.params)}</legend>
      <div className="ob-conv-options">
        {question.options.map((option) => {
          const label = option.labelKey
            ? t(option.labelKey, option.params)
            : option.label;
          return (
            <Button
              key={option.value}
              small
              className="ob-conv-option"
              // The chip clamps long values visually (CSS line-clamp); the
              // full text stays the accessible name via content and here as
              // the hover title.
              title={label}
              onClick={() => onAnswer(question.id, option.value)}
            >
              <span>{label}</span>
              {option.detailKey && (
                <small>{t(option.detailKey, option.params)}</small>
              )}
            </Button>
          );
        })}
      </div>
      {question.dismissLabelKey !== undefined && onDismiss !== undefined && (
        <Button
          small
          variant="ghost"
          className="ob-conv-question-dismiss"
          onClick={() => onDismiss(question.id)}
        >
          {t(question.dismissLabelKey)}
        </Button>
      )}
    </fieldset>
  );
}
