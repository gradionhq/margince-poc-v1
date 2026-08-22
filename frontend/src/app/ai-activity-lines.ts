// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import type { MessageKey } from "../i18n/en";

type ActivityKind = components["schemas"]["AiActivityItem"]["kind"];
type ActivityState = components["schemas"]["AiActivityItem"]["state"];

/**
 * A kind the rail deliberately does not narrate, and why.
 *
 * The server reports every AI task this build can run, because a task that
 * reports nothing is AI work the product performed and then denied. What a
 * reader is SHOWN is a different question, and it is this file's to answer —
 * which is why the reason lives in the code rather than in a review comment.
 */
type NotDisplayed = Readonly<{ notDisplayed: string }>;

/** The (state -> message key) table of a kind the rail narrates. */
type LineSet = Readonly<Record<ActivityState, MessageKey>>;

const notDisplayed = (reason: string): NotDisplayed => ({
  notDisplayed: reason,
});

const WATCHED_BY_THE_ASKER = notDisplayed(
  "growth_fit lands on the panel that asked for it and renders the band it returns, so a line here would narrate what the reader is already looking at. cold_start has a reason that does not depend on judgement at all: it runs during onboarding, and the onboarding routes are deliberately RAILLESS (shell.tsx) — there is no rail on screen for its line to appear on. That is why the two sit together despite cold_start also having an out-of-band arm that becomes an approval nobody watches: a task-level map could not separate those arms, and it does not have to, because neither can be drawn where the task runs",
);
const SYSTEM_SWEEP = notDisplayed(
  "background workspace work that belongs to nobody in particular, so it has no personal line to draw",
);

/**
 * The line for one (kind, state), by literal key — or the reason there is none.
 *
 * LITERAL, not `t(`agent.activity.${kind}.${state}`)`. The orphan guard in
 * i18n.test.ts counts a key as rendered when it starts with a template STEM,
 * and its regex stops at the first `${` — so an interpolated key would vouch
 * for the whole `agent.activity.` namespace forever, and a retired kind's copy
 * would sit in three catalogs with nothing to flag it.
 *
 * TOTAL over the contract's kinds, and the compiler is what holds it there: a
 * new kind fails the build until somebody either writes its copy in every
 * locale or says, here, why it is not shown. The second branch exists because
 * every AI task now reports — nineteen of them, most narrating work no rep
 * asked to watch — and forcing copy for all of them would have bought 300
 * strings nobody reads and taught the next author that the answer to a new kind
 * is boilerplate.
 *
 * Total over the STATE axis too, wherever a kind is displayed: every state the
 * feed can report is reachable by every kind. `stalled` in particular is
 * DERIVED by the server from a lease the occurrence's own source declared, so
 * it can arrive for anything, and a keyless entry would render nothing for
 * exactly the case the projection exists to show.
 */
export const ACTIVITY_LINE: Readonly<
  Record<
    ActivityKind,
    Readonly<Record<ActivityState, MessageKey>> | NotDisplayed
  >
