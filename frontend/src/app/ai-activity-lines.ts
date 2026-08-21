// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import type { MessageKey } from "../i18n/en";

type ActivityKind = components["schemas"]["AiActivityItem"]["kind"];
type ActivityState = components["schemas"]["AiActivityItem"]["state"];

/**
 * The line for one (kind, state), by literal key.
 *
 * LITERAL, not `t(`agent.activity.${kind}.${state}`)`. The orphan guard in
 * i18n.test.ts counts a key as rendered when it starts with a template STEM,
 * and its regex stops at the first `${` — so an interpolated key would vouch
 * for the whole `agent.activity.` namespace forever, and a retired kind's copy
 * would sit in three catalogs with nothing to flag it.
 *
 * TOTAL over both axes, and the compiler is what holds it there: a new kind or
 * a new state on the contract fails the build until somebody writes the copy,
 * in every locale. That is the point of typing it `Record` rather than
 * `Partial<Record>` — a runtime test can only check the states somebody
 * remembered to list, and the list is the thing that goes stale.
 *
 * It became total when `awaiting_approval` left the contract. Every state the
 * feed can now report is reachable by every kind: `stalled` in particular is
 * DERIVED by the server from a lease the occurrence's own source declared, so
 * it can arrive for anything, and a keyless entry would render nothing for
 * exactly the case the projection exists to show.
 */
export const ACTIVITY_LINE: Readonly<
  Record<ActivityKind, Readonly<Record<ActivityState, MessageKey>>>
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
 */
export function lineFor(
  item: Readonly<{ kind: ActivityKind; state: ActivityState }>,
  t: (key: MessageKey) => string,
): string | null {
  const key = ACTIVITY_LINE[item.kind]?.[item.state];
  return key === undefined ? null : t(key);
}
