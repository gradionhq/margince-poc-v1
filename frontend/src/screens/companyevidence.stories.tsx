// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import type { components } from "../api/schema";
import { type CitedRecord, EvidenceModal } from "./companyevidence";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// EvidenceModal renders one receipt at a time, keyed by the citation the
// caller passed in (`cited.entityType` + `cited.entityId`). Each story below
// stubs the single GET the drawer issues for that pair, so every fixture
// below is reachable through the same route the real drawer would call.

const meta: Meta = {
  title: "Screens/Company evidence",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;
type Receipt = components["schemas"]["ClaimEvidence"];

function evidencePath(cited: CitedRecord): string {
  return `GET /organizations/o-1/evidence/${cited.entityType}/${cited.entityId}`;
}

function Drawer({
  cited,
  receipt,
  onStep,
}: Readonly<{
  cited: CitedRecord;
  receipt?: Receipt;
  onStep?: (direction: -1 | 1) => void;
}>) {
  installFetchStub({
    [evidencePath(cited)]: () => jsonResponse(receipt ?? {}),
  });
  return (
    <StoryProviders>
      <EvidenceModal
        orgId="o-1"
        cited={cited}
        onClose={() => {}}
        onStep={onStep}
      />
    </StoryProviders>
  );
}

// site_read: the model-extraction kind. Carries a confidence score (the only
// kind allowed one — DOSS-AC-16), the verbatim excerpt it was read from, and
// a `source_url` identity field that renders as a followable link. No
// `last_verified_at`, so the "AI extracted, not yet confirmed" badge shows —
// this is the state that badge exists for.
export const SiteReadUnconfirmed: Story = {
  render: () => (
    <Drawer
      cited={{ entityType: "organization", entityId: "org-1" }}
      receipt={{
        entity_type: "organization",
        entity_id: "org-1",
        source_kind: "site_read",
        label: "Industry",
        value: "Automotive",
        excerpt: "Brandt Automotive GmbH is a fleet electrification partner.",
        identity: { source_url: "https://brandt.example/about" },
        retrieved_at: "2026-06-01T08:00:00Z",
        confidence: 0.82,
        produced_by: "extraction:site-read-v3",
      }}
    />
  ),
};

// human: a person's own assertion, so `last_verified_at` is set and no
// confidence prints (a human value never carries a model score). Also the
// story that exercises the prev/next steps — the ordering belongs to the
// citing card, not the drawer, so `onStep` is the only thing that turns the
// arrows on.
export const HumanConfirmed: Story = {
  render: () => (
    <Drawer
      cited={{ entityType: "profile_field", entityId: "pf-1" }}
      onStep={() => {}}
      receipt={{
        entity_type: "profile_field",
        entity_id: "pf-1",
        source_kind: "human",
        label: "Decision maker",
        value: "Anke Brandt, Head of Fleet",
        identity: {
          actor: "u-mira-voss",
          confirmed_at: "2026-07-02T10:00:00Z",
        },
        last_verified_at: "2026-07-02T10:00:00Z",
        produced_by: "human:u-mira-voss",
      }}
    />
  ),
};

// connector: a value read verbatim out of a provider API. Its identity names
// the provider and the external record so the reader could go check the
// same account there.
export const Connector: Story = {
  render: () => (
    <Drawer
      cited={{ entityType: "fact", entityId: "f-1" }}
      receipt={{
        entity_type: "fact",
        entity_id: "f-1",
        source_kind: "connector",
        label: "Employee count",
        value: "128",
        identity: {
          provider: "clearbit",
          external_record: "brandt-automotive",
        },
        retrieved_at: "2026-06-15T09:30:00Z",
        produced_by: "connector:clearbit",
      }}
    />
  ),
};

// migration: carried in by an older system's import rather than read live —
// not in the spec's own provenance vocabulary, but one of the four values
// migration 0099 actually stores, so the badge and label still have to be
// right for it.
export const Migration: Story = {
  render: () => (
    <Drawer
      cited={{ entityType: "fact", entityId: "f-2" }}
      receipt={{
        entity_type: "fact",
        entity_id: "f-2",
        source_kind: "migration",
        label: "Legacy account id",
        value: "ACC-04471",
        identity: { import_batch: "2024-crm-import" },
        produced_by: "migration:crm-import-2024",
      }}
    />
  ),
};

// rule: computed by code, not read or asserted. Gaps also show here — a
// field the rule owes but could not fill, named rather than left blank so a
// missing input reads as missing rather than as nothing to report.
export const RuleWithGaps: Story = {
  render: () => (
    <Drawer
      cited={{ entityType: "fact", entityId: "f-3" }}
      receipt={{
        entity_type: "fact",
        entity_id: "f-3",
        source_kind: "rule",
        label: "Growth fit band",
        value: "moderate",
        gaps: ["revenue_band", "decision_maker_confirmed"],
        produced_by: "rule:growth-fit-v2",
      }}
    />
  ),
};

function PendingDrawer() {
  installFetchStub({
    "GET /organizations/o-1/evidence/organization/org-1": () =>
      new Promise<Response>(() => {}),
  });
  return (
    <StoryProviders>
      <EvidenceModal
        orgId="o-1"
        cited={{ entityType: "organization", entityId: "org-1" }}
        onClose={() => {}}
      />
    </StoryProviders>
  );
}

// Pending: the fetch never settles, so the drawer stays on the Skeleton
// placeholder — the state a reader sees for the moment between opening the
// drawer and the receipt arriving.
export const Pending: Story = {
  render: () => <PendingDrawer />,
};

// Unavailable: the drawer opened for a citation that has no receipt behind
// it. The component treats "the server had nothing" and "the request
// failed" identically here (both leave `shown?.source_kind` unset), so one
// story stands for both — a claim that was told to be checkable and turns
// out uncheckable either way should read the same to the person checking it.
export const Unavailable: Story = {
  render: () => (
    <Drawer
      cited={{ entityType: "organization", entityId: "org-missing" }}
      receipt={undefined}
    />
  ),
};
