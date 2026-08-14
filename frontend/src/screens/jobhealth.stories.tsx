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
  title: "Settings/Organization/Maintenance/Job health",
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

// Dead work in dark, which is the only story that has every tone on screen at
// once: the danger Callout an operator must not scroll past, the danger `dead`
// pill and the warn `retrying` one beside it, and — the pairing that actually
// needs looking at — the two UNTONED pills for waiting and running. An untoned
// Badge is filled with --bgCard flat (atoms.css), one step off the card ground it
// sits on, so in dark a count of zero either still reads as a pill or stops
// looking like one while its toned neighbours shout.
export const DeadWorkDark: Story = {
  globals: { theme: "dark" },
  render: story({
    ...HEALTHY,
    kinds: [{ ...CLASSIFY, retrying: 0, dead: 3 }, DISPATCHER],
    recent_failures: [{ ...FAILURE, state: "discarded", attempt: 5 }],
  }),
};

// The counts at 390px, which is the far side of FactList's own breakpoint: below
// 480px (factlist.css) the two columns stop splitting and the term becomes a
// LABEL above its value. That rule is what this story is here to show landing on
// real content, because this card is the hardest case for it — the term is a
// River job kind in mono with underscores and nothing to break on, and the value
// is four pills that are always all four drawn, since a zero is a reading an
// operator came for. What to check is that the pill row wraps inside the width it
// has just been given, and that a kind and its counts still read as one row once
// nothing but a small gap separates them from the next kind.
//
// Storybook applies the viewport from the MANAGER, by resizing the preview
// iframe — so the fe-uat capture, which loads a bare iframe.html, renders this at
// the harness's own width and its PNG is NOT a picture of a phone. Review it in
// Storybook, or by narrowing the browser.
export const HealthyPhone: Story = {
  globals: { viewport: { value: "phone" } },
  tags: ["uat-phone"],
  render: story(HEALTHY),
};
