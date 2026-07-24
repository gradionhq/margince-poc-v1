// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { BackfillPanel } from "./backfill";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// BackfillPanel stories for the fe-uat render gate — one per honest branch
// the panel now covers: the ordinary setup/running/done/error/cancelled run
// (seeded via `initial`, matching how connectors.tsx now mounts this panel
// off the embedded CaptureConnection.backfill), plus the three added by this
// change — a provider with no Backfiller, a stalled running run, and a
// refused window narrowing.

type BackfillStatus = components["schemas"]["BackfillStatus"];
type Provider = components["schemas"]["CaptureConnection"]["provider"];

function panelStory(
  provider: Provider,
  initial: BackfillStatus,
  routes: Record<string, (body: unknown) => Response> = {},
) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <BackfillPanel provider={provider} initial={initial} />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof BackfillPanel> = {
  title: "screens/backfill",
  component: BackfillPanel,
};
export default meta;
type Story = StoryObj<typeof BackfillPanel>;

export const Setup: Story = {
  render: panelStory(
    "gmail",
    { state: "none" },
    {
      "POST /connectors/gmail/backfill/preview": () =>
        jsonResponse({
          window: "6m",
          estimated_messages: 1234,
          computed_at: "2026-07-23T10:00:00Z",
        }),
    },
  ),
};

export const Running: Story = {
  render: panelStory("gmail", {
    state: "running",
    backfill_id: "018f3a1b-0000-7000-8000-0000000000b1",
    window: "6m",
    estimated_messages: 400,
    counts: {
      captured: 128,
      people_created: 47,
      organizations_created: 12,
      messages_scanned: 150,
    },
    updated_at: new Date().toISOString(),
  }),
};

export const Done: Story = {
  render: panelStory("gmail", {
    state: "done",
    counts: {
      captured: 512,
      people_created: 90,
      organizations_created: 20,
      messages_scanned: 600,
    },
  }),
};

export const ErrorState: Story = {
  render: panelStory("gmail", {
    state: "error",
    counts: { captured: 40, people_created: 9 },
    last_error_class: "auth",
  }),
};

export const Cancelled: Story = {
  render: panelStory("gmail", {
    state: "cancelled",
    counts: { captured: 20, messages_scanned: 40 },
  }),
};

// A running run whose updated_at hasn't moved in 20 minutes: the progress
// bar stops claiming motion and the panel says when it last actually moved.
export const Stale: Story = {
  render: panelStory("gmail", {
    state: "running",
    estimated_messages: 400,
    counts: { captured: 40, messages_scanned: 40 },
    updated_at: new Date(Date.now() - 20 * 60_000).toISOString(),
  }),
};

// No provider-side message count to divide by: absolute counts only, never
// a percentage over a guess.
export const NullEstimate: Story = {
  render: panelStory("gmail", {
    state: "running",
    estimated_messages: null,
    counts: { captured: 12 },
  }),
};

// IMAP has no Backfiller — the setup screen's auto-preview hits
// connector_unsupported and the panel states the capability gap plainly
// instead of rendering a window picker that can only fail again.
export const Unsupported: Story = {
  render: panelStory(
    "imap",
    { state: "none" },
    {
      "POST /connectors/imap/backfill/preview": () =>
        jsonResponse({ code: "connector_unsupported" }, 422),
    },
  ),
};

// A prior wider run already covers more history than the newly-picked
// window would — start refuses with window_narrowing, and the panel
// explains the widen-only rule rather than a generic failure.
export const Narrowing: Story = {
  render: panelStory(
    "gmail",
    { state: "none" },
    {
      "POST /connectors/gmail/backfill/preview": () =>
        jsonResponse({
          window: "3m",
          estimated_messages: 200,
          computed_at: "2026-07-23T10:00:00Z",
        }),
      "POST /connectors/gmail/backfill": () =>
        jsonResponse({ code: "window_narrowing" }, 409),
    },
  ),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /Start the import/ }),
    );
    await canvas.findByText(/only be widened/i);
  },
};
