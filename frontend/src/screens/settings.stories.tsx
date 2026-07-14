// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { SettingsScreen } from "./settings";
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
    user: { email: "ada@acme.test" },
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
