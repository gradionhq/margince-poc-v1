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
  account_label: "lars@example.de",
  last_synced_at: "2026-07-23T09:30:00Z",
  next_sync_due_at: "2026-07-23T09:35:00Z",
  watch_expires_at: "2026-08-01T00:00:00Z",
  // Seeds the mounted BackfillPanel so it renders the finished state with no
  // extra request against a route this story never stubs.
  backfill: {
    state: "done",
    counts: { captured: 842, people_created: 96, organizations_created: 21 },
  },
};

const gcalReauth: CaptureConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000c2",
  provider: "gcal",
  status: "reauth_required",
  scopes: ["read"],
  account_label: "lars@example.de",
  last_synced_at: "2026-07-20T08:00:00Z",
  last_sync_error_class: "auth",
};

const imapError: CaptureConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000c3",
  provider: "imap",
  status: "error",
  scopes: [],
  last_synced_at: "2026-07-18T12:00:00Z",
  last_sync_error_class: "unreachable",
};

// IMAP is poll-only — there is no push subscription to renew, so
// watch_expires_at is always null for this provider. The card must read
// that null as "polled", never as an expired push renewal.
const imapPolled: CaptureConnection = {
  id: "018f3a1b-0000-7000-8000-0000000000c4",
  provider: "imap",
  status: "connected",
  scopes: [],
  account_label: "sales@example.org",
  last_synced_at: "2026-07-23T09:00:00Z",
  next_sync_due_at: "2026-07-23T09:15:00Z",
  watch_expires_at: null,
  // IMAP has no Backfiller (connector_unsupported) — the panel's own
  // capability statement, seeded straight from "none" with no run ever
  // possible, needs no preview stub here since IMAP never reaches preview
  // successfully in the first place.
  backfill: { state: "none" },
};

function cardStory(connections: CaptureConnection[]) {
  return () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: connections }),
      // IMAP has no Backfiller — the mounted BackfillPanel's setup screen
      // auto-loads this preview and must render the capability statement
      // rather than crash on the default empty-list fallback shape.
      "POST /connectors/imap/backfill/preview": () =>
        jsonResponse({ code: "connector_unsupported" }, 422),
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
  render: cardStory([gmailConnected, gcalReauth, imapError, imapPolled]),
};

export const ImapPolled: Story = {
  render: cardStory([imapPolled]),
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

// A deployment that never wired mail capture answers 501 code:not_implemented
// (httperr.NotImplemented) — a calm, documented feature-off state, never an
// error card.
export const NotConfigured: Story = {
  render: () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ code: "not_implemented" }, 501),
    });
    return (
      <StoryProviders>
        <ConnectorsCard />
      </StoryProviders>
    );
  },
};
