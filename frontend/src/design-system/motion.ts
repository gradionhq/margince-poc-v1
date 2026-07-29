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
    if (reduced) {
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
