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
    // `document.hidden` is checked at mount rather than subscribed to: a stream
    // that started while visible should keep streaming if the user tabs away and
    // back, and one that started hidden has already been resolved to its end
    // state, so there is nothing for a visibilitychange listener to do.
    if (reduced || document.hidden) {
      setShown(text);
      setDone(true);
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
    return () => clearTimeout(timer);
  }, [text, speed, startDelay, enabled, reduced]);

  return { shown, done };
}
