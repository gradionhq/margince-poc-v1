// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { OnboardingBackread } from "./onboarding-backread";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The backread step for the fe-uat render gate — one story per honest branch:
// the window pick with its scope, a failed estimate that still allows the read,
// a run in progress with and without a denominator, the finished read, and the
// two failure states. Every fixture is a valid BackfillStatus/BackfillPreview,
// so nothing here renders a count the wire could not have sent.

type BackfillStatus = components["schemas"]["BackfillStatus"];

function backreadStory(
  initial: BackfillStatus,
  routes: Record<string, (body: unknown) => Response> = {},
) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <OnboardingBackread
          provider="gmail"
          initial={initial}
          onFinish={() => {}}
        />
      </StoryProviders>
    );
  };
}

const preview =
  (extra: Record<string, unknown> = {}) =>
  () =>
    jsonResponse({
      window: "6m",
      estimated_messages: 4820,
      computed_at: "2026-07-31T09:12:00Z",
      ...extra,
    });

const meta: Meta<typeof OnboardingBackread> = {
  title: "screens/onboarding-backread",
  component: OnboardingBackread,
};
export default meta;
type Story = StoryObj<typeof OnboardingBackread>;

export const Pick: Story = {
  render: backreadStory(
    { state: "none" },
    {
      "POST /connectors/gmail/backfill/preview": preview({
        estimated_cost_minor: 310,
        currency: "USD",
        estimate_quality: "observed",
      }),
    },
  ),
};

// A cold-start workspace has no priced call history, so the estimate is a
// work-shape floor and says so.
export const HeuristicEstimate: Story = {
  render: backreadStory(
    { state: "none" },
    {
      "POST /connectors/gmail/backfill/preview": preview({
        estimate_quality: "heuristic",
      }),
    },
  ),
};

// The estimator failed. The read is still offered: not knowing the size of a
// mailbox is a reason to say so, never a reason to refuse.
export const EstimateFailed: Story = {
  render: backreadStory(
    { state: "none" },
    {
      "POST /connectors/gmail/backfill/preview": () =>
        jsonResponse(
          { code: "internal", detail: "The mailbox did not answer in time." },
          502,
        ),
    },
  ),
  play: async ({ canvasElement }) => {
    await within(canvasElement).findByText(/could not estimate that window/i);
  },
};

export const Queued: Story = {
  render: backreadStory({ state: "queued", window: "6m" }),
};

export const Running: Story = {
  render: backreadStory({
    state: "running",
    window: "6m",
    estimated_messages: 4820,
    counts: {
      messages_scanned: 1960,
      captured: 730,
      skipped: 1230,
      people_created: 214,
      organizations_created: 58,
    },
    updated_at: "2026-07-31T09:20:00Z",
  }),
};

// No provider-side count to divide by: an open tally, and no bar that would
// imply an end nobody knows.
export const RunningWithoutDenominator: Story = {
  render: backreadStory({
    state: "running",
    window: "12m",
    estimated_messages: null,
    counts: { messages_scanned: 640, captured: 191 },
  }),
};

// Partial counts: the tallies the run reported and no others — an absent count
// is a measurement nobody took, not a zero.
export const PartialCounts: Story = {
  render: backreadStory({
    state: "running",
    counts: { messages_scanned: 88 },
  }),
};

export const Done: Story = {
  render: backreadStory({
    state: "done",
    window: "6m",
    estimated_messages: 4820,
    counts: {
      messages_scanned: 4820,
      captured: 1804,
      skipped: 3016,
      people_created: 512,
      organizations_created: 143,
    },
    completed_at: "2026-07-31T09:41:00Z",
  }),
};

export const Failed: Story = {
  render: backreadStory({
    state: "error",
    counts: { messages_scanned: 410, captured: 120 },
    last_error_class: "auth",
  }),
};

export const Cancelled: Story = {
  render: backreadStory({
    state: "cancelled",
    counts: { messages_scanned: 300, captured: 96, people_created: 31 },
  }),
};

// Stopping a live read: the status row answers cancelled and the outcome says
// plainly that nothing was written.
export const Stopping: Story = {
  render: backreadStory(
    { state: "running", counts: { messages_scanned: 120 } },
    {
      "DELETE /connectors/gmail/backfill": () =>
        jsonResponse({ state: "cancelled" }),
      "GET /connectors/gmail/backfill": () =>
        jsonResponse({ state: "cancelled", counts: { messages_scanned: 120 } }),
    },
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /stop reading/i }),
    );
    await canvas.findByText(/Nothing was written/i);
  },
};
