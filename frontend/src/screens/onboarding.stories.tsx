// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { OnboardingScreen } from "./onboarding";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

const meta: Meta = {
  title: "Screens/Onboarding",
  parameters: { layout: "fullscreen" },
};
export default meta;

type Story = StoryObj;

const company = {
  organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
  display_name: "Gradion",
  website: "gradion.com",
  legal_name: "Gradion GmbH",
  offer_summary: "Revenue software for industrial companies",
  icp: "Mid-market manufacturers with complex sales cycles",
  value_proposition:
    "Turn fragmented relationship data into coordinated revenue action",
  buying_center: "Head of Sales, Revenue Operations, Managing Director",
  minimum_complete: true,
};

function wizardState(step: "confirm" | "results") {
  return {
    path: "creator",
    step,
    source_mode: "manual",
    website_url: null,
    site_read_id: null,
    company_draft: company,
    selected_fact_keys: [],
    voice_skipped: step === "results",
    connect_skipped: false,
    version: 4,
    completed_at: null,
    created_at: "2026-07-19T08:00:00Z",
    updated_at: "2026-07-19T08:03:00Z",
  };
}

function FullScreenStory({ step }: Readonly<{ step: "confirm" | "results" }>) {
  installFetchStub({
    "GET /company": () => jsonResponse(company),
    "GET /onboarding/state": () => jsonResponse(wizardState(step)),
  });
  return (
    <StoryProviders>
      <OnboardingScreen />
    </StoryProviders>
  );
}

export const Review: Story = {
  render: () => <FullScreenStory step="confirm" />,
};

export const RealDataCompletion: Story = {
  render: () => <FullScreenStory step="results" />,
};
