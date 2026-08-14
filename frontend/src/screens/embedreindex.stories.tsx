// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { meFixture } from "../app/mefixture";
import { EmbedReindexCard } from "./embedreindex";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

const STATUS_NEEDED = {
  configured_identity: "anthropic/voyage-3@1024",
  populated_identity: "anthropic/voyage-2@1024",
  status: "idle",
  updated_at: "2026-07-22T00:00:00Z",
  reindex_needed: true,
  entities_pending: 128,
};

const STATUS_IDLE = {
  ...STATUS_NEEDED,
  populated_identity: "anthropic/voyage-3@1024",
  reindex_needed: false,
  entities_pending: 0,
};

// utilization_impact is a top-level field of EmbedReindexPreview — the band the
// INSTALLATION would land in (A107/ADR-0061: one installation, one
// organization). It sat under a `per_workspace` array the contract has no such
// property for, so the card read `preview.utilization_impact`, found nothing,
// and the impact badge this story exists to show never rendered.
const PREVIEW = {
  entities_pending: 128,
  estimated_ai_tokens: 34_500,
  estimated_cost_minor: 980,
  estimate_quality: "heuristic",
  currency: "USD",
  computed_at: "2026-07-22T00:00:00Z",
  utilization_impact: "degraded",
};

function admin(overrides: Record<string, unknown> = {}) {
  return () =>
    jsonResponse({
      ...meFixture({ allow: { embedding_reindex: ["read", "update"] } }),
      ...overrides,
    });
}

const meta: Meta<typeof EmbedReindexCard> = {
  title: "Settings/Organization/Maintenance/Embedding reindex",
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
// utilization-impact disclosure — the budget band the installation would land
// in were this spend added — captured after the estimate loads (confirm starts
// disabled until then).
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
    // The badge is the half of the disclosure a story cannot assert by
    // eyeballing a number: it renders only when the top-level
    // utilization_impact is present, so naming it here keeps the fixture
    // honest to the contract.
    await canvas.findByText("would enter economy mode");
  },
};

// The status read is admin/ops-only server-side (migration 0115): a rep holds no
// grant on embedding_reindex at all, so the card keeps its place and says the
// status is withheld rather than disappearing off a page the rep reaches for its
// other sections. The BANNER gates on the same predicate and is genuinely absent
// there, which is not an inconsistency: a banner is an advisory, and there is no
// advice to give somebody who may not act on it.
export const WithheldForNonOpsRole: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse({
          ...meFixture({ roles: ["rep"] }),
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
