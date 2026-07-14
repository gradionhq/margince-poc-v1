// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { AuditLogCard, PipelinesCard, SettingsScreen } from "./settings";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  StoryProviders,
} from "./story-utils";

// PipelinesCard (D-8) reads GET /me (roles → canConfigureAutomations gate) and
// GET /pipelines (the ["pipelines","all"] list). Both stubbed here so the card
// renders off fixtures — never a live call. Admin sees the write affordances;
// a rep sees the same list read-only (server stays the RBAC authority).
const meta: Meta = {
  title: "Screens/Settings",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const pipelines = {
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

function me(roles: string[]) {
  return {
    user: { id: "u-1", display_name: "Me" },
    roles,
    teams: [],
  };
}

// useMe() fails fast without a workspace slug (there is no tenant to ask), which
// would leave canConfigureAutomations false and collapse the Admin state into the
// read-only one. Seed the slug so /me resolves and the admin affordances render.
function seedWorkspace() {
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
}

export const Admin: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({
      "GET /me": () => jsonResponse(me(["admin"])),
      "GET /pipelines": () => jsonResponse(pipelines),
    });
    return (
      <StoryProviders>
        <PipelinesCard />
      </StoryProviders>
    );
  },
};

export const ReadOnly: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({
      "GET /me": () => jsonResponse(me(["rep"])),
      "GET /pipelines": () => jsonResponse(pipelines),
    });
    return (
      <StoryProviders>
        <PipelinesCard />
      </StoryProviders>
    );
  },
};

function installSettingsStub() {
  installFetchStub(
    {
      "GET /me": () =>
        jsonResponse({
          user: { email: "ada@acme.test" },
          roles: ["admin"],
          teams: [],
        }),
    },
    () => jsonResponse(emptyPage),
  );
}

export const Default: Story = {
  render: () => {
    installSettingsStub();
    return (
      <StoryProviders>
        <SettingsScreen />
      </StoryProviders>
    );
  },
};

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

export const AuditLog: Story = {
  render: () => {
    seedWorkspace();
    installFetchStub({
      "GET /me": () => jsonResponse(me(["admin"])),
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
