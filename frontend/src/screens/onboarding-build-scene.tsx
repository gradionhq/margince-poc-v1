// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import {
  type CSSProperties,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";
import { usePrefersReducedMotion } from "../design-system/motion";
import { useT } from "../i18n";
import { Wordmark } from "./auth";
import "./onboarding-build-scene.css";

/**
 * The beat between finishing setup and landing in the app.
 *
 * It exists so the workspace appears to be assembled rather than swapped in:
 * the product name rises out of a blur letter by letter, resolves into the real
 * wordmark, and the app's surfaces settle in behind it. The scene then dissolves
 * instead of cutting, and `onDone` fires and the caller navigates — this
 * component never routes.
 *
 * Two things keep a deliberate delay from becoming a trap:
 *  - it says what it is doing. A blocking full-screen scene that is silent to
 *    a screen reader is a dead end, so the whole thing is one `role="status"`
 *    named by the sentence it also prints.
 *  - under `prefers-reduced-motion` there is no scene at all. The end state of
 *    a decorative delay is *being past it*, so the callback fires immediately
 *    and nothing renders. Anything else makes the people who asked for less
 *    motion wait longest.
 */

/**
 * Long enough for the letters to land and the ghosts to settle, short enough
 * that nobody waiting to work resents it. A prop, not a literal buried in the
 * timer, so a test drives the clock instead of sleeping on it.
 */
export const BUILD_SCENE_DURATION_MS = 2400;

/**
 * The share of the duration spent leaving. The reader should arrive in the app
 * through a dissolve rather than a cut, and that beat is part of the time the
 * caller asked for instead of a wait bolted onto the end of it — which is also
 * why the exit's length travels to the stylesheet as a fraction of the same
 * duration rather than as a second number to keep in step.
 */
const EXIT_FRACTION = 0.14;

export type BuildSceneProps = Readonly<{
  onDone: () => void;
  durationMs?: number;
}>;

// Custom properties travel through `style` so the CSS choreography derives
// every delay from the ONE duration the caller set — the pattern
// margince-core-feed.tsx already uses for its mote table.
type SceneVars = CSSProperties & Record<`--${string}`, string | number>;

// Bar widths only: the ghosts are silhouettes of the app behind the wordmark,
// carrying no text, no data and no promise about what the workspace contains.
// Each card's widths are distinct, which is also what gives every bar a key of
// its own without reaching for its position in the array.
const GHOST_CARDS: readonly (readonly string[])[] = [
  ["62%", "100%", "44%"],
  ["58%", "100%", "84%", "40%"],
  ["66%", "48%"],
];

/**
 * The word split for the stagger, each letter carrying an identity and a
 * position.
 *
 * The id counts occurrences rather than reading the array index: "Margince"
 * happens to spell out with no repeated letter, and a key that quietly depended
 * on that would break on the first product name that does repeat one.
 */
function letterCells(
  word: string,
): readonly Readonly<{ id: string; char: string; position: number }>[] {
  const seen = new Map<string, number>();
  return Array.from(word).map((char, position) => {
    const occurrence = (seen.get(char) ?? 0) + 1;
    seen.set(char, occurrence);
    return { id: `${char}${occurrence}`, char, position };
  });
}

export function BuildScene({
  onDone,
  durationMs = BUILD_SCENE_DURATION_MS,
}: BuildSceneProps) {
  const t = useT();
  const reduced = usePrefersReducedMotion();
  const [leaving, setLeaving] = useState(false);

  // The callback is read through a ref so a parent passing an inline arrow
  // cannot restart the timer on every render — a scene that keeps rewinding
  // never ends.
  const done = useRef(onDone);
  useEffect(() => {
    done.current = onDone;
  }, [onDone]);

  // The reduced-motion arm below fires done.current() directly rather than
  // through a cancellable timer, so it has nothing to clean up between
  // StrictMode's mount-cleanup-remount replay of this effect in
  // development — without this guard that replay calls it a second time.
  const completed = useRef(false);
  // Stable across renders (both refs it reads are themselves stable), so
  // listing it as an effect dependency below cannot restart the timers —
  // only `reduced`/`durationMs` changing does that.
  const complete = useCallback(() => {
    if (completed.current) {
      return;
    }
    completed.current = true;
    done.current();
  }, []);

  useEffect(() => {
    if (reduced) {
      complete();
      return;
    }
    // The handoff is the clock's, never the exit animation's. Hanging it off
    // the dissolve finishing (an `animationend` a throttled tab or a platform
    // that refuses the animation may never deliver) would leave the reader
    // stranded on a full-screen scene; the exit only ever changes how the last
    // fraction of an already-scheduled handoff looks.
    const dissolve = setTimeout(
      () => setLeaving(true),
      Math.round(durationMs * (1 - EXIT_FRACTION)),
    );
    const handoff = setTimeout(complete, durationMs);
    // Cleared on unmount, or the callback navigates out from under whoever
    // took over the screen in the meantime.
    return () => {
      clearTimeout(dissolve);
      clearTimeout(handoff);
    };
  }, [reduced, durationMs, complete]);

  if (reduced) {
    return null;
  }

  const label = t("ob.enter.assembling");
  const word = t("shell.logoAria");
  const vars: SceneVars = {
    "--obBuildMs": `${durationMs}ms`,
    // Rounded: a fraction of a duration is a float, and no frame is a
    // millionth of a millisecond long.
    "--obExitMs": `${Math.round(durationMs * EXIT_FRACTION)}ms`,
  };

  return (
    <div
      className={leaving ? "ob-build is-leaving" : "ob-build"}
      role="status"
      aria-label={label}
      style={vars}
    >
      <div className="ob-build-ghosts" aria-hidden="true">
        {GHOST_CARDS.map((rows, index) => (
          <GhostCard key={rows.join("-")} rows={rows} index={index} />
        ))}
      </div>
      <div className="ob-build-mark">
        {/* Decorative twice over: the letters are the animation, and the
            wordmark beside them already carries the product's name for
            assistive tech. */}
        <span className="ob-build-letters" aria-hidden="true">
          {letterCells(word).map((cell) => (
            <Letter key={cell.id} letter={cell.char} index={cell.position} />
          ))}
        </span>
        {/* The same <Wordmark> the auth surface renders: two PNGs swapped by
            the theme, one accessible name on the container. The letters
            crossfade into it, so the typed word resolves into the real mark
            rather than a second one appearing next to it. */}
        <Wordmark alt={word} className="ob-build-wordmark" />
      </div>
      <p className="ob-build-sub">{label}</p>
    </div>
  );
}

// Its position in the stagger is the only thing a letter knows; the animation
// itself is CSS, paced off the scene's own duration.
function Letter({
  letter,
  index,
}: Readonly<{ letter: string; index: number }>) {
  // Declared rather than inlined: a fresh literal carrying custom-property
  // keys fails the excess-property check against CSSProperties (TS2559).
  const vars: SceneVars = { "--obLetter": index };
  return (
    <span className="ob-build-letter" style={vars}>
      {letter}
    </span>
  );
}

function GhostCard({
  rows,
  index,
}: Readonly<{ rows: readonly string[]; index: number }>) {
  const vars: SceneVars = { "--obGhost": index };
  return (
    <div className="ob-build-ghost" style={vars}>
      {rows.map((width) => (
        <span className="ob-build-ghost-row" key={width} style={{ width }} />
      ))}
    </div>
  );
}
