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
 * (`agentrail.tsx`): approvals waiting, which sources are unreachable, the
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
  /** The month's estimated spend, and it says estimated by saying "so far":
   *  the server prices on read, so the figure moves as rates change. */
  spend: "Cost this month",
  /** Under the figure in the panel head, where the label is the second line. */
  thisMonth: "this month",
  /** The panel's own name. It is portalled to the body, so it is not inside the
   *  region that would otherwise have named it. */
  panel: "Margince agent detail",
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
  duplicates: "duplicate pairs to decide",
  duplicatesRow: "Duplicate pairs open",
  states: "State (review only)",
  /** The run control beside the state chips. It plays invented work: what it is
   *  for is the MOTION between the states, which a held chip cannot show. */
  runPlay: "Play a run",
  runStop: "Stop",
  expand: "Expand the agent panel",
  collapse: "Collapse the agent panel",
  region: "Margince agent",
  unreadable: "not readable on this seat",
  noCallsYet: "nothing has run yet",
  /** The resting line when every read came back with nothing to report. */
  allClear: "Nothing needs you",
  /** The shape of the month's spend, for the reader who cannot see the line. */
  spendShape: "What the agent has cost, day by day",
  nothingPriced: "nothing priced yet",
  spentThisMonth: "spent this month",
  runningOn: "running on",
  duplicatesIdle: "duplicates to decide",
} as const;

/**
 * The states no read can reach on this surface, and the sentence each carries.
 *
 * The section reaches `ingest`, `working` and `error` on its own, but about the
 * TOOL: a read in flight, a write in flight, a source it cannot get to. `working`
 * about the AGENT is real too now — `useAiActivity` reads the run row and
 * `RunSection` (`agentrail.tsx`) names it in the reader's own words. What stays
 * unreachable is narrower: there is no per-step progress stream (a run is
 * `queued`, `running` or settled, never "40% through"), and `ingest` here is
 * about the CAPTURE pipeline reading mail, which still has no read of its own.
 *
 * They are here so the vocabulary can be reviewed whole, reachable only from the
 * switcher in the panel under its own review-only heading. Nothing derives them.
 */
export const REVIEW_ONLY: Readonly<Partial<Record<MarginceCoreState, string>>> =
  {
    ingest: "Reading captured mail",
    working: "Checking 1,204 records",
  };

/**
 * The whole vocabulary, in lifecycle order, for the switcher.
 *
 * Written out rather than derived from `REVIEW_ONLY`: the section reaches most of
 * these on its own from what it read, and the switcher has to be able to put it
 * back into one of them after a reviewer has walked the invented ones.
 */
export const VOCABULARY: readonly MarginceCoreState[] = [
  "idle",
  "ingest",
  "working",
  "warning",
  "error",
];

/** The states whose line describes work still running. */
export const RUNNING: ReadonlySet<MarginceCoreState> = new Set([
  "ingest",
  "working",
]);

/**
 * What a read in flight is CALLED, keyed by the first segment of its cache key.
 *
 * The line under the orb names one thing at a time (`agentrail-ticker.ts`), and
 * this is the vocabulary it names them in: the words a salesperson uses about
 * their own day, not the words the cache uses about itself. "Reading this
 * company" is a sentence; "fetching organization360" is a key.
 *
 * A key with no entry here produces NO LINE. That is the point of a table rather
 * than a fallback that opens up the key: half of what a session fetches is
 * plumbing (the session, the feature flags, the custom-field catalog), and a
 * status line that narrated those would bury the two or three events a reader
 * actually cares about.
 */
/**
 * The lines the section rotates through while nothing is happening.
 *
 * An idle agent still has things to say, and they are all TRUE THINGS it already
 * read: what is waiting, what it cannot decide alone, what it has cost, what it
 * is running on. Nothing here is invented activity. A resting surface that
 * narrated fake work would teach a reader that none of the line means anything,
 * which is exactly the credit this surface cannot afford to spend.
 *
 * Each entry is a function of what was read, and one that has nothing to report
 * returns null and is skipped, so the rotation is only ever as long as the facts
 * are.
 */
