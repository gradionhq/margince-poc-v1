// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { meFixture } from "../app/mefixture";
import { ProviderCard } from "./integrations-provider";
import {
  PersonBriefCard,
  PersonCommercialCard,
  PersonCommitmentsCard,
  PersonMattersCard,
} from "./personcards";
import {
  PersonComposer,
  PersonMeetingBrief,
  PersonResearchDrawer,
} from "./persondrawers";
import { PersonMemory } from "./personmemory";
import { PersonPageV2 } from "./personpage";
import { PersonRail } from "./personrail";
import { PersonStrip } from "./personstrip";
import { PersonToday } from "./persontoday";
import "./person360.css";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The person record page V2 (ADR-0096) — its own gallery, one per surface the
// concept names: the whole page behind the three reads it makes, the readings
// strip on its own (both with and without a grant), the lead moment in both
// tints, the rail, and the overview stack of cards.
//
// This gallery is what the live stack CANNOT show: every seeded demo contact
// carries full RBAC and a clean sections_omitted, so a reader never sees a
// withheld reading rendered — that state exists only here.

const meta: Meta = {
  title: "Records/Person record",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type View = components["schemas"]["Person360"];

const page = { has_more: false, next_cursor: null };

// The lead moment on the meeting-prep rung: a booked meeting close enough to
// need a brief. Typed on its own so the story rendering PersonToday directly
// never has to narrow an optional field back out of the 360.
const meetingPrepMoment: components["schemas"]["PersonMoment"] = {
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
};

// One contact at an organization: one unanswered inbound thread, a meeting
// accepted, no open deal, one colleague who knows them, email consent
// allowed. The demo-seed spirit — a record with enough on it to fill every
// card, and nothing invented past what the fixture states.
const populated: View = {
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
  },
  moment: meetingPrepMoment,
};

// The same record with a moment on the amber ladder rung: a relationship that
// stopped rather than one that is merely upcoming, so both tints of the lead
// card are on screen across the gallery.
const goneQuietMoment: components["schemas"]["PersonMoment"] = {
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
};

// The eight rungs of the ladder LeadMoment/LeadMomentWarning above never
// reach: PersonToday renders one PersonMomentRule per fixture, and a gallery
// with only meeting_prep and gone_quiet on screen hides the other eight the
// component can render. Spread across them: two evidence items (the
// "sources" plural), a `will_confirm` action, a `blocked` one with its
// reason, and a freshness older than the moment's own headline date.

const reEngagedMoment: components["schemas"]["PersonMoment"] = {
  claim_key: "re_engaged:p-1",
  evidence_fingerprint: "fp-reengaged-1",
  rule: "re_engaged",
  rule_version: "v1",
  headline: "Dana wrote back after six weeks quiet.",
  why_now: "A reply landed after a long silence, the door is open again.",
  confidence: "observed_fact",
  freshness_at: "2026-08-13T09:00:00Z",
  evidence: [
    {
      type: "activity",
      id: "a-3",
      label: "Re: still interested in the retrofit",
      observed_at: "2026-08-13T08:00:00Z",
    },
    {
      type: "activity",
      id: "a-1",
      label: "Re: retrofit timeline",
      observed_at: "2026-08-01T10:15:00Z",
    },
  ],
  recommended_action: {
    kind: "draft_reply",
    label: "Welcome her back",
    state: "will_confirm",
  },
  secondary_actions: [
    { kind: "schedule_meeting", label: "Book a catch-up", state: "available" },
  ],
};

