/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { act, cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode, RefObject } from "react";
import { createRef } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { en } from "../../i18n/en";
import { VOICE_MIN_WORDS } from "../onboarding";
import {
  VoiceBuildScene,
  VoiceCollectScene,
  VoiceResultScene,
} from "./voice-scenes";

type CorpusSummary = components["schemas"]["VoiceCorpusSummary"];
type VoiceProfileVersion = components["schemas"]["VoiceProfileVersion"];

// A minimal but contract-complete VoiceProfileVersion; profileJSON carries
// only what the result board actually reads, `sample_drafts` swapped per
// test between the starter-corpus empty array and a real held-out draft.
function resultVersion(
  profileJSON: Record<string, unknown>,
): VoiceProfileVersion {
  return {
    id: "v-1",
    profile_id: "p-1",
    profile_version: 1,
    status: "active",
    voice_profile_md: "# Voice DNA",
    profile_json: profileJSON,
    stats_json: {
      word_count: 3994,
      mean_sentence_words: 18.93,
      sample_count: 1,
    },
    source_hash: "h",
    source_count: 1,
    reason: "onboarding",
    predecessor_version: null,
    activation_policy_version: "2",
    model_provider: "routed",
    model_name: "gemini-3.1-flash-lite",
    builder_version: "voicebuilder/1",
    source: "ui",
    captured_by: "agent:voice-builder",
    evaluation: {
      held_out_prompts: 5,
      repeats_per_prompt: 3,
      active_median_voice_score: null,
      candidate_median_voice_score: 0,
      anti_ai_hard_failures: 0,
      structured_output_valid: true,
      corpus_citations_valid: true,
      identity_word_jaccard: 1,
      signature_set_jaccard: 1,
      removed_avoid_rules: 0,
      removed_register_rules: 0,
      classification: "routine",
      passed: true,
    },
    review_reasons: [],
    version: 1,
    created_at: "2026-08-05T00:00:00Z",
    updated_at: "2026-08-05T00:00:00Z",
    archived_at: null,
    activated_at: "2026-08-05T00:00:00Z",
  };
}

