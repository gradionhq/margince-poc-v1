// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { TodayOnThisAccount } from "./companytoday";
import { StoryProviders } from "./story-utils";

// The daily brief, above the tab bar: a context band read off the account's
// dated readings, then the moves — a booked meeting, a manual draft, and
// whatever the account's own suggestions advise.
//
// The withheld story is the one no seeded demo account can reach: every one
// grants the viewer full RBAC, so `state_strip` (whose move, the open risk)
// and `people` (the best route in) are never omitted on a live session. A
// role scoped away from either still reads the rest of the brief; it just
// says so for the two it cannot answer, rather than silently dropping them.

const meta: Meta = {
  title: "Screens/Today on this account",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type View = components["schemas"]["Organization360"];

const page = { has_more: false, next_cursor: null };

const org = {
  id: "o-1",
  workspace_id: "w-1",
  display_name: "Brandt Automotive GmbH",
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
};

const populated = {
  as_of: "2026-07-13T09:00:00Z",
  organization: org,
  sections_omitted: [],
  state_strip: {
    account: { lifecycle: "customer", relationship_types: ["customer"] },
    engagement: {
      state: "waiting_on_us",
      last_inbound_at: "2026-07-11T09:00:00Z",
      last_outbound_at: null,
    },
    signal: {
      kind: "stalled_deal",
      severity: "warn",
      headline: "Depot pilot has had no activity in 18 days.",
    },
  },
  people: {
    data: [
      {
        person_id: "p-1",
        full_name: "Dana Buyer",
        title: "Head of Fleet",
        deal_roles: [],
        consent: { marketing_email: "granted" },
        routes: {
          top: [
            {
              person_id: "u-1",
              display_name: "Mira Voss",
              strength_bucket: "strong",
            },
          ],
          remainder: 0,
        },
        strength: {
          score: 71,
          bucket: "strong",
          factors: {
            recency: 0.9,
            frequency: 0.6,
            reciprocity: 0.8,
            direction: 0.8,
          },
        },
      },
    ],
    page,
  },
  next_meeting: {
    activity_id: "a-1",
    starts_at: "2026-07-14T09:00:00Z",
    subject: "Renewal review",
    participants: [{ person_id: "p-1", display_name: "Dana Buyer" }],
  },
  suggestions: [
    {
      kind: "no_reply",
      fingerprint: "f-1",
      reason: "You reached out 11 days ago and nobody has come back.",
      evidence: [{ entity_type: "activity", entity_id: "a-2" }],
    },
  ],
  next_steps: {
    data: [
      {
        activity_id: "a-3",
        subject: "Send the renewal paperwork",
        due_at: "2026-07-01T09:00:00Z",
        overdue: true,
      },
    ],
    page,
  },
} as unknown as View;

// state_strip and people withheld — the two readings no seeded demo account
// ever omits, so this is the only place the brief's own withheld path for
// either one renders.
const withheld = {
  ...populated,
  state_strip: undefined,
  people: undefined,
  next_meeting: undefined,
  sections_omitted: ["state_strip", "people", "next_meeting"],
} as unknown as View;

function Brief({
  view,
  loading = false,
  failed = false,
}: Readonly<{ view?: View; loading?: boolean; failed?: boolean }>) {
  return (
    <StoryProviders>
      <div style={{ maxWidth: 720 }}>
        <TodayOnThisAccount
          orgId="o-1"
          view={view}
          loading={loading}
          failed={failed}
        />
      </div>
    </StoryProviders>
  );
}

export const Populated: Story = { render: () => <Brief view={populated} /> };

export const SectionWithheld: Story = {
  render: () => <Brief view={withheld} />,
};

export const Loading: Story = { render: () => <Brief loading /> };

export const Failed: Story = { render: () => <Brief failed /> };
