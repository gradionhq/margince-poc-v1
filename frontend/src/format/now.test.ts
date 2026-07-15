/** @vitest-environment jsdom */
import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { translate } from "../i18n";
import { formatCountdown, useNow } from "./now";

// Task 9 (AC-7 groundwork): useNow/formatCountdown feed Task 10's live
// expiry countdown. No real clock in tests (craft T11) — vi.useFakeTimers()
// pins Date.now() and drives setInterval deterministically.

const t = (
  key: Parameters<typeof translate>[1],
  params?: Record<string, string | number>,
) => translate("en", key, params);

describe("formatCountdown (pure)", () => {
  it("renders minutes and seconds for a positive remainder", () => {
    expect(formatCountdown(90_000, t)).toBe("1m 30s");
  });

  it("renders the expired sentinel for zero or negative remainders", () => {
    expect(formatCountdown(0, t)).toBe("Expired");
    expect(formatCountdown(-1, t)).toBe("Expired");
  });
});

describe("useNow (interval clock)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("advances as fake time is advanced by the interval", () => {
    vi.setSystemTime(0);
    const { result, unmount } = renderHook(() => useNow(1000));
    expect(result.current).toBe(0);

    act(() => {
      vi.advanceTimersByTime(1000);
    });
    expect(result.current).toBe(1000);

    act(() => {
      vi.advanceTimersByTime(3000);
    });
    expect(result.current).toBe(4000);

    unmount();
  });
});
