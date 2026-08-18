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
    // A non-positive interval disables the clock: the caller doesn't render
    // anything time-dependent (e.g. a read-only row), so there is no reason to
    // re-render every tick. `now` stays pinned at its mount value.
    if (intervalMs <= 0) {
      return;
    }
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

const SECONDS_PER_MINUTE = 60;
const MINUTES_PER_HOUR = 60;
const HOURS_PER_DAY = 24;

// Pure: given a millisecond span and the caller's `t` (e.g. useT()'s bound
// translator, called as t(key, params)), renders the two largest units the
// span reaches, or the localized "expired" sentinel once it has run out.
//
// Two units, and the span picks which two: a deadline three days out reads
// "3d 4h", not the 4316 minutes that carrying minutes as the top unit
// produces. The pair always answers "how long have I got" at the precision
// that span deserves — seconds matter in the last minutes of an approval
// window and are noise a day earlier.
export function formatCountdown(msRemaining: number, t: Translator): string {
  if (msRemaining <= 0) {
    return t("countdown.expired");
  }
  const totalSeconds = Math.floor(msRemaining / 1000);
  const totalMinutes = Math.floor(totalSeconds / SECONDS_PER_MINUTE);
  const totalHours = Math.floor(totalMinutes / MINUTES_PER_HOUR);
  const days = Math.floor(totalHours / HOURS_PER_DAY);

  if (days >= 1) {
    return t("countdown.daysHours", {
      days,
      hours: totalHours % HOURS_PER_DAY,
    });
  }
  if (totalHours >= 1) {
    return t("countdown.hoursMinutes", {
      hours: totalHours,
      minutes: totalMinutes % MINUTES_PER_HOUR,
    });
  }
  return t("countdown.minutesSeconds", {
    minutes: totalMinutes,
    seconds: totalSeconds % SECONDS_PER_MINUTE,
  });
}
