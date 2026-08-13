// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { useT } from "../i18n";
import {
  AccountBrief,
  CommercialPanel,
  DealsCard,
  NextSteps,
  PeopleCard,
  RecentActivityPanel,
  StateStrip,
} from "./company360";
import { LIFECYCLE_LABELS } from "./companylookups";
import { RELATIONSHIP_TYPE_LABELS } from "./organizations";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The company view's Panel-shaped cards, rendered straight from a payload
// rather than through the screen — so the three answers a card can give are
// visible side by side: here it is, there is none, and your role cannot
// read this.
//
// This gallery is what the live stack CANNOT show: every seeded demo
// account grants the viewer full RBAC and omits nothing, so SectionWithheld
// is a state no browser session reaches. It is real — a role scoped to
// fewer objects hits it on every 360 read that names a section it may not
// see — and this is the only place it can be looked at.

const meta: Meta = {
  title: "Screens/Company 360 cards",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type View = components["schemas"]["Organization360"];
type FinanceSummary = components["schemas"]["OrganizationFinanceSummary"];

const page = { has_more: false, next_cursor: null };

const populated = {
  as_of: "2026-07-13T09:00:00Z",
  organization: {
    id: "o-1",
    display_name: "Brandt Automotive GmbH",
    lifecycle: "customer",
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
  activities: {
    data: [
      {
        id: "a-1",
        kind: "email",
        direction: "outbound",
        subject: "Re: retrofit timeline",
        occurred_at: "2026-07-12T10:00:00Z",
        links: [{ entity_type: "deal", entity_id: "d-1" }],
      },
    ],
    page,
  },
  next_steps: {
    data: [
      {
        activity_id: "a-2",
        subject: "Send the renewal paperwork",
        due_at: "2026-07-01T09:00:00Z",
        overdue: true,
        linked_deal_id: null,
        linked_person_id: null,
        assignee_id: null,
      },
      {
        activity_id: "a-3",
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
  state_strip: {
    account: { lifecycle: "customer", relationship_types: ["customer"] },
    engagement: {
      state: "active",
      last_inbound_at: "2026-07-11T09:00:00Z",
      last_outbound_at: "2026-07-12T09:00:00Z",
    },
    commercial: {
      open_count: 2,
      stalled_count: 1,
      priced_count: 2,
      converted_count: 0,
      open_pipeline_minor_base: 5_700_000,
      base_currency: "EUR",
      next_close_on: "2026-08-15",
    },
  },
  // Two rated dimensions, not one: HealthSummaryStat's verdict is a worst-of
  // over relationship, commercial and payment, and a fixture that only ever
  // rated one dimension could never show the "N of 3 rated" count meaning
  // anything. Days-since-inbound matches the engagement block's own
  // last_inbound_at (as_of minus two days), and reply_balance sits inside the
  // 0.34-0.66 band on purpose: that is the branch HealthStat calls "Balanced"
  // rather than one-sided, so the fixture exercises the reading the state
  // strip most often shows for a healthy account.
  health: {
    relationship: {
      rating: "strong",
      reason: "Two contacts active, replies arrive within a day.",
    },
    commercial: {
      rating: "good",
      reason: "One deal stalled, the other moving on schedule.",
    },
    days_since_last_inbound: 2,
    reply_balance: 0.5,
    last_meeting_at: "2026-07-05T14:00:00Z",
    active_contacts: 2,
    single_threaded: false,
    open_commitments: 1,
  },
} as unknown as View;

// The same account read by someone whose role cannot see deals, people or
// the state strip: each card says so rather than reading as an account with
// no pipeline, no contacts and no standing. This is the state no seeded demo
// account can reach — every one of them grants the viewer full RBAC — so
// this gallery is the only place a reader ever sees it rendered.
const withheld = {
  ...populated,
  deals: undefined,
  people: undefined,
  state_strip: undefined,
  sections_omitted: ["deals", "people", "state_strip"],
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
  activities: { data: [], page },
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
    // The brief is the card the stories exist to show. Unstubbed it fell
    // through to the empty fallback, and all three stories rendered the same
    // blank block instead of the three answers they are here to compare.
    //
    // Sentences sit inside `sections`, the real wire shape (OrganizationBrief):
    // a flat top-level `sentences` array reads as `Array.isArray(sections)`
    // false, so the panel falls to its unavailable state and every sentence
    // below, deal citation included, renders nothing at all.
    "GET /organizations/o-1/brief": () =>
      jsonResponse({
        organization_id: "o-1",
        generated_at: "2026-07-13T09:00:00Z",
        generated_by: "deterministic",
        sections: view.people?.data?.length
          ? [
              {
                kind: "snapshot",
                sentences: [
                  {
                    text: "Relationship strength 62 across 2 known contact(s).",
                    evidence: [
                      { entity_type: "organization", entity_id: "o-1" },
                    ],
                  },
                  {
                    text: "What they sell: managed commerce hosting.",
                    evidence: [
                      { entity_type: "organization", entity_id: "o-1" },
                    ],
                  },
                ],
              },
              {
                kind: "next_step",
                sentences: [
                  {
                    // The one place a citation carries the cited record's own
                    // name rather than just its kind (company360.tsx's
                    // Citations, chip.count === 1 && chip.name): the name
                    // matches the deal the view already carries, the same way
                    // a real writer only ever names a record it read.
                    text: '"Fleet retrofit 2026" is next up for a follow-up call.',
                    evidence: view.deals?.data?.length
                      ? [
                          {
                            entity_type: "deal",
                            entity_id: "d-1",
                            name: "Fleet retrofit 2026",
                          },
                        ]
                      : [],
                  },
                ],
              },
            ]
          : [],
      }),
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
      <div style={{ display: "grid", gap: "var(--space-3)", maxWidth: 420 }}>
        {/* The live page always wires an opener; without one every citation's
            `isOpenable` is false and the named-deal chip below can never
            take the branch that names it (company360.tsx's Citations). */}
        <AccountBrief orgId="o-1" view={view} enabled onOpenRecord={() => {}} />
        <CommercialPanel view={view} />
        <RecentActivityPanel view={view} />
        <PeopleCard view={view} writable orgId="o-1" />
        <DealsCard view={view} />
        <NextSteps view={view} />
      </div>
    </StoryProviders>
  );
}

export const Populated: Story = { render: () => <Cards view={populated} /> };

export const SectionWithheld: Story = {
  render: () => <Cards view={withheld} />,
};

export const NothingYet: Story = { render: () => <Cards view={empty} /> };

// A connected finance source, shaped exactly like companyfinance.stories.tsx's
// own `connected` fixture: two stories reading the same wire shape must not
// drift into two different ideas of what "connected" looks like. The one
// addition is `net_invoiced_lifetime`, which the strip reads beside
// `net_invoiced` (FINANCE_READINGS in company360.tsx); companyfinance's
// fixture never sets it because that card has no lifetime slot to feed.
// Made larger than the trailing-year figure on purpose: lifetime is
// everything this account has ever been billed, so it can never read smaller
// than one year's worth of it.
const connectedFinance: FinanceSummary = {
  organization_id: "o-1",
  state: "connected",
  provider: "offline_demo",
  last_synced_at: "2026-08-10T06:00:00Z",
  net_invoiced_lifetime: { amount_minor: 9_400_000, currency: "EUR" },
  net_invoiced: { amount_minor: 1_864_200, currency: "EUR" },
  open_balance: { amount_minor: 240_000, currency: "EUR" },
  overdue: { amount_minor: 89_000, currency: "EUR" },
  median_days_after_due: 4,
};

// The two lookups below are keyed on the real wire enums (Lifecycle,
// RelationshipType), but StateStrip's own label props take a bare `string` —
// it draws whatever the account's enum happens to be without knowing the
// lookup's key type. A type guard narrows the string to the lookup's key
// rather than casting it, which is what the real caller
// (organizations.tsx's CompanyBand) reaches for with `as` because it already
// knows the value came off `Organization["lifecycle"]`; the strip here has
// no such upstream guarantee to lean on.
function isLifecycleLabelKey(
  value: string,
): value is keyof typeof LIFECYCLE_LABELS {
  return value in LIFECYCLE_LABELS;
}

function isRelationshipTypeLabelKey(
  value: string,
): value is keyof typeof RELATIONSHIP_TYPE_LABELS {
  return value in RELATIONSHIP_TYPE_LABELS;
}

// StateStrip: the record's own KPI row, above the tabs. Withheld is the
// state this gallery exists for — no seeded demo account carries it, so
// this story is the only place it renders. Connected is the other state
// nothing seeded reaches for a story: the demo stack's finance stub always
// answers `no_connection`, so the money figure and the provider name on its
// detail line (FinanceStat) never render anywhere else.
//
// `Strip` itself returns `<StoryProviders>`, so it sits outside the
// LocaleProvider it renders and cannot call `useT` directly; `StripBody`
// is the inner component that mounts inside that context, mirroring the
// real caller's label wiring (organizations.tsx's CompanyBand) rather than
// the identity functions that used to stand in for it and rendered the raw
// wire enum instead of its copy.
function StripBody({ view }: Readonly<{ view?: View }>) {
  const t = useT();
  return (
    <StateStrip
      orgId="o-1"
      view={view}
      lifecycleLabel={(value) =>
        isLifecycleLabelKey(value) ? t(LIFECYCLE_LABELS[value]) : value
      }
      relationshipLabels={(values) =>
        values
          .map((value) =>
            isRelationshipTypeLabelKey(value)
              ? t(RELATIONSHIP_TYPE_LABELS[value])
              : value,
          )
          .join(" · ")
      }
    />
  );
}

function Strip({
  view,
  finance = { organization_id: "o-1", state: "no_connection" },
}: Readonly<{ view?: View; finance?: FinanceSummary }>) {
  installFetchStub({
    // The customer branch's money slots read this directly (FinanceStat) —
    // the same query the finance card runs — so a customer story with
    // nothing stubbed here fires a real request the static build has
    // nowhere to send.
    "GET /organizations/o-1/finance-summary": () => jsonResponse(finance),
  });
  return (
    <StoryProviders>
      {/* Room for the row to use, not a promise about its shape: the strip's
          column count answers to the VIEWPORT, not to this box. company360.css
          flips it to three columns at max-width 68rem (1088px), and the render
          gate shoots at 1024px wide (frontend/scripts/fe-uat.mjs), so the
          captured screenshot is always the three-column fold rather than the
          single row a full-width desktop draws. That fold is a real state and
          worth seeing; the single row is what opening Storybook in a wide
          window shows. */}
      <div style={{ maxWidth: 1200 }}>
        <StripBody view={view} />
      </div>
    </StoryProviders>
  );
}

export const StateStripPopulated: Story = {
  render: () => <Strip view={populated} />,
};

// The customer row with a real accounting connection behind it: the money
// figure and its provider name (FinanceStat's detail line) instead of the
// "connect your accounting" fallback every other story here shows. The
// overdue figure is also what pushes HealthSummaryStat's payment dimension
// to "at_risk" (usePaymentHealth reads the same query), so this is the only
// story where that fourth slot renders too.
export const StateStripConnected: Story = {
  render: () => <Strip view={populated} finance={connectedFinance} />,
};

export const StateStripWithheld: Story = {
  render: () => (
    <Strip
      view={
        {
          ...populated,
          state_strip: undefined,
          sections_omitted: ["state_strip"],
        } as unknown as View
      }
    />
  ),
};
