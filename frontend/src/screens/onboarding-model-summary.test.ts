// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { describe, expect, it } from "vitest";
import type { components } from "../api/schema";
import { translate } from "../i18n";
import {
  configuredModelLabel,
  configuredModelSummary,
} from "./onboarding-read";

// The rail footer's plain-language line replaced a raw, truncated
// "provider/model · tier + provider/model · tier" string. These cases prove
// the summary always says a true count and a true place — the reader who
// funds this out of their own key can trust "three models" even though the
// footer no longer spells out which three.

type AiProfile = components["schemas"]["AiProfile"];

const en = (
  key: Parameters<typeof translate>[1],
  params?: Record<string, string | number>,
) => translate("en", key, params);
const de = (
  key: Parameters<typeof translate>[1],
  params?: Record<string, string | number>,
) => translate("de", key, params);

function profile(overrides: Partial<AiProfile>): AiProfile {
  return {
    name: "Margince",
    kind: "ai",
    state: "configured",
    inference_mode: "cloud",
    providers: ["gemini"],
    configured_models: [
      { tier: "cheap_cloud", provider: "gemini", model: "gemini-3.5-flash" },
    ],
    ...overrides,
  };
}

describe("configuredModelSummary", () => {
  it("counts distinct models and names the real running mode", () => {
    const three = profile({
      inference_mode: "cloud",
      configured_models: [
        {
          tier: "cheap_cloud",
          provider: "gemini",
          model: "gemini-3.1-flash-lite",
        },
        { tier: "premium", provider: "anthropic", model: "claude-opus-4" },
        { tier: "local_small", provider: "ollama", model: "qwen3-32b" },
      ],
    });
    // inference_mode is the server's own aggregate call, not one this
    // function infers from the per-model tiers itself.
    expect(configuredModelSummary(three, "unavailable", en)).toBe(
      "3 models, running in the cloud",
    );
  });

  it("reads as singular, not '1 models', for exactly one configured model", () => {
    const one = profile({
      inference_mode: "local",
      configured_models: [
        { tier: "local_small", provider: "ollama", model: "gemma3" },
      ],
    });
    expect(configuredModelSummary(one, "unavailable", en)).toBe(
      "1 model, running locally",
    );
    expect(configuredModelSummary(one, "unavailable", de)).toBe(
      "1 Modell, läuft lokal",
    );
  });

  it("says split rather than claiming a mixed fleet is all cloud", () => {
    const mixed = profile({
      inference_mode: "hybrid",
      configured_models: [
        { tier: "cheap_cloud", provider: "gemini", model: "gemini-3.5-flash" },
        { tier: "local_large", provider: "ollama", model: "llama3-70b" },
      ],
    });
    expect(configuredModelSummary(mixed, "unavailable", en)).toBe(
      "2 models, split between cloud and local",
    );
  });

  it("dedupes a model bound to more than one tier before counting", () => {
    const dup = profile({
      configured_models: [
        { tier: "cheap_cloud", provider: "gemini", model: "gemini-3.5-flash" },
        { tier: "premium", provider: "gemini", model: "gemini-3.5-flash" },
      ],
    });
    expect(configuredModelSummary(dup, "unavailable", en)).toBe(
      "1 model, running in the cloud",
    );
  });

  it("falls back to a provider count when no per-model bindings are reported", () => {
    const providersOnly = profile({
      configured_models: [],
      providers: ["anthropic", "gemini"],
    });
    expect(configuredModelSummary(providersOnly, "unavailable", en)).toBe(
      "2 providers configured",
    );
  });

  it("says nothing is configured rather than a false zero-count sentence", () => {
    const empty = profile({ configured_models: [], providers: [] });
    expect(configuredModelSummary(empty, "unavailable", en)).toBe(
      "No model configured yet",
    );
  });

  it("hands back the unavailable label while the profile has not loaded", () => {
    expect(configuredModelSummary(undefined, "unavailable", en)).toBe(
      "unavailable",
    );
  });

  it("leaves the exact identifiers reachable through the detail label", () => {
    const three = profile({
      configured_models: [
        {
          tier: "cheap_cloud",
          provider: "gemini",
          model: "gemini-3.1-flash-lite",
        },
        { tier: "local_small", provider: "ollama", model: "qwen3-32b" },
        { tier: "premium", provider: "anthropic", model: "claude-opus-4" },
      ],
    });
    expect(configuredModelLabel(three, "unavailable", en)).toBe(
      "gemini/gemini-3.1-flash-lite · cloud, efficient + " +
        "ollama/qwen3-32b · local, fast + " +
        "anthropic/claude-opus-4 · premium reasoning",
    );
  });
});
