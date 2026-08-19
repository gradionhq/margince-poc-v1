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

/**
 * The agent's tasks, in words a person who does not work on this product can
 * read.
 *
 * The wire carries `growth_fit` and `site_fact_extract`, which are the names of
 * INVOCATION SITES — correct for a trace, and meaningless to the salesperson
 * whose company page they ran on. A recap that prints them is a log with a
 * friendlier heading: the reader learns that something happened five times and
 * nothing about what.
 *
 * Each line says what the agent DID, in the past tense, from the reader's side
 * rather than the pipeline's. A task with no entry falls back to its token with
 * the underscores opened up, so a task added upstream degrades to something
 * readable instead of disappearing.
 */
export const TASK_SAID: Readonly<Record<string, string>> = {
  agent_loop: "Worked through a request",
  brief_ranking: "Ranked your morning brief",
  capture_classify: "Sorted captured mail",
  capture_counterparty_verdict: "Decided who a message was with",
  cert_judge: "Checked its own answer",
  cold_start: "Set up your workspace",
  deal_health: "Read the health of a deal",
  document_extract: "Pulled fields out of a document",
  draft_reply: "Drafted a reply",
  enrich: "Filled in contact details",
  growth_fit: "Scored how well a company fits",
  nl_search: "Answered a search",
  offer_draft: "Drafted an offer",
  rate_extract: "Read pricing off a page",
  signal_extract: "Found signals in a thread",
  site_extract: "Read a company website",
  site_fact_extract: "Pulled facts off a web page",
  site_triage: "Picked which pages to read",
  summarize: "Wrote a summary",
  transcript: "Processed a call transcript",
  transcript_propose: "Proposed next steps from a call",
  voice_build: "Learned your writing voice",
};

export const LABELS = {
  onThisPage: "On this page",
  acrossWorkspace: "Across the workspace",
  runtime: "Runtime",
  recap: "What it has done",
  justNow: "just now",
  fullLog: "Full log",
  logUnreadable: "The call log is not readable on this seat",
  model: "model",
  sources: "sources",
  tools: "tools",
  approvals: "Approvals waiting",
  offline: "offline",
  idle: "Idle",
  reading: "Loading",
  readingRecord: "Reading this record",
  working: "Working",
  unreachable: "Cannot reach Margince",
  waiting: "waiting for you",
  cannotReach: "Cannot reach",
  review: "Review",
  reconnect: "Reconnect",
  configure: "Set up",
  noModel: "No AI model is configured",
  devModel: "development (offline fake)",
  devLine: "Running on the offline model",
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
 * The bar reaches `ingesting`, `reasoning` and `error` on its own, but about the
 * TOOL — a read in flight, a write in flight, a request that failed. What it
 * cannot report is the same three words about the AGENT: the overnight run
 * pulling captured mail in, traversing the graph, or failing at 04:12. The
 * contract has no run-progress read and no stream — the `agent_run` and
 * `runner_job` tables exist, but nothing serves their phase — and `applied`
 * needs a decided-approvals read scoped to that run.
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
