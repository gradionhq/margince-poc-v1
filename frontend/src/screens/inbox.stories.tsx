// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { InboxScreen } from "./inbox";
import { jsonResponse, StoryProviders } from "./story-utils";

// The approvals inbox across its Task-10 states (AC-1..7). The contract has no
// status=expired filter — the server expires lazily and wires status="expired"
// back on the status=pending response — so these stubs are status-aware
// (installFetchStub keys by path only) to reproduce the real partition:
// Pending drops wire-expired rows, Decided merges approved + rejected + the
// salvaged expired ones.

type Approval = components["schemas"]["Approval"];

const base: Approval = {
  id: "ap-1",
  kind: "send_email",
  status: "pending",
  proposed_by: "agent:runner",
  summary: "Send the follow-up to Anna Weber",
  proposed_change: {
    subject: "Follow-up",
    body: "Hi Anna — shall we sync next week?",
  },
  confidence: 0.62,
  evidence: [
    { evidence_snippet: "…shall we sync next week?…", source_type: "activity" },
  ],
  target_version: 3,
  on_behalf_of: "u-99",
  created_at: "2026-07-05T05:00:00Z",
} as Approval;

// Each fixture carries its own drafted subject, because the subject is now the
// card's headline: derived rows that all inherited `base`'s made the Decided
// story three copies of one sentence — the sameness the headline change exists
// to remove, reproduced in the catalog that documents it.
//
// A pending row that expires comfortably in the future, so the live countdown
// chip renders a stable value under the story's real clock.
const pendingSoon: Approval = {
  ...base,
  id: "ap-soon",
  summary: "Awaiting your call",
  expires_at: new Date(Date.now() + 8 * 60_000).toISOString(),
} as Approval;

const expiredRow: Approval = {
  ...base,
  id: "ap-expired",
  kind: "advance_deal",
  summary: "Lapsed before anyone acted",
  proposed_change: { ...base.proposed_change, subject: "Move PIM to Proposal" },
  status: "expired",
  expires_at: "2026-07-01T00:00:00Z",
} as Approval;

const approvedRow: Approval = {
  ...base,
  id: "ap-approved",
  kind: "promote_lead",
  summary: "Committed last Tuesday",
  proposed_change: {
    ...base.proposed_change,
    subject: "Promote Kilian Wenzel to a contact",
  },
  status: "approved",
  decided_at: "2026-07-06T09:00:00Z",
} as Approval;

const rejectedRow: Approval = {
  ...base,
  id: "ap-rejected",
  kind: "send_email",
  summary: "Declined — off-brand",
  proposed_change: {
    ...base.proposed_change,
    subject: "Q3 price list — the new tiers",
  },
  status: "rejected",
  decided_at: "2026-07-06T10:00:00Z",
} as Approval;

// A held draft, whose summary the server composes out of the addressee alone —
// five staged drafts to the same counterparties read as one sentence. The
// subject the automation wrote is what tells them apart, so it leads the card
// and the summary explains it underneath.
const heldDraft: Approval = {
  ...base,
  id: "ap-held",
  kind: "held_draft",
  summary:
    "an automation drafted a reply to Anna Weber — read it before it goes",
  proposed_change: {
    anchor_activity_id: "018f3a1b-0000-7000-8000-000000000010",
    to: "anna@example.com",
    subject: "Re: kickoff — the two dates that work",
    body: "Hi Anna — here is what we agreed.",
    consent_purpose: "business_correspondence",
    intent: "recap the meeting",
  },
} as Approval;

function statusOf(url: string): string | null {
  const match = /[?&]status=([^&]+)/.exec(url);
  return match ? match[1] : null;
}

type StubConfig = {
  byStatus: Record<string, Approval[]>;
  detail?: Approval;
  post?: () => Response;
};

function isDecideUrl(url: string): boolean {
  return /\/approvals\/[^/]+\/(approve|reject)/.test(url);
}

function isDetailUrl(url: string): boolean {
  return /\/approvals\/[^/?]+(\?|$)/.test(url) && !isDecideUrl(url);
}

