// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { CompanyApprovalsPanel, DecisionsChip } from "./companyapprovals";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// What is waiting on a decision FOR THIS ACCOUNT. `DecisionsChip` is absent —
// not empty — when nothing is waiting, which the Empty story below pins
// deliberately: a demo account with a clean queue never shows it, so this
// is the only place a reader sees the chip stay gone. The panel groups
// same-kind proposals (a deep read stages one per person it found), which the
// Populated story pins with two `send_email` rows under one heading.

const meta: Meta = {
  title: "Screens/Company approvals",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Approval = components["schemas"]["Approval"];
type View = components["schemas"]["Organization360"];

const page = { has_more: false, next_cursor: null };

const approvals: Approval[] = [
  {
    id: "ap-1",
    kind: "send_email",
    status: "pending",
    proposed_by: "agent:runner",
    summary: "Send the follow-up to Anna Weber",
    target_version: 3,
    on_behalf_of: "u-1",
    created_at: "2026-08-05T09:00:00Z",
  } as unknown as Approval,
  {
    id: "ap-2",
    kind: "send_email",
    status: "pending",
    proposed_by: "agent:runner",
    summary: "Send the intro to Jonas Weiss",
    target_version: 3,
    on_behalf_of: "u-1",
    created_at: "2026-08-05T09:05:00Z",
  } as unknown as Approval,
  {
    id: "ap-3",
    kind: "advance_deal",
    status: "pending",
    proposed_by: "agent:runner",
    summary: "Move Depot expansion to Negotiation",
    target_version: 1,
    on_behalf_of: "u-1",
    created_at: "2026-08-05T09:10:00Z",
  } as unknown as Approval,
];

function stubApprovals(data: Approval[]) {
  installFetchStub({
    "GET /me": () =>
      jsonResponse({ user: { id: "u-1", display_name: "Mira Voss" } }),
    "GET /approvals": () => jsonResponse({ data, page }),
  });
}

function Panel({ approvals: data }: Readonly<{ approvals: Approval[] }>) {
  stubApprovals(data);
  return (
    <StoryProviders>
      <CompanyApprovalsPanel orgId="o-1" onClose={() => {}} />
    </StoryProviders>
  );
}

export const Populated: Story = {
  render: () => <Panel approvals={approvals} />,
};

export const Empty: Story = { render: () => <Panel approvals={[]} /> };

function Chip({ view }: Readonly<{ view?: View }>) {
  stubApprovals(approvals);
  return (
    <StoryProviders>
      <DecisionsChip view={view} onOpen={() => {}} />
    </StoryProviders>
  );
}

export const ChipOpen: Story = {
  render: () => (
    <Chip
      view={
        {
          pending_approvals: { data: approvals, page },
        } as unknown as View
      }
    />
  ),
};

// The chip is ABSENT — not empty — with nothing waiting, so the story labels
// the state itself rather than leaving an unexplained blank canvas.
export const ChipAbsent: Story = {
  render: () => (
    <div>
      <p className="t-caption">
        Nothing waiting on this account — the chip renders nothing.
      </p>
      <Chip
        view={{ pending_approvals: { data: [], page } } as unknown as View}
      />
    </div>
  ),
};
