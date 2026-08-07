// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";
import { TasksScreen } from "./tasks";

// TasksScreen (B-EP09.12d): open tasks grouped overdue/today/upcoming/undated.
// Reads GET /me (to gate itself off in overlay mode — the mirror refuses
// both the kind=task filter and a task write) and GET /activities.
function admin(overrides: Record<string, unknown> = {}) {
  return () =>
    jsonResponse({
      user: { id: "u1", email: "ada@acme.test", display_name: "Ada" },
      roles: ["admin"],
      teams: [],
      ...overrides,
    });
}

const task = {
  id: "a-1",
  workspace_id: "w1",
  kind: "task" as const,
  subject: "Follow up on proposal",
  occurred_at: "2026-07-20T09:00:00Z",
  due_at: "2026-07-20T17:00:00Z",
  is_done: false,
  source: "manual",
  captured_by: "human:u1",
  created_at: "2026-07-20T09:00:00Z",
  updated_at: "2026-07-20T09:00:00Z",
};

const meta: Meta<typeof TasksScreen> = {
  title: "Screens/tasks",
  component: TasksScreen,
};
export default meta;
type Story = StoryObj<typeof TasksScreen>;

export const Grouped: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /activities": () =>
        jsonResponse({ data: [task], page: { next_cursor: null } }),
    });
    return (
      <StoryProviders>
        <TasksScreen />
      </StoryProviders>
    );
  },
};

// Overlay mode renders the honest unavailable state instead of the list:
// kind=task is a filter dial the mirror refuses (unsupported_in_overlay_mode),
// so the fetch is skipped rather than sent to fail.
export const OverlayUnavailable: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin({ system_of_record: { mode: "overlay" } }),
    });
    return (
      <StoryProviders>
        <TasksScreen />
      </StoryProviders>
    );
  },
};
