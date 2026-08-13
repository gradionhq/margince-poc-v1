// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import type { components } from "../api/schema";
import { CompanyContextCard, ManualCompanySetup } from "./company-context";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// Two surfaces the company-context rollout shares one read hook between:
// ManualCompanySetup is the rollback-safe floor below the `onboarding` stage
// and never calls useCompanyContextCapabilities, so it needs no capability
// stub at all. CompanyContextCard sits above the rollout gate: its own
// docblock withholds itself entirely (renders null) once the capability
// answer comes back `read_enabled: false`, so a granted rollout has to be
// stubbed explicitly or every story below would render nothing.

const meta: Meta = {
  title: "Screens/Company context",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type CompanyProfile = components["schemas"]["CompanyProfile"];
type Capabilities = components["schemas"]["CompanyContextCapabilities"];
type SiteRead = components["schemas"]["CompanySiteRead"];

const READ_ENABLED: Capabilities = {
  rollout: "read",
  read_enabled: true,
  tasks_enabled: false,
  onboarding_enabled: false,
};

const READ_DISABLED: Capabilities = {
  rollout: "off",
  read_enabled: false,
  tasks_enabled: false,
  onboarding_enabled: false,
};

// A company that has been through the five-field onboarding flow once and
// then had its website read confirmed at least one round: some fields carry
// human provenance (typed once and never revisited), others site_read (the
// confirmed refresh left its source URL behind so a reviewer can re-check it).
const POPULATED_PROFILE: CompanyProfile = {
  organization_id: "o-1",
  display_name: "Nordlicht Logistics GmbH",
  website: "nordlicht-logistics.example",
  legal_name: "Nordlicht Logistics GmbH",
  registered_address: "Speicherstraße 4, 20457 Hamburg",
  register_vat: "DE291837465",
  industry: "Logistics",
  offer_summary: "Same-day freight forwarding across the Nordic corridor.",
  icp: "Mid-market manufacturers shipping palletised freight across borders weekly.",
  value_proposition: "Door-to-door in one booking, tracked at every handover.",
  usp: "The only forwarder with its own Nordic fleet, not a broker's.",
  customer_pains: "Freight brokers who can't say where a pallet actually is.",
  desired_outcomes:
    "Predictable transit times a planner can put in a contract.",
  buying_center: "Logistics director, procurement lead.",
  buying_intents: "RFQ for a new lane, dissatisfaction with current carrier.",
  common_objections: '"Our current forwarder is cheaper per pallet."',
  sales_motion: "Field sales, quoted per lane.",
  history: "Founded 2011 as a regional haulier, added Nordic sea legs in 2018.",
  minimum_complete: true,
  updated_at: "2026-07-01T09:00:00Z",
  fields: [
    {
      field: "display_name",
      value: "Nordlicht Logistics GmbH",
      source: "human",
      captured_by: "human:u-1",
      updated_at: "2026-05-10T10:00:00Z",
    },
    {
      field: "offer_summary",
      value: "Same-day freight forwarding across the Nordic corridor.",
      source: "site_read",
      captured_by: "system:site-read",
      source_url: "https://nordlicht-logistics.example/services",
      updated_at: "2026-07-01T09:00:00Z",
    },
    {
      field: "icp",
      value:
        "Mid-market manufacturers shipping palletised freight across borders weekly.",
      source: "human",
      captured_by: "human:u-1",
      updated_at: "2026-05-10T10:00:00Z",
    },
  ],
};

// The onboarding minimum, right after the semantic-minimum three fields were
// typed and nothing else has ever been confirmed: no website, no provenance
// badges, every optional group blank. This is what a brand-new workspace's
// card looks like the first time a reviewer opens Settings.
const EMPTY_PROFILE: CompanyProfile = {
  organization_id: "o-2",
  display_name: "Havbris AS",
  offer_summary: "Coastal ferry maintenance contracts.",
  icp: "Municipal ferry operators in the Norwegian fjords.",
  minimum_complete: true,
  updated_at: "2026-06-20T08:00:00Z",
  fields: [],
};

function ManualSetup() {
  return (
    <StoryProviders>
      <ManualCompanySetup />
    </StoryProviders>
  );
}

export const ManualSetupDefault: Story = {
  render: () => <ManualSetup />,
};

// The reviewer fills the semantic minimum and submits, but the workspace
// PUT fails server-side (a duplicate domain, a validation the client can't
// see), the one branch that shows the form's own error paragraph rather
// than a disabled button.
export const ManualSetupSaveFailed: Story = {
  render: () => {
    installFetchStub({
      "PUT /company": () =>
        jsonResponse(
          { title: "A workspace already exists for this domain." },
          409,
        ),
    });
    return <ManualSetup />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.type(canvas.getByLabelText("Company name"), "Havbris AS");
    await userEvent.type(
      canvas.getByLabelText("What do you sell?"),
      "Coastal ferry maintenance contracts.",
    );
    await userEvent.type(
      canvas.getByLabelText("Ideal customer"),
      "Municipal ferry operators.",
    );
    await userEvent.click(
      canvas.getByRole("button", { name: /Create company context/ }),
    );
    await canvas.findByText(/already exists for this domain/);
  },
};

function Card() {
  return (
    <StoryProviders>
      <CompanyContextCard />
    </StoryProviders>
  );
}

export const Populated: Story = {
  render: () => {
    installFetchStub({
      "GET /company/context/capabilities": () => jsonResponse(READ_ENABLED),
      "GET /company": () => jsonResponse(POPULATED_PROFILE),
    });
    return <Card />;
  },
};

export const Empty: Story = {
  render: () => {
    installFetchStub({
      "GET /company/context/capabilities": () => jsonResponse(READ_ENABLED),
      "GET /company": () => jsonResponse(EMPTY_PROFILE),
    });
    return <Card />;
  },
};

// The company query never resolves: QueryGate's shared skeleton, not a
// bespoke one this card invented for itself.
export const Loading: Story = {
  render: () => {
    installFetchStub({
      "GET /company/context/capabilities": () => jsonResponse(READ_ENABLED),
      "GET /company": () => new Promise<Response>(() => {}),
    });
    return <Card />;
  },
};

// The company read fails outright (workspace not provisioned yet, a
// transient 500), QueryGate's EmptyState-with-retry, the same shape every
// query-backed screen falls back to.
export const Failed: Story = {
  render: () => {
    installFetchStub({
      "GET /company/context/capabilities": () => jsonResponse(READ_ENABLED),
      "GET /company": () => jsonResponse({ title: "company unreadable" }, 500),
    });
    return <Card />;
  },
};

// The rollout answer comes back closed: the card withholds itself entirely
// rather than rendering a denied state, per its own docblock. There is
// nothing to look at here on purpose: the story exists so a reader can
// confirm that "withheld" really means an empty canvas, not a stray error.
export const CapabilityDenied: Story = {
  render: () => {
    installFetchStub({
      "GET /company/context/capabilities": () => jsonResponse(READ_DISABLED),
      "GET /company": () => jsonResponse(POPULATED_PROFILE),
    });
    return <Card />;
  },
};

// One read the review area proposes against the current profile, hitting
// all four comparison classes at once: `new` (a fact the site said and the
// profile never had), `machine_change` (the site now disagrees with a
// site_read-sourced field, so no human ever asserted the old value),
// `human_conflict` (the site disagrees with a HUMAN-sourced field, which
// only a person may resolve), and `unchanged` (confirms the read actually
// looked, not just proposed). The pending resolution on the conflict row is
// what keeps "Apply selected changes" disabled: the honest state for a read
// nobody has finished reviewing yet.
const REVIEW_READ: SiteRead = {
  id: "sr-1",
  target_kind: "onboarding",
  organization_id: "o-1",
  root_url: "https://nordlicht-logistics.example",
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  stopped_reason: null,
  phase: null,
  pages_read: 4,
  pages: [
    {
      url: "https://nordlicht-logistics.example",
      status: "fetched",
      kind: "home",
    },
    {
      url: "https://nordlicht-logistics.example/services",
      status: "fetched",
      kind: "services",
    },
    {
      url: "https://nordlicht-logistics.example/about",
      status: "fetched",
      kind: "about",
    },
    {
      url: "https://nordlicht-logistics.example/impressum",
      status: "skipped",
      kind: "impressum",
      reason: "robots.txt disallow",
    },
  ],
  profile_fields: [],
  facts: [
    {
      category: "company",
      field: "employee_range",
      value: "51-200",
      value_key: "fact/employee_range",
      evidence_snippet: "Our team of over 120 people spans three ports.",
      evidence_url: "https://nordlicht-logistics.example/about",
      confidence: 0.82,
    },
  ],
  comparisons: [
    {
      key: "employee_range",
      value_kind: "fact",
      classification: "new",
      current_value: null,
      current_source: null,
      proposed_value: "51-200",
    },
    {
      key: "offer_summary",
      value_kind: "profile_field",
      classification: "machine_change",
      current_value: "Same-day freight forwarding across the Nordic corridor.",
      current_source: "site_read",
      proposed_value:
        "Same-day and next-day freight forwarding across the Nordic corridor.",
    },
    {
      key: "icp",
      value_kind: "profile_field",
      classification: "human_conflict",
      current_value:
        "Mid-market manufacturers shipping palletised freight across borders weekly.",
      current_source: "human",
      proposed_value:
        "Any manufacturer shipping palletised freight in Northern Europe.",
    },
    {
      key: "display_name",
      value_kind: "profile_field",
      classification: "unchanged",
      current_value: "Nordlicht Logistics GmbH",
      current_source: "human",
      proposed_value: "Nordlicht Logistics GmbH",
    },
  ],
  people: [],
  legal_entities: [],
  warnings: [],
  draft_version: 1,
  proposal_hash: "hash-sr-1",
  created_at: "2026-08-01T09:00:00Z",
  updated_at: "2026-08-01T09:02:00Z",
};

export const RefreshReview: Story = {
  render: () => {
    installFetchStub({
      "GET /company/context/capabilities": () => jsonResponse(READ_ENABLED),
      "GET /company": () => jsonResponse(POPULATED_PROFILE),
      "POST /company/site-reads": () => jsonResponse(REVIEW_READ),
      "GET /company/site-reads/sr-1": () => jsonResponse(REVIEW_READ),
    });
    return <Card />;
  },
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      canvas.getByRole("button", { name: /Refresh from website/ }),
    );
    await canvas.findByText("Review what changed");
  },
};