const jobChangeMoment: components["schemas"]["PersonMoment"] = {
  claim_key: "job_change:p-1",
  evidence_fingerprint: "fp-jobchange-1",
  rule: "job_change",
  rule_version: "v1",
  headline: "Dana moved to Head of Fleet at Brandt Automotive.",
  why_now: "A recorded employment change: the buying context here just moved.",
  confidence: "observed_fact",
  // Older than the headline's own evidence, so the "updated N days ago" wording
  // reads as a real gap rather than same-day freshness.
  freshness_at: "2026-08-05T09:00:00Z",
  evidence: [
    {
      type: "relationship_change",
      label: "Employment updated: Head of Fleet at Brandt Automotive GmbH",
      observed_at: "2026-08-05T09:00:00Z",
    },
  ],
  recommended_action: {
    kind: "open_record",
    label: "Review the account",
    destination: { surface: "record", entity_type: "organization" },
    state: "available",
  },
  secondary_actions: [
    {
      kind: "draft_reply",
      label: "Congratulate her",
      state: "blocked",
      blocked_reason: "No consent recorded for marketing outreach yet.",
    },
  ],
};

const overduePromiseMoment: components["schemas"]["PersonMoment"] = {
  claim_key: "overdue_promise:p-1:c-1",
  evidence_fingerprint: "fp-overdue-1",
  rule: "overdue_promise",
  rule_version: "v1",
  headline: "The updated retrofit quote is three days late.",
  why_now: "A commitment past its own due date, still open.",
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
    label: "Send the quote",
    state: "available",
  },
};

const roleChangeMoment: components["schemas"]["PersonMoment"] = {
  claim_key: "role_change:p-1",
  evidence_fingerprint: "fp-rolechange-1",
  rule: "role_change",
  rule_version: "v1",
  headline: "Dana's recorded buying role moved from influencer to champion.",
  why_now: "A committee seat changed, the pitch to her changes with it.",
  confidence: "observed_fact",
  freshness_at: "2026-08-12T09:00:00Z",
  evidence: [
    {
      type: "relationship_change",
      label: "Buying role updated: champion",
      observed_at: "2026-08-12T09:00:00Z",
    },
  ],
  recommended_action: {
    kind: "open_record",
    label: "Open the deal",
    destination: { surface: "record", entity_type: "deal" },
    state: "available",
  },
};

const publicSignalMoment: components["schemas"]["PersonMoment"] = {
  claim_key: "public_signal:p-1",
  evidence_fingerprint: "fp-publicsignal-1",
  rule: "public_signal",
  rule_version: "v1",
  headline: "Dana posted about the depot's EV rollout timeline.",
  why_now: "A public statement that bears on the retrofit conversation.",
  confidence: "medium",
  freshness_at: "2026-08-11T09:00:00Z",
  evidence: [
    {
      type: "activity",
      label: 'LinkedIn post: "2027 is the depot\'s EV deadline"',
      observed_at: "2026-08-11T09:00:00Z",
    },
  ],
  recommended_action: {
    kind: "draft_reply",
    label: "Reference it in your next note",
    state: "available",
  },
};

const missingNextStepMoment: components["schemas"]["PersonMoment"] = {
  claim_key: "missing_next_step:p-1",
  evidence_fingerprint: "fp-missingstep-1",
  rule: "missing_next_step",
  rule_version: "v1",
  headline: "The retrofit walkthrough has no next step booked after it.",
  why_now: "A meeting is on the calendar with nothing recorded for after it.",
  confidence: "observed_fact",
  freshness_at: "2026-08-13T09:00:00Z",
  evidence: [
    {
      type: "task",
      label: "Fleet retrofit walkthrough, 20 Aug",
      observed_at: "2026-08-20T13:00:00Z",
    },
  ],
  recommended_action: {
    kind: "schedule_meeting",
    label: "Book the follow-up",
    state: "available",
  },
};

const thinRelationshipMoment: components["schemas"]["PersonMoment"] = {
  claim_key: "thin_relationship:p-1",
  evidence_fingerprint: "fp-thin-1",
  rule: "thin_relationship",
  rule_version: "v1",
  headline: "Dana is the only contact captured at Brandt Automotive.",
  why_now: "One thread carries the whole account, nobody else is on record.",
  confidence: "observed_fact",
  freshness_at: "2026-08-13T09:00:00Z",
  evidence: [
    {
      type: "relationship_change",
      label: "One employment edge on this account",
      observed_at: "2026-06-01T08:00:00Z",
    },
  ],
  recommended_action: {
    kind: "ask_colleague",
    label: "Ask who else to loop in",
    state: "available",
  },
};

