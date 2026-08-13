// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import {
  PersonBriefCard,
  PersonCommercialCard,
  PersonCommitmentsCard,
  PersonMattersCard,
} from "./personcards";
import { PersonMemory } from "./personmemory";
import { PersonPageV2 } from "./personpage";
import { PersonRail } from "./personrail";
import { PersonStrip } from "./personstrip";
import { PersonToday } from "./persontoday";
import "./person360.css";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The person record page V2 (ADR-0096) — its own gallery, one per surface the
// concept names: the whole page behind the three reads it makes, the readings
// strip on its own (both with and without a grant), the lead moment in both
// tints, the rail, and the overview stack of cards.
//
// This gallery is what the live stack CANNOT show: every seeded demo contact
// carries full RBAC and a clean sections_omitted, so a reader never sees a
// withheld reading rendered — that state exists only here.

const meta: Meta = {
  title: "Screens/Person record",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type View = components["schemas"]["Person360"];

const page = { has_more: false, next_cursor: null };

// The lead moment on the meeting-prep rung: a booked meeting close enough to
// need a brief. Typed on its own so the story rendering PersonToday directly
// never has to narrow an optional field back out of the 360.
const meetingPrepMoment = {
  claim_key: "meeting_prep:p-1:a-2",
  evidence_fingerprint: "fp-meeting-1",
  rule: "meeting_prep",
  rule_version: "v1",
  headline: "Dana's retrofit walkthrough is in 7 days.",
  why_now: "A booked meeting inside two weeks with no brief prepared yet.",
  confidence: "observed_fact",
  freshness_at: "2026-08-13T09:00:00Z",
  evidence: [
    {
      type: "activity",
      id: "a-2",
      label: "Fleet retrofit walkthrough, 20 Aug",
      observed_at: "2026-08-20T13:00:00Z",
    },
  ],
  recommended_action: {
    kind: "open_meeting_brief",
    label: "Open meeting brief",
    destination: { surface: "meeting_brief" },
    state: "available",
  },
  secondary_actions: [
    {
      kind: "draft_reply",
      label: "Confirm the time",
      state: "available",
    },
  ],
} as unknown as components["schemas"]["PersonMoment"];

// One contact at an organization: one unanswered inbound thread, a meeting
// accepted, no open deal, one colleague who knows them, email consent
// allowed. The demo-seed spirit — a record with enough on it to fill every
// card, and nothing invented past what the fixture states.
const populated = {
  as_of: "2026-08-13T09:00:00Z",
  person: {
    id: "p-1",
    full_name: "Dana Buyer",
    first_name: "Dana",
    last_name: "Buyer",
    title: "Head of Fleet",
    owner_id: "u-1",
    social: { linkedin: "https://linkedin.com/in/danabuyer" },
    address: { city: "Munich", country: "DE" },
    emails: [
      {
        id: "pe-1",
        person_id: "p-1",
        email: "dana@brandt.example",
        email_type: "work",
        is_primary: true,
        position: 0,
        source: "manual",
        captured_by: "human:u1",
      },
    ],
    phones: [
      {
        id: "pp-1",
        person_id: "p-1",
        phone: "+493012345678",
        phone_type: "work",
        is_primary: true,
        position: 0,
        source: "manual",
        captured_by: "human:u1",
      },
    ],
    source: "manual",
    captured_by: "human:u1",
    created_at: "2026-06-01T08:00:00Z",
    updated_at: "2026-08-01T08:00:00Z",
  },
  last_inbound_at: "2026-08-01T10:15:00Z",
  last_outbound_at: "2026-07-20T09:00:00Z",
  sections_omitted: [],
  network: {
    colleagues: [
      {
        user_id: "u-2",
        display_name: "Sam Rivera",
        strength: 0.7,
        strength_bucket: "strong",
        interactions_90d: 6,
        last_at: "2026-07-30T09:00:00Z",
        inbound_90d: 3,
        outbound_90d: 3,
        last_inbound_at: "2026-07-30T09:00:00Z",
        last_outbound_at: "2026-07-29T09:00:00Z",
      },
    ],
  },
  employments: {
    data: [
      {
        relationship_id: "rel-1",
        organization_id: "o-1",
        organization_name: "Brandt Automotive GmbH",
        role: "Head of Fleet",
        is_current_primary: true,
        started_at: "2022-03-01T00:00:00Z",
        ended_at: null,
      },
    ],
    page,
  },
  activities: {
    data: [
      {
        id: "a-1",
        kind: "email",
        direction: "inbound",
        subject: "Re: retrofit timeline",
        body: "Can we push the fleet retrofit review back a week?",
        occurred_at: "2026-08-01T10:15:00Z",
        links: [{ entity_type: "person", entity_id: "p-1" }],
        source: "gmail",
        captured_by: "connector:gmail",
        created_at: "2026-08-01T10:15:00Z",
        updated_at: "2026-08-01T10:15:00Z",
        is_done: false,
      },
    ],
    page,
  },
  commercial: {
    role: "champion",
    committee: [],
  },
  next_meeting: {
    activity_id: "a-2",
    starts_at: "2026-08-20T13:00:00Z",
    subject: "Fleet retrofit walkthrough",
    linked_deal_id: null,
    participants: [{ person_id: "p-1", full_name: "Dana Buyer" }],
  },
  claims: [
    {
      id: "c-1",
      kind: "commitment_ours",
      body: "send the updated retrofit quote",
      source_activity_id: "a-1",
      source_quote: "Can we push the fleet retrofit review back a week?",
      source_label: "Re: retrofit timeline",
      occurred_at: "2026-08-01T10:15:00Z",
      status: "open",
      due_at: "2026-08-15T00:00:00Z",
      needs_review: false,
    },
    {
      id: "c-2",
      kind: "priority",
      body: "keep the depot offline window under four hours",
      source_activity_id: "a-1",
      source_quote: "We can't have the depot down for more than four hours.",
      source_label: "Re: retrofit timeline",
      occurred_at: "2026-08-01T10:15:00Z",
      status: "open",
      needs_review: false,
    },
  ],
  conversation_memory: [
    {
      key: "thread-1",
      channel: "email",
      direction: "inbound",
      title: "Re: retrofit timeline",
      summary: "Dana asked to push the retrofit review back a week.",
      generated_by: "deterministic",
      occurred_at: "2026-08-01T10:15:00Z",
      activity_count: 3,
      status: "unanswered",
      linked_deal_id: null,
      first_activity_id: "a-1",
    },
  ],
  since_last_visit: {
    baseline_at: "2026-07-25T09:00:00Z",
    new_activities: 1,
    deal_stage_moves: 0,
    pending_proposals: 0,
  },
  moment: meetingPrepMoment,
} as unknown as View;

// The same record with a moment on the amber ladder rung: a relationship that
// stopped rather than one that is merely upcoming, so both tints of the lead
// card are on screen across the gallery.
const goneQuietMoment = {
  claim_key: "gone_quiet:p-1",
  evidence_fingerprint: "fp-quiet-1",
  rule: "gone_quiet",
  rule_version: "v1",
  headline: "Dana has gone quiet for 18 days.",
  why_now:
    "No reply in 18 days after two outbound messages — the gone-quiet rung fired ahead of meeting prep.",
  confidence: "observed_fact",
  freshness_at: "2026-08-13T09:00:00Z",
  evidence: [
    {
      type: "activity",
      id: "a-1",
      label: "Re: retrofit timeline",
      snippet: "Can we push the fleet retrofit review back a week?",
      observed_at: "2026-08-01T10:15:00Z",
    },
  ],
  recommended_action: {
    kind: "draft_reply",
    label: "Send a check-in",
    state: "available",
  },
  secondary_actions: [
    {
      kind: "ask_colleague",
      label: "Ask Sam Rivera",
      destination: { surface: "record", entity_id: "u-2" },
      state: "available",
    },
  ],
} as unknown as components["schemas"]["PersonMoment"];

// The same record read by someone whose grant covers none of the relationship
// sections: every reading says so instead of reading as a thin or dormant
// contact. No seeded demo grant reaches this state, so this fixture is the
// only place it renders.
const withheld = {
  ...populated,
  last_inbound_at: undefined,
  last_outbound_at: undefined,
  activities: undefined,
  commercial: undefined,
  next_meeting: undefined,
  sections_omitted: [
    "last_touch",
    "activities",
    "commercial",
    "next_meeting",
    "consent",
  ],
} as unknown as View;

// --- Page: the whole PersonPageV2 behind its three reads --------------------

function Page() {
  installFetchStub({
    "GET /people/p-1/360": () => jsonResponse(populated),
    "GET /people/p-1/brief": () =>
      jsonResponse({
        person_id: "p-1",
        generated_at: "2026-08-13T09:00:00Z",
        generated_by: "deterministic",
        sentences: [
          {
            text: "Dana Buyer leads fleet operations at Brandt Automotive and is the champion on the retrofit work.",
            evidence: [{ entity_type: "person", entity_id: "p-1" }],
          },
          {
            text: "She asked to push the retrofit review back a week and has not replied since.",
            evidence: [{ entity_type: "activity", entity_id: "a-1" }],
          },
        ],
      }),
    "GET /people/p-1/consent/guard": () =>
      jsonResponse({
        person_id: "p-1",
        entries: [
          {
            purpose_key: "business_correspondence",
            purpose_label: "Business correspondence",
            purpose_class: "business_correspondence",
            channel: "email",
            verdict: "allowed",
            reason: "She wrote to you on 1 Aug 2026.",
            qualifying_event: {
              kind: "inbound_message",
              occurred_at: "2026-08-01T10:15:00Z",
              source_entity_type: "activity",
              source_entity_id: "a-1",
            },
          },
          {
            purpose_key: "phone_outreach",
            purpose_label: "Phone outreach",
            purpose_class: "phone_outreach",
            channel: "phone",
            verdict: "unknown",
            reason: "No consent recorded.",
          },
        ],
      }),
  });
  return (
    <StoryProviders>
      <PersonPageV2 id="p-1" tab="overview" />
    </StoryProviders>
  );
}

export const PageStory: Story = { name: "Page", render: () => <Page /> };

// --- Readings: PersonStrip alone --------------------------------------------

export const Readings: Story = {
  render: () => (
    <StoryProviders>
      <div style={{ maxWidth: 900 }}>
        <PersonStrip view={populated} consentVerdict="allowed" />
      </div>
    </StoryProviders>
  ),
};

export const ReadingsWithheld: Story = {
  render: () => (
    <StoryProviders>
      <div style={{ maxWidth: 900 }}>
        <PersonStrip view={withheld} consentVerdict={undefined} />
      </div>
    </StoryProviders>
  ),
};

// --- Lead moment: PersonToday in both tints ---------------------------------

export const LeadMoment: Story = {
  render: () => (
    <StoryProviders>
      <div style={{ maxWidth: 720 }}>
        <PersonToday
          moment={meetingPrepMoment}
          firstName="Dana"
          onAction={() => {}}
        />
      </div>
    </StoryProviders>
  ),
};

export const LeadMomentWarning: Story = {
  render: () => (
    <StoryProviders>
      <div style={{ maxWidth: 720 }}>
        <PersonToday
          moment={goneQuietMoment}
          firstName="Dana"
          onAction={() => {}}
        />
      </div>
    </StoryProviders>
  ),
};

// --- Rail --------------------------------------------------------------------

export const Rail: Story = {
  render: () => (
    <StoryProviders>
      <div style={{ maxWidth: 320 }}>
        <PersonRail
          view={populated}
          guard={{
            person_id: "p-1",
            entries: [
              {
                purpose_key: "business_correspondence",
                purpose_class: "business_correspondence",
                channel: "email",
                verdict: "allowed",
                reason: "She wrote to you on 1 Aug 2026.",
              },
              {
                purpose_key: "phone_outreach",
                purpose_class: "phone_outreach",
                channel: "phone",
                verdict: "unknown",
                reason: "No consent recorded.",
              },
            ],
          }}
          firstName="Dana"
          onAction={() => {}}
          onExplain={() => {}}
        />
      </div>
    </StoryProviders>
  ),
};

// --- Brief states: the band's populated and empty readings side by side ----

// A record with no open deal, no committed loop and no captured priority —
// the reading the band exists for, where the three panels below it would
// otherwise each repeat the same "nothing here" three times over.
const emptyBand = {
  ...populated,
  commercial: undefined,
  claims: [],
} as unknown as View;

export const BriefStates: Story = {
  render: () => (
    <StoryProviders>
      <div className="pe-overview-stack" style={{ maxWidth: 720 }}>
        <PersonBriefCard
          brief={{
            person_id: "p-1",
            generated_at: "2026-08-13T09:00:00Z",
            generated_by: "deterministic",
            sentences: [
              {
                text: "Dana Buyer leads fleet operations at Brandt Automotive and is the champion on the retrofit work.",
                evidence: [{ entity_type: "person", entity_id: "p-1" }],
              },
            ],
          }}
          loading={false}
          view={populated}
        />
        <PersonBriefCard
          brief={{
            person_id: "p-1",
            generated_at: "2026-08-13T09:00:00Z",
            generated_by: "deterministic",
            sentences: [],
          }}
          loading={false}
          view={emptyBand}
        />
      </div>
    </StoryProviders>
  ),
};

// --- Overview panels: the four cards plus PersonMemory, stacked -------------

export const OverviewPanels: Story = {
  render: () => (
    <StoryProviders>
      <div className="pe-overview-stack" style={{ maxWidth: 720 }}>
        <PersonBriefCard
          brief={{
            person_id: "p-1",
            generated_at: "2026-08-13T09:00:00Z",
            generated_by: "deterministic",
            sentences: [
              {
                text: "Dana Buyer leads fleet operations at Brandt Automotive and is the champion on the retrofit work.",
                evidence: [{ entity_type: "person", entity_id: "p-1" }],
              },
              {
                text: "She asked to push the retrofit review back a week and has not replied since.",
                evidence: [{ entity_type: "activity", entity_id: "a-1" }],
              },
            ],
          }}
          loading={false}
          view={populated}
        />
        <PersonCommercialCard view={populated} />
        <PersonCommitmentsCard view={populated} firstName="Dana" />
        <PersonMattersCard view={populated} firstName="Dana" />
        <PersonMemory view={populated} />
      </div>
    </StoryProviders>
  ),
};
