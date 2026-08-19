// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { PersonRail } from "./personrail";
import "./person360.css";
import { installFetchStub, meRoute, StoryProviders } from "./story-utils";

// The person rail, and specifically the states a permission produces — which
// the live stack cannot show at all: every seeded demo contact carries full
// RBAC, so `sections_omitted` is always empty there and a withheld reading
// never draws.
//
// The PAIR is the whole gallery. A rail whose relationship sections were
// withheld and a rail belonging to a contact nobody has ever written to must
// not look alike: the first is a fact about the reader's grants, the second a
// fact about the relationship, and the rail's four short verdicts —
// "One-sided", "Never", "No inbound", "Thin" — belong to the second only. Read
// the two stories side by side; if they read the same, the rail is lying to
// somebody.
//
// Check both themes. The Overall reading is the only one drawn in the verdict
// colour, and a withheld reading has to lose that colour in dark as well as in
// light — every tone here is a `color-mix()` of a token and the dark palette
// lifts the accents, so the one surface that must NOT look like a verdict is
// the one worth re-checking after the theme switch.

type View = components["schemas"]["Person360"];

const meta: Meta = {
  title: "Records/Person record/Rail grants",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const page = { has_more: false, next_cursor: null };

// The readings are distances to NOW, so the fixtures state an age rather than a
// date: a pinned date drifts into "412 days" and stops showing the reading this
// gallery is about.
function daysAgo(days: number): string {
  return new Date(Date.now() - days * 86_400_000).toISOString();
}

const person: components["schemas"]["Person"] = {
  id: "p-1",
  full_name: "Dana Buyer",
  first_name: "Dana",
  last_name: "Buyer",
  title: "Head of Fleet",
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
      captured_by: "human:u-1",
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
      captured_by: "human:u-1",
    },
  ],
  source: "manual",
  captured_by: "human:u-1",
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-08-01T08:00:00Z",
};

const guard: components["schemas"]["PersonConsentGuard"] = {
  person_id: "p-1",
  entries: [
    {
      purpose_key: "business_correspondence",
      purpose_class: "business_correspondence",
      channel: "email",
      verdict: "allowed",
      reason: "She wrote to you three days ago.",
    },
    {
      purpose_key: "phone_outreach",
      purpose_class: "phone_outreach",
      channel: "phone",
      verdict: "unknown",
      reason: "No consent recorded.",
    },
  ],
};

// A reader holding every grant, on a relationship with something on it: they
// wrote three days ago, we wrote five days before that, a colleague knows them,
// and there is an open deal with a meeting booked against it.
const granted: View = {
  as_of: "2026-08-18T09:00:00Z",
  person,
  sections_omitted: [],
  last_inbound_at: daysAgo(3),
  last_outbound_at: daysAgo(8),
  network: {
    colleagues: [
      {
        user_id: "u-2",
        display_name: "Sam Rivera",
        strength: 0.7,
        strength_bucket: "strong",
        interactions_90d: 6,
        last_at: daysAgo(3),
        inbound_90d: 3,
        outbound_90d: 3,
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
        occurred_at: daysAgo(3),
        source: "gmail",
        captured_by: "connector:gmail",
        created_at: daysAgo(3),
        updated_at: daysAgo(3),
        is_done: false,
      },
    ],
    page,
  },
  commercial: {
    role: "champion",
    committee: [
      { person_id: "p-2", full_name: "Ines Klaas", role: "economic_buyer" },
    ],
    deal: {
      deal_id: "d-1",
      title: "Fleet retrofit",
      amount_minor: 9_500_000,
      currency: "EUR",
    },
  },
  next_meeting: {
    activity_id: "a-2",
    starts_at: daysAgo(-4),
    subject: "Fleet retrofit walkthrough",
  },
};

// The same reader, on a contact nobody here has ever corresponded with. Every
// section came back and every one of them is empty — the ONLY state entitled to
// the rail's negative verdicts, and the comparison every story below is read
// against.
const emptyButGranted: View = {
  as_of: granted.as_of,
  person,
  sections_omitted: [],
  last_inbound_at: null,
  last_outbound_at: null,
  network: { colleagues: [] },
  employments: { data: [], page },
  activities: { data: [], page },
  commercial: { committee: [] },
};

// A reader whose grants cover the record and its employers but none of the
// relationship: the four governed sections are absent, and the list beside them
// is the only thing that separates this from the record above.
const relationshipWithheld: View = {
  ...granted,
  last_inbound_at: undefined,
  last_outbound_at: undefined,
  activities: undefined,
  commercial: undefined,
  next_meeting: undefined,
  sections_omitted: ["last_touch", "activities", "commercial", "next_meeting"],
};

// The narrowest read this page serves: the person's own fields and nothing
// else. Every section that could carry a verdict says it is withheld, which is
// the state a rail returning `null` on a denial would have drawn as a contact
// with an empty life.
const everythingWithheld: View = {
  as_of: granted.as_of,
  person,
  sections_omitted: [
    "last_touch",
    "activities",
    "commercial",
    "next_meeting",
    "network",
    "employments",
  ],
};

// A deal this reader may see, single-threaded, with a calendar they may not.
// One signal is derivable and one is not, so the section states that it is
// showing part of the list — a single signal otherwise reads as the whole
// finding.
const signalsPartlyDerivable: View = {
  ...granted,
  commercial: {
    role: "champion",
    committee: [],
    deal: { deal_id: "d-1", title: "Fleet retrofit" },
  },
  next_meeting: undefined,
  sections_omitted: ["next_meeting"],
};

// The rail sits in a 320px column in the record page's grid, and its readings
// are laid out for that width: shown any wider, the label and its verdict drift
// apart and the section stops reading as a glance.
function rail(view: View) {
  installFetchStub({
    // The rail draws capability-gated verbs (the inline edits, add employment),
    // so the session has to be routed: an unrouted probe denies every grant and
    // the gallery would show the read-only rail under every story name.
    "GET /me": meRoute({ person: ["read", "update"] }),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 320 }}>
        <PersonRail
          view={view}
          guard={guard}
          firstName="Dana"
          onExplain={() => {}}
        />
      </div>
    </StoryProviders>
  );
}

export const EverySectionGranted: Story = {
  name: "Granted, with a live relationship",
  render: () => rail(granted),
};

export const GrantedAndEmpty: Story = {
  name: "Granted, and genuinely empty",
  render: () => rail(emptyButGranted),
};

export const RelationshipWithheld: Story = {
  name: "Relationship sections withheld",
  render: () => rail(relationshipWithheld),
};

export const EverythingWithheld: Story = {
  name: "Every relationship section withheld",
  render: () => rail(everythingWithheld),
};

export const SignalsPartlyDerivable: Story = {
  name: "Signals only partly derivable",
  render: () => rail(signalsPartlyDerivable),
};
