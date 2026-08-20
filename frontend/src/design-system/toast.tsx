import {
  type ReactNode,
  useCallback,
  useEffect,
  useRef,
  useState,
} from "react";

/**
 * The transient confirmation — "that worked" — and the one place it is spelled.
 *
 * Three screens had hand-copied it, and the three copies disagreed about the
 * only interesting part, which is when the message goes away. One held its timer
 * in a ref, cancelled a previous toast and cleared on unmount. One called
 * `setTimeout` inline with no cleanup at all, so navigating away left a timer
 * running against an unmounted tree. One had no timer, so its message stayed on
 * screen until the reader dismissed it by hand — deliberately, because that
 * toast carries an Undo, and a confirmation you can act on must not expire while
 * you are reading it.
 *
 * All three behaviours were defensible; having all three by accident was not. So
 * the correct one is the default and the sticky one is asked for.
 */

/** How long a confirmation stays before it withdraws itself. */
const TOAST_MS = 3500;

/**
 * `mark` is the green dot that reads as "done", and it belongs to the MESSAGE
 * rather than to the region: the same region shows a save that worked and a save
 * that was refused, and the copies this replaces put a completion tick beside
 * both. A failure with a green dot beside it is worse than a failure with no
 * glyph — it says the opposite of what the sentence says.
 */
type ToastMessage = Readonly<{ node: ReactNode; mark: boolean }>;

export type ToastOptions = Readonly<{
  /**
   * Keep it until something dismisses it, which is what a toast carrying an
   * action needs: a reader reaching for Undo must not lose it mid-reach.
   */
  sticky?: boolean;
  /** False for anything that is not a completion — a refusal, a warning. */
  mark?: boolean;
}>;

export type Toast = Readonly<{
  shown: ToastMessage | null;
  show: (message: ReactNode, options?: ToastOptions) => void;
  dismiss: () => void;
}>;

export function useToast(): Toast {
  const [shown, setShown] = useState<ToastMessage | null>(null);
  // In a ref rather than in state: a second confirmation arriving before the
  // first has withdrawn must cancel the first one's timer, or the pair share a
  // deadline and the second message vanishes early.
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const clear = useCallback(() => {
    if (timer.current) {
      clearTimeout(timer.current);
      timer.current = null;
    }
  }, []);

  const dismiss = useCallback(() => {
    clear();
    setShown(null);
  }, [clear]);

  const show = useCallback(
    (message: ReactNode, options?: ToastOptions) => {
      clear();
      setShown({ node: message, mark: options?.mark ?? true });
      if (!options?.sticky) {
        timer.current = setTimeout(() => setShown(null), TOAST_MS);
      }
    },
    [clear],
  );

  // The cleanup one of the three copies was missing. A timer belongs to the tree
  // that started it: left running, it fires a state update into a component that
  // is no longer mounted, and on a screen a reader leaves quickly — a settings
  // tab, a record — that is every save they make.
  useEffect(() => clear, [clear]);

  return { shown, show, dismiss };
}

/**
 * Where a confirmation appears: fixed to the foot of the viewport, centred.
 *
 * Renders nothing at all when there is nothing to say, rather than an empty
 * region. The distinction matters here because the region is `position: fixed`
 * and therefore a SIBLING in the markup that occupies no space — an
 * always-present empty node is invisible until some layout rule counts children,
 * and `.wrap:has(> .lt)` in the shell is exactly such a rule.
 *
 * `<output>` is the element: it is a live region by default, so the confirmation
 * is announced without anything having to declare `role="status"` beside it.
 */
export function ToastRegion({ toast }: Readonly<{ toast: Toast }>) {
  const shown = toast.shown;
  if (shown === null) {
    return null;
  }
  return (
    <div className="toast-region">
      {/* `.arrive` (enter.css): it rises into place from below, which is the
          direction it comes from — the region is anchored to the bottom edge. */}
      <output className="toast arrive">
        {shown.mark && <span className="dot dot-auto" />}
        {shown.node}
      </output>
    </div>
  );
}