// Resolves one approvals request against the story's config: POST decide,
// GET by-id detail, GET status-filtered list, else an empty page (the honest
// default — never a confusing 404 error state).
function resolveApprovals(
  url: string,
  method: string,
  { byStatus, detail, post }: StubConfig,
): Response {
  if (method === "POST" && isDecideUrl(url)) {
    return post ? post() : jsonResponse({ ...base, status: "approved" });
  }
  if (isDetailUrl(url)) {
    return jsonResponse(detail ?? base);
  }
  if (/\/approvals(\?|$)/.test(url)) {
    const status = statusOf(url) ?? "pending";
    return jsonResponse({
      data: byStatus[status] ?? [],
      page: { next_cursor: null, has_more: false },
    });
  }
  return jsonResponse({
    data: [],
    page: { next_cursor: null, has_more: false },
  });
}

// Installs a status-aware approvals stub (installFetchStub keys by path only,
// so it can't branch on ?status=).
function installApprovalsStub(config: StubConfig) {
  globalThis.fetch = (async (
    input: RequestInfo | URL,
    init?: RequestInit,
  ): Promise<Response> => {
    const request = input instanceof Request ? input : null;
    const url = String(request ? request.url : input);
    const method = request?.method ?? init?.method ?? "GET";
    return resolveApprovals(url, method, config);
  }) as typeof fetch;
}

function inbox(config: Parameters<typeof installApprovalsStub>[0]) {
  return () => {
    installApprovalsStub(config);
    return (
      <StoryProviders>
        <InboxScreen />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof InboxScreen> = {
  title: "Records/Inbox",
  component: InboxScreen,
};
export default meta;

type Story = StoryObj<typeof InboxScreen>;

// AC-1 (pending): the live countdown chip + the full decision cluster.
export const Pending: Story = {
  render: inbox({ byStatus: { pending: [pendingSoon] } }),
};

// The subject-led card: the drafted subject as the headline, the server's
// summary demoted to the why line under it.
export const SubjectLedDraft: Story = {
  render: inbox({ byStatus: { pending: [heldDraft] } }),
};

// AC-1 (decided): approved + rejected + the salvaged expired row, read-only.
export const Decided: Story = {
  render: inbox({
    byStatus: {
      pending: [expiredRow],
      approved: [approvedRow],
      rejected: [rejectedRow],
    },
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Decided" }),
    );
  },
};

// AC-2: the detail modal (full proposed_change + evidence + target_version).
export const DetailModal: Story = {
  render: inbox({ byStatus: { pending: [base] }, detail: base }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Approval detail" }),
    );
  },
};

// AC-3: reject opens the reason field.
export const RejectWithReason: Story = {
  render: inbox({ byStatus: { pending: [base] } }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Reject" }),
    );
  },
};

// AC-4: a successful approve carrying an approval_token → once-shown inset.
export const TokenShown: Story = {
  render: inbox({
    byStatus: { pending: [base] },
    post: () =>
      jsonResponse({
        ...base,
        status: "approved",
        approval_token: "example-approval-token",
      }),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Accept" }),
    );
    // `screen`, not the canvas: the token is revealed in a Modal portalled to
    // document.body, so a canvas-scoped lookup rejects even though the reveal
    // is on screen — and a rejecting play() used to report after the render
    // gate had already screenshotted and passed the story.
    await screen.findByText("example-approval-token");
  },
};

// AC-5: approve 409 version_skew → honest re-stage state + re-read CTA.
export const VersionSkew: Story = {
  render: inbox({
    byStatus: { pending: [base] },
    post: () =>
      jsonResponse(
        {
          title: "Conflict",
          detail: "if-match version 3 does not match current 4",
          code: "version_skew",
        },
        409,
      ),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Accept" }),
    );
    await canvas.findByRole("button", { name: "Re-read" });
  },
};

// AC-6: approve 409 already_decided → the stale-row note.
export const AlreadyDecided: Story = {
  render: inbox({
    byStatus: { pending: [base] },
    post: () =>
      jsonResponse({ title: "Conflict", code: "already_decided" }, 409),
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("button", { name: "Accept" }),
    );
    await canvas.findByText("Already decided — nothing left to do here.");
  },
};

// AC-7: the live expiry countdown chip (fixed future expires_at).
export const LiveCountdown: Story = {
  render: inbox({ byStatus: { pending: [pendingSoon] } }),
};
