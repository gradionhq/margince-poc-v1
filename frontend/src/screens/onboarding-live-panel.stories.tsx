// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { OnboardingLivePanel } from "./onboarding-live-panel";
import { StoryProviders } from "./story-utils";

// The live panel across the three states an onboarding reader actually sees:
// mid-read (no cards at all, because nothing is saved yet), the finished read
// with the legal-entity decision still open, and the same read with every
// decision resolved. Every value is a canned CompanySiteRead — the panel
// itself makes no calls.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type ColdStartField = components["schemas"]["ColdStartField"];
type CompanySiteReadFact = components["schemas"]["CompanySiteReadFact"];

function coldField(
  field: ColdStartField["field"],
  value: string,
  confidence: number,
  snippet: string,
  path: string,
): ColdStartField {
  return {
    field,
    value,
    confidence,
    evidence_snippet: snippet,
    source_kind: "url",
    source_url: `https://nordwind-systems.de${path}`,
  };
}

function fact(
  category: CompanySiteReadFact["category"],
  field: CompanySiteReadFact["field"],
  value: string,
): CompanySiteReadFact {
  return {
    category,
    field,
    value,
    value_key: `${category}:${field}:${value}`,
    evidence_snippet: `… ${value} …`,
    evidence_url: "https://nordwind-systems.de/about",
    confidence: 0.82,
  };
}

const READ: CompanySiteRead = {
  id: "22222222-2222-4222-8222-222222222222",
  target_kind: "onboarding",
  root_url: "https://nordwind-systems.de",
  status: "partial",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: null,
  pages: [
    { url: "https://nordwind-systems.de", status: "fetched", kind: "home" },
    {
      url: "https://nordwind-systems.de/impressum",
      status: "fetched",
      kind: "impressum",
    },
    {
      url: "https://nordwind-systems.de/leistungen",
      status: "fetched",
      kind: "services",
    },
    {
      url: "https://nordwind-systems.de/team",
      status: "fetched",
      kind: "team",
    },
    {
      url: "https://nordwind-systems.de/intern",
      status: "skipped",
      kind: "other",
      reason: "robots.txt disallows this path",
    },
    {
      url: "https://nordwind-systems.de/karriere",
      status: "failed",
      kind: "other",
      reason: "the page answered 503 twice",
    },
  ],
  profile_fields: [
    coldField(
      "display_name",
      "Nordwind Systems",
      0.96,
      "Nordwind Systems — Prozesse, die halten.",
      "/",
    ),
    coldField(
      "legal_name",
      "Nordwind Systems GmbH",
      0.94,
      "Nordwind Systems GmbH, Am Hafen 12, 20457 Hamburg",
      "/impressum",
    ),
    coldField(
      "registered_address",
      "Am Hafen 12, 20457 Hamburg",
      0.9,
      "Am Hafen 12, 20457 Hamburg",
      "/impressum",
    ),
    coldField(
      "register_vat",
      "HRB 118 240, DE 271 552 019",
      0.88,
      "Handelsregister HRB 118 240 · USt-IdNr. DE 271 552 019",
      "/impressum",
    ),
    coldField(
      "industry",
      "Industrial software integration",
      0.71,
      "Wir verbinden Maschinen, ERP und Leitstand.",
      "/leistungen",
    ),
    coldField(
      "offer_summary",
      "Fixed-scope MES and ERP integration for mid-sized manufacturers",
      0.89,
      "Feste Pakete für MES- und ERP-Anbindung im Mittelstand.",
      "/leistungen",
    ),
    coldField(
      "icp",
      "German manufacturers with 80–600 staff and their own production planning",
      0.62,
      "Unsere Kunden fertigen mit 80 bis 600 Mitarbeitenden.",
      "/leistungen",
    ),
    coldField(
      "usp",
      "Every integration ships with a rollback plan",
      0.44,
      "Jede Anbindung kommt mit einem Rückfallplan.",
      "/leistungen",
    ),
  ],
  facts: [
    fact("company", "location", "Hamburg"),
    fact("company", "employee_range", "24 staff"),
    fact("offering", "service", "MES integration"),
    fact("offering", "service", "ERP interface audits"),
    fact("market", "served_industry", "Metal fabrication"),
    fact("signal", "certification", "ISO 9001"),
  ],
  comparisons: [],
  people: [
    {
      name: "Katrin Vogel",
      role: "Geschäftsführerin",
      published_email: "vogel@nordwind-systems.de",
      linkedin_url: null,
      evidence_snippet: "Katrin Vogel, Geschäftsführerin",
      evidence_url: "https://nordwind-systems.de/team",
      disposition: "separate_lead_proposal",
    },
  ],
  legal_entities: [
    {
      name: "Nordwind Systems GmbH",
      registered_address: "Am Hafen 12, 20457 Hamburg",
      register_number: "HRB 118 240",
      evidence_snippet: "Nordwind Systems GmbH, Am Hafen 12, 20457 Hamburg",
      source_url: "https://nordwind-systems.de/impressum",
    },
    {
      name: "Nordwind Beteiligungs GmbH",
      registered_address: "Am Hafen 12, 20457 Hamburg",
      register_number: "HRB 104 991",
      evidence_snippet: "Nordwind Beteiligungs GmbH, Am Hafen 12",
      source_url: "https://nordwind-systems.de/impressum",
    },
  ],
  warnings: ["The sitemap listed 31 pages; the read budget covered 6."],
  draft_version: 1,
  proposal_hash: "sha256:7f1c",
  created_at: "2026-07-31T08:00:00Z",
  updated_at: "2026-07-31T08:03:20Z",
};

const meta: Meta<typeof OnboardingLivePanel> = {
  title: "screens/onboarding-live-panel",
  component: OnboardingLivePanel,
  decorators: [
    (Story) => (
      <StoryProviders>
        <div style={{ height: "100vh", maxWidth: 460 }}>
          <Story />
        </div>
      </StoryProviders>
    ),
  ],
  args: {
    host: "nordwind-systems.de",
    onConfirmEntity: () => {},
    onDeclineEntity: () => {},
  },
};
export default meta;

type Story = StoryObj<typeof OnboardingLivePanel>;

// Mid-read: the head says what it is doing and the body says nothing is
// saved. No cards — a half-filled dossier invites corrections to data that
// is still arriving.
export const MidRead: Story = {
  args: {
    done: false,
    read: null,
    entityChoice: null,
    voiceState: "waiting",
    connectState: "waiting",
  },
};

// The read finished and the legal notice named two entities, so the dossier
// opens with the one card that cannot be folded away.
export const OpenDecision: Story = {
  args: {
    done: true,
    read: READ,
    entityChoice: null,
    voiceState: "waiting",
    connectState: "waiting",
  },
};

// Everything resolved: the entity is chosen, so its card collapses into a
// done row, and the conversation has moved on to the voice step.
export const Resolved: Story = {
  args: {
    done: true,
    read: READ,
    entityChoice: "Nordwind Systems GmbH",
    voiceState: "now",
    connectState: "waiting",
  },
};
