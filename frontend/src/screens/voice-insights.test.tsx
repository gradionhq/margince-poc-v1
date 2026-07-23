/** @vitest-environment jsdom */
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { parseVoiceInsights, VoiceInsights } from "./voice-insights";

type VoiceProfileVersion = components["schemas"]["VoiceProfileVersion"];

afterEach(cleanup);

function version(
  profileJSON: Record<string, unknown>,
  statsJSON: Record<string, unknown>,
): VoiceProfileVersion {
  return {
    id: "v-1",
    profile_id: "p-1",
    profile_version: 3,
    status: "active",
    voice_profile_md: "# Voice DNA",
    profile_json: profileJSON,
    stats_json: statsJSON,
    source_hash: "h",
    source_count: 4,
    reason: "manual",
    predecessor_version: null,
    activation_policy_version: "2",
    model_provider: "routed",
    model_name: "gemini-3.5-flash",
    builder_version: "voicebuilder/1",
    source: "build",
    captured_by: "human:x",
    evaluation: {
      held_out_prompts: 5,
      repeats_per_prompt: 3,
      active_median_voice_score: null,
      candidate_median_voice_score: 0.82,
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
    created_at: "2026-07-22T08:00:00Z",
    updated_at: "2026-07-22T08:00:00Z",
    archived_at: null,
    activated_at: "2026-07-22T08:00:00Z",
  };
}

const richVersion = version(
  {
    inference: {
      identity_summary: "Direct and operational.",
      thinking_pattern: "Verdict first, then the operational why.",
      observed_obsessions: ["second-order effects"],
      signature_moves: [
        {
          move: "verdict first",
          quote: "We ship on Monday, no excuses.",
          sample_id: "s-1",
        },
      ],
      avoid: ["corporate filler"],
    },
    sample_drafts: [
      { subject: "Re: plan", body: "The plan holds.", voice_score: 0.9 },
    ],
    guidance: {
      next_best: "Add a call transcript.",
      register_gaps: ["spoken"],
    },
  },
  { word_count: 12345, mean_sentence_words: 9.4, sample_count: 4 },
);

describe("parseVoiceInsights", () => {
  it("extracts every structured section the builder stored", () => {
    const data = parseVoiceInsights(richVersion);
    expect(data.thinking).toBe("Verdict first, then the operational why.");
    expect(data.moves).toEqual([
      { move: "verdict first", quote: "We ship on Monday, no excuses." },
    ]);
    expect(data.sampleDrafts).toHaveLength(1);
    expect(data.nextBest).toBe("Add a call transcript.");
    expect(data.words).toBe(12345);
    expect(data.modelName).toBe("gemini-3.5-flash");
  });

  it("treats missing or malformed sections as absent, never as a crash", () => {
    const data = parseVoiceInsights(
      version({ inference: "not-an-object", sample_drafts: [42] }, {}),
    );
    expect(data.thinking).toBeNull();
    expect(data.moves).toEqual([]);
    expect(data.sampleDrafts).toEqual([]);
    expect(data.nextBest).toBeNull();
  });
});

describe("VoiceInsights", () => {
  it("shows what was learned with the user's own words as proof", () => {
    render(
      <LocaleProvider>
        <VoiceInsights
          data={parseVoiceInsights(richVersion)}
          profileVersion={3}
        />
      </LocaleProvider>,
    );
    expect(screen.getByText(/How you think/)).toBeTruthy();
    expect(
      screen.getByText(/Verdict first, then the operational why./),
    ).toBeTruthy();
    expect(screen.getByText(/We ship on Monday, no excuses./)).toBeTruthy();
    expect(screen.getByText(/draft only/)).toBeTruthy();
    expect(screen.getByText(/Add a call transcript./)).toBeTruthy();
    expect(screen.getByText(/v3/)).toBeTruthy();
  });
});
