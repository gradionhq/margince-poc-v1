// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { ProjectScreen } from "./project360";
import { ProjectsScreen } from "./projects";
import { ORG, project, project360 } from "./projects.fixtures";
import {
  emptyPage,
  installFetchStub,
  jsonResponse,
  meRoute,
  type RouteMap,
  StoryProviders,
} from "./story-utils";

// The projects list in its three readings — rows, the instructional first-run
// plate, and a narrowed list — and the project page with every section
// present, with two sections withheld, and closed.

const meta: Meta = {
  title: "Records/Projects",
  parameters: { layout: "padded" },
};
export default meta;

type Story = StoryObj;

const rows = [
  project({ id: "pr-1", name: "CRM rollout", key: "ACME-CRM" }),
  project({
    id: "pr-2",
    name: "Fleet retrofit",
    key: "FLEET",
    phase: "delivering",
    last_activity_at: "2026-07-20T09:00:00Z",
  }),
  project({
    id: "pr-3",
    name: "Warehouse pilot",
    key: null,
    phase: "closed",
    owner_id: null,
    last_activity_at: null,
  }),
];

function sharedRoutes(): RouteMap {
  return {
    "GET /me": meRoute({}),
    "GET /organizations": () =>
      jsonResponse({ data: [ORG], page: { next_cursor: null } }),
    [`GET /organizations/${ORG.id}`]: () => jsonResponse(ORG),
    "GET /users": () =>
      jsonResponse({
        data: [{ id: "u-me", display_name: "Me", status: "active" }],
        page: { next_cursor: null },
      }),
  };
}

function installList(projects: unknown[]) {
  installFetchStub({
    ...sharedRoutes(),
    "GET /projects": () =>
      jsonResponse({
        data: projects,
        page: { next_cursor: null, has_more: false },
      }),
  });
}

function installPage(view: unknown) {
  installFetchStub({
    ...sharedRoutes(),
    "GET /projects/pr-1/360": () => jsonResponse(view),
    "GET /projects/pr-1": () => jsonResponse(project()),
    "GET /field-history": () => jsonResponse(emptyPage),
    "GET /pipelines": () => jsonResponse(emptyPage),
  });
}

export const List: Story = {
  render: () => {
    installList(rows);
    return (
      <StoryProviders>
        <ProjectsScreen />
      </StoryProviders>
    );
  },
};

export const FirstRun: Story = {
  render: () => {
    installList([]);
    return (
      <StoryProviders>
        <ProjectsScreen />
      </StoryProviders>
    );
  },
};

export const Page: Story = {
  render: () => {
    installPage(
      project360({
        deals: {
          data: [
            {
              id: "d-1",
              name: "Phase one licence",
              pipeline_id: "pl",
              stage_id: "s3",
              status: "won",
              amount_minor: 450_000,
              currency: "EUR",
              source: "manual",
              captured_by: "u-me",
              created_at: "2026-06-02T09:00:00Z",
              updated_at: "2026-06-02T09:00:00Z",
            },
            {
              id: "d-2",
              name: "Phase two services",
              pipeline_id: "pl",
              stage_id: "s1",
              status: "open",
              amount_minor: 1_200_000,
              currency: "EUR",
              source: "manual",
              captured_by: "u-me",
              created_at: "2026-07-02T09:00:00Z",
              updated_at: "2026-07-02T09:00:00Z",
            },
          ],
          page: { next_cursor: null, has_more: false },
        },
        commitments: {
          data: [
            {
              activity_id: "a-1",
              subject: "Send the kickoff agenda",
              due_at: "2026-07-03T09:00:00Z",
              assignee_id: "u-me",
              assignee_name: "Me",
              overdue: true,
            },
          ],
          page: { next_cursor: null, has_more: false },
        },
      }),
    );
    return (
      <StoryProviders>
        <ProjectScreen id="pr-1" />
      </StoryProviders>
    );
  },
};

// A reader whose role holds the project grant but not the contracts or the
// deal figures: the cards say so rather than reading as empty.
export const PageWithheld: Story = {
  render: () => {
    installPage(
      project360({
        sections_omitted: ["contracts", "rollups", "deals"],
        contracts: undefined,
        rollups: undefined,
        deals: undefined,
      }),
    );
    return (
      <StoryProviders>
        <ProjectScreen id="pr-1" />
      </StoryProviders>
    );
  },
};

export const PageClosed: Story = {
  render: () => {
    installPage(
      project360({
        project: project({ phase: "closed", closed_reason: "Delivered" }),
        phase_history: {
          data: [
            {
              id: "ph-1",
              from_phase: null,
              to_phase: "initiative",
              reason: null,
              changed_at: "2026-03-01T09:00:00Z",
              changed_by: { id: "u-me", display_name: "Me" },
            },
            {
              id: "ph-2",
              from_phase: "initiative",
              to_phase: "delivering",
              reason: "Contract signed",
              changed_at: "2026-04-01T09:00:00Z",
              changed_by: { id: "u-me", display_name: "Me" },
            },
            {
              id: "ph-3",
              from_phase: "delivering",
              to_phase: "closed",
              reason: "Delivered",
              changed_at: "2026-07-01T09:00:00Z",
              changed_by: { id: "u-me", display_name: "Me" },
            },
          ],
          phase_durations: [
            { phase: "initiative", seconds: 2_678_400, current: false },
            { phase: "delivering", seconds: 7_862_400, current: false },
            { phase: "closed", seconds: 86_400, current: true },
          ],
        },
      }),
    );
    return (
      <StoryProviders>
        <ProjectScreen id="pr-1" />
      </StoryProviders>
    );
  },
};
