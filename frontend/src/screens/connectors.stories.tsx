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
  scopes: [
    "https://www.googleapis.com/auth/gmail.readonly",
    "https://www.googleapis.com/auth/gmail.send",
  ],
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

// The mailbox connected before Margince asked for the send scope: capturing
// happily, and permanently unable to send until it is reconnected.
const gmailNoSendGrant: CaptureConnection = {
  ...gmailConnected,
  scopes: ["https://www.googleapis.com/auth/gmail.readonly"],
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
  title: "Settings/You/Connections/Connectors",
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

export const CannotSend: Story = {
  render: cardStory([gmailNoSendGrant]),
};

export const SyncError: Story = {
  render: cardStory([imapError]),
};

export const MixedRows: Story = {
  render: cardStory([gmailConnected, gcalReauth, imapError, imapPolled]),
};

// All four statuses in dark, side by side. `statusTone` is the only thing that
// separates "connected", "needs reconnecting" and "erroring" at a glance, and the
// roster is where a reader compares them — three tones that hold apart in light
// and collapse together in dark would read as four healthy mailboxes. The
// finished BackfillPanel rides along inside the healthy row with its own
// success-tinted plate.
export const MixedRowsDark: Story = {
  globals: { theme: "dark" },
  render: cardStory([gmailConnected, gcalReauth, imapError, imapPolled]),
};

// The roster at 390px. A row is an identity column (provider, mailbox address,
// three timestamp lines) beside up to three controls — a status badge, Reconnect
// and Disconnect — and the reauth row is the one that has to fit all of them.
// The healthy row also carries a whole nested panel (the finished backfill hero
// with its three-stat grid), so this is a card inside a row inside a card at
// phone width.
export const MixedRowsPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: cardStory([gmailConnected, gcalReauth, imapError, imapPolled]),
};

export const ImapPolled: Story = {
  render: cardStory([imapPolled]),
};

export const Empty: Story = {
  render: cardStory([]),
};

// The "Add a connection" affordance (Task 1): the empty state offers all
// four providers, and once one is connected the roster grows a footer
// offering only the ones still missing.
export const EmptyStateAllProviders: Story = {
  render: () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [] }),
    });
    return (
      <StoryProviders>
        <ConnectorsCard />
      </StoryProviders>
    );
  },
};

export const OneConnectedWithFooter: Story = {
  render: cardStory([gmailConnected]),
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

// The OAuth return outcome (Task 2): the backend lands the callback on
// #/settings/connections/{outcome}; the card reads id2 off the route and
// renders a dismissible inline note. Each story sets the hash before
// mounting, exactly like installFetchStub is wired before mount.
function outcomeStory(outcome: string, connections: CaptureConnection[]) {
  return () => {
    globalThis.location.hash = `#/settings/connections/${outcome}`;
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

export const OAuthDenied: Story = {
  render: outcomeStory("denied", []),
};

export const OAuthError: Story = {
  render: outcomeStory("error", []),
};

export const OAuthOk: Story = {
  render: outcomeStory("ok", [gmailConnected]),
};
