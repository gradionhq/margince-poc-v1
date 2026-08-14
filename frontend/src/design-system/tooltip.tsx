// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import {
  type FocusEvent as ReactFocusEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import "./tooltip.css";

// The gap between the anchor's box and the tip, and the margin the tip keeps
// from the viewport's own edges.
const ANCHOR_GAP = 6;
const VIEWPORT_MARGIN = 8;

type TipFrame = Readonly<{
  left: number;
  // Exactly one of these is set, for the same reason as the select popup's
  // frame: a tip placed above the anchor is anchored by its bottom, because
  // anchoring it by `top` would need its height before it has painted.
  top?: number;
  bottom?: number;
}>;

/**
 * Where the tip goes: above the anchor by default, below it when the room
 * above is not enough, and never past the viewport's side edges.
 *
 * Above by default because these anchors are headings and row labels — what a
 * reader wants next is directly under them, and a tip that covers it answers
 * one question by hiding the next.
 */
function frameFor(
  rect: DOMRect,
  tip: DOMRect,
  view: { width: number; height: number },
): TipFrame {
  const below = rect.top - ANCHOR_GAP - VIEWPORT_MARGIN < tip.height;
  const rightEdge = view.width - tip.width - VIEWPORT_MARGIN;
  // The clamp reads left-to-right: never past the right edge, never before the
  // left margin. VIEWPORT_MARGIN wins when the tip is wider than the viewport,
  // which is a tip that has to overflow somewhere and does it on the side the
  // reader is not reading from.
  const left = Math.max(VIEWPORT_MARGIN, Math.min(rect.left, rightEdge));
  return below
    ? { left, top: rect.bottom + ANCHOR_GAP }
    : { left, bottom: view.height - rect.top + ANCHOR_GAP };
}

// The anchor has scrolled out of sight, so the tip has nothing left to point
// at. A box of zeros is NOT that case: it means nothing has been laid out (a
// jsdom render, an anchor inside a collapsed section), and there is no position
// to judge yet.
function outOfView(rect: DOMRect, viewHeight: number): boolean {
  const laidOut = rect.width > 0 || rect.height > 0;
  return laidOut && (rect.bottom <= 0 || rect.top >= viewHeight);
}

/**
 * Anchor the tip to its trigger's box.
 *
 * Portalled and fixed rather than positioned inside the trigger, because every
 * trigger this serves is truncating — which means it sits under an
 * `overflow: hidden` that would clip the tip exactly as it clips the text.
 *
 * Deliberately NOT the select's `useAnchoredPopup`: that one sizes its popup to
 * the trigger's width and gives it a scrollable max-height, which are the two
 * things a tip must not do — it is as wide as its own wrapped text needs and it
 * never scrolls. Sharing the hook would mean adding both as options to the
 * control that has the least room for a regression.
 */
