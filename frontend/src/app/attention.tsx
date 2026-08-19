// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import {
  createContext,
  type ReactNode,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";
import { useT } from "../i18n";

/**
 * What the agent surface knows about the person using the app, as opposed to
 * what it knows about the database.
 *
 * The taskbar's right half reports the workspace, which arrives from reads. Its
 * left half reports the SCREEN, and most of what a reader experiences as "it
 * noticed" is this half: not a query answering, but the chrome tracking what
 * they are doing — the rows they selected, the record they have been sitting on,
 * the thing they just saved.
 *
 * The direction matters. Screens PUBLISH; chrome READS. Chrome that reached into
 * screens to work out what was happening would need a branch per screen and
 * would go quietly wrong every time one was rewritten — and it could only ever
 * guess at what a selection MEANS. A screen naming its own state is a screen
 * that stays correct when it changes.
 *
 * What travels here is STANDING state: it is true until the screen says
 * otherwise, and the bar shows it for as long as it stands. Moments — a write
 * landing, a record being read — do not, because both are already legible from
 * the caches the app keeps (app/writewatch.ts), and a channel screens had to
 * remember to fire would go quiet exactly on the screen nobody updated.
 */

/** What the screen is currently about, beyond its route. */
export type AttentionScope = Readonly<{
  /** Short, in the reader's own language. "3 selected", "no matches". */
  label: string;
}>;

type AttentionValue = Readonly<{
  scope: AttentionScope | null;
  setScope: (scope: AttentionScope | null) => void;
}>;

const AttentionContext = createContext<AttentionValue | null>(null);

export function AttentionProvider({
  children,
}: Readonly<{ children: ReactNode }>) {
  const [scope, setScope] = useState<AttentionScope | null>(null);
  const value = useMemo(() => ({ scope, setScope }), [scope]);
  return (
    <AttentionContext.Provider value={value}>
      {children}
    </AttentionContext.Provider>
  );
}

/**
 * What the chrome reads.
 *
 * Absent provider is not an error: a screen rendered in isolation — a story, a
 * test, the login path before the shell mounts — has no chrome listening, and
 * publishing into nothing is exactly right there.
 */
export function useAttention(): AttentionScope | null {
  return useContext(AttentionContext)?.scope ?? null;
}

/**
 * The selection case, which is most of what a list screen has to say.
 *
 * Wrapped rather than left to each caller because the branch and the copy are
 * the same every time, and a screen that spells them itself is a screen that can
 * word it differently from the one beside it — and one more branch on a function
 * the complexity ceiling is already watching.
 */
export function usePublishSelection(count: number): void {
  const t = useT();
  usePublishScope(
    count > 0 ? t("attention.selected", { n: String(count) }) : null,
  );
}

/**
 * What a screen calls to state its standing scope.
 *
 * Takes the label rather than a setter, so the screen declares what is true and
 * the clean-up on unmount is not the caller's to remember — a scope left behind
 * by a screen nobody is on any more is the bar reporting a page that is gone.
 */
export function usePublishScope(label: string | null): void {
  const value = useContext(AttentionContext);
  const setScope = value?.setScope;
  useEffect(() => {
    if (!setScope) {
      return;
    }
    setScope(label ? { label } : null);
    return () => setScope(null);
  }, [label, setScope]);
}
