import type { Meta, StoryObj } from "@storybook/react-vite";
import { userEvent, within } from "storybook/test";
import {
  ArchiveQuotaAction,
  EditTargetAction,
  SetTargetAction,
} from "./quotas.forms";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

// The quota write surface for the fe-uat render gate. Each action owns its own
// modal open state, so a play() click drives the trigger — fe-uat waits for the
// interaction to settle before it screenshots, capturing the dialog itself
// rather than the closed button. The roster reads are stubbed; no live call.

const page = { next_cursor: null, has_more: false };

const users = {
  data: [
    {
      id: "u1",
      workspace_id: "w1",
      email: "riya@example.co",
      display_name: "Riya Patel",
      timezone: "UTC",
      status: "active",
      is_agent: false,
    },
  ],
  page,
};

const teams = {
  data: [{ id: "t1", workspace_id: "w1", name: "DACH Enterprise" }],
  page,
};

const ownerQuota = {
  id: "q1",
  workspace_id: "w1",
  owner_id: "u1",
  team_id: null,
  period_start: "2026-07-01",
  period_end: "2026-09-30",
  target_minor: 28000000,
  currency: "EUR",
  version: 3,
  created_at: "2026-06-28T16:40:00Z",
  updated_at: "2026-07-01T09:12:00Z",
};

const meta: Meta = { title: "screens/quotas-forms" };
export default meta;

// The owner-XOR-team create form, opened so the render gate captures the side
// picker + roster select + money entry.
export const SetTarget: StoryObj = {
  render: () => {
    installFetchStub({
      "GET /users": () => jsonResponse(users),
      "GET /teams": () => jsonResponse(teams),
    });
    return (
      <StoryProviders>
        <SetTargetAction label="Set a target" />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await userEvent.click(within(canvasElement).getByTestId("quota-create"));
  },
};

// The target editor, opened — reassigns period/target/currency within the
// existing owner side (a merge-PATCH can't clear a side).
export const EditTarget: StoryObj = {
  render: () => {
    installFetchStub({});
    return (
      <StoryProviders>
        <EditTargetAction label="Edit target" quota={ownerQuota} />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await userEvent.click(within(canvasElement).getByTestId("edit-record"));
  },
};

// The confirm-first archive dialog.
export const ArchiveConfirm: StoryObj = {
  render: () => {
    installFetchStub({});
    return (
      <StoryProviders>
        <ArchiveQuotaAction quota={ownerQuota} onArchived={() => undefined} />
      </StoryProviders>
    );
  },
  play: async ({ canvasElement }) => {
    await userEvent.click(within(canvasElement).getByTestId("archive-record"));
  },
};
