// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { Check, ChevronDown } from "lucide-react";
import {
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { usePrefersReducedMotion } from "./motion";
import "./select.css";

/**
 * The Margince select: a button trigger plus a portalled listbox popup.
 *
 * It exists because a native `<select>` is the one control the browser draws
 * itself. Its closed face takes our tokens, and everything the user actually
 * chooses from — the option list, its fill, its type, its highlight, its
 * scrollbar — is painted by the platform in the platform's own idiom. On the
 * same screen as the rest of this design system that reads as a hole, and no
 * amount of CSS closes it: `option` is not stylable in any engine we ship to.
 *
 * Two consequences the caller sees, both deliberate:
 *
 *  - `onChange` reports the VALUE, not an event. A listbox has no
 *    `event.target.value`, and threading a synthetic event through would be a
 *    lie about where the value came from.
 *  - `options` is data, not `<option>` children. The component has to know the
 *    labels to render a trigger face, to run typeahead and to skip a disabled
 *    entry from the keyboard; reading that back out of children would be
 *    guesswork.
 *
 * `required` becomes `aria-required` rather than a `required` attribute: a
 * button carries no constraint validation, and neither does the hidden input
 * that mirrors the value into a real `<form>` (the HTML spec exempts hidden
 * inputs from validation). So a required select announces the requirement and
 * the surrounding form still owns refusing an empty submit — which every screen
 * here already does in its own submit handler.
 */
export type SelectOption = Readonly<{
  value: string;
  label: string;
  disabled?: boolean;
}>;

export type SelectProps = Readonly<{
  options: readonly SelectOption[];
  value: string;
  onChange: (value: string) => void;
  /** The trigger face when `value` is "" or matches no option. */
  placeholder?: string;
  id?: string;
  /** Rendered on a hidden input so a real `<form>` still carries the value. */
  name?: string;
  disabled?: boolean;
  required?: boolean;
  className?: string;
  "aria-label"?: string;
  "aria-labelledby"?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}>;

// The popup sits this far from the trigger, never closer than this to a viewport
// edge, and below MIN_ROOM there is not enough room to show a list worth reading
// — so it flips to the other side rather than being squeezed.
const ANCHOR_GAP = 4;
const VIEWPORT_MARGIN = 8;
const MIN_ROOM = 96;

// How long a typeahead buffer survives between keystrokes. Measured from the
// previous keystroke rather than reset by a timer: there is no timeout to cancel
// when the control unmounts mid-word, and nothing to fake in a test.
const TYPEAHEAD_RESET_MS = 500;

type PopupFrame = Readonly<{
  left: number;
  width: number;
  maxHeight: number;
  // Exactly one of these is set. A popup below the trigger is anchored by its
  // top; a flipped one by its bottom, because anchoring it by `top` would need
  // its rendered height, which is not known until after it has painted.
  top?: number;
  bottom?: number;
  above: boolean;
}>;

function frameFor(rect: DOMRect, view: { width: number; height: number }) {
  const roomBelow = view.height - rect.bottom - ANCHOR_GAP - VIEWPORT_MARGIN;
  const roomAbove = rect.top - ANCHOR_GAP - VIEWPORT_MARGIN;
  const flip = roomBelow < MIN_ROOM && roomAbove > roomBelow;
  const rightEdge = view.width - rect.width - VIEWPORT_MARGIN;
  const frame = {
    left: Math.max(VIEWPORT_MARGIN, Math.min(rect.left, rightEdge)),
    width: rect.width,
    maxHeight: Math.max(flip ? roomAbove : roomBelow, MIN_ROOM),
  };
  return flip
    ? { ...frame, bottom: view.height - rect.top + ANCHOR_GAP, above: true }
    : { ...frame, top: rect.bottom + ANCHOR_GAP, above: false };
}

// The trigger has scrolled out of sight, so the popup has nothing left to point
// at. A box of zeros is NOT that case: it means nothing has been laid out (a
// jsdom render, a trigger inside a collapsed container), and there is no
// position to judge yet.
function outOfView(rect: DOMRect, viewHeight: number): boolean {
  const laidOut = rect.width > 0 || rect.height > 0;
  return laidOut && (rect.bottom <= 0 || rect.top >= viewHeight);
}

/**
 * Anchor a fixed-position popup to a trigger's box.
 *
 * The popup is portalled to the body because most of these controls sit in a
 * toolbar inside `.scroll` (app/shell.css), which is `overflow-y: auto;
 * position: relative` — it clips an absolutely positioned child and scrolls it
 * away from its own trigger. Leaving the scroller costs the popup everything
 * that used to move it, so the frame is recomputed on every scroll and resize,
 * and `onLost` closes it once the trigger itself is gone from view.
 *
 * The scroll listener is CAPTURE-phase: scroll does not bubble, so a listener on
 * the window never hears the toolbar's own scroller otherwise.
 */
function useAnchoredPopup(
  anchor: RefObject<HTMLElement | null>,
  open: boolean,
  onLost: () => void,
): PopupFrame | null {
  const [frame, setFrame] = useState<PopupFrame | null>(null);

  // Layout effect, not effect: the position is computed before the browser
  // paints, so the popup never appears at the wrong place for one frame.
  useLayoutEffect(() => {
    if (!open) {
      setFrame(null);
      return;
    }
    const place = () => {
      const element = anchor.current;
      if (!element) {
        return;
      }
      const rect = element.getBoundingClientRect();
      const view = {
        width: globalThis.innerWidth,
        height: globalThis.innerHeight,
      };
      if (outOfView(rect, view.height)) {
        onLost();
        return;
      }
      setFrame(frameFor(rect, view));
    };
    place();
    globalThis.addEventListener("scroll", place, true);
    globalThis.addEventListener("resize", place);
    return () => {
      globalThis.removeEventListener("scroll", place, true);
      globalThis.removeEventListener("resize", place);
    };
  }, [open, anchor, onLost]);

  return frame;
}

// A pointer press outside both the trigger and the popup closes the list.
// Capture phase so a surface that stops the event on its own container cannot
// leave the popup stranded open, and `pointerdown` rather than `click` so the
// list is gone by the time the press lands on whatever the reader was reaching
// for underneath it.
function useDismissOnOutsidePress(
  open: boolean,
  dismiss: () => void,
  trigger: RefObject<HTMLElement | null>,
  popup: RefObject<HTMLElement | null>,
) {
  useEffect(() => {
    if (!open) {
      return;
    }
    const onPointerDown = (event: Event) => {
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      if (
        trigger.current?.contains(target) ||
        popup.current?.contains(target)
      ) {
        return;
      }
      dismiss();
    };
    globalThis.addEventListener("pointerdown", onPointerDown, true);
    return () =>
      globalThis.removeEventListener("pointerdown", onPointerDown, true);
  }, [open, dismiss, trigger, popup]);
}

// The next option at or after `from` that a keyboard may land on, walking in
// `step`'s direction. Deliberately does not wrap: a list that jumps from its
// last entry back to its first hides from the reader that they reached the end.
function stepEnabled(
  options: readonly SelectOption[],
  from: number,
  step: 1 | -1,
): number {
  for (let index = from; index >= 0 && index < options.length; index += step) {
    if (!options[index]?.disabled) {
      return index;
    }
  }
  return -1;
}

/**
 * What a keypress means to a combobox, as data and with no React in it.
 *
 * The whole keyboard contract lives here so it can be read in one screen and
 * argued with: the same key means different things open and closed, which is the
 * part every hand-rolled dropdown gets partly right.
 *
 * `null` is "not ours" — the press keeps its default, which is what lets Tab,
 * the browser's own shortcuts and a screen reader's keys through.
 */
type KeyIntent =
  | Readonly<{ act: "open"; step: 1 | -1 }>
  | Readonly<{ act: "move"; step: 1 | -1 }>
  | Readonly<{ act: "edge"; step: 1 | -1 }>
  | Readonly<{ act: "commit" }>
  | Readonly<{ act: "cancel" }>
  | Readonly<{ act: "leave" }>
  | Readonly<{ act: "search"; char: string }>
  | null;

function intentFor(key: string, open: boolean): KeyIntent {
  if (!open) {
    if (key === "ArrowUp") {
      return { act: "open", step: -1 };
    }
    // Typeahead on a CLOSED control is deliberately absent: a native select
    // changes its value when someone types "w" while tabbing past it, and a
    // stage that moved on a stray keystroke is a defect, not a shortcut.
    const opens = key === "ArrowDown" || key === "Enter" || key === " ";
    return opens ? { act: "open", step: 1 } : null;
  }
  switch (key) {
    case "ArrowDown":
      return { act: "move", step: 1 };
    case "ArrowUp":
      return { act: "move", step: -1 };
    case "Home":
      return { act: "edge", step: 1 };
    case "End":
      return { act: "edge", step: -1 };
    case "Enter":
    case " ":
      return { act: "commit" };
    case "Escape":
      return { act: "cancel" };
    case "Tab":
      return { act: "leave" };
    default:
      return key.length === 1 ? { act: "search", char: key } : null;
  }
}

// What each intent does. Named callbacks rather than a bag of setters, so the
// table in the hook below reads as the behaviour it is.
type IntentActions = Readonly<{
  openFrom: (step: 1 | -1) => void;
  moveBy: (step: 1 | -1) => void;
  toEdge: (step: 1 | -1) => void;
  commitActive: () => void;
  cancel: () => void;
  leave: () => void;
  search: (char: string) => void;
}>;

function keyDownHandler(open: boolean, actions: IntentActions) {
  return (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    // A modified press belongs to the browser or the OS (Alt+Arrow is history
    // navigation, Cmd+F is find) — never to a typeahead buffer.
    if (event.altKey || event.ctrlKey || event.metaKey) {
      return;
    }
    const intent = intentFor(event.key, open);
    if (!intent) {
      return;
    }
    // Tab keeps its default so focus can leave; every other press we claim is
    // ours, and scrolling the page on Space is never what was meant.
    //
    // Claimed also means it STOPS HERE. A dropdown is usually inside something
    // else that listens for the same keys on the document — `Modal` closes on
    // Escape, a form submits on Enter — and a press meant for the open list must
    // not also reach them: abandoning a dropdown would take the whole dialog
    // with it, and choosing an option would submit the form around it.
    if (intent.act !== "leave") {
      event.preventDefault();
      event.stopPropagation();
    }
    switch (intent.act) {
      case "open":
        return actions.openFrom(intent.step);
      case "move":
        return actions.moveBy(intent.step);
      case "edge":
        return actions.toEdge(intent.step);
      case "commit":
        return actions.commitActive();
      case "cancel":
        return actions.cancel();
      case "leave":
        return actions.leave();
      case "search":
        return actions.search(intent.char);
    }
  };
}

// The typeahead match, kept out of React: a buffer, the character just typed and
// the moment it arrived produce the next buffer and the option it points at.
function typeaheadMatch(
  options: readonly SelectOption[],
  buffer: Readonly<{ query: string; at: number }>,
  char: string,
  now: number,
): Readonly<{ query: string; at: number; hit: number }> {
  const carried = now - buffer.at < TYPEAHEAD_RESET_MS;
  const query = (carried ? buffer.query : "") + char.toLowerCase();
  const hit = options.findIndex(
    (option) =>
      !option.disabled && option.label.toLowerCase().startsWith(query),
  );
  return { query, at: now, hit };
}

// Everything the trigger and the popup need from the state machine below.
type Listbox = Readonly<{
  open: boolean;
  active: number;
  frame: PopupFrame | null;
  trigger: RefObject<HTMLButtonElement | null>;
  popup: RefObject<HTMLDivElement | null>;
  listboxId: string;
  optionDomId: (index: number) => string;
  onKeyDown: (event: ReactKeyboardEvent<HTMLButtonElement>) => void;
  onTriggerClick: () => void;
  pick: (index: number) => void;
  hover: (index: number) => void;
}>;

/**
 * The open/active state machine, one level below the markup.
 *
 * It owns four things and nothing else: whether the list is open, which option
 * is active, where the popup sits, and what a keypress does about any of that.
 * The commit is the only place `onChange` is called, so there is exactly one
 * path by which a value changes.
 */
function useSelectListbox(
  options: readonly SelectOption[],
  value: string,
  onChange: (next: string) => void,
): Listbox {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(-1);
  const trigger = useRef<HTMLButtonElement | null>(null);
  const popup = useRef<HTMLDivElement | null>(null);
  const typed = useRef({ query: "", at: 0 });
  const listboxId = useId();

  // Dismissal WITHOUT moving focus, for the two cases where the reader is
  // already somewhere else: a press outside, and a trigger scrolled out of view.
  const dismiss = useCallback(() => setOpen(false), []);
  const frame = useAnchoredPopup(trigger, open, dismiss);
  useDismissOnOutsidePress(open, dismiss, trigger, popup);
  useActiveOptionVisible(open, active, listboxId);

  const selectedIndex = options.findIndex((option) => option.value === value);
  const edge = (step: 1 | -1) =>
    stepEnabled(options, step === 1 ? 0 : options.length - 1, step);

  const openFrom = (step: 1 | -1) => {
    const onSelected =
      selectedIndex !== -1 && !options[selectedIndex]?.disabled;
    setActive(onSelected ? selectedIndex : edge(step));
    setOpen(true);
  };

  const commit = (index: number) => {
    const option = options[index];
    if (!option || option.disabled) {
      // Nothing to commit — the list stays open on the reader's own choice
      // rather than closing as if something had been picked.
      return;
    }
    onChange(option.value);
    setOpen(false);
    trigger.current?.focus();
  };

  const search = (char: string) => {
    const match = typeaheadMatch(options, typed.current, char, Date.now());
    typed.current = { query: match.query, at: match.at };
    if (match.hit !== -1) {
      setActive(match.hit);
    }
  };

  // What each intent actually does, as a table. It reads as the keyboard's
  // contract spelled a second way — `intentFor` says what a key means, this says
  // what happens — and keeping the two apart is what makes either readable.
  const actions: IntentActions = {
    openFrom,
    moveBy: (step) => {
      const from = active === -1 ? edge(step) : active + step;
      const next = stepEnabled(options, from, step);
      if (next !== -1) {
        setActive(next);
      }
    },
    toEdge: (step) => setActive(edge(step)),
    commitActive: () => commit(active),
    cancel: () => {
      setOpen(false);
      trigger.current?.focus();
    },
    leave: () => setOpen(false),
    search,
  };

  return {
    open,
    active,
    frame,
    trigger,
    popup,
    listboxId,
    optionDomId: (index: number) => `${listboxId}-option-${index}`,
    onKeyDown: keyDownHandler(open, actions),
    onTriggerClick: () => (open ? setOpen(false) : openFrom(1)),
    pick: commit,
    hover: setActive,
  };
}

// Keeps the active option visible while a reader arrows through a list longer
// than the popup. The call is optional because jsdom implements no
// scrollIntoView, and the keyboard path has to stay testable.
function useActiveOptionVisible(
  open: boolean,
  active: number,
  listboxId: string,
) {
  useEffect(() => {
    if (!open || active === -1) {
      return;
    }
    const element = document.getElementById(`${listboxId}-option-${active}`);
    element?.scrollIntoView?.({ block: "nearest" });
  }, [open, active, listboxId]);
}

export function Select(props: SelectProps) {
  const { options, value, onChange, name } = props;
  const listbox = useSelectListbox(options, value, onChange);
  const reduced = usePrefersReducedMotion();

  return (
    <>
      <SelectTrigger field={props} listbox={listbox} />
      {/* The value a real <form> submits. The trigger is a button, which carries
          no form value of its own, so a screen that posts a form rather than
          calling the typed client keeps working unchanged. */}
      {name !== undefined && <input type="hidden" name={name} value={value} />}
      {listbox.open && listbox.frame
        ? createPortal(
            <SelectPopup
              options={options}
              value={value}
              frame={listbox.frame}
              listbox={listbox}
              animate={!reduced}
            />,
            document.body,
          )
        : null}
    </>
  );
}

function SelectTrigger({
  field,
  listbox,
}: Readonly<{ field: SelectProps; listbox: Listbox }>) {
  const { open, active } = listbox;
  const selected = field.options.find((option) => option.value === field.value);
  return (
    <button
      type="button"
      ref={listbox.trigger}
      id={field.id}
      className={["input", "select-control", field.className ?? ""]
        .filter(Boolean)
        .join(" ")}
      // NOSONAR: an ARIA combobox over a native <select>, which no engine lets
      // us style past its closed face — see the module comment.
      role="combobox"
      aria-expanded={open}
      aria-haspopup="listbox"
      // Only while open: an aria-controls pointing at an element that is not in
      // the document is an invalid reference, which axe reports and a screen
      // reader cannot follow.
      aria-controls={open ? listbox.listboxId : undefined}
      aria-activedescendant={
        open && active !== -1 ? listbox.optionDomId(active) : undefined
      }
      aria-label={field["aria-label"]}
      aria-labelledby={field["aria-labelledby"]}
      aria-describedby={field["aria-describedby"]}
      aria-invalid={field["aria-invalid"]}
      aria-required={field.required}
      disabled={field.disabled}
      onClick={listbox.onTriggerClick}
      onKeyDown={listbox.onKeyDown}
    >
      <span
        className={
          selected ? "select-face" : "select-face select-face-placeholder"
        }
      >
        {selected?.label ?? field.placeholder}
      </span>
      <ChevronDown className="select-chevron" size={16} aria-hidden="true" />
    </button>
  );
}

function SelectPopup({
  options,
  value,
  frame,
  listbox,
  animate,
}: Readonly<{
  options: readonly SelectOption[];
  value: string;
  frame: PopupFrame;
  listbox: Listbox;
  animate: boolean;
}>) {
  return (
    <div
      ref={listbox.popup}
      className="select-popup"
      // Reduced motion resolves in one place — the hook, not a second media
      // query in the stylesheet — so the decision is assertable by the suite
      // rather than only visible in a browser.
      data-motion={animate ? "in" : "none"}
      data-above={frame.above ? "true" : undefined}
      style={{
        left: frame.left,
        top: frame.top,
        bottom: frame.bottom,
        width: frame.width,
        maxHeight: frame.maxHeight,
      }}
    >
      {/* Divs rather than ul/li: `role="listbox"` and `role="option"` are the
          semantics, and a list element that also claims an interactive role is
          announced twice over. The listbox carries no name of its own either —
          the combobox that owns it is named, and a second name on the popup is
          read out on top of it. */}
      <div className="select-list" id={listbox.listboxId} role="listbox">
        {options.map((option, index) => (
          // biome-ignore lint/a11y/useKeyWithClickEvents: the keyboard path is the combobox trigger's own keydown handling
          // biome-ignore lint/a11y/useFocusableInteractive: an option in an aria-activedescendant listbox must NOT be focusable — focus stays on the combobox, which is what makes typeahead and Escape work
          <div
            key={option.value}
            id={listbox.optionDomId(index)}
            role="option"
            aria-selected={option.value === value}
            aria-disabled={option.disabled}
            className={[
              "select-option",
              index === listbox.active ? "is-active" : "",
              option.disabled ? "is-disabled" : "",
            ]
              .filter(Boolean)
              .join(" ")}
            onClick={option.disabled ? undefined : () => listbox.pick(index)}
            onMouseEnter={
              option.disabled ? undefined : () => listbox.hover(index)
            }
          >
            <span className="select-option-label">{option.label}</span>
            {option.value === value && (
              <Check className="select-option-check" size={14} aria-hidden />
            )}
          </div>
        ))}
      </div>
    </div>
  );
}
