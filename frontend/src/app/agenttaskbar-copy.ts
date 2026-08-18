// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { MarginceCoreState } from "../design-system/margince-core";

/**
 * The taskbar preview's own copy, and the one table of invented lines left on it.
 *
 * Deliberately NOT in the i18n catalogs. Product copy is translated into every
 * locale a reader can pick; a design-review surface behind
 * `VITE_UI_PREVIEW_TASKBAR` is not, and putting it there would hand every future
 * translator strings no installation serves. Same class as `mefixture.ts`.
 *
 * Everything the bar reports about the installation is now read from the API
 * (`agenttaskbar.tsx`): approvals waiting, which sources are unreachable, the
 * model the last call actually ran on, the account's own suggestions. What is
 * left here is labels — and REVIEW_ONLY below.
 */

export const LABELS = {
  onThisPage: "On this page",
  acrossWorkspace: "Across the workspace",
  runtime: "Runtime",
  model: "model",
  sources: "sources",
  tools: "tools",
  approvals: "Approvals waiting",
  offline: "offline",
  idle: "Idle",
  waiting: "waiting for you",
  cannotReach: "Cannot reach",
  review: "Review",
  reconnect: "Reconnect",
  decide: "Decide",
  duplicates: "duplicate pairs to decide",
  duplicatesRow: "Duplicate pairs open",
  nothingHere: "Nothing on this screen",
  states: "State (review only)",
  expand: "Expand the agent panel",
  collapse: "Collapse the agent panel",
  region: "Margince AI taskbar preview",
  unreadable: "not readable on this seat",
  noCallsYet: "nothing has run yet",
} as const;

/**
 * The states no read can reach, and the sentence each would carry.
 *
 * `ingesting`, `reasoning` and `drafting` describe work IN FLIGHT, and the
 * contract has no run-progress read and no stream — the `agent_run` and
 * `runner_job` tables exist, but nothing serves their phase. `applied` and
 * `error` are the other two: the first needs a decided-approvals read scoped to
 * the overnight run, the second `/admin/job-health`, which a sales seat cannot
 * call.
 *
 * They are here so the vocabulary can be reviewed whole, reachable only from the
 * switcher in the panel, which says review-only on its own heading. Nothing
 * derives them, and the bar never enters one on its own.
 */
export const REVIEW_ONLY: Readonly<Partial<Record<MarginceCoreState, string>>> =
  {
    ingesting: "Reading 128 captured items",
    reasoning: "Checking 1,204 records",
    drafting: "Drafting proposals",
    applied: "9 changes applied",
    error: "Overnight run failed at 04:12",
  };

/**
 * The whole vocabulary, in lifecycle order, for the switcher.
 *
 * Written out rather than derived from `REVIEW_ONLY`: three of these states the
 * bar reaches on its own from what it read, and the switcher has to be able to
 * put the bar back into one of them after a reviewer has walked the invented
 * five.
 */
export const VOCABULARY: readonly MarginceCoreState[] = [
  "dormant",
  "ingesting",
  "reasoning",
  "drafting",
  "applied",
  "flagged",
  "disconnected",
  "error",
];

/** The states whose line above describes work still running. */
export const RUNNING: ReadonlySet<MarginceCoreState> = new Set([
  "ingesting",
  "reasoning",
  "drafting",
]);
