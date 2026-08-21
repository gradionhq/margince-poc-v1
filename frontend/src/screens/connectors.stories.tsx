// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { ConnectorsCard } from "./connectors";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// ConnectorsCard stories for the fe-uat render gate: a healthy connection, a
// reauth-needed one (the reconnect affordance), a sync-error one, the empty
// state, and a load failure — all off the same GET /connectors shape the
// unit tests (connectors.test.tsx) already exercise.
//
// Every story renders TWO panels now — mail capture and the workspace's
// Telegram bot — because the component draws both, and both are built from the
// same SettingRow: identity and health on the left, state and verbs on the
// right, at one x. What to check in any of these pictures is that the right
// column really does line up down the whole pair, including across the
// history-import block that rides under a connected mailbox's row.
//
// Both panels also put their description in the BODY rather than in `Panel`'s
// `sub`, which is what keeps the two header bands the same height: a sentence in
// the band raises it, and one card taller than its neighbour is a page that has
// lost its beat.

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

// No mailbox at all. The sentence that says so is a ROW of the card's list now
// — a stacked row, so `.empty` gives up its 90px page-furniture slab and reads
// left-aligned at a row's own interval — and the verb that fills it sits in the
// header rather than in the column a reader travels to audit the roster.
export const Empty: Story = {
  render: cardStory([]),
};

/** Open the card's header verb, so the picture is the dialog behind it. */
async function openAddDialog(canvasElement: HTMLElement) {
  const body = within(canvasElement.ownerDocument.body);
  await userEvent.click(
    await body.findByRole("button", { name: "Connect an account" }),
  );
  await body.findByRole("dialog", { name: "Add a connection" });
}

// The "Add a connection" affordance (Task 1): ONE verb in the card's header,
// and the picks in the dialog it opens. What to check is that four providers
// each carrying a sentence read as four rows of one list — the shape they
// replaced was a strip of four buttons squeezed against a wrapping description,
// with Gmail drawn primary on a card that exists to report a roster rather than
// to push one mailbox.
export const AddConnectionDialogAllProviders: Story = {
  render: cardStory([]),
  play: async ({ canvasElement }) => {
    await openAddDialog(canvasElement);
  },
};

// The same dialog once Gmail is on the roster: three picks, not four. And in
// dark, where the muted description under each provider name is the ink most
// likely to collapse into the dialog's own ground under the accent lift.
export const AddConnectionDialogDark: Story = {
  globals: { theme: "dark" },
  render: cardStory([gmailConnected]),
  play: async ({ canvasElement }) => {
    await openAddDialog(canvasElement);
  },
};

// One mailbox connected, dialog closed: the header carries the verb and the
// body carries only the roster.
export const OneConnectedWithHeaderVerb: Story = {
  render: cardStory([gmailConnected]),
};

// A refused connect reports itself where the press happened, not in a band of
// its own further down the card. One `connect` mutation serves two surfaces, so
// both placements are worth seeing: the add dialog's provider picks, and a
// roster row's Reconnect.
function connectFailureStory(connections: CaptureConnection[], path: string) {
  return () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: connections }),
      [`POST /connectors/${path}/connect`]: () =>
        jsonResponse(
          { title: "Bad Gateway", detail: "The provider did not answer." },
          502,
        ),
    });
    return (
      <StoryProviders>
        <ConnectorsCard />
      </StoryProviders>
    );
  };
}

export const AddConnectionFailed: Story = {
  render: connectFailureStory([gmailConnected], "graph"),
  play: async ({ canvasElement }) => {
    await openAddDialog(canvasElement);
    const body = within(canvasElement.ownerDocument.body);
    await userEvent.click(
      await body.findByRole("button", { name: "Connect Microsoft" }),
    );
    await body.findByRole("alert");
  },
};

export const ReconnectFailed: Story = {
  render: connectFailureStory([gcalReauth], "gcal"),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: /Reconnect/ }),
    );
    await canvas.findByRole("alert");
  },
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

// The Telegram panel is its own card now, and it has three states of its own
// that the mail roster's stories never reach: no bot yet (one row offering the
// connect), live bots (a row each), and a deployment with no credential store
// to seal a token in (503, a calm feature-off state and not an error).
function telegramStory(channels: unknown[] | null) {
  return () => {
    installFetchStub({
      "GET /connectors": () => jsonResponse({ data: [gmailConnected] }),
      "GET /channel-connections": () =>
        channels === null
          ? jsonResponse({ code: "channel_credentials_not_configured" }, 503)
          : jsonResponse({ data: channels }),
    });
    return (
      <StoryProviders>
        <ConnectorsCard />
      </StoryProviders>
    );
  };
}

const salesBot = {
  id: "018f3a1b-0000-7000-8000-0000000000d1",
  provider: "telegram",
  channelId: "555000111",
  channelLabel: "acme_sales_bot",
  status: "connected",
  version: 1,
};

export const TelegramNoBotYet: Story = { render: telegramStory([]) };

// Two live bots is the state a send refuses outright in, and this panel is the
// only surface that can end it — so both have to be here, each with its own
// Disconnect.
export const TelegramTwoBots: Story = {
  render: telegramStory([
    salesBot,
    {
      ...salesBot,
      id: "018f3a1b-0000-7000-8000-0000000000d2",
      channelId: "555000222",
      channelLabel: "acme_support_bot",
      status: "pending",
    },
  ]),
};

export const TelegramNotConfigured: Story = { render: telegramStory(null) };

// Both panels' rows in dark. The row language puts the answer in the right
// column against the panel's own ground, and `--textMuted` (the description)
// against `--textSecondary` (the account label) is the pair most likely to
// collapse into one grey under the dark accent lift — a mailbox address that
// reads as help text is the failure to look for.
export const BothPanelsDark: Story = {
  globals: { theme: "dark" },
  render: telegramStory([salesBot]),
};
