// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { ReadCompanyStep } from "./onboarding-read";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";
import "./onboarding.css";

const meta: Meta = {
  title: "Screens/Onboarding/Read",
  parameters: { layout: "fullscreen" },
};
export default meta;

type Story = StoryObj;

const noAction = () => undefined;
const norm = {
  ok: true,
  host: "gradion.com",
  full: "https://gradion.com",
};

const reading = {
  id: "018f3a1b-0000-7000-8000-0000000000b2",
  target_kind: "onboarding" as const,
  organization_id: null,
  root_url: "https://gradion.com",
  status: "reading" as const,
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: "extracting" as const,
  pages_read: 2,
  pages: [
    {
      url: "https://gradion.com",
      status: "fetched" as const,
      kind: "home" as const,
    },
    {
      url: "https://gradion.com/about",
      status: "fetched" as const,
      kind: "about" as const,
    },
    {
      url: "https://gradion.com/careers",
      status: "skipped" as const,
      kind: "other" as const,
      reason: "not company context",
    },
  ],
  profile_fields: [
    {
      field: "legal_name" as const,
      value: "Gradion GmbH",
      evidence_snippet: "© 2026 Gradion GmbH",
      source_kind: "url" as const,
      source_url: "https://gradion.com",
      confidence: 0.94,
    },
    {
      field: "offer_summary" as const,
      value: "Revenue software for industrial companies",
      evidence_snippet: "Revenue operations built for industrial sales teams",
      source_kind: "url" as const,
      source_url: "https://gradion.com/about",
      confidence: 0.86,
    },
  ],
  facts: [],
  comparisons: [],
  people: [],
  warnings: [],
  draft_version: 2,
  proposal_hash: "proposal-2",
  created_at: "2026-07-19T08:00:00Z",
  updated_at: "2026-07-19T08:00:04Z",
  ai_runtime: {
    currency: "USD" as const,
    call_attempts: 4,
    tokens_in: 3420,
    tokens_out: 680,
    latency_ms: 5240,
    estimated_cost_microusd: 12_750,
    unpriced_calls: 0,
    models: [
      {
        task: "site_fact_extract",
        tier: "cheap_cloud",
        provider: "gemini",
        configured_model: "gemini-3.1-flash-lite",
        served_model: "gemini-3.1-flash-lite-2026-07",
        call_attempts: 3,
        tokens_in: 2100,
        tokens_out: 390,
        cached_tokens: 0,
        cache_write_tokens: 0,
        reasoning_tokens: 0,
        latency_ms: 3020,
        estimated_cost_microusd: 1110,
        unpriced_calls: 0,
        last_used_at: "2026-07-19T08:00:03Z",
      },
      {
        task: "site_extract",
        tier: "premium",
        provider: "gemini",
        configured_model: "gemini-3.5-flash",
        served_model: "gemini-3.5-flash-2026-07",
        call_attempts: 1,
        tokens_in: 1320,
        tokens_out: 290,
        cached_tokens: 0,
        cache_write_tokens: 0,
        reasoning_tokens: 80,
        latency_ms: 2220,
        estimated_cost_microusd: 11640,
        unpriced_calls: 0,
        last_used_at: "2026-07-19T08:00:04Z",
      },
    ],
  },
};

const partial = {
  ...reading,
  status: "partial" as const,
  phase: null,
  facts: [
    {
      category: "company" as const,
      field: "founded_year" as const,
      value: "2021",
      value_key: "founded_year:2021",
      evidence_snippet: "Founded in 2021",
      evidence_url: "https://gradion.com/about",
      confidence: 0.88,
    },
  ],
  warnings: [
    "Two pages blocked automated access; available findings remain reviewable.",
  ],
  draft_version: 3,
  proposal_hash: "proposal-3",
};

const deferred = {
  ...reading,
  status: "deferred" as const,
  phase: null,
  status_code: "budget_deferred" as const,
  status_detail:
    "AI budget reached its current limit. This website read will resume automatically.",
  next_attempt_at: "2026-08-01T00:00:00Z",
};

function ReadStory({
  mode = "website",
  read = null,
  error = null,
}: Readonly<{
  mode?: "website" | "manual" | null;
  read?: typeof reading | typeof partial | typeof deferred | null;
  error?: string | null;
}>) {
  installFetchStub({
    "GET /assistant/profile": () =>
      jsonResponse({
        name: "Margince",
        kind: "ai",
        state: "configured",
        inference_mode: "hybrid",
        providers: ["gemini", "ollama"],
        configured_models: [
          {
            tier: "cheap_cloud",
            provider: "gemini",
            model: "gemini-3.1-flash-lite",
          },
          { tier: "local_small", provider: "ollama", model: "gemma3" },
          {
            tier: "premium",
            provider: "gemini",
            model: "gemini-3.5-flash",
          },
        ],
      }),
  });
  return (
    <StoryProviders>
      <div className="ob-page">
        <div className="wiz">
          <ReadCompanyStep
            mode={mode}
            website={mode === "website" ? "gradion.com" : ""}
            norm={mode === "website" ? norm : { ok: false, host: "", full: "" }}
            read={read}
            pending={false}
            refreshing={read?.status === "reading"}
            error={error}
            confirmPending={false}
            confirmDisabled={false}
            onWebsiteChange={noAction}
            onChooseManual={noAction}
            onStart={noAction}
            onConfirm={noAction}
            onApplyChanges={noAction}
            reviewContent={read ? <p>Company draft</p> : undefined}
          />
        </div>
      </div>
    </StoryProviders>
  );
}

export const EmptyChoice: Story = {
  render: () => <ReadStory mode={null} />,
};

export const ReadingProgress: Story = {
  render: () => <ReadStory read={reading} />,
};

export const WaitingForBudget: Story = {
  render: () => <ReadStory read={deferred} />,
};

export const PartialCoverage: Story = {
  render: () => <ReadStory read={partial} />,
};

export const RobotsBlocked: Story = {
  render: () => (
    <ReadStory error="The site blocked automated access. You can retry or continue manually." />
  ),
};

export const NoModelAvailable: Story = {
  render: () => (
    <ReadStory error="No extraction model is configured. Manual setup remains fully available." />
  ),
};
