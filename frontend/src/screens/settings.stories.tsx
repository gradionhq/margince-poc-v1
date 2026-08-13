// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { pickOption } from "../design-system/select-testing";
import { AuditLogCard, PipelinesCard, SettingsScreen } from "./settings";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The settings entries and the cards each one carries. Every story installs the
// fetch stub those cards read through, so the render is deterministic and
// network-free — the same fixture shapes the settings.test.tsx cases use.
//
// An organization entry is only reachable when the principal holds what its
// cards ask for, and SettingsScreen falls back to Account for anything else. So
// a story about such an entry has to name its grants: `me({...})` builds the
// /me body that opens the entry the story is capturing.

const me =
  (allow: GrantSpec = {}) =>
  () =>
    jsonResponse({
      ...meFixture({ roles: ["admin"], allow }),
      // id matches the audit fixture's human actor so the Privacy story reads
      // "You" for the viewer's own entry (ActorTag resolves it via meUserId).
      user: { ...meFixture().user, id: "u-mor", email: "ada@acme.test" },
    });

const passports = () =>
  jsonResponse({
    data: [
      {
        id: "pp-1",
        label: "Scout",
        scopes: ["read", "draft"],
        created_at: "2026-07-01T08:00:00Z",
        expires_at: "2026-10-01T08:00:00Z",
        revoked_at: null,
      },
    ],
    page: { next_cursor: null, has_more: false },
  });

// IT-1 governed tool console: two tools of differing tier/egress, plus a
// read-only passport so the play() below can show the send_email row dim
// (its "send" scope isn't in the selected passport's grant). Both live on the
// personal "Your agents" entry, which no grant gates.
const tools = () =>
  jsonResponse({
    data: [
      {
        name: "search_records",
        required_scope: "read",
        tier: "auto_execute",
        egress: false,
      },
      {
        name: "send_email",
        required_scope: "send",
        tier: "confirmation_required",
        egress: true,
      },
    ],
  });

const auditLog = () =>
  jsonResponse({
    data: [
      {
        id: "a1",
        occurred_at: "2026-07-10T14:09:00Z",
        actor_type: "human",
        actor_id: "u-mor",
        action: "create",
        entity_type: "custom_field",
        entity_id: "cf-1",
      },
      {
        id: "a2",
        occurred_at: "2026-07-10T09:41:00Z",
        actor_type: "agent",
        actor_id: "sdr",
        action: "update",
        entity_type: "deal",
        entity_id: "d-1",
      },
    ],
    page: { next_cursor: null, has_more: false },
  });

function tab(tabId: string, routes: RouteMap) {
  return () => {
    installFetchStub(routes);
    return (
      <StoryProviders>
        <SettingsScreen tab={tabId} />
      </StoryProviders>
    );
  };
}

const meta: Meta<typeof SettingsScreen> = {
  title: "Screens/settings",
  component: SettingsScreen,
};
export default meta;

type Story = StoryObj<typeof SettingsScreen>;

export const AccountTab: Story = {
  render: tab("account", { "GET /me": me() }),
};

// Theme and language sit on this tab because they belong to the person, not to
// the sidebar. The play() opens the language listbox so the capture carries the
// options rather than only the control's closed face.
export const AccountPreferences: Story = {
  render: tab("account", { "GET /me": me() }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await userEvent.click(
      await canvas.findByRole("combobox", { name: "Language" }),
    );
  },
};

// The person's own agent authority: the autonomy table, the passports they have
// minted, the clients holding one, and the tools those credentials reach.
export const AgentsTab: Story = {
  render: tab("agents", { "GET /me": me(), "GET /passports": passports }),
};

// AS-2 kill-switch: PassportCard revoke is a hard DELETE behind a ConfirmModal.
// Mirrors share.stories' revoke play() — render the card with a live
// (non-revoked) passport, click Revoke, leave the confirm modal open so the
// guarded state is what the render gate captures.
export const PassportRevokeConfirm: Story = {
  render: tab("agents", { "GET /me": me(), "GET /passports": passports }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    const revokeButton = await canvas.findByRole("button", { name: "Revoke" });
    await userEvent.click(revokeButton);
  },
};