// Rung 10, the quiet-success case: nothing needs the reader today, and it
// renders through this same component rather than an empty card
// (persontoday.tsx's own comment on `isQuiet`).
const nothingNeededMoment: components["schemas"]["PersonMoment"] = {
  claim_key: "nothing_needed:p-1",
  evidence_fingerprint: "fp-nothingneeded-1",
  rule: "nothing_needed",
  rule_version: "v1",
  headline: "Nothing needs you on this account today.",
  why_now: "Every open loop is answered and the next meeting is booked.",
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
    kind: "open_record",
    label: "Open the record",
    destination: { surface: "record", entity_type: "person", entity_id: "p-1" },
    state: "available",
  },
};

// The same record read by someone whose grant covers none of the relationship
// sections: every reading says so instead of reading as a thin or dormant
// contact. No seeded demo grant reaches this state, so this fixture is the
// only place it renders.
const withheld: View = {
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
};

// --- Page: the whole PersonPageV2 behind its three reads --------------------

function Page() {
  installFetchStub({
    // The page mounts capability-aware chrome, so the session has to be routed:
    // the stub refuses to guess one, and an unrouted probe fails every grant
    // closed — the embedded rail would render read-only no matter what the
    // fixture below grants, which is not what a permitted reader sees.
    "GET /me": meRoute({ person: ["read", "update"] }),
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

// The danger tone: a consent verdict that refuses rather than merely being
// unrecorded. Every other fixture in this file reads "allowed" or "unknown":
// this is the only place the strip's red slot renders at all.
export const ReadingsBlocked: Story = {
  render: () => (
    <StoryProviders>
      <div style={{ maxWidth: 900 }}>
        <PersonStrip view={populated} consentVerdict="blocked" />
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

// The eight rungs LeadMoment and LeadMomentWarning above don't reach, stacked
// rather than split into eight near-identical stories: each one differs only
// in its moment, so a designer scanning this gallery sees the whole ladder in
// one scroll instead of hunting eight sidebar entries for the same panel.
const REMAINING_MOMENTS: ReadonlyArray<components["schemas"]["PersonMoment"]> =
  [
    reEngagedMoment,
    jobChangeMoment,
    overduePromiseMoment,
    roleChangeMoment,
    publicSignalMoment,
    missingNextStepMoment,
    thinRelationshipMoment,
    nothingNeededMoment,
  ];

export const LeadMomentLadder: Story = {
  render: () => (
    <StoryProviders>
      <div
        style={{
          maxWidth: 720,
          display: "flex",
          flexDirection: "column",
          gap: "var(--space-4)",
        }}
      >
        {REMAINING_MOMENTS.map((moment) => (
          <PersonToday
            key={moment.claim_key}
            moment={moment}
            firstName="Dana"
            onAction={() => {}}
          />
        ))}
      </div>
    </StoryProviders>
  ),
};

// --- Rail --------------------------------------------------------------------

export const Rail: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { person: ["update"] } })),
    });
    return (
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
            onExplain={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};

// The Companies section with two employment edges — one current, one former
// — so the current/former distinction and the add/mark-ended/remove verbs
// are all on screen at once.
const twoEmployers: View = {
  ...populated,
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
      {
        relationship_id: "rel-2",
        organization_id: "o-2",
        organization_name: "Voss Logistics",
        role: "Fleet Coordinator",
        is_current_primary: false,
        started_at: "2018-01-01T00:00:00Z",
        ended_at: "2022-02-01T00:00:00Z",
      },
    ],
    page,
  },
};