export const IDLE_ORDER = [
  "waiting",
  "duplicates",
  // Third: after the two things a person has to answer for, before the small
  // print. What the scheduled runner finished overnight is news rather than a
  // task, so it does not push a queue down the rotation — and it sits above the
  // cost and the runtime lines because it is about this morning, while those are
  // standing facts that read the same on any day.
  "finished",
  "spend",
  "model",
  "licence",
] as const;

export type IdleKind = (typeof IDLE_ORDER)[number];

/**
 * The named line, per cache key, with `%s` where the record's name goes.
 *
 * This is the version of the line that does the work Lars asked the surface to
 * do: "Reading zenloop" is a sentence about the reader's own afternoon, and
 * "Reading a company" is a sentence about software. Only the keys that carry a
 * single record's id are here, because those are the only ones there is a name
 * to put in.
 *
 * The name is never fetched for this (`agentrail-ticker.ts`): it is used when the
 * tab already knows it, and `SAID` below is what shows when it does not.
 */
/**
 * What a WRITE is called, keyed by the first segment of its `mutationKey`.
 *
 * Present tense and the reader's own verb, with `%s` where the record's name
 * goes: what a salesperson wants to see is "Enriching zenloop", because that is
 * the sentence that says the tool did the thing they did not want to do. A write
 * whose key carries no id, or whose record nothing can name yet, falls back to
 * the same phrase with the article in place of the name.
 *
 * A mutation with no `mutationKey` says nothing at all. That is deliberate: the
 * settings screens, the admin surfaces and the consent flows all write too, and a
 * status line that narrated those would bury the handful of events that are
 * actually the agent doing sales work.
 */
export const WROTE: Readonly<Record<string, [named: string, plain: string]>> = {
  "activity-log": ["Logging an activity on %s", "Logging an activity"],
  "company-edit": ["Editing %s", "Editing a company"],
  "company-new": ["Creating %s", "Creating a company"],
  "contact-edit": ["Editing %s", "Editing a contact"],
  "contact-new": ["Creating %s", "Creating a contact"],
  "deal-edit": ["Editing the %s deal", "Editing a deal"],
  "deal-new": ["Creating a deal on %s", "Creating a deal"],
  dedupe: ["Deciding a duplicate of %s", "Deciding a duplicate"],
  // SENDING, not drafting. The two are told apart by their mutation key rather
  // than by this table: `email` is the send, and the AI draft carries
  // `email-draft` so the rail can own it alone. They used to share one key,
  // which put one action into two vocabularies at once — the bar saying
  // "Writing to Anna" from here while the panel said "I'm drafting your reply."
  // from the server's feed. Deleting this entry fixed that and broke something
  // else: a send is a real write a rep waits on and the rail knows nothing
  // about it, so it went silent. Splitting the key is what serves both.
  email: ["Writing to %s", "Writing an email"],
  enrich: ["Enriching %s", "Enriching a contact"],
  ingest: ["Ingesting %s", "Ingesting"],
  "lead-edit": ["Editing %s", "Editing a lead"],
  "site-read": ["Reading the %s website", "Reading a website"],
  "task-new": ["Adding a task on %s", "Adding a task"],
};

export const NAMED: Readonly<Record<string, string>> = {
  deal: "Reading the %s deal",
  lead: "Reading %s",
  organization: "Reading %s",
  organization360: "Reading everything about %s",
  person: "Reading %s",
  person360: "Reading everything about %s",
  personBrief: "Summarising %s",
};

export const SAID: Readonly<Record<string, string>> = {
  activities: "Reading the activity trail",
  approvals: "Checking what needs you",
  "ai-calls": "Reading its own log",
  "ai-usage": "Adding up what it spent",
  companies: "Reading companies",
  connectors: "Checking its sources",
  deal: "Reading a deal",
  "deal-offers": "Reading the offers on a deal",
  deals: "Reading the pipeline",
  dsrs: "Checking privacy requests",
  lead: "Reading a lead",
  leads: "Reading leads",
  organization: "Reading a company",
  organization360: "Reading everything about this company",
  organizations: "Reading companies",
  overlay: "Reading what it wrote here",
  people: "Reading contacts",
  person: "Reading a contact",
  person360: "Reading everything about this contact",
  personBrief: "Summarising a contact",
  pipelines: "Reading the pipeline",
  "record-history": "Reading what changed",
  tasks: "Reading your tasks",
  teams: "Reading the team",
};
