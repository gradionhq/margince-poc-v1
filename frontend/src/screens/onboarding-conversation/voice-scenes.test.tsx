/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode, RefObject } from "react";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import { VoiceBuildScene, VoiceCollectScene } from "./voice-scenes";

// VoiceCollectScene owns every way a source enters the corpus (browse, the
// window-wide drop, and now the pasted text this suite pins), so nothing
// upstream needs a composer of its own. VoiceBuildScene's ring is the other
// half: the percentage has to move on its own, honestly, and stop moving for
// readers who asked it to.

function withLocale(ui: ReactNode) {
  return render(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

// jsdom's own matchMedia always answers false; the reduced-motion arm needs
// it stubbed to answer true, listener included.
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

function collectScene(overrides: {
  onAddPaste?: (text: string) => void;
  fileRef?: RefObject<HTMLInputElement | null>;
}) {
  return withLocale(
    <VoiceCollectScene
      eyebrow="Step 3 of 5: Voice"
      summary={null}
      manifest={[]}
      fileRef={overrides.fileRef ?? createRef<HTMLInputElement>()}
      onFiles={() => undefined}
      onAddPaste={overrides.onAddPaste ?? (() => undefined)}
      onBuild={() => undefined}
      onSkip={() => undefined}
      canBuild={false}
      startPending={false}
      startError={null}
    />,
  );
}

describe("VoiceCollectScene", () => {
  it("adds pasted text to the corpus only once the field holds something", async () => {
    const onAddPaste = vi.fn();
    collectScene({ onAddPaste });

    await userEvent.click(
      screen.getByRole("button", { name: "Paste text instead" }),
    );
    const add = screen.getByRole("button", {
      name: "Yes, add it to my corpus.",
    });
    expect(add).toBeDisabled();

    await userEvent.type(
      screen.getByLabelText("Paste the text you wrote here"),
      "  A paragraph I actually wrote.  ",
    );
    expect(add).not.toBeDisabled();
    await userEvent.click(add);

    expect(onAddPaste).toHaveBeenCalledWith("A paragraph I actually wrote.");
    // The field closes after adding, so a second click cannot resubmit it.
    expect(screen.queryByLabelText("Paste the text you wrote here")).toBeNull();
  });

  it("discards the draft without ever calling onAddPaste", async () => {
    const onAddPaste = vi.fn();
    collectScene({ onAddPaste });

    await userEvent.click(
      screen.getByRole("button", { name: "Paste text instead" }),
    );
    await userEvent.type(
      screen.getByLabelText("Paste the text you wrote here"),
      "Something",
    );
    await userEvent.click(
      screen.getByRole("button", { name: "No, discard it." }),
    );

    expect(onAddPaste).not.toHaveBeenCalled();
    expect(screen.queryByLabelText("Paste the text you wrote here")).toBeNull();
  });

  it("wires the browse button to the same hidden input the scene renders", async () => {
    const fileRef = createRef<HTMLInputElement>();
    collectScene({ fileRef });
    const input = fileRef.current;
    expect(input).not.toBeNull();
    const click = vi.spyOn(input as HTMLInputElement, "click");

    await userEvent.click(screen.getByRole("button", { name: "Browse files" }));

    expect(click).toHaveBeenCalledTimes(1);
  });
});

describe("VoiceBuildScene", () => {
  function buildScene(stage: "snapshot" | "extract" | null) {
    return withLocale(
      <VoiceBuildScene
        stage={stage}
        summary={null}
        sources={1}
        model="gemini-3.5-flash"
      />,
    );
  }

  function pct(): number {
    return Number(
      (screen.getByText("%").parentElement?.textContent ?? "0%").replace(
        "%",
        "",
      ),
    );
  }

  it("creeps toward the reported stage's ceiling instead of jumping to it, and never passes it", () => {
    stubReducedMotion(false);
    vi.useFakeTimers();
    buildScene("snapshot");

    // snapshot's ceiling is 1/5 = 20%; the crawl starts below it and only
    // approaches, tick by tick, rather than rendering 20 on the first frame.
    const first = pct();
    expect(first).toBeLessThan(20);

    act(() => {
      vi.advanceTimersByTime(2000);
    });
    const settled = pct();
    expect(settled).toBeGreaterThan(first);
    expect(settled).toBeLessThanOrEqual(20);
  });

  it("keeps easing toward a higher ceiling when the server reports the next stage, never snapping", () => {
    stubReducedMotion(false);
    vi.useFakeTimers();
    const { rerender } = buildScene("snapshot");

    act(() => {
      vi.advanceTimersByTime(2000);
    });
    const beforeStage = pct();

    rerender(
      <LocaleProvider initial="en">
        <VoiceBuildScene
          stage="extract"
          summary={null}
          sources={1}
          model="gemini-3.5-flash"
        />
      </LocaleProvider>,
    );
    // extract's ceiling is 2/5 = 40%; the very next frame must not already
    // read 40 — the display keeps closing the gap, it does not teleport.
    const justAfter = pct();
    expect(justAfter).toBeGreaterThanOrEqual(beforeStage);
    expect(justAfter).toBeLessThan(40);
  });

  it("reads the stage's ceiling directly under prefers-reduced-motion, with no crawl", () => {
    stubReducedMotion(true);
    vi.useFakeTimers();
    buildScene("snapshot");

    expect(pct()).toBe(20);
    act(() => {
      vi.advanceTimersByTime(5000);
    });
    // No further motion: the ceiling did not change, so the reading must not
    // have either.
    expect(pct()).toBe(20);
  });
});