export const RailEmployments: Story = {
  render: () => {
    // The rail's editable rows read the grant map, so the fixture has to be
    // the real /me shape rather than a hand-written stand-in: an object absent
    // from `allow` is absent the way the server omits it, which is the case a
    // client that treats "missing" as "permitted" gets wrong.
    installFetchStub({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { person: ["update"] } })),
    });
    return (
      <StoryProviders>
        <div style={{ maxWidth: 320 }}>
          <PersonRail
            view={twoEmployers}
            guard={undefined}
            firstName="Dana"
            onExplain={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};

// The rail's consent slot when a purpose is refused rather than merely
// unrecorded: verdictClass (personrail.tsx) reads this as the refused/warn
// treatment, the same red-toned reason the strip's own consentTone renders
// as its danger tone (see ReadingsBlocked above for that surface).
export const RailConsentBlocked: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { person: ["update"] } })),
    });
    return (
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
                  verdict: "blocked",
                  reason:
                    "She opted out of business correspondence on 3 Aug 2026.",
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
            onExplain={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};

// A record with an open deal, an empty committee and no meeting booked, and
// a last inbound reply well past the 14-day threshold: the shape that fires
// three of derivedSignals' four warnings at once (personrail.tsx) plus the
// pulse's own "cooling" trend and "at risk" overall word, none of which any
// other fixture in this file reaches because none of them carries a deal.
const dealAtRisk: View = {
  ...populated,
  last_inbound_at: "2026-07-10T09:00:00Z",
  last_outbound_at: "2026-07-25T09:00:00Z",
  commercial: {
    role: "champion",
    deal: {
      deal_id: "d-1",
      title: "Fleet retrofit, 40 vehicles",
      stage: "negotiation",
      amount_minor: 9_500_000,
      currency: "EUR",
      close_date: "2026-09-30",
    },
    committee: [],
  },
  next_meeting: undefined,
};

export const RailAtRisk: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { person: ["update"] } })),
    });
    return (
      <StoryProviders>
        <div style={{ maxWidth: 320 }}>
          <PersonRail
            view={dealAtRisk}
            guard={undefined}
            firstName="Dana"
            onExplain={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};

// The thin end of every rail section at once: no reply ever captured (so the
// pulse's "thin" overall word and the signals' own empty state both render),
// one direction only (the one-sided reading, since twoWay needs both), no
// employer, nobody who knows her, and nothing in the recent-activity list.
// A record can genuinely look like this the day after it is captured.
const thinContact: View = {
  ...populated,
  last_inbound_at: undefined,
  last_outbound_at: "2026-08-10T09:00:00Z",
  network: { colleagues: [] },
  employments: { data: [], page },
  activities: { data: [], page },
  commercial: { role: null, committee: [] },
  next_meeting: undefined,
};

export const RailThin: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { person: ["update"] } })),
    });
    return (
      <StoryProviders>
        <div style={{ maxWidth: 320 }}>
          <PersonRail
            view={thinContact}
            guard={undefined}
            firstName="Dana"
            onExplain={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};

// Every profile field DetailsGrid can hold, unset at once: title, linkedin,
// city, email and phone all blank. Email and phone are always read-only
// (personrail.tsx's CONTACT_METHOD_IMMUTABLE), so they read `field.unset`
// here whether or not the reader can edit; title, linkedin and city ARE
// editable under this fixture's granted /me, so they read as the "Add …"
// placeholder instead: the two empty-field states side by side.
const unsetFields: View = {
  ...populated,
  person: {
    ...populated.person,
    title: null,
    social: {},
    address: undefined,
    emails: [],
    phones: [],
  },
};

export const RailUnsetFields: Story = {
  render: () => {
    installFetchStub({
      "GET /me": () =>
        jsonResponse(meFixture({ allow: { person: ["update"] } })),
    });
    return (
      <StoryProviders>
        <div style={{ maxWidth: 320 }}>
          <PersonRail
            view={unsetFields}
            guard={undefined}
            firstName="Dana"
            onExplain={() => {}}
          />
        </div>
      </StoryProviders>
    );
  },
};

// --- Brief states: the band's populated and empty readings side by side ----

// A record with no open deal, no committed loop and no captured priority —
// the reading the band exists for, where the three panels below it would
// otherwise each repeat the same "nothing here" three times over.
//
// `commercial` is PRESENT and empty rather than absent, which is the whole
// distinction: the section arrives with a null deal when there is none to
// show, and arrives not at all when the reader may not see deals. Dropping it
// here would make an empty record claim a permission boundary.
const emptyBand: View = {
  ...populated,
  commercial: { role: null, committee: [] },
  claims: [],
};

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

// A deal with money, stage and a close date, and more than one stakeholder in
// the room: the populated end of PersonCommercialCard that `populated`
// itself never reaches (its own commercial section carries no deal).
const dealWithCommittee: View = {
  ...populated,
  commercial: {
    role: "champion",
    deal: {
      deal_id: "d-1",
      title: "Fleet retrofit, 40 vehicles",
      stage: "negotiation",
      amount_minor: 9_500_000,
      currency: "EUR",
      close_date: "2026-09-30",
    },
    committee: [
      { person_id: "p-2", full_name: "Mika Voss", role: "economic_buyer" },
      { person_id: "p-3", full_name: "Jonas Reiter", role: "influencer" },
    ],
  },
};

// One of every loop kind PersonCommitmentsCard renders, plus a `done` row:
// `populated` carries only a single `commitment_ours`, so neither
// `commitment_theirs`, `open_question` nor the done state ever appear.
const richLoops: View = {
  ...populated,
  claims: [
    ...(populated.claims ?? []),
    {
      id: "c-3",
      kind: "commitment_theirs",
      body: "send the fleet utilization numbers",
      source_activity_id: "a-1",
      source_quote: "I'll get you the utilization numbers by Friday.",
      source_label: "Re: retrofit timeline",
      occurred_at: "2026-08-01T10:15:00Z",
      status: "open",
      needs_review: false,
    },
    {
      id: "c-4",
      kind: "open_question",
      body: "whether the depot needs a second charger bank",
      source_activity_id: "a-1",
      source_quote: "Not sure yet if one charger bank covers the whole depot.",
      source_label: "Re: retrofit timeline",
      occurred_at: "2026-08-01T10:15:00Z",
      status: "open",
      needs_review: false,
    },
    {
      id: "c-5",
      kind: "commitment_ours",
      body: "confirm the installation window",
      source_activity_id: "a-1",
      source_quote: "We'll confirm the installation window by Monday.",
      source_label: "Re: retrofit timeline",
      occurred_at: "2026-07-28T10:15:00Z",
      status: "done",
      needs_review: false,
    },
  ],
};

// Every channel PersonMemory labels (meeting, call, note, beside the email
// entries every other fixture already carries) and a `replied` status, none
// of which the single email entry on `populated` reaches.
const richMemory: View = {
  ...populated,
  conversation_memory: [
    ...(populated.conversation_memory ?? []),
    {
      key: "thread-2",
      channel: "meeting",
      direction: "internal",
      title: "Fleet retrofit kickoff",
      summary: "Walked through the retrofit scope and the depot's constraints.",
      generated_by: "deterministic",
      occurred_at: "2026-07-15T13:00:00Z",
      activity_count: 1,
      status: null,
      linked_deal_id: null,
      first_activity_id: "a-4",
    },
    {
      key: "thread-3",
      channel: "call",
      direction: "outbound",
      title: "Check-in call",
      summary: "Confirmed the depot's offline window with Dana.",
      generated_by: "deterministic",
      occurred_at: "2026-07-22T09:30:00Z",
      activity_count: 1,
      status: "replied",
      linked_deal_id: null,
      first_activity_id: "a-5",
    },
    {
      key: "thread-4",
      channel: "note",
      direction: "internal",
      title: "Internal note",
      summary:
        "Sam flagged Dana as the sole technical contact on this account.",
      generated_by: "deterministic",
      occurred_at: "2026-07-10T09:00:00Z",
      activity_count: 1,
      status: null,
      linked_deal_id: null,
      first_activity_id: "a-6",
    },
  ],
};

// No thread, no meeting, no captured activity to fold: the honest empty
// state, distinct from `populated`'s one email entry.
const emptyMemory: View = {
  ...populated,
  conversation_memory: [],
  activities: { data: [], page },
};

// The gaps `OverviewPanels` above leaves: the withheld commercial card, the
// populated one with a deal and a committee, every commitments loop kind
// including a done row, the brief's loading and undefined-brief readings,
// and the memory panel's full channel set plus its empty state.
export const OverviewGaps: Story = {
  render: () => (
    <StoryProviders>
      <div className="pe-overview-stack" style={{ maxWidth: 720 }}>
        <PersonBriefCard brief={undefined} loading view={populated} />
        <PersonBriefCard brief={undefined} loading={false} view={populated} />
        <PersonCommercialCard view={withheld} />
        <PersonCommercialCard view={dealWithCommittee} />
        <PersonCommitmentsCard view={richLoops} firstName="Dana" />
        <PersonMemory view={richMemory} />
        <PersonMemory view={emptyMemory} />
      </div>
    </StoryProviders>
  ),
};

// --- Drawers: the three surfaces the page opens over itself -----------------
//
// Each renders `open`, since a closed drawer paints nothing and would capture
// as a blank frame — the point of these stories is the drawer itself.

const consentGuardAllowed: components["schemas"]["PersonConsentGuard"] = {
  person_id: "p-1",
  entries: [
    {
      purpose_key: "business_correspondence",
      purpose_label: "Business correspondence",
      purpose_class: "business_correspondence",
      channel: "email",
      verdict: "allowed",
      reason: "She wrote to you on 1 Aug 2026.",
    },
  ],
};

export const Composer: Story = {
  render: () => {
    installFetchStub({
      "POST /people/p-1/draft-email": () =>
        jsonResponse({
          subject: "Re: retrofit timeline",
          body: "Hi Dana,\n\nHappy to push the review back a week — does the 20th work?\n\nBest,",
          to: ["dana@brandt.example"],
          reasoning: [
            {
              kind: "commitment",
              label: "You owe her the updated retrofit quote.",
            },
            {
              kind: "conversation",
              label: "She asked to push the review back a week.",
            },
          ],
          generated_by: "deterministic",
          ai_generated: false,
        }),
    });
    return (
      <StoryProviders>
        <PersonComposer
          personId="p-1"
          view={populated}
          guard={consentGuardAllowed}
          open
          onClose={() => {}}
        />
      </StoryProviders>
    );
  },
};

// The research drawer under ADR-0096 D4's supported configuration: no
// provider is registered. `providerProfile` is PRESENT with state
// "not_connected" rather than absent — absent means the caller lacks the
// grant, and this reader has one; there is simply no provider behind it to
// report on, which is a fact about the deployment and not a permission
// boundary.
const providerNotConnected: components["schemas"]["PersonProviderProfile"] = {
  state: "not_connected",
  categories_not_requested: [],
  emails: [],
  mobile_phones: [],
  job_history: [],
  departments: [],
  seniorities: [],
};

export const ResearchDrawer: Story = {
  render: () => {
    installFetchStub({
      "POST /people/p-1/research": () =>
        jsonResponse({
          person_id: "p-1",
          state: "not_connected",
          generated_at: "2026-08-13T09:00:00Z",
          claims: [],
        }),
    });
    return (
      <StoryProviders>
        <PersonResearchDrawer
          personId="p-1"
          personName="Dana Buyer"
          providerProfile={providerNotConnected}
          open
          onClose={() => {}}
        />
      </StoryProviders>
    );
  },
};

// A run that landed with values on every branch ProviderValues renders
// (personprovider.tsx): `providerNotConnected` above leaves every array
// empty, so none of emails/mobiles/employment/job history/location/
// departments/seniorities, nor the EnrichNow button, ever render without
// this fixture.
const completedRun: components["schemas"]["ProviderRun"] = {
  id: "run-1",
  subject_kind: "person",
  person_id: "p-1",
  provider: "surfe",
  trigger: "manual",
  state: "completed",
  skip_reason: null,
  connection_version: 1,
  configuration_snapshot: {
    mode: "on_demand",
    preset: "professional_only",
    automatic_individual_create: true,
    automatic_import: false,
    categories: { email: true, mobile: true },
  },
  requested_categories: ["email", "mobile"],
  reservations: [{ pool: "email", reserved_credits: 1, actual_credits: 1 }],
  claims_unwritten: false,
  submitted_at: "2026-08-12T09:00:00Z",
  completed_at: "2026-08-12T09:02:00Z",
  safe_status_code: null,
  created_at: "2026-08-12T09:00:00Z",
  updated_at: "2026-08-12T09:02:00Z",
};

const providerCompleted: components["schemas"]["PersonProviderProfile"] = {
  state: "completed",
  provider: "surfe",
  retrieved_at: "2026-08-12T09:02:00Z",
  safe_status_code: null,
  // Both categories were asked for and both came back, so nothing here is
  // reported as skipped: `mobile_phones` below is populated, and claiming it
  // as "not requested" beside a value the run actually returned would say
  // two contradictory things about the same run.
  categories_not_requested: [],
  emails: [
    {
      value: "dana.buyer@surfe.example",
      email_type: "professional",
      email_type_source: "provider",
      validation_status: "valid",
    },
  ],
  mobile_phones: [{ value: "+491701234567", confidence: 0.82 }],
  linkedin_url: "https://linkedin.com/in/danabuyer",
  current_employment: {
    company_name: "Brandt Automotive GmbH",
    company_domain: "brandt-automotive.example",
    job_title: "Head of Fleet",
  },
  job_history: [
    {
      company_name: "Voss Logistics",
      job_title: "Fleet Coordinator",
      started_at: "2018-01-01T00:00:00Z",
      ended_at: "2022-02-01T00:00:00Z",
    },
  ],
  location: "Munich, Germany",
  city: "Munich",
  region: "Bavaria",
  country: "DE",
  departments: ["Operations"],
  seniorities: ["Head"],
  latest_run: completedRun,
};

export const ResearchDrawerProviderCompleted: Story = {
  render: () => {
    installFetchStub({
      "POST /people/p-1/research": () =>
        jsonResponse({
          person_id: "p-1",
          state: "ready",
          generated_at: "2026-08-13T09:00:00Z",
          claims: [],
        }),
    });
    return (
      <StoryProviders>
        <PersonResearchDrawer
          personId="p-1"
          personName="Dana Buyer"
          providerProfile={providerCompleted}
          open
          onClose={() => {}}
        />
      </StoryProviders>
    );
  },
};

// A run still moving: RunWatch polls `GET .../enrichment-runs/{run_id}`
// while `isRunning` holds, and the badge reads "In progress" rather than any
// terminal word: a state `providerNotConnected` and `providerCompleted`
// above never reach.
const inProgressRun: components["schemas"]["ProviderRun"] = {
  ...completedRun,
  id: "run-2",
  state: "in_progress",
  completed_at: null,
  submitted_at: "2026-08-13T09:00:00Z",
  requested_categories: ["email"],
};

const providerRunning: components["schemas"]["PersonProviderProfile"] = {
  state: "in_progress",
  provider: "surfe",
  retrieved_at: null,
  // This run asked for email only, so mobile is honestly "not requested"
  // rather than "requested but not yet back": the run has nothing to show
  // for either while it is still moving.
  categories_not_requested: ["mobile"],
  emails: [],
  mobile_phones: [],
  job_history: [],
  departments: [],
  seniorities: [],
  latest_run: inProgressRun,
};

export const ResearchDrawerProviderRunning: Story = {
  render: () => {
    installFetchStub({
      "POST /people/p-1/research": () =>
        jsonResponse({
          person_id: "p-1",
          state: "not_connected",
          generated_at: "2026-08-13T09:00:00Z",
          claims: [],
        }),
      "GET /people/p-1/enrichment-runs/run-2": () =>
        jsonResponse(inProgressRun),
    });
    return (
      <StoryProviders>
        <PersonResearchDrawer
          personId="p-1"
          personName="Dana Buyer"
          providerProfile={providerRunning}
          open
          onClose={() => {}}
        />
      </StoryProviders>
    );
  },
};

// A run the provider refused: paid nothing (no run reached the vendor's
// meter) and returned nothing, so the badge carries the danger tone
// (provider-status.ts's PROFILE_TONE) rather than the neutral or success one
// every other provider fixture here shows.
const failedRun: components["schemas"]["ProviderRun"] = {
  ...completedRun,
  id: "run-3",
  state: "failed",
  completed_at: "2026-08-13T09:05:00Z",
  safe_status_code: "provider_unavailable",
};

const providerError: components["schemas"]["PersonProviderProfile"] = {
  state: "provider_error",
  provider: "surfe",
  retrieved_at: null,
  safe_status_code: "provider_unavailable",
  categories_not_requested: [],
  emails: [],
  mobile_phones: [],
  job_history: [],
  departments: [],
  seniorities: [],
  latest_run: failedRun,
};

export const ResearchDrawerProviderError: Story = {
  render: () => {
    installFetchStub({
      "POST /people/p-1/research": () =>
        jsonResponse({
          person_id: "p-1",
          state: "not_connected",
          generated_at: "2026-08-13T09:00:00Z",
          claims: [],
        }),
    });
    return (
      <StoryProviders>
        <PersonResearchDrawer
          personId="p-1"
          personName="Dana Buyer"
          providerProfile={providerError}
          open
          onClose={() => {}}
        />
      </StoryProviders>
    );
  },
};

export const MeetingBrief: Story = {
  render: () => {
    installFetchStub({
      "GET /activities/a-2/meeting-brief": () =>
        jsonResponse({
          activity_id: "a-2",
          generated_at: "2026-08-13T09:00:00Z",
          generated_by: "deterministic",
          sections: [
            {
              kind: "goal",
              sentences: [
                {
                  text: "Confirm the retrofit timeline and lock the depot offline window.",
                  evidence: [{ entity_type: "activity", entity_id: "a-1" }],
                },
              ],
            },
            {
              kind: "commitments",
              sentences: [
                {
                  text: "You owe Dana the updated retrofit quote.",
                  evidence: [{ entity_type: "activity", entity_id: "a-1" }],
                },
              ],
            },
          ],
        }),
    });
    return (
      <StoryProviders>
        <PersonMeetingBrief activityId="a-2" open onClose={() => {}} />
      </StoryProviders>
    );
  },
};

// --- Provider section: the not-configured state -----------------------------
//
// The state a stack with no provider key actually renders (PI-AC-9) — the
// server answers 501 code:not_implemented, the same shape connectors.tsx
// stubs for a connector nobody wired.
export const ProviderNotConfigured: Story = {
  render: () => {
    installFetchStub({
      // ProviderCard reads the session to decide whether the surface is
      // read-only, so the probe has to be routed even for a story about the
      // server answering 501.
      "GET /me": meRoute({ integrations: ["read"] }),
      "GET /provider-connections": () =>
        jsonResponse({ code: "not_implemented" }, 501),
    });
    return (
      <StoryProviders>
        <ProviderCard />
      </StoryProviders>
    );
  },
};
