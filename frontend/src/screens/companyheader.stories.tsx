// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { screen, userEvent } from "storybook/test";
import type { components } from "../api/schema";
import {
  CompanyActionBadges,
  CompanyDescription,
  CompanyIdentityLine,
  CompanyLifecycleControl,
  CompanyPrimaryActions,
} from "./companyheader";
import {
  installFetchStub,
  jsonResponse,
  meRoute,
  StoryProviders,
} from "./story-utils";

// The account header's own pieces (RecordView's nameBadge/subtitle/pulse/
// actions slots in organizations.tsx), mounted together rather than through
// the whole record page: the header does not own a screen of its own, so
// reaching for it through CompanyScreen would drag in every other tab's reads.

const meta: Meta = {
  title: "Records/Company 360/Header",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Organization = components["schemas"]["Organization"];
type View = components["schemas"]["Organization360"];

const page = { has_more: false, next_cursor: null };

const org = {
  id: "o-1",
  workspace_id: "w-1",
  display_name: "Brandt Automotive GmbH",
  legal_name: "Brandt Automotive GmbH",
  lifecycle: "customer",
  owner_id: "u-1",
  industry: "Automotive",
  size_band: "51-200",
  description: "Retrofits commercial fleets for zero-emission depots.",
  domains: [{ domain: "brandt.example", is_primary: true, source: "manual" }],
  captured_by: "human:u1",
  source: "manual",
  version: 1,
  // formatDateAbbrev(org.created_at) throws RangeError on anything that isn't
  // a real ISO string — an org fixture missing this renders the whole header
  // as nothing rather than a legible date.
  created_at: "2026-06-01T08:00:00Z",
  updated_at: "2026-06-01T08:00:00Z",
} as unknown as Organization;

// The "way in" — the contact the relationship actually runs through — plus a
// last exchange date. Both are withheld together whenever the 360 is still
// loading, so this is the state a reader sees once it lands.
const withWayIn = {
  as_of: "2026-06-01T09:00:00Z",
  organization: org,
  sections_omitted: [],
  strength: {
    score: 71,
    bucket: "strong",
    contact_count: 2,
    contributor_person_id: "p-1",
    factors: { recency: 0.9, frequency: 0.6, reciprocity: 0.8, direction: 0.8 },
  },
  last_inbound_at: "2026-05-28T10:00:00Z",
  last_outbound_at: "2026-05-30T14:00:00Z",
} as unknown as View;

// No contact has yet earned the "way in" — an account with an owner and a
// touch history but nobody who carries the relationship. `strength` is
// present (the 360 always returns it) but empty of a contributor.
const noWayIn = {
  ...withWayIn,
  strength: {
    score: 0,
    bucket: "dormant",
    contact_count: 0,
    contributor_person_id: null,
    factors: { recency: 0, frequency: 0, reciprocity: 0, direction: 0 },
  },
} as unknown as View;

// The roster the owner control reads, and — since the identity line resolves
// `captured_by` against the same `["users"]` entry — the only place a record's
// AUTHOR can be named from. Mira owns the account; Sofia wrote the row, and is
// here so one story can show that second half.
const roster = [
  { id: "u-1", display_name: "Mira Voss" },
  { id: "u-2", display_name: "Sofia Meier" },
];

// `record` overrides the account only where a story is about the RECORD rather
// than the 360 read around it (who wrote it, below). The default keeps every
// other story on the one fixture.
function Header({
  view,
  loading,
  record = org,
}: Readonly<{ view?: View; loading?: boolean; record?: Organization }>) {
  installFetchStub({
    "GET /me": meRoute({ organization: ["read", "update"] }),
    "GET /users": () => jsonResponse({ data: roster, page }),
    "GET /people/p-1": () =>
      jsonResponse({ id: "p-1", full_name: "Dana Buyer" }),
  });
  return (
    <StoryProviders>
      <div style={{ maxWidth: 640 }}>
        <CompanyLifecycleControl org={record} />
        <CompanyDescription org={record} />
        <CompanyIdentityLine org={record} view={view} loading={loading} />
        <div
          style={{
            marginTop: "var(--space-2)",
            display: "flex",
            gap: "var(--space-2)",
          }}
        >
          <CompanyPrimaryActions
            org={record}
            composerOpen={false}
            onComposerOpen={() => {}}
          />
          <CompanyActionBadges
            org={record}
            view={view}
            onOpenHistory={() => {}}
            onSetUpPartner={() => {}}
          />
        </div>
      </div>
    </StoryProviders>
  );
}

// The three stories below all render the quiet line's FALLBACK provenance,
// "typed by a person": the fixture's `captured_by` names `u1`, which is nobody
// the roster answers with, and an author the roster cannot resolve is not named
// with the raw uuid. That is the state a record lands in when its author is
// somebody the roster does not carry, and it stays covered here.
export const WithWayIn: Story = { render: () => <Header view={withWayIn} /> };

export const NoWayIn: Story = { render: () => <Header view={noWayIn} /> };

// Still fetching the composite read: the way-in and the last-exchange dates
// withhold together rather than reading "never contacted" off no answer yet.
export const Loading: Story = {
  render: () => <Header view={withWayIn} loading />,
};

// The other half of the provenance tag, and the state the header did not show
// until the identity line was given the roster: the NAMED author, "typed by
// Sofia Meier", beside the date the record was created. `captured_by` names a
// colleague the roster answers with, and one this viewer is not — the tag reads
// "typed by you" for the reader's own writing.
export const AuthorNamed: Story = {
  render: () => (
    <Header view={withWayIn} record={{ ...org, captured_by: "human:u-2" }} />
  ),
};

// An archived account. Its verbs stay in the menu, refused, over the one
// sentence that says why — a control blocked by the record's STATE is disabled
// with its reason, never dropped (STATE-4a), because a missing button reads as
// a build without the feature. The play() opens the menu, since the refusal is
// the thing worth seeing here.
export const ArchivedAccount: Story = {
  render: () => (
    <Header
      view={withWayIn}
      record={{ ...org, archived_at: "2026-07-13T00:00:00Z" }}
    />
  ),
  play: async () => {
    // The panel portals to document.body, so it is reached through `screen`
    // rather than a canvas-scoped query that would find nothing.
    await userEvent.click(
      await screen.findByRole("button", { name: "More actions" }),
    );
  },
};
