// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useEffect, useState } from "react";

// The phone breakpoint, spelled once for the code that has to KNOW it rather
// than merely be laid out by it: at this width the sidebar is a bottom bar of
// four destinations, which changes what the panel renders and not only how it
// looks. The `@media (max-width: 700px)` block in app/shell.css is the other
// half of the same rule — a stylesheet cannot read a TypeScript constant, so
// that block cites this file and the two are changed together.
export const PHONE_MAX_WIDTH = 700;

const PHONE_QUERY = `(max-width: ${PHONE_MAX_WIDTH}px)`;

function phoneQuery(): MediaQueryList | undefined {
  return globalThis.matchMedia?.(PHONE_QUERY);
}

/**
 * Whether the viewport is at phone width.
 *
 * Subscribed rather than measured once: a window is resized and a phone is
 * rotated while the app is open, and chrome that read the width at mount would
 * keep the other width's arrangement for the rest of the session.
 *
 * `matchMedia` is absent in some embedded contexts, and a missing media query is
 * a DEFAULT rather than an error — the answer is then "not a phone", which is
 * the arrangement that works at any width.
 */
export function usePhoneViewport(): boolean {
  const [phone, setPhone] = useState(() => phoneQuery()?.matches ?? false);
  useEffect(() => {
    const query = phoneQuery();
    if (!query) {
      return;
    }
    // Read again on subscribe: the first render can happen before the window
    // has settled at the size it ends up at, and the listener below only ever
    // reports a CHANGE from whatever the query matched then.
    setPhone(query.matches);
    const listen = () => setPhone(query.matches);
    query.addEventListener("change", listen);
    return () => query.removeEventListener("change", listen);
  }, []);
  return phone;
}
