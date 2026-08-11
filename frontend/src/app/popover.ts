import { type RefObject, useEffect } from "react";

/**
 * Dismissal for a popover that owns the document while it is open.
 *
 * The listeners live on the document so Escape works from anywhere inside the
 * popover and any outside click closes it. The click listener is registered a
 * tick late, so the click that OPENED the popover does not immediately close it
 * again.
 *
 * One implementation, two callers (the sidebar's account menu and the page
 * head's agent dock): a second copy of this is how two popovers in the same
 * product end up dismissing differently.
 */
export function usePopoverDismiss(
  open: boolean,
  panel: RefObject<HTMLElement | null>,
  dismiss: () => void,
): void {
  useEffect(() => {
    if (!open) {
      return;
    }
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      // One keystroke closes one layer. A row inside may open a popover of its
      // own (the language list does), and both dismissals listen on the
      // document, so without this a single Escape would collapse the inner
      // layer AND this one — leaving the reader two steps from where they were.
      // The inner layer announces itself through the trigger it expanded; while
      // one is open it owns Escape, and the second press reaches here.
      if (
        panel.current?.querySelector(
          '[aria-haspopup="menu"][aria-expanded="true"]',
        )
      ) {
        return;
      }
      dismiss();
    };
    const onClick = () => dismiss();
    document.addEventListener("keydown", onKey);
    const timer = window.setTimeout(
      () => document.addEventListener("click", onClick),
      0,
    );
    return () => {
      document.removeEventListener("keydown", onKey);
      window.clearTimeout(timer);
      document.removeEventListener("click", onClick);
    };
  }, [open, panel, dismiss]);
}
