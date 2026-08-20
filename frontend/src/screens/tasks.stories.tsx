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

// One message still waiting on its moment. The queue is a bare array on the
// wire, not a page envelope, so a story that leaves it unrouted does not render
// an empty callout — the list-shaped fallback is not a list at all here.
const waitingSend = {
  id: "s-1",
  status: "scheduled" as const,
  scheduled_at: "2026-07-21T07:00:00Z",
  scheduled_tz: "Europe/Berlin",
  subject: "Retrofit quote, as promised",
  to: ["dana@acme.test"],
  version: 1,
  created_at: "2026-07-20T09:00:00Z",
  updated_at: "2026-07-20T09:00:00Z",
};

const meta: Meta<typeof TasksScreen> = {
  title: "Records/Tasks",
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
      "GET /scheduled-sends": () => jsonResponse([]),
    });
    return (
      <StoryProviders>
        <TasksScreen />
      </StoryProviders>
    );
  },
};

// A rep with something queued: the tasks page is where "send later" is picked up
// again, so the queue announces itself here rather than only behind the nav.
export const ScheduledWaiting: Story = {
  render: () => {
    installFetchStub({
      "GET /me": admin(),
      "GET /activities": () =>
        jsonResponse({ data: [task], page: { next_cursor: null } }),
      "GET /scheduled-sends": () => jsonResponse([waitingSend]),
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
      // Routed even though the settled overlay state renders no callout: the
      // mode is a fact the session carries, so the first paint happens before
      // the verdict is in and the callout reads the queue on the way there.
      "GET /scheduled-sends": () => jsonResponse([]),
    });
    return (
      <StoryProviders>
        <TasksScreen />
      </StoryProviders>
    );
  },
};
