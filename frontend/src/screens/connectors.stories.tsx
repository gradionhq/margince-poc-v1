// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { ConnectorsCard } from "./connectors";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ConnectorsCard stories for the fe-uat render gate: a healthy connection, a
// reauth-needed one (the reconnect affordance), a sync-error one, the empty
// state, and a load failure — all off the same GET /connectors shape the
// unit tests (connectors.test.tsx) already exercise.

type CaptureConnection = components["schemas"]["CaptureConnection"];

const gmailConnected: CaptureConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000c1",
  provider: "gmail",
  status: "connected",
  scopes: ["read"],
  last_synced_at: "2026-07-23T09:30:00Z",
};

const gcalReauth: CaptureConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000c2",
  provider: "gcal",
  status: "reauth_required",
  scopes: ["read"],
  last_synced_at: "2026-07-20T08:00:00Z",
};

const imapError: CaptureConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000c3",
  provider: "imap",
  status: "error",
  scopes: [],
  last_synced_at: "2026-07-18T12:00:00Z",
  last_sync_error_class: "unreachable",
};

function cardStory(connections: CaptureConnection[]) {
  return () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: connections }),
    });
    return (
      <StoryProviders>
        <ConnectorsCard />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof ConnectorsCard> = {
  title: "screens/connectors",
  component: ConnectorsCard,
};
export default meta;
type Story = StoryObj<typeof ConnectorsCard>;

export const Connected: Story = {
  render: cardStory([gmailConnected]),
};

export const NeedsReconnect: Story = {
  render: cardStory([gcalReauth]),
};

export const SyncError: Story = {
  render: cardStory([imapError]),
};

export const MixedRows: Story = {
  render: cardStory([gmailConnected, gcalReauth, imapError]),
};

export const Empty: Story = {
  render: cardStory([]),
};

export const LoadFailed: Story = {
  render: () => {
    installFetchStub({
      "GET /connectors": () =>
        jsonResponse({ title: "Internal Server Error", detail: "boom" }, 500),
    });
    return (
      <StoryProviders>
        <ConnectorsCard />
      </StoryProviders>
    );
  },
};
