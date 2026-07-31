// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { AssistantPanel } from "./assistant";
import { DealsCard, NextSteps, PeopleCard, TagsCard } from "./company360";
import { MeetingBrief } from "./meetingbrief";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The company view's cards, rendered straight from a payload rather than
// through the screen — so the three answers a card can give are visible side
// by side: here it is, there is none, and your role cannot read this.
// A fixed instant, so the brief renders the same lines in every snapshot
// rather than drifting as the story ages.
const STORY_NOW = new Date("2026-07-31T09:00:00Z");

const meta: Meta = {
  title: "Screens/Company 360 cards",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type View = components["schemas"]["Organization360"];

const page = { has_more: false, next_cursor: null };

const populated = {
  as_of: "2026-07-13T09:00:00Z",
  organization: {
    id: "o-1",
    workspace_id: "w-1",
    display_name: "Brandt Automotive GmbH",
    captured_by: "human:u1",
    source: "manual",
    created_at: "2026-06-01T08:00:00Z",
    updated_at: "2026-06-01T08:00:00Z",
  },
  sections_omitted: [],
  suggestions_dropped: 0,
  // Two suggestions from two different rules, so the card shows what makes it
  // useful: each row's own reason, and its own evidence.
  suggestions: [
    {
      kind: "stalled_deal",
      reason:
        '"Fleet retrofit 2026" has had no activity long enough to count as stalled.',
      fingerprint: "fp-1",
      subject_type: "deal",
      subject_id: "d-1",
      evidence: [{ entity_type: "deal", entity_id: "d-1" }],
    },
    {
      kind: "no_reply",
      reason: "You reached out 11 days ago and nobody has come back.",
      fingerprint: "fp-2",
      evidence: [{ entity_type: "activity", entity_id: "a-1" }],
    },
  ],
  people: {
    data: [
      {
        person_id: "p-1",
        full_name: "Dana Buyer",
        title: "Head of Fleet",
        primary_email: "dana@brandt.example",
        deal_roles: [{ deal_id: "d-1", role: "champion" }],
        consent: { marketing_email: "granted" },
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
      {
        person_id: "p-2",
        full_name: "Kim Ops",
        title: "Operations",
        deal_roles: [],
        consent: { marketing_email: "unknown" },
        strength: {
          score: 18,
          bucket: "weak",
          factors: {
            recency: 0.3,
            frequency: 0.1,
            reciprocity: 0.5,
            direction: 0.4,
          },
        },
      },
    ],
    page,
  },
  deals: {
    data: [
      {
        deal_id: "d-1",
        name: "Fleet retrofit 2026",
        status: "open",
        stage_name: "Proposal",
        amount: { amount_minor: 4_800_000, currency: "EUR" },
        stalled: false,
      },
      {
        deal_id: "d-2",
        name: "Depot pilot",
        status: "open",
        stage_name: "Discovery",
        amount: { amount_minor: 900_000, currency: "EUR" },
        stalled: true,
      },
    ],
    page,
    won_lifetime: { amount_minor: 12_000_000, currency: "EUR" },
    lost_count: 1,
  },
  activities: { data: [], page },
  next_steps: {
    data: [
      {
        activity_id: "a-1",
        subject: "Send the renewal paperwork",
        due_at: "2026-07-01T09:00:00Z",
        overdue: true,
        linked_deal_id: null,
        linked_person_id: null,
        assignee_id: null,
      },
      {
        activity_id: "a-2",
        subject: "Confirm the depot walkthrough date",
        due_at: "2026-08-04T09:00:00Z",
        overdue: false,
        linked_deal_id: null,
        linked_person_id: null,
        assignee_id: null,
      },
    ],
    page,
  },
  pending_approvals: { data: [], page },
  tags: [{ id: "t-1", workspace_id: "w-1", name: "Key account" }],
  list_memberships: [
    {
      id: "l-1",
      workspace_id: "w-1",
      name: "Q3 renewals",
      entity_type: "organization",
      list_type: "static",
    },
  ],
  since_last_visit: {
    baseline_at: "2026-07-10T09:00:00Z",
    new_activities: 2,
    deal_stage_moves: 1,
    pending_proposals: 0,
  },
} as unknown as View;

// The same account read by someone whose role cannot see deals: the card
// says so rather than reading as an account with no pipeline.
const withheld = {
  ...populated,
  deals: undefined,
  sections_omitted: ["deals"],
} as unknown as View;

// An account nobody has worked yet — every card in its own empty state.
const empty = {
  ...populated,
  people: { data: [], page },
  deals: {
    data: [],
    page,
    won_lifetime: { amount_minor: 0, currency: "EUR" },
    lost_count: 0,
  },
  next_steps: { data: [], page },
  // Nothing to advise on a dormant account: the card renders nothing at all,
  // which is the state this story exists to show.
  suggestions: [],
  tags: [],
  list_memberships: [],
  since_last_visit: {
    baseline_at: null,
    new_activities: 0,
    deal_stage_moves: 0,
    pending_proposals: 0,
  },
} as unknown as View;

function Cards({ view }: Readonly<{ view: View }>) {
  installFetchStub({
    "GET /signals": () => jsonResponse({ data: [], page }),
    // The prepared questions answer from the account; the story serves the
    // deterministic floor, which is what a deployment with no model lane shows.
    "POST /organizations/o-1/ask": () =>
      jsonResponse({
        organization_id: "o-1",
        question: "whats_open",
        generated_at: "2026-07-13T09:00:00Z",
        generated_by: "deterministic",
        // The answer follows the account the story renders: an account with no
        // deals must not answer with one, or the empty-state story shows a
        // populated card.
        sentences: view.deals?.data?.length
          ? [
              {
                text: "2 open deal(s) worth about 57000 EUR.",
                evidence: [{ entity_type: "deal", entity_id: "d-1" }],
              },
            ]
          : [],
      }),
  });
  return (
    <StoryProviders>
      <div style={{ display: "grid", gap: "var(--space-3)", maxWidth: 380 }}>
        <MeetingBrief view={view} now={STORY_NOW} />
        <AssistantPanel orgId="o-1" view={view} enabled />
        <NextSteps view={view} />
        <PeopleCard view={view} />
        <DealsCard view={view} />
        <TagsCard view={view} />
      </div>
    </StoryProviders>
  );
}

export const Populated: Story = { render: () => <Cards view={populated} /> };

export const SectionWithheld: Story = {
  render: () => <Cards view={withheld} />,
};

export const NothingYet: Story = { render: () => <Cards view={empty} /> };
