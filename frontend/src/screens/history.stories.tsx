// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { RecordHistoryTab } from "./history";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// RecordHistoryTab (B-EP09.x) reads through two endpoints depending on the
// SegmentedControl toggle — GET /records/{entity_type}/{id}/history (Changes)
// and GET /field-history (Field history) — both carrying an :id/query-param
// path openapi-fetch resolves at request time, so exact-key stubbing can't
// pin them; every story here answers via installFetchStub's `fallback`,
// which routes any unmatched GET to the fixture the story cares about.
const meta: Meta = {
  title: "Screens/History",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

function seedWorkspace() {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
}

const created = {
  id: "h1",
  actor_type: "human",
  actor_id: "u1",
  action: "create",
  occurred_at: "2026-07-13T10:00:00Z",
  summary: "Demo Admin created the record",
};
const updated = {
  id: "h2",
  actor_type: "agent",
  actor_id: "sdr",
  on_behalf_of_name: "Anna Weber",
  action: "update",
  occurred_at: "2026-07-14T10:00:00Z",
  summary: "Overnight agent updated the record",
};

export const Changes: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({}, () =>
      jsonResponse({
        data: [created, updated],
        page: { next_cursor: null, has_more: false },
      }),
    );
    return (
      <StoryProviders>
        <RecordHistoryTab kind="deal" id="d1" />
      </StoryProviders>
    );
  },
};

const fhCreated = {
  id: "f0",
  entity_type: "deal",
  entity_id: "d1",
  field: "name",
  old_value: null,
  new_value: "Globex Renewal",
  changed_at: "2026-07-13T10:00:00Z",
  actor_type: "human",
  actor_id: "u1",
};
const fhUpdated = {
  id: "f1",
  entity_type: "deal",
  entity_id: "d1",
  field: "name",
  old_value: "Globex Renewal",
  new_value: "Globex Renewal (updated)",
  changed_at: "2026-07-14T10:00:00Z",
  actor_type: "agent",
  actor_id: "sdr",
  passport_id: "psp_7Q3fa91",
  evidence: { snippet: "renewal signed", source: "email#42" },
};

// The field-history fixture answers both endpoints the same way so the
// story reads correctly whichever tab is active on mount, then the user can
// click "Field history" to see the old→new diff grouping directly.
export const FieldDiffs: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({}, () =>
      jsonResponse({
        data: [fhUpdated, fhCreated],
        page: { next_cursor: null, has_more: false },
      }),
    );
    return (
      <StoryProviders>
        <RecordHistoryTab kind="deal" id="d1" />
      </StoryProviders>
    );
  },
};

export const Empty: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({}, () =>
      jsonResponse({ data: [], page: { next_cursor: null, has_more: false } }),
    );
    return (
      <StoryProviders>
        <RecordHistoryTab kind="deal" id="d1" />
      </StoryProviders>
    );
  },
};

export const ErrorState: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({}, () => jsonResponse({ title: "boom" }, 500));
    return (
      <StoryProviders>
        <RecordHistoryTab kind="deal" id="d1" />
      </StoryProviders>
    );
  },
};

// A field-history entry carrying passport_id + evidence — the agent-
// attribution surface (PassportChip + EvidenceChip) on the field-diff view.
export const AgentAttribution: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({}, () =>
      jsonResponse({
        data: [fhUpdated],
        page: { next_cursor: null, has_more: false },
      }),
    );
    return (
      <StoryProviders>
        <RecordHistoryTab kind="deal" id="d1" />
      </StoryProviders>
    );
  },
};
