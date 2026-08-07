import { useEffect, useState } from "react";

/**
 * Motion primitives for the design system.
 *
 * One rule governs everything here, and it is the interesting half: under
 * `prefers-reduced-motion` an animation jumps to its **end** state, never to
 * nothing. A typewriter that renders no text under reduced motion is broken, not
 * accessible — and where the UI waits on a completion, the end state includes
 * having FIRED that completion, or the sequence stops dead for exactly the users
 * who asked for less motion (ADR-0076 Decision 5c).
 */

/**
 * Whether the viewer has asked for reduced motion.
 *
 * Subscribed rather than read once: the preference can change while the page is
 * open (a system setting, or a screen recorder turning it on), and a component
 * that read it at mount would keep animating for the rest of the session.
 */
export function usePrefersReducedMotion(): boolean {
  const [reduced, setReduced] = useState(() => matches());

  useEffect(() => {
    const query = globalThis.matchMedia?.("(prefers-reduced-motion: reduce)");
    if (!query) {
      return;
    }
    const listen = () => setReduced(query.matches);
    query.addEventListener("change", listen);
    return () => query.removeEventListener("change", listen);
  }, []);

  return reduced;
}

function matches(): boolean {
  return (
    globalThis.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false
  );
}

/**
 * Reveal a string one character at a time.
 *
 * Bound by ADR-0076 Decision 5b where it is used on the unauthenticated surface:
 * copy reaches its full text within 1200 ms of first paint or renders complete
 * immediately, so `speed × text.length + startDelay` is a budget rather than a
 * taste. A slower reveal is not a slower animation, it is a screen that cannot
 * be read yet.
 *
 * Under reduced motion the full string is present on the first render and `done`
 * is already true.
 *
 * **A hidden tab gets the complete string, not a slow one.** Chrome throttles
 * `setTimeout` to roughly one call per second in a tab that is not visible, so a
 * 65-character sentence at 15 ms/char takes a second per character when nobody
 * is watching. Nobody reads an animation in a background tab, and the user who
 * switches to it a minute later would arrive mid-sentence, so the honest end
 * state for `hidden` is the same as for reduced motion: finished.
 *
 * This is the sibling of the rAF trap the guides already record. There the
 * lesson was "use timers, not rAF, because rAF is suspended when unfocused";
 * here it is that timers are not immune either, only less severely affected.
 * Anything with a deadline has to say what it does when the tab is away.
 */
export function useTypeStream(
  text: string,
  { speed = 34, startDelay = 0, enabled = true } = {},
): { shown: string; done: boolean } {
  const reduced = usePrefersReducedMotion();
  const [shown, setShown] = useState("");
  const [done, setDone] = useState(false);

  useEffect(() => {
    if (!enabled) {
      setShown("");
      setDone(false);
      return;
    }
    const finish = () => {
      setShown(text);
      setDone(true);
    };
    if (reduced || document.hidden) {
      finish();
      return;
    }
    setShown("");
    setDone(false);
    let index = 0;
    let timer: ReturnType<typeof setTimeout>;
    const tick = () => {
      index += 1;
      setShown(text.slice(0, index));
      if (index < text.length) {
        timer = setTimeout(tick, speed);
      } else {
        setDone(true);
      }
    };
    timer = setTimeout(tick, startDelay);
    // Hiding the tab MID-STREAM resolves the same way as starting hidden. The
    // throttle is what forces it: once the tab goes away the remaining
    // characters arrive about one per second, so a viewer who tabs back after a
    // minute finds the sentence still being typed at them. Reading the flag once
    // at mount covered only the stream that started hidden and left that one
    // stranded.
    const onVisibility = () => {
      if (document.hidden) {
        clearTimeout(timer);
        finish();
      }
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [text, speed, startDelay, enabled, reduced]);

  return { shown, done };
}

/**
 * How long an entry choreography owns the screen. Past this the document has
 * been introduced, and anything that mounts later renders in its end state.
 *
 * It is a ceiling over the longest sequence any surface runs (the login
 * surface's staggered rows plus its typed statement), not a per-surface number:
 * an intro that is still mid-flight when a remount lands should finish where it
 * was going rather than be cut off by a shorter budget.
 */
export const INTRO_MS = 2400;

/**
 * Whether an entry animation should PLAY, which is true once per document load
 * and not once per mount.
 *
 * The distinction is the whole point. A React remount is not a page load — a
 * background refetch, a notice appearing, a parent re-branching all replace the
 * subtree — and a surface that keys its intro to the mount replays the whole
 * choreography on every one of them. Copy that types itself out again, after
 * the reader has already read it, reads as the page reloading under them.
 *
 * What is recorded is a DEADLINE, stamped by the first mount: the moment the
 * document stops being newly loaded. Anything mounting before it plays, anything
 * after it renders the end state.
 *
 * A deadline rather than a "spent" flag set on a timer, and the difference is a
 * real hole: a timer belongs to the mount that started it, so an unmount cancels
 * it and the next mount starts a fresh one — a surface that remounts every couple
 * of seconds would keep pushing the finish line out and replay its intro every
 * time. An absolute instant cannot be pushed. It also needs no timer at all,
 * which is one fewer thing to cancel.
 *
 * It is kept on the document element rather than in a module variable because
 * the document is what the rule is about: a real page load builds a new one and
 * the intro comes back with it, which is exactly when a reader expects to see
 * it. It is also then observable — a test or a browser can read when this
 * document stopped being new.
 */
const INTRO_UNTIL = "marginceIntroUntil";

export function useDocumentIntro(): boolean {
  // Decided once, at mount, and held: the deadline passes while this component is
  // alive, and a surface whose intro is mid-flight must not lose its animation
  // halfway through it.
  const [play] = useState(() => {
    const root = document.documentElement;
    const until = Number(root.dataset[INTRO_UNTIL]);
    if (Number.isFinite(until)) {
      return performance.now() < until;
    }
    // The first mount in this document is the one that starts the clock.
    root.dataset[INTRO_UNTIL] = String(performance.now() + INTRO_MS);
    return true;
  });

  return play;
}
