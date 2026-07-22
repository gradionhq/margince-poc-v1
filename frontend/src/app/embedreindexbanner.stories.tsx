// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import {
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "../screens/story-utils";
import { EmbedReindexBanner } from "./embedreindexbanner";

function story(reindexNeeded: boolean) {
  return () => {
    installFetchStub({
      "GET /embeddings/reindex/status": () =>
        jsonResponse({
          configured_identity: "anthropic/voyage-3@1024",
          populated_identity: reindexNeeded
            ? "anthropic/voyage-2@1024"
            : "anthropic/voyage-3@1024",
          status: "idle",
          reindex_needed: reindexNeeded,
          entities_pending: reindexNeeded ? 128 : 0,
          per_workspace: [],
        }),
    });
    return (
      <StoryProviders>
        <EmbedReindexBanner />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof EmbedReindexBanner> = {
  title: "app/embed-reindex-banner",
  component: EmbedReindexBanner,
};
export default meta;
type Story = StoryObj<typeof EmbedReindexBanner>;

export const Needed: Story = { render: story(true) };
export const UpToDate: Story = { render: story(false) };
