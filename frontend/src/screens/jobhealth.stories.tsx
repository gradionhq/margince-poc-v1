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
  job_id: 4711,
  first_failed_at: "2026-08-13T09:08:00Z",
  failure_class: "provider_unavailable",
  remedy:
    "check the provider status page; the retry ladder rides out a brief outage",
  reason: "the model provider refused the request",
};

// The same failure as the job layer reports it when the stored text could not be
// vetted: the fixed substitute sentence, and no class, remedy or first failure at
// all. Its own story because the absent halves are what has to be LOOKED at —
// nothing automated can see a row that kept a separator, a label or an empty
// line where a value used to be.
const UNVETTED_FAILURE = {
  ...FAILURE,
  first_failed_at: null,
  failure_class: null,
  remedy: null,
  reason: "the job failed for a reason it could not classify",
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
  title: "Settings/Admin settings/Maintenance/Job health",
  component: JobHealthCard,
};
export default meta;
type Story = StoryObj<typeof JobHealthCard>;

// The three readings as stacked rows: each one's naming sits above counts that
// take the card's full width, because a list of counts IS the subject rather
// than an answer that would fit beside its label. What to look at is the
// hairline between the three — it has to separate the readings without reading
// as a border around any one of them.
export const Healthy: Story = { render: story(HEALTHY) };

// Two of the three readings with nothing to report, which the stacked rows have
// to say one at a time: no fleet dispatcher is registered and no failure was
// recorded, while this organization's own queue is busy. The card's own idle
// state does NOT apply here — something is queued — so the empty branches must
// stand inside their rows, each naming what it found none of. What to check is
// that an EmptyState given a row's full width still reads as a finding rather
// than as a gap where a list failed to draw.
export const ReadingsWithNothingToReport: Story = {
  render: story({
    ...HEALTHY,
    kinds: [CLASSIFY],
    recent_failures: [],
  }),
};

export const DeadWork: Story = {
  render: story({
    ...HEALTHY,
    kinds: [{ ...CLASSIFY, retrying: 0, dead: 3 }, DISPATCHER],
    recent_failures: [{ ...FAILURE, state: "discarded", attempt: 5 }],
  }),
};

export const UnclassifiedFailure: Story = {
  render: story({ ...HEALTHY, recent_failures: [UNVETTED_FAILURE] }),
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
// Below 640px `SettingRow` gives up its two columns too (settingrow.css), so the
// reading's naming and the list under it are already stacked — the thing to
// watch is that the row's own naming does not start reading as one of the
// FactList terms beneath it.
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