> = {
  morning_brief: {
    queued: "agent.activity.morningBrief.queued",
    running: "agent.activity.morningBrief.running",
    stalled: "agent.activity.morningBrief.stalled",
    done: "agent.activity.morningBrief.done",
    degraded: "agent.activity.morningBrief.degraded",
    failed: "agent.activity.morningBrief.failed",
  },
  overnight_at_risk_sweep: {
    queued: "agent.activity.riskSweep.queued",
    running: "agent.activity.riskSweep.running",
    stalled: "agent.activity.riskSweep.stalled",
    done: "agent.activity.riskSweep.done",
    degraded: "agent.activity.riskSweep.degraded",
    failed: "agent.activity.riskSweep.failed",
  },
  document_extract: {
    queued: "agent.activity.documentExtract.queued",
    running: "agent.activity.documentExtract.running",
    stalled: "agent.activity.documentExtract.stalled",
    done: "agent.activity.documentExtract.done",
    degraded: "agent.activity.documentExtract.degraded",
    failed: "agent.activity.documentExtract.failed",
  },

  // `queued` is written for all three and reachable by none of them: the router
  // announces a call it is ABOUT to serve, never one waiting, and no carrier
  // owns these tasks. The LineSet type is total over the state axis, so the key
  // exists because the compiler requires it — not because a producer is
  // missing. Saying so here saves the next reader the hunt.
  summarize: {
    queued: "agent.activity.summarize.queued",
    running: "agent.activity.summarize.running",
    stalled: "agent.activity.summarize.stalled",
    done: "agent.activity.summarize.done",
    degraded: "agent.activity.summarize.degraded",
    failed: "agent.activity.summarize.failed",
  },
  draft_reply: {
    queued: "agent.activity.draftReply.queued",
    running: "agent.activity.draftReply.running",
    stalled: "agent.activity.draftReply.stalled",
    done: "agent.activity.draftReply.done",
    degraded: "agent.activity.draftReply.degraded",
    failed: "agent.activity.draftReply.failed",
  },
  offer_draft: {
    queued: "agent.activity.offerDraft.queued",
    running: "agent.activity.offerDraft.running",
    stalled: "agent.activity.offerDraft.stalled",
    done: "agent.activity.offerDraft.done",
    degraded: "agent.activity.offerDraft.degraded",
    failed: "agent.activity.offerDraft.failed",
  },

  growth_fit: WATCHED_BY_THE_ASKER,
  cold_start: WATCHED_BY_THE_ASKER,

  brief_ranking: SYSTEM_SWEEP,
  capture_classify: SYSTEM_SWEEP,
  capture_counterparty_verdict: SYSTEM_SWEEP,
  enrich: notDisplayed(
    "it can reach nobody, and that is a fact about the work rather than an editorial choice: the one production site for this task is the signature-enrichment pass, which runs under a system principal with no on_behalf_of, so ResolveActor scopes every occurrence to the workspace with a NULL actor_user_id — and the personal feed selects on actor_user_id. Copy for it would be copy no reader can ever be shown. The ticker's own `enrich` key names DIFFERENT work (a provider run on a person, and the site-read lanes on a company), which is what makes this one easy to mistake for visible",
  ),
  rate_extract: SYSTEM_SWEEP,
  signal_extract: SYSTEM_SWEEP,
  site_extract: SYSTEM_SWEEP,
  site_fact_extract: SYSTEM_SWEEP,
  site_triage: SYSTEM_SWEEP,
  transcript_propose: SYSTEM_SWEEP,
  voice_build: SYSTEM_SWEEP,

  cert_judge: notDisplayed(
    "the certification lane grading this build's own answers — an operator's measurement, not a rep's work",
  ),
  deal_health: notDisplayed(
    "declared in api/ai-tasks.yaml and not built: no site runs it, so nothing reports it yet",
  ),
  nl_search: notDisplayed(
    "declared in api/ai-tasks.yaml and not built: no site runs it, so nothing reports it yet",
  ),
  transcript: notDisplayed(
    "declared in api/ai-tasks.yaml and not built: no site runs it, so nothing reports it yet",
  ),
};

/**
 * The panel's own headings, which name no single run.
 *
 * They live beside the lines rather than in them: the map above is the (kind,
 * state) table its test holds to exactly, and a heading in it would be a key no
 * state could reach.
 *
 * `settled` names the section the terminal states are read under. It is not
 * decoration: `done`, `degraded` and `failed` — and with them every
 * `degrade_reason` and `summary`, both written only alongside a terminal status
 * — reach a reader through that section and nowhere else, because the read puts
 * a settled run in `recent` and never in `running`.
 */
export const PANEL_HEADING: Readonly<
  Record<"running" | "settled", MessageKey>
> = {
  running: "agent.panel.runningNow",
  settled: "agent.panel.finishedToday",
};