function useTipFrame(
  anchor: RefObject<HTMLElement | null>,
  tip: RefObject<HTMLElement | null>,
  open: boolean,
  onLost: () => void,
): TipFrame | null {
  const [frame, setFrame] = useState<TipFrame | null>(null);

  // Layout effect, not effect: the position is computed before the browser
  // paints, so the tip never appears at the wrong place for one frame.
  useLayoutEffect(() => {
    if (!open) {
      setFrame(null);
      return;
    }
    const place = () => {
      const element = anchor.current;
      const box = tip.current;
      if (!element || !box) {
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
      setFrame(frameFor(rect, box.getBoundingClientRect(), view));
    };
    place();
    // Capture phase: scroll does not bubble, so a listener on the window never
    // hears the scroller the anchor actually sits in otherwise.
    globalThis.addEventListener("scroll", place, true);
    globalThis.addEventListener("resize", place);
    return () => {
      globalThis.removeEventListener("scroll", place, true);
      globalThis.removeEventListener("resize", place);
    };
  }, [open, anchor, tip, onLost]);

  return frame;
}

// A tip is worth showing only for a string the row could not fit. Repeating a
// name the reader can already read in full is noise, and on a page of short
// names it would be noise on every row.
function clipped(element: HTMLElement | null): boolean {
  return element !== null && element.scrollWidth > element.clientWidth;
}

type TruncationTooltip<T extends HTMLElement> = Readonly<{
  /** Goes on the element that truncates. */
  ref: RefObject<T | null>;
  /** Spread onto the same element: what opens, closes and describes the tip. */
  trigger: Readonly<{
    "aria-describedby"?: string;
    tabIndex?: number;
    onPointerEnter: () => void;
    onPointerLeave: () => void;
    onFocus: (event: ReactFocusEvent<HTMLElement>) => void;
    onBlur: () => void;
    onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => void;
  }>;
  /** Render inside the same element; it portals to the body regardless. */
  tip: ReactNode;
}>;

/**
 * Reveal, on hover or focus, a string that its own row had to truncate.
 *
 * A hook rather than a wrapping component because the three strings that need
 * this are an `h1`, a breadcrumb `span` and a rail row's `span`: a wrapper
 * would either add an element inside a heading whose one job is to BE the
 * heading, or take an `as` prop and carry the ref-typing that comes with it.
 * The caller keeps its own element and spreads this onto it.
 *
 * The tip appears only when the text is actually clipped, measured at the
 * moment of hover rather than at render — the same string is clipped at one
 * window width and whole at the next, and no render happens in between.
 *
 * The element type is the caller's to name (`useTruncationTooltip<HTMLSpanElement>`)
 * so the ref this hands back fits the element it goes on. A ref typed to
 * HTMLElement would not: a `Ref<HTMLSpanElement>` may be written by React with
 * any span, which a holder of the wider type cannot promise to accept.
 */
export function useTruncationTooltip<T extends HTMLElement = HTMLElement>(
  text: string,
): TruncationTooltip<T> {
  const anchor = useRef<T | null>(null);
  const box = useRef<HTMLDivElement | null>(null);
  const [open, setOpen] = useState(false);
  // Whether the text is clipped AT REST, which is a different question from
  // whether to show the tip now: this one decides whether the element is worth
  // a tab stop, and it has to be answered before anybody reaches it.
  const [reachable, setReachable] = useState(false);
  const id = useId();
  const close = useCallback(() => setOpen(false), []);
  const frame = useTipFrame(anchor, box, open, close);

  // Measured after every render rather than when `text` changes: the width the
  // string has to fit in moves for reasons this hook never sees — a rail that
  // narrowed, a sibling badge that appeared — and re-reading it is one property
  // read. Setting the same answer twice is a no-op, so this cannot loop.
  useLayoutEffect(() => {
    setReachable(clipped(anchor.current));
  });

  useEffect(() => {
    const measure = () => setReachable(clipped(anchor.current));
    globalThis.addEventListener("resize", measure);
    return () => globalThis.removeEventListener("resize", measure);
  }, []);

  const reveal = useCallback(() => {
    const isClipped = clipped(anchor.current);
    setReachable(isClipped);
    setOpen(isClipped);
  }, []);

  return {
    ref: anchor,
    trigger: {
      "aria-describedby": open ? id : undefined,
      // A truncated string is the one case where a heading or a label earns a
      // tab stop: without it the whole of it is reachable by pointer only.
      // Untruncated, it earns nothing and takes none.
      tabIndex: reachable ? 0 : undefined,
      onPointerEnter: reveal,
      onPointerLeave: close,
      onFocus: reveal,
      onBlur: close,
      onKeyDown: (event: ReactKeyboardEvent<HTMLElement>) => {
        if (event.key === "Escape" && open) {
          // Stopped, because a tip dismissed by Escape must not also close the
          // dialog or panel the anchor happens to sit in.
          event.stopPropagation();
          close();
        }
      },
    },
    tip: open
      ? createPortal(
          <div
            className="tooltip"
            id={id}
            ref={box}
            role="tooltip"
            style={
              frame
                ? { left: frame.left, top: frame.top, bottom: frame.bottom }
                : // Before the first measurement the tip is laid out where it
                  // can be measured but not seen. It cannot be `display: none`,
                  // which has no box to measure at all.
                  { left: 0, top: 0, visibility: "hidden" }
            }
          >
            {text}
          </div>,
          document.body,
        )
      : null,
  };
}
