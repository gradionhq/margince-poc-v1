// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { AuditLogCard, PipelinesCard, SettingsScreen } from "./settings";
import {
  installFetchStub,
  jsonResponse,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The settings tab layout (section nav + the active tab's cards) across its
// real tabs. Each story installs the fetch stub the tab's cards read through,
// so the render is deterministic and network-free — the same fixture shapes
// the settings.test.tsx cases use.

const me = () =>
  jsonResponse({
    // id matches the audit fixture's human actor so the AuditTab story reads
    // "You" for the viewer's own entry (AuditEntryLine resolves it via meUserId).
    user: { id: "u-mor", email: "ada@acme.test" },
    roles: ["admin"],
    teams: [],
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
  title: "screens/settings",
  component: SettingsScreen,
};
export default meta;

type Story = StoryObj<typeof SettingsScreen>;

export const AccountTab: Story = {
  render: tab("account", { "GET /me": me }),
};

export const AiTab: Story = {
  render: tab("ai", { "GET /me": me, "GET /passports": passports }),
};

export const DataTab: Story = {
  render: tab("data", { "GET /me": me }),
};

export const CatalogTab: Story = {
  render: tab("catalog", { "GET /me": me }),
};

export const PrivacyTab: Story = {
  render: tab("privacy", { "GET /me": me }),
};

export const AuditTab: Story = {
  render: tab("audit", { "GET /me": me, "GET /audit-log": auditLog }),
};

// PipelinesCard (D-8, on the Catalog tab) reads GET /me (roles →
// canConfigureAutomations gate) and GET /pipelines. Rendered directly here so
// the admin write affordances vs the rep read-only state each get a story.
const pipelinesFixture = {
  data: [
    {
      id: "pl",
      workspace_id: "w",
      name: "Sales",
      is_default: true,
      position: 0,
      stages: [
        {
          id: "s1",
          workspace_id: "w",
          pipeline_id: "pl",
          name: "Qualify",
          position: 1,
          semantic: "open",
          win_probability: 20,
        },
        {
          id: "s2",
          workspace_id: "w",
          pipeline_id: "pl",
          name: "Proposal",
          position: 2,
          semantic: "open",
          win_probability: 50,
        },
        {
          id: "s3",
          workspace_id: "w",
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

const pipelineMe = (roles: string[]) =>
  jsonResponse({ user: { id: "u-1", display_name: "Me" }, roles, teams: [] });

// useMe() fails fast without a workspace slug, collapsing the admin state into
// read-only — seed the slug so /me resolves and the affordances render.
function pipelinesCard(roles: string[]) {
  return () => {
    globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
    installFetchStub({
      "GET /me": () => pipelineMe(roles),
      "GET /pipelines": () => jsonResponse(pipelinesFixture),
    });
    return (
      <StoryProviders>
        <PipelinesCard />
      </StoryProviders>
    );
  };
}

export const PipelinesAdmin: Story = { render: pipelinesCard(["admin"]) };

export const PipelinesReadOnly: Story = { render: pipelinesCard(["rep"]) };

// AuditLogCard (AO-3/AO-4): one entry carrying a full before/after diff plus
// the agent attribution trail (passport, on-behalf-of human, authorization
// rule, grounding evidence), collapsed by default — the expand toggle is
// what a reviewer exercises to confirm the panel renders honestly.
const auditLogPage = {
  data: [
    {
      id: "al-1",
      workspace_id: "w",
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
