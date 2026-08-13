// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { Meta, StoryObj } from "@storybook/react-vite";
import { meFixture } from "../app/mefixture";
import { JobHealthCard } from "./jobhealth";
import { installFetchStub, jsonResponse, StoryProviders } from "./story-utils";

const CLASSIFY = {
  kind: "capture_classify",
  queue: "default",
  fleet_wide: false,
  waiting: 12,
  running: 1,
  retrying: 2,
  dead: 0,
  oldest_waiting_age_seconds: 4_500,
};

const DISPATCHER = {
  kind: "retention_sweep_dispatch",
  queue: "periodic",
  fleet_wide: true,
  waiting: 0,
  running: 0,
  retrying: 0,
  dead: 0,
  oldest_waiting_age_seconds: null,
};

const FAILURE = {
  kind: "capture_classify",
  state: "retryable",
  attempt: 2,
  max_attempts: 5,
  failed_at: "2026-08-13T09:20:00Z",
  reason: "the model provider refused the request",
};

// Every story serves /me too: the card gates its own fetch on the admin role,
// so a story without a principal would only ever render the withheld state.
function story(health: Record<string, unknown>, roles: string[] = ["admin"]) {
  return () => {
    installFetchStub({
      "GET /me": () => jsonResponse(meFixture({ roles })),
      "GET /admin/job-health": () => jsonResponse(health),
    });
    return (
      <StoryProviders>
        <JobHealthCard />
      </StoryProviders>
    );
  };
}

const HEALTHY = {
  generated_at: "2026-08-13T09:30:00Z",
  kinds: [CLASSIFY, DISPATCHER],
  recent_failures: [FAILURE],
};

const meta: Meta<typeof JobHealthCard> = {
  title: "Screens/job-health",
  component: JobHealthCard,
};
export default meta;
type Story = StoryObj<typeof JobHealthCard>;

export const Healthy: Story = { render: story(HEALTHY) };

export const DeadWork: Story = {
  render: story({
    ...HEALTHY,
    kinds: [{ ...CLASSIFY, retrying: 0, dead: 3 }, DISPATCHER],
    recent_failures: [{ ...FAILURE, state: "discarded", attempt: 5 }],
  }),
};

export const Idle: Story = {
  render: story({
    generated_at: "2026-08-13T09:30:00Z",
    kinds: [],
    recent_failures: [],
  }),
};

export const Withheld: Story = { render: story(HEALTHY, ["ops"]) };