/**
 * A label drawn on ONE run's own detail line, not a section heading.
 *
 * Kept out of `PANEL_HEADING`: that map's contract is a heading that names no
 * single run, and `stopped` sits inline next to one run's `degrade_reason` in
 * `RunSection` — it is closer kin to `ACTIVITY_LINE` than to a section title,
 * so it gets its own export rather than stretching the heading map's meaning.
 */
export const RUN_DETAIL_LABEL: Readonly<Record<"stopped", MessageKey>> = {
  stopped: "agent.panel.stoppedEarly",
};

/**
 * What to say about one item, or nothing at all.
 *
 * The existence check is not optional and `t()` cannot do it: translate() falls
 * back to THE KEY STRING, so a missing entry would put
 * `agent.activity.foo.running` in front of a reader.
 *
 * There are three ways to draw nothing, and they are different facts: the kind
 * is one this build has never heard of, the kind is one it deliberately does
 * not narrate, or the state has no line under a kind it does narrate. All three
 * answer null, because the reader's screen is the same either way — but they
 * are distinguished HERE rather than collapsed into a lookup miss, so a kind
 * that silently lost its copy cannot hide among the ones that never had any.
 *
 * It takes RAW strings rather than the contract's unions, and that widening is
 * the point: the map is total over the contract, so the only way to reach the
 * first case is a value the contract does not carry — which is exactly what an
 * older tab gets from a newer server that has added a kind or a state. Typed
 * narrowly, that case could only be written with a cast, and a test that casts
 * is asserting against its own escape hatch instead of against the function.
 */
export function lineFor(
  item: Readonly<{ kind: string; state: string }>,
  t: (key: MessageKey) => string,
): string | null {
  if (!isActivityKind(item.kind)) {
    return null;
  }
  const entry = ACTIVITY_LINE[item.kind];
  if ("notDisplayed" in entry) {
    return null;
  }
  const byState: Readonly<Partial<Record<string, MessageKey>>> = entry;
  const key = byState[item.state];
  return key === undefined ? null : t(key);
}

/**
 * Whether a raw string is a kind this build knows.
 *
 * An own-key check rather than a cast, so the narrowing is something the
 * runtime actually did: a newer server's kind is not in the map, and saying so
 * is the whole answer lineFor gives for it.
 */
function isActivityKind(kind: string): kind is ActivityKind {
  return Object.hasOwn(ACTIVITY_LINE, kind);
}

/**
 * The kinds this rail draws, as the server's `kinds` filter takes them.
 *
 * DERIVED from ACTIVITY_LINE rather than listed, and that is the whole point:
 * the server reports every AI task, and `recent` is bounded at ten. A client
 * that asked for everything and drew three kinds would be handed the newest ten
 * of twenty-three — ten it renders nothing for — and the rail would go blank on
 * the day a rep used the composer a lot, while the projection was right the
 * whole time. Naming the kinds moves the bound inside this list; deriving it
 * means the list cannot fall out of step with the copy that decides it.
 */
export function displayedKinds(): ActivityKind[] {
  return displayedLines().map(([kind]) => kind);
}

/**
 * The narrated kinds paired with their line tables.
 *
 * The narrowing happens HERE, once, where `"notDisplayed" in entry` is a check
 * the compiler performs rather than an assertion somebody makes: a caller that
 * looked the entry up again would be holding `LineSet | NotDisplayed` and would
 * need a cast to say what it already knows. Returning the pair means nobody
 * downstream has to.
 *
 * `Object.entries` widens the key to `string`, so the entry list is rebuilt from
 * the map's own keys rather than trusting that widening back — `ACTIVITY_LINE`
 * is a total `Record<ActivityKind, …>`, so its keys ARE the kinds.
 */
export function displayedLines(): [ActivityKind, LineSet][] {
  const kinds = Object.keys(ACTIVITY_LINE) as ActivityKind[];
  return kinds.flatMap((kind) => {
    const entry = ACTIVITY_LINE[kind];
    return "notDisplayed" in entry
      ? []
      : [[kind, entry] as [ActivityKind, LineSet]];
  });
}
