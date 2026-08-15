// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { taskWriteKeys } from "./activitykeys";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";
import {
  TaskCompleteCheck,
  TaskDetailModal,
  TaskQuickActions,
  useTaskUpdate,
} from "./taskactions";

// Acting on a task from the record it belongs to: the tick, the quick-action
// row, and the detail modal a next-step row opens into. Mirrors company360.test.tsx's
// NextStepsWithVerbs pairing — the verbs take a live `useTaskUpdate` mutation
// rather than a stub function, so a story exercises the same wiring the record
// page does.

const meta: Meta = {
  title: "Patterns/Task actions",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

// The mutation is raised INSIDE StoryProviders, not in the component that
// renders it: useTaskUpdate reads the query client from context, so a caller
// that mounts the provider in its own return value is still outside it when
// the hook runs. Every story here splits along that line.
function TaskRowBody({ dueAt }: Readonly<{ dueAt?: string | null }>) {
  const update = useTaskUpdate(taskWriteKeys("organization", "o-1"));
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: "var(--space-2)",
      }}
    >
      <TaskCompleteCheck activityId="a-1" update={update} />
      <span>Send the renewal paperwork</span>
      <TaskQuickActions
        activityId="a-1"
        dueAt={dueAt}
        update={update}
        showComplete={false}
      />
    </div>
  );
}

function TaskRow({ dueAt }: Readonly<{ dueAt?: string | null }>) {
  installFetchStub({});
  return (
    <StoryProviders>
      <TaskRowBody dueAt={dueAt} />
    </StoryProviders>
  );
}

// Snooze is offered only for a dated task — there is no day to move an
// undated one to.
export const RowWithDueDate: Story = {
  render: () => <TaskRow dueAt="2026-08-01T09:00:00Z" />,
};

export const RowUndated: Story = { render: () => <TaskRow dueAt={null} /> };

function DetailModal() {
  installFetchStub({
    "GET /activities/a-1": () =>
      jsonResponse({
        id: "a-1",
        kind: "task",
        subject: "Send the renewal paperwork",
        body: "Draft went to legal on Tuesday; needs Dana's sign-off.",
        due_at: "2026-08-01T09:00:00Z",
        occurred_at: "2026-07-28T09:00:00Z",
        is_done: false,
        assignee_id: "u-1",
      }),
    "GET /users": () =>
      jsonResponse({
        data: [{ id: "u-1", display_name: "Mira Voss" }],
        page: { has_more: false, next_cursor: null },
      }),
  });
  return (
    <StoryProviders>
      <DetailModalBody />
    </StoryProviders>
  );
}

function DetailModalBody() {
  const update = useTaskUpdate(taskWriteKeys("organization", "o-1"));
  return (
    <TaskDetailModal
      activityId="a-1"
      readOnly={false}
      onClose={() => {}}
      update={update}
    />
  );
}

export const DetailOpen: Story = { render: () => <DetailModal /> };