// The governed tool console renders the inventory unfiltered by default,
// then dims the send_email row once the read-only "Scout" passport (whose
// only granted scope is "read") is selected — its required "send" scope
// is absent from that grant.
export const AgentToolConsole: Story = {
  render: tab("agents", {
    "GET /me": me(),
    "GET /passports": passports,
    "GET /agent-tools": tools,
  }),
  play: async ({ canvasElement }) => {
    const canvas = within(canvasElement);
    await canvas.findByText("search_records");
    // The listbox is portalled to the body, outside this story's canvas, so the
    // pick goes through the shared helper rather than a canvas-scoped query.
    await pickOption(
      userEvent.setup(),
      canvas.getByRole("combobox", { name: "All passports" }),
      "Reachable by Scout",
    );
  },
};

// The shape a record takes, on one page: the field editor, the pipeline
// designer, the product list and the offer templates. The custom_field write is
// what opens the entry — the four surfaces used to be three separate screens
// behind door-cards, and the doors are gone.
export const DataModelTab: Story = {
  render: tab("data-model", {
    "GET /me": me({ custom_field: ["create", "update"] }),
  }),
};

// The consent registry and the audit trail on one page: the trail is what proves
// the surfaces above it were honoured, so it moved here from a tab of its own.
export const PrivacyTab: Story = {
  render: tab("privacy", { "GET /me": me(), "GET /audit-log": auditLog }),
};

// PipelinesCard (D-8, on the Data model entry) reads GET /me (roles →
// pipeline grant) and GET /pipelines. Rendered directly here so
// the admin write affordances vs the rep read-only state each get a story.
const pipelinesFixture = {
  data: [
    {
      id: "pl",
      name: "Sales",
      is_default: true,
      position: 0,
      stages: [
        {
          id: "s1",
          pipeline_id: "pl",
          name: "Qualify",
          position: 1,
          semantic: "open",
          win_probability: 20,
        },
        {
          id: "s2",
          pipeline_id: "pl",
          name: "Proposal",
          position: 2,
          semantic: "open",
          win_probability: 50,
        },
        {
          id: "s3",
          pipeline_id: "pl",
          name: "Won",
          position: 3,
          semantic: "won",
          win_probability: 100,
        },
      ],
    },
  ],
  page: { next_cursor: null, has_more: false },
};

const pipelineMe = (allow: GrantSpec) =>
  jsonResponse({
    ...meFixture({ allow }),
    user: { ...meFixture().user, id: "u-1", display_name: "Me" },
  });

// useMe() fails fast without a workspace slug, collapsing the admin state into
// read-only — seed the slug so /me resolves and the affordances render.
function pipelinesCard(allow: GrantSpec) {
  return () => {
    globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
    installFetchStub({
      "GET /me": () => pipelineMe(allow),
      "GET /pipelines": () => jsonResponse(pipelinesFixture),
    });
    return (
      <StoryProviders>
        <PipelinesCard />
      </StoryProviders>
    );
  };
}

export const PipelinesAdmin: Story = {
  render: pipelinesCard({ pipeline: ["read", "create", "update"] }),
};

export const PipelinesReadOnly: Story = {
  render: pipelinesCard({ pipeline: ["read"] }),
};

// AuditLogCard (AO-3/AO-4): one entry carrying a full before/after diff plus
// the agent attribution trail (passport, on-behalf-of human, authorization
// rule, grounding evidence), collapsed by default — the expand toggle is
// what a reviewer exercises to confirm the panel renders honestly.
const auditLogPage = {
  data: [
    {
      id: "al-1",
      actor_type: "agent",
      actor_id: "agent:sdr",
      passport_id: "pp-9",
      on_behalf_of: "u-1",
      action: "update",
      entity_type: "person",
      entity_id: "p-1",
      before: { stage: "new" },
      after: { stage: "qualified" },
      authorization_rule: "role:admin",
      evidence: { snippet: "Reply confirmed budget", source: "email:msg-1" },
      occurred_at: "2026-07-10T09:00:00Z",
    },
  ],
  page: { next_cursor: null, has_more: false },
};

const auditLogMe = (roles: string[]) =>
  jsonResponse({ user: { id: "u-1", display_name: "Me" }, roles, teams: [] });

export const AuditLog: Story = {
  render: () => {
    globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
    installFetchStub({
      "GET /me": () => auditLogMe(["admin"]),
      "GET /audit-log": () => jsonResponse(auditLogPage),
      "GET /people/p-1": () =>
        jsonResponse({ id: "p-1", full_name: "Priya Shah" }),
    });
    return (
      <StoryProviders>
        <AuditLogCard />
      </StoryProviders>
    );
  },
};
