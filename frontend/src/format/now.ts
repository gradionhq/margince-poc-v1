import { useEffect, useState } from "react";
import type { MessageKey } from "../i18n/en";

// AC-7 groundwork (feeds Task 10's live approvals-inbox countdown). useNow
// is the ONLY place a real clock touches this codebase's rendering — every
// consumer (formatCountdown included) stays pure and takes epoch ms as
// input, so tests never race a real setInterval (craft T11:
// vi.useFakeTimers() + vi.advanceTimersByTime() drive both sides).

// Re-renders the calling component every `intervalMs`, exposing the current
// epoch ms. The interval is cleared on unmount or when intervalMs changes.
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);

  return now;
}

// The shape of useT()'s bound translator: (key, params?) => string.
export type Translator = (
  key: MessageKey,
  params?: Record<string, string | number>,
) => string;

// Pure: given a millisecond span and the caller's `t` (e.g. useT()'s bound
// translator, called as t(key, params)), renders "Xm Ys" while time
// remains, or the localized "expired" sentinel once it has run out.
export function formatCountdown(msRemaining: number, t: Translator): string {
  if (msRemaining <= 0) {
    return t("countdown.expired");
  }
  const totalSeconds = Math.floor(msRemaining / 1000);
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return t("countdown.minutesSeconds", { minutes, seconds });
}
