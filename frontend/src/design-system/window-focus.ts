// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/**
 * Whether this window has focus — ONE signal, for the whole document.
 *
 * A window sitting behind another one is not being watched, so the Core holds
 * still there. Two very different consumers need that answer: the WebGL draw
 * loop, which parks, and the stylesheet, which pauses its rhythms off
 * `data-window-blurred` on the root element. Both read the state maintained
 * here, so the sphere and its glow can never disagree about whether the Core is
 * moving — the failure that duplicating the condition per consumer produces.
 *
 * ONE `focus`/`blur` pair for the document however many Cores are mounted, held
 * only while somebody wants it: the listeners arrive with the first consumer and
 * leave with the last, so nothing outlives the last Core — not a listener, and
 * not the attribute.
 *
 * `document.hasFocus()` is read once per attach, to seed the state, and never
 * again. It is deliberately not polled: a poll answers only for the instant it
 * is asked, and the draw loop's rule is that a condition may park it only if the
 * END of that condition is announced by an event (see the loop in
 * margince-core-liquid.tsx). `focus` is that announcement; asking would be a
 * missed resume and a permanently frozen sphere.
 */

/** Present on `<html>` exactly while the window does not have focus. */
export const WINDOW_BLURRED_ATTRIBUTE = "data-window-blurred";

/** Null while nothing holds the signal: there are no listeners keeping it true,
 *  so there is no state to report either. */
let focused: boolean | null = null;
const listeners = new Set<(focused: boolean) => void>();
/** Consumers that want the attribute reflected but have nothing to be told. */
let holders = 0;

function held(): boolean {
  return listeners.size + holders > 0;
}

function reflect(): void {
  const root = document.documentElement;
  if (focused === false) {
    root.setAttribute(WINDOW_BLURRED_ATTRIBUTE, "");
  } else {
    root.removeAttribute(WINDOW_BLURRED_ATTRIBUTE);
  }
}

/**
 * Publish a change, once.
 *
 * The equality check is not a micro-optimisation: a window activation can fire
 * `focus` more than once (returning to a window that also moves focus into an
 * element inside it), and without it every consumer would be told to wake for
 * each of them.
 */
function announce(next: boolean): void {
  if (focused === next) {
    return;
  }
  focused = next;
  reflect();
  for (const listener of listeners) {
    listener(next);
  }
}

// Named, module-level handlers: the same function identity has to reach
// removeEventListener, or the pair below would attach again on every attach.
const onWindowFocus = () => announce(true);
const onWindowBlur = () => announce(false);

function attachIfFirst(): void {
  if (held()) {
    return;
  }
  focused = document.hasFocus();
  reflect();
  window.addEventListener("focus", onWindowFocus);
  window.addEventListener("blur", onWindowBlur);
}

function detachIfLast(): void {
  if (held()) {
    return;
  }
  window.removeEventListener("focus", onWindowFocus);
  window.removeEventListener("blur", onWindowBlur);
  // Nothing maintains the state from here, so it must stop being ASSERTED too:
  // a `data-window-blurred` left behind would pause the animations of the next
  // Core mounted into a focused window, with no event coming to release it.
  focused = null;
  reflect();
}

/**
 * Keep the signal — and so `data-window-blurred` — live until the returned
 * release runs. For the consumer that reads the state through CSS and has
 * nothing to be called back about.
 */
export function retainWindowFocusSignal(): () => void {
  attachIfFirst();
  holders += 1;
  return () => {
    holders -= 1;
    detachIfLast();
  };
}

/**
 * Be told when focus changes, for as long as the returned release is unused.
 *
 * The CURRENT state arrives first, synchronously, before this returns. That is
 * the difference between a subscription and a notification: a consumer that only
 * hears about changes has to seed itself from `isWindowFocused()` and get that
 * read on the right side of the attach, and a consumer that forgets starts its
 * life assuming focus it may not have — which for the draw loop means one
 * frameless sphere until the window is next clicked.
 */
export function subscribeToWindowFocus(
  onChange: (focused: boolean) => void,
): () => void {
  attachIfFirst();
  listeners.add(onChange);
  onChange(isWindowFocused());
  return () => {
    listeners.delete(onChange);
    detachIfLast();
  };
}

/**
 * The current answer.
 *
 * Honest before the first attach as well: with no listeners maintaining it there
 * is no state to hand back, so it asks the document rather than guessing focused
 * — which is also what seeds a subscriber that arrives into a blurred window.
 */
export function isWindowFocused(): boolean {
  return focused ?? document.hasFocus();
}
