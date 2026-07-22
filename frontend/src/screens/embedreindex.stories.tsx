// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { EmbedReindexCard } from "./embedreindex";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

const STATUS_NEEDED = {
  configured_identity: "anthropic/voyage-3@1024",
  populated_identity: "anthropic/voyage-2@1024",
  status: "idle",
  updated_at: "2026-07-22T00:00:00Z",
  reindex_needed: true,
  entities_pending: 128,
  per_workspace: [
    {
      workspace_id: "018f3a1b-0000-7000-8000-000000000001",
      entities_pending: 128,
    },
  ],
};

const STATUS_IDLE = {
  ...STATUS_NEEDED,
  populated_identity: "anthropic/voyage-3@1024",
  reindex_needed: false,
  entities_pending: 0,
};

const PREVIEW = {
  entities_pending: 128,
  estimated_ai_tokens: 34_500,
  estimated_cost_minor: 980,
  estimate_quality: "heuristic",
  currency: "USD",
  computed_at: "2026-07-22T00:00:00Z",
  per_workspace: [
    {
      workspace_id: "018f3a1b-0000-7000-8000-000000000001",
      entities_pending: 128,
      estimated_ai_tokens: 34_500,
      utilization_impact: "degraded",
    },
  ],
};

function admin(overrides: Record<string, unknown> = {}) {
  return () =>
    jsonResponse({
      user: { id: "u1", email: "admin@example.test", display_name: "Admin" },
      roles: ["admin"],
      ...overrides,
    });
}

const meta: Meta<typeof EmbedReindexCard> = {
  title: "screens/embed-reindex-card",
  component: EmbedReindexCard,
};
export default meta;
type Story = StoryObj<typeof EmbedReindexCard>;

// The ops banner's companion card: reindex_needed is true, an admin sees the
// "Review & reindex" trigger alongside the always-available "Rebuild index".
export const NeedsReindex: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /embeddings/reindex/status": () => jsonResponse(STATUS_NEEDED),
    });
    return (
      <StoryProviders>
        <EmbedReindexCard />
      </StoryProviders>
    );
  },
};

// The v6 B2 rebuild affordance stays available even when nothing is pending —
// only "Rebuild index" renders, never "Review & reindex".
export const UpToDateRebuildAvailable: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /embeddings/reindex/status": () => jsonResponse(STATUS_IDLE),
    });
    return (
      <StoryProviders>
        <EmbedReindexCard />
      </StoryProviders>
    );
  },
};

// The preview→confirm dialog's consent surface: tokens/cost/quality plus the
// per-workspace utilization-impact disclosure, captured after the estimate
// loads (confirm starts disabled until then).
export const PreviewDialogWithEstimate: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /embeddings/reindex/status": () => jsonResponse(STATUS_NEEDED),
      "GET /embeddings/reindex/preview": () => jsonResponse(PREVIEW),
    });
    return (
      <StoryProviders>
        <EmbedReindexCard />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const reviewButton = await canvas.findByRole("button", {
      name: "Review & reindex",
    });
    await userEvent.click(reviewButton);
    await canvas.findByText(/34,500/);
  },
};

// The status read is admin/ops-only server-side now (migration 0114): a rep
// holds no grant on embedding_reindex at all, so the card renders nothing —
// same predicate EmbedReindexBanner's own HiddenForNonOpsRole story gates on.
export const HiddenForNonOpsRole: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse({
          user: { id: "u2", email: "rep@example.test", display_name: "Rep" },
          roles: ["rep"],
        }),
      "GET /embeddings/reindex/status": () => jsonResponse(STATUS_NEEDED),
    });
    return (
      <StoryProviders>
        <EmbedReindexCard />
      </StoryProviders>
    );
  },
};
