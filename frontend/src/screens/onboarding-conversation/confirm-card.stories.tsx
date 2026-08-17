// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { useState } from "react";
import type { components } from "../../api/schema";
import {
  type CompanyDraft,
  changeDraftField,
  EMPTY_DRAFT,
  REQUIRED_FIELDS,
} from "../onboarding";
import { StoryProviders } from "../story-utils";
import type { ClarifyAnswer } from "./company-proposal";
import { CompanyConfirmCard } from "./confirm-card";

// The review board the company act hands the reader once the read settles.
// The card takes no data of its own — every state it can be in is named by a
// prop — so the stories differ only in those props: the board as it arrives,
// the two in-flight states that disable Continue for different reasons, a
// failed save, and the proposal-only render where no read ever ran.
//
// The card reaches for no endpoint (the thread beside it owns the session
// probe), so StoryProviders' locale + query context is the whole harness.

type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

const SITE = "https://gradion.test";

const proposal: Proposal = {
  ready: true,
  fields: [
    {
      field: "display_name",
      value: "Gradion",
      confidence: 0.94,
      evidence_snippet: "Gradion — revenue software for industrial companies",
      source_url: `${SITE}/`,
    },
    {
      field: "offer_summary",
      value: "Revenue software for industrial companies",
      confidence: 0.71,
      evidence_snippet:
        "We turn fragmented relationship data into coordinated revenue action.",
      source_url: `${SITE}/product`,
    },
    {
      field: "industry",
      value: "Logistics software",
      confidence: 0.29,
      evidence_snippet: "We build software for freight forwarders.",
      source_url: `${SITE}/about`,
    },
  ],
  facts: [
    {
      category: "company",
      field: "location",
      value: "Munich, Germany",
      value_key: "location:munich-germany",
      evidence_snippet: "Our team works from Munich.",
      evidence_url: `${SITE}/about`,
      confidence: 0.88,
    },
    {
      category: "offering",
      field: "service",
      value: "Pipeline coaching",
      value_key: "service:pipeline-coaching",
      evidence_snippet: "Pipeline coaching for mid-market sales teams.",
      evidence_url: `${SITE}/product`,
      confidence: 0.62,
    },
    {
      category: "market",
      field: "served_industry",
      value: "Freight forwarding",
      value_key: "served_industry:freight-forwarding",
      evidence_snippet: "Trusted by freight forwarders across the EU.",
      evidence_url: `${SITE}/customers`,
      confidence: 0.55,
    },
  ],
  open_questions: [],
  remaining_required_fields: ["icp"],
  draft_version: 1,
  proposal_hash: "hash",
};

// A read that reached the imprint page and still came back without the legal
// trio: that is what lets the board name those rows as omitted with a reason
// it can actually support, rather than leaving three unexplained blank boxes.
const siteRead: CompanySiteRead = {
  id: "018f3a1b-0000-7000-8000-0000000000b2",
  target_kind: "onboarding",
  root_url: SITE,
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  pages: [
    { url: `${SITE}/`, status: "fetched", kind: "home" },
    { url: `${SITE}/product`, status: "fetched" },
    { url: `${SITE}/impressum`, status: "fetched", kind: "impressum" },
  ],
  profile_fields: [],
  facts: [],
  comparisons: [],
  people: [
    {
      name: "Luitpold Alexander",
      role: "Managing Director",
      published_email: "office@gradion.test",
      linkedin_url: null,
      evidence_snippet: "Luitpold Alexander, Managing Director",
      evidence_url: `${SITE}/impressum`,
    },
  ],
  warnings: [],
  draft_version: 1,
  proposal_hash: "hash",
  created_at: "2026-08-05T09:00:00Z",
  updated_at: "2026-08-05T09:02:00Z",
};

// One required field still open (icp), so the board arrives with real work on
// it: a blocking count in the nav, a named to-do under its section, and a
// Continue the reader has to earn.
const startingDraft: CompanyDraft = {
  ...EMPTY_DRAFT,
  values: {
    ...EMPTY_DRAFT.values,
    display_name: "Gradion",
    website: "gradion.test",
    offer_summary: "Revenue software for industrial companies",
    industry: "Logistics software",
  },
};

// A question the reader declined themselves — the board owes them a plain
// sentence naming it, since nothing was written to the field.
const answers: readonly ClarifyAnswer[] = [
  {
    clarifyId: "clarify:history:1",
    field: "history",
    value: "",
    dismissed: true,
  },
];

// Editable, because the board is a work surface: the value a story types has
// to land somewhere for the row's state, the section badge and the Continue
// gate to move with it. The draft writes through `changeDraftField`, the same
// function the company act uses, so a story never invents its own draft rules.
function ReviewBoard({
  read = siteRead,
  pending = false,
  authorizing = false,
  error = null,
}: Readonly<{
  read?: CompanySiteRead | null;
  pending?: boolean;
  authorizing?: boolean;
  error?: string | null;
}>) {
  const [draft, setDraft] = useState<CompanyDraft>(startingDraft);
  const [selectedFactKeys, setSelectedFactKeys] = useState<string[]>([]);
  const missingRequired = REQUIRED_FIELDS.filter(
    (field) => draft.values[field].trim() === "",
  );
  return (
    <StoryProviders>
      <CompanyConfirmCard
        proposal={proposal}
        draft={draft}
        answers={answers}
        read={read}
        selectedFactKeys={selectedFactKeys}
        setSelectedFactKeys={setSelectedFactKeys}
        missingRequired={missingRequired}
        setField={(field, value) =>
          setDraft((current) => changeDraftField(current, field, value))
        }
        onAcceptAll={() => {}}
        pending={pending}
        authorizing={authorizing}
        error={error}
      />
    </StoryProviders>
  );
}

const meta: Meta<typeof ReviewBoard> = {
  title: "Onboarding/Company confirm card",
  component: ReviewBoard,
  parameters: { layout: "fullscreen" },
};
export default meta;

type Story = StoryObj<typeof ReviewBoard>;

// The board as the read leaves it: identity card, four field sections plus
// the people and facts the crawl found, one required gap still open.
export const FieldsToConfirm: Story = {
  render: () => <ReviewBoard />,
};

// The confirmation is on the wire. Continue names the save rather than the
// step, and stays disabled until the answer lands.
export const Saving: Story = {
  render: () => <ReviewBoard pending />,
};

// A clarify authorization is still in flight. Nothing about the board's own
// state blocks Continue here — the reader may have filled every required
// field — but accepting has to wait for the answer the server is recording.
export const Authorizing: Story = {
  render: () => <ReviewBoard authorizing />,
};

// The save came back refused. Margince says so in its own voice, with the
// server's safe detail carried as a param rather than dropped on the card as
// a bare string.
export const SaveFailed: Story = {
  render: () => (
    <ReviewBoard error="the draft version has moved on since this review opened" />
  ),
};

// The proposal-only render: no read backs this board, so there is no people
// section, no coverage card, and — the honesty that matters — no omission
// notice on the legal rows. Nothing looked for them, so nothing may claim
// they were missing.
export const WithoutRead: Story = {
  render: () => <ReviewBoard read={null} />,
};
