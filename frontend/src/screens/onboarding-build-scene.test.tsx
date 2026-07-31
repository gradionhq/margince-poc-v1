/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { act, cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { BuildScene } from "./onboarding-build-scene";

// The scene is a deliberate pause, which makes two things safety-critical: it
// must fire its completion exactly once and only when the clock says so, and it
// must not fire it at all after the caller has moved on. The clock is faked in
// every case — a scene tested by waiting is a scene tested by luck.

function withLocale(ui: ReactNode) {
  return render(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

// The preference the whole reduced-motion arm turns on. jsdom's own
// matchMedia always answers `false`, so the query has to be stubbed to say
// otherwise — including the listener the hook subscribes with.
function stubReducedMotion(reduce: boolean) {
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: reduce && query.includes("prefers-reduced-motion"),
    media: query,
    onchange: null,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
    addListener: () => undefined,
    removeListener: () => undefined,
    dispatchEvent: () => false,
  }));
}

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("BuildScene", () => {
  it("fires onDone once when its duration elapses, and not a tick before", () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    withLocale(<BuildScene onDone={onDone} durationMs={1200} />);

    act(() => {
      vi.advanceTimersByTime(1199);
    });
    expect(onDone).not.toHaveBeenCalled();

    act(() => {
      vi.advanceTimersByTime(1);
    });
    expect(onDone).toHaveBeenCalledTimes(1);

    // Nothing schedules a second handoff after the first.
    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("never navigates out from under a caller that moved on first", () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    const { unmount } = withLocale(
      <BuildScene onDone={onDone} durationMs={1200} />,
    );

    act(() => {
      vi.advanceTimersByTime(600);
    });
    unmount();
    act(() => {
      vi.advanceTimersByTime(5000);
    });

    expect(onDone).not.toHaveBeenCalled();
  });

  it("dissolves over the last stretch of its duration, then hands off on the clock", () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    withLocale(<BuildScene onDone={onDone} durationMs={1200} />);
    const scene = screen.getByRole("status", {
      name: "Assembling your workspace",
    });

    expect(scene).not.toHaveClass("is-leaving");

    // 0.86 of the duration: the exit starts inside the time the caller asked
    // for, so leaving costs the reader nothing extra.
    act(() => {
      vi.advanceTimersByTime(1032);
    });
    expect(scene).toHaveClass("is-leaving");
    expect(onDone).not.toHaveBeenCalled();

    // No animation event is ever dispatched here, and the handoff still lands:
    // the dissolve cannot strand a reader on a full-screen scene.
    act(() => {
      vi.advanceTimersByTime(168);
    });
    expect(onDone).toHaveBeenCalledTimes(1);
  });

  it("stays silent when the caller moves on mid-dissolve", () => {
    vi.useFakeTimers();
    const onDone = vi.fn();
    const { unmount } = withLocale(
      <BuildScene onDone={onDone} durationMs={1200} />,
    );

    // Past the start of the exit, before the handoff: the window in which the
    // scene is already leaving and could still navigate.
    act(() => {
      vi.advanceTimersByTime(1100);
    });
    unmount();
    act(() => {
      vi.advanceTimersByTime(5000);
    });

    expect(onDone).not.toHaveBeenCalled();
  });

  it("skips the scene entirely under reduced motion, completing immediately", () => {
    stubReducedMotion(true);
    vi.useFakeTimers();
    const onDone = vi.fn();
    const { container } = withLocale(
      <BuildScene onDone={onDone} durationMs={1200} />,
    );

    // No clock advance at all: the end state of a decorative pause is being
    // past it, so the callback has already run on the first commit.
    expect(onDone).toHaveBeenCalledTimes(1);
    expect(container).toBeEmptyDOMElement();
  });

  it("states what it is doing, and keeps the letter stagger out of the a11y tree", () => {
    vi.useFakeTimers();
    withLocale(<BuildScene onDone={vi.fn()} durationMs={1200} />);

    // A blocking scene that says nothing to a screen reader is a dead end.
    const scene = screen.getByRole("status", {
      name: "Assembling your workspace",
    });
    expect(scene).toBeInTheDocument();
    expect(screen.getByText("Assembling your workspace")).toBeInTheDocument();

    // The word is readable through the real wordmark's accessible name...
    expect(screen.getByRole("img", { name: "Margince" })).toBeInTheDocument();
    // ...while the animated letters that spell it are decorative.
    const letters = scene.querySelector(".ob-build-letters");
    expect(letters).toHaveAttribute("aria-hidden", "true");
    expect(letters?.textContent).toBe("Margince");
  });
});
