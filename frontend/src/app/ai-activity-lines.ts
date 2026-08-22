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

const notDisplayed = (reason: string): NotDisplayed => ({
  notDisplayed: reason,
});

const WATCHED_BY_THE_ASKER = notDisplayed(
  "interactive: the person asked for it and is watching the request it answers, so a rail line would narrate work already in front of them",
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

  summarize: WATCHED_BY_THE_ASKER,
  draft_reply: WATCHED_BY_THE_ASKER,
  offer_draft: WATCHED_BY_THE_ASKER,
  growth_fit: WATCHED_BY_THE_ASKER,
  cold_start: WATCHED_BY_THE_ASKER,

  brief_ranking: SYSTEM_SWEEP,
  capture_classify: SYSTEM_SWEEP,
  capture_counterparty_verdict: SYSTEM_SWEEP,
  enrich: SYSTEM_SWEEP,
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