function summaryOf(totalWords: number): CorpusSummary {
  return {
    total_words: totalWords,
    target_words: 30000,
    maturity: "collecting",
    quality_band: totalWords >= 800 ? "good" : "thin",
    source_count: 1,
    register_words: { general: totalWords },
  };
}

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
  summary?: CorpusSummary | null;
  canBuild?: boolean;
}) {
  return withLocale(
    <VoiceCollectScene
      eyebrow="Step 3 of 5: Voice"
      summary={overrides.summary ?? null}
      manifest={[]}
      fileRef={overrides.fileRef ?? createRef<HTMLInputElement>()}
      onFiles={() => undefined}
      onAddPaste={overrides.onAddPaste ?? (() => undefined)}
      onBuild={() => undefined}
      onSkip={() => undefined}
      canBuild={overrides.canBuild ?? false}
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

  // Why the step is worth doing is available on the scene, behind its own
  // control rather than above the drop target. The control has to name what it
  // opens, and what it opens has to be the rationale — a disclosure whose body
  // never arrived would look identical from the outside.
  it("offers the payoff copy behind a control that names it", () => {
    collectScene({});

    expect(screen.getByText(en["ob.conv.voice.whyToggle"])).toBeInTheDocument();
    expect(screen.getByText(en["ob.conv.voice.heroBody"])).toBeInTheDocument();
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

describe("the collect scene's corpus floor meter", () => {
  it("reflects the real corpus count, never a derived or estimated one", () => {
    collectScene({ summary: summaryOf(342) });

    const bar = document.querySelector(
      ".ob-voice-meter-bar",
    ) as HTMLProgressElement;
    expect(bar.value).toBe(342);
    expect(bar.max).toBe(VOICE_MIN_WORDS);
    expect(document.querySelector(".ob-voice-meter-line")?.textContent).toBe(
      `342 of ${VOICE_MIN_WORDS} words`,
    );
  });

  it("shows the same floor the Build action gates on, below and at it", () => {
    // Below the floor: the scene's own canBuild (computed from the same
    // VOICE_MIN_WORDS) disables Build, and the meter still reads "not yet".
    collectScene({ summary: summaryOf(200), canBuild: false });
    expect(
      screen.getByRole("button", { name: "Build my voice profile" }),
    ).toBeDisabled();
    expect(
      document.querySelector(".ob-voice-meter-line")?.textContent,
    ).toContain(`of ${VOICE_MIN_WORDS} words`);
    cleanup();

    // At the floor: Build enables, and the meter switches to the ready
    // wording — the two can never disagree, because both read the same
    // VOICE_MIN_WORDS constant.
    collectScene({ summary: summaryOf(VOICE_MIN_WORDS), canBuild: true });
    expect(
      screen.getByRole("button", { name: "Build my voice profile" }),
    ).toBeEnabled();
    expect(
      document.querySelector(".ob-voice-meter-line")?.textContent,
    ).toContain("enough to build");
  });

  it("changes what it says, and announces once, the moment the count crosses the floor", () => {
    const { rerender } = collectScene({ summary: summaryOf(500) });
    expect(document.querySelector(".ob-voice-meter-line")?.textContent).toBe(
      `500 of ${VOICE_MIN_WORDS} words`,
    );
    expect(document.querySelector('[role="status"]')?.textContent).toBe("");

    rerender(
      <LocaleProvider initial="en">
        <VoiceCollectScene
          eyebrow="Step 3 of 5: Voice"
          summary={summaryOf(VOICE_MIN_WORDS)}
          manifest={[]}
          fileRef={createRef<HTMLInputElement>()}
          onFiles={() => undefined}
          onAddPaste={() => undefined}
          onBuild={() => undefined}
          onSkip={() => undefined}
          canBuild
          startPending={false}
          startError={null}
        />
      </LocaleProvider>,
    );

    const ready = `${VOICE_MIN_WORDS} words — enough to build. More still sharpens it.`;
    expect(document.querySelector(".ob-voice-meter-line")?.textContent).toBe(
      ready,
    );
    // The floor-reached announcement fires exactly once, in the visually
    // hidden status region — not on every word the corpus already had.
    expect(document.querySelector('[role="status"]')?.textContent).toBe(ready);
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

  it("does not carry the collect scene's payoff band", () => {
    buildScene(null);

    expect(screen.queryByText("Why this step matters")).toBeNull();
  });

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

describe("VoiceResultScene", () => {
  it("renders the sample the build reserved, and points the reader at it", () => {
    const version = resultVersion({
      inference: { signature_moves: [] },
      sample_drafts: [
        { subject: "Re: kickoff", body: "The plan holds.", voice_score: 0.9 },
      ],
      guidance: {},
    });
    withLocale(
      <VoiceResultScene
        eyebrow="Voice"
        loading={false}
        version={version}
        onContinue={() => undefined}
      />,
    );

    expect(
      screen.getByText("Read the sample first.", { exact: false }),
    ).toBeInTheDocument();
    expect(screen.getByText("The plan holds.")).toBeInTheDocument();
  });

  it("never tells the reader to read a sample the starter corpus could not reserve", () => {
    const version = resultVersion({
      inference: { signature_moves: [] },
      sample_drafts: [],
      guidance: {},
    });
    withLocale(
      <VoiceResultScene
        eyebrow="Voice"
        loading={false}
        version={version}
        onContinue={() => undefined}
      />,
    );

    expect(screen.queryByText(en["ob.conv.voice.resultSub"])).toBeNull();
    expect(screen.queryByText(en["ob.conv.voice.sampleEyebrow"])).toBeNull();
    expect(
      screen.getByText(en["ob.conv.voice.resultSubNoSample"]),
    ).toBeInTheDocument();
  });
});
