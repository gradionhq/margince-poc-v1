// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { OnboardingGate, ReadTheatre } from "./onboarding-gate";
import { StoryProviders } from "./story-utils";
import "./onboarding.css";

// The first two states of onboarding, each from a fixture that is shaped exactly
// like the wire: the gate before anything has been asked, and the theatre driven
// by a polled read mid-flight and settled.

const meta: Meta = {
  title: "Screens/onboarding-gate",
  parameters: { layout: "fullscreen" },
};
export default meta;

type Story = StoryObj;
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

const noAction = () => undefined;

// The label the real gate gets from useConfiguredModel(); spelled out here so a
// story shows the disclosure at its realistic width.
const MODEL = "gemini/gemini-3.5-flash · cloud, efficient";

const facts: CompanySiteRead["facts"] = [
  {
    category: "offering",
    field: "service",
    value: "Revenue software for industrial companies",
    value_key: "service:revenue-software",
    evidence_snippet: "Revenue operations built for industrial sales teams",
    evidence_url: "https://gradion.com/services",
    confidence: 0.86,
  },
  {
    category: "company",
    field: "location",
    value: "Frankfurt, Germany",
    value_key: "location:frankfurt",
    evidence_snippet: "Gradion GmbH, Frankfurt am Main",
    evidence_url: "https://gradion.com/impressum",
    confidence: 0.93,
  },
  {
    category: "market",
    field: "served_industry",
    value: "Industrial manufacturing",
    value_key: "served_industry:manufacturing",
    evidence_snippet:
      "We serve industrial manufacturers across the DACH region",
    evidence_url: "https://gradion.com/customers",
    confidence: 0.79,
  },
];

const reading: CompanySiteRead = {
  id: "018f3a1b-0000-7000-8000-0000000000b2",
  target_kind: "onboarding",
  organization_id: null,
  root_url: "https://gradion.com",
  status: "reading",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: "crawling",
  pages_read: 6,
  pages: [
    { url: "https://gradion.com", status: "fetched", kind: "home" },
    { url: "https://gradion.com/about", status: "fetched", kind: "about" },
    {
      url: "https://gradion.com/services",
      status: "fetched",
      kind: "services",
    },
    {
      url: "https://gradion.com/products",
      status: "fetched",
      kind: "products",
    },
    { url: "https://gradion.com/team", status: "fetched", kind: "team" },
    {
      url: "https://gradion.com/impressum",
      status: "fetched",
      kind: "impressum",
    },
    {
      url: "https://gradion.com/careers",
      status: "skipped",
      kind: "other",
      reason: "not company context",
    },
    {
      url: "https://gradion.com/blog",
      status: "failed",
      kind: "other",
      reason: "blocked by robots.txt",
    },
  ],
  profile_fields: [],
  facts: facts.slice(0, 2),
  comparisons: [],
  people: [],
  warnings: [],
  draft_version: 2,
  proposal_hash: "proposal-2",
  created_at: "2026-07-19T08:00:00Z",
  updated_at: "2026-07-19T08:00:04Z",
  ai_runtime: {
    currency: "USD",
    call_attempts: 4,
    tokens_in: 3420,
    tokens_out: 680,
    latency_ms: 5240,
    estimated_cost_microusd: 12_750,
    unpriced_calls: 0,
    models: [],
  },
};

const settled: CompanySiteRead = {
  ...reading,
  status: "ready",
  phase: null,
  pages_read: 6,
  facts,
  profile_fields: [
    {
      field: "legal_name",
      value: "Gradion GmbH",
      evidence_snippet: "© 2026 Gradion GmbH",
      source_kind: "url",
      source_url: "https://gradion.com/impressum",
      confidence: 0.94,
    },
    {
      field: "offer_summary",
      value: "Revenue software for industrial companies",
      evidence_snippet: "Revenue operations built for industrial sales teams",
      source_kind: "url",
      source_url: "https://gradion.com/about",
      confidence: 0.86,
    },
  ],
  updated_at: "2026-07-19T08:01:22Z",
};

function Surface({ children }: Readonly<{ children: ReactNode }>) {
  return (
    <StoryProviders>
      <div className="ob-page">{children}</div>
    </StoryProviders>
  );
}

export const AtRest: Story = {
  render: () => (
    <Surface>
      <OnboardingGate
        name="Lars"
        running={false}
        configuredModel={MODEL}
        onSubmit={noAction}
        onManual={noAction}
      />
    </Surface>
  ),
};

export const Anonymous: Story = {
  render: () => (
    <Surface>
      <OnboardingGate
        running={false}
        configuredModel={MODEL}
        onSubmit={noAction}
        onManual={noAction}
      />
    </Surface>
  ),
};

export const StartFailed: Story = {
  render: () => (
    <Surface>
      <OnboardingGate
        name="Lars"
        running={false}
        configuredModel={MODEL}
        notice={{
          tone: "error",
          message:
            "I could not read that site. The host did not answer. Try another address, or enter the details yourself.",
        }}
        onSubmit={noAction}
        onManual={noAction}
      />
    </Surface>
  ),
};

export const MidRead: Story = {
  render: () => (
    <Surface>
      <ReadTheatre
        read={reading}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />
    </Surface>
  ),
};

export const ReadComplete: Story = {
  render: () => (
    <Surface>
      <ReadTheatre
        read={settled}
        host="gradion.com"
        locale="en"
        configuredModel={MODEL}
      />
    </Surface>
  ),
};
