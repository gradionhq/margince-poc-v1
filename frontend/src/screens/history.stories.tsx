// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { RecordHistoryTab } from "./history";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

// RecordHistoryTab (B-EP09.x) reads through two endpoints depending on the
// SegmentedControl toggle — GET /records/{entity_type}/{id}/history (Changes,
// the default tab on mount) and GET /field-history (Field history) — both
// resolving to a static pathname for the kind="deal" id="d1" every story
// below hardcodes, so each route is stubbed explicitly by its own key. A
// blanket fallback would answer the Changes-tab mount with field-history-
// shaped fixtures (`changed_at`, no `occurred_at`), which crashes
// formatDateTime on an Invalid time value — this keeps each endpoint's
// response shaped for the schema it actually is.
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
    installFetchStub({
      "GET /records/deal/d1/history": () =>
        jsonResponse({
          data: [created, updated],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /field-history": () => jsonResponse(emptyPage),
    });
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

// The record-history fixture is a single unrelated "name field touched"
// entry — the story mounts on the Changes tab first (no initialTab prop on
// the component), then the reviewer clicks "Field history" to see the
// old→new diff grouping this story is actually named for.
export const FieldDiffs: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({
      "GET /records/deal/d1/history": () =>
        jsonResponse({
          data: [updated],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /field-history": () =>
        jsonResponse({
          data: [fhUpdated, fhCreated],
          page: { next_cursor: null, has_more: false },
        }),
    });
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
    installFetchStub({
      "GET /records/deal/d1/history": () => jsonResponse(emptyPage),
      "GET /field-history": () => jsonResponse(emptyPage),
    });
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
    // The Changes tab is what's shown on mount, so that's the route the
    // error has to come back on for the story to demonstrate the error
    // state; field-history stays healthy in case the reviewer switches tabs.
    installFetchStub({
      "GET /records/deal/d1/history": () =>
        jsonResponse({ title: "boom" }, 500),
      "GET /field-history": () => jsonResponse(emptyPage),
    });
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
    installFetchStub({
      "GET /records/deal/d1/history": () =>
        jsonResponse({
          data: [updated],
          page: { next_cursor: null, has_more: false },
        }),
      "GET /field-history": () =>
        jsonResponse({
          data: [fhUpdated],
          page: { next_cursor: null, has_more: false },
        }),
    });
    return (
      <StoryProviders>
        <RecordHistoryTab kind="deal" id="d1" />
      </StoryProviders>
    );
  },
};
