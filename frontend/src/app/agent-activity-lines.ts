// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import type { components } from "../api/schema";
import type { MessageKey } from "../i18n/en";

type ActivityKind = components["schemas"]["ActivityItem"]["kind"];
type ActivityState = components["schemas"]["ActivityItem"]["state"];

/**
 * The line for one (kind, state), by literal key.
 *
 * LITERAL, not `t(`agent.activity.${kind}.${state}`)`. The orphan guard in
 * i18n.test.ts counts a key as rendered when it starts with a template STEM,
 * and its regex stops at the first `${` — so an interpolated key would vouch
 * for the whole `agent.activity.` namespace forever, and a retired kind's copy
 * would sit in three catalogs with nothing to flag it.
 *
 * Total over kind, partial over state, and both halves earn their keep: a third
 * kind on the contract fails the build until somebody writes its copy, while
 * `awaiting_approval` stays keyless because neither v1 spec can stage a
 * confirmation (both are auto-execute). Writing that key would mean translating
 * a sentence nothing can produce.
 */
export const ACTIVITY_LINE: Readonly<
  Record<ActivityKind, Readonly<Partial<Record<ActivityState, MessageKey>>>>
> = {
  morning_brief: {
    queued: "agent.activity.morningBrief.queued",
    running: "agent.activity.morningBrief.running",
    done: "agent.activity.morningBrief.done",
    degraded: "agent.activity.morningBrief.degraded",
    failed: "agent.activity.morningBrief.failed",
  },
  overnight_at_risk_sweep: {
    queued: "agent.activity.riskSweep.queued",
    running: "agent.activity.riskSweep.running",
    done: "agent.activity.riskSweep.done",
    degraded: "agent.activity.riskSweep.degraded",
    failed: "agent.activity.riskSweep.failed",
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
  Record<"running" | "settled" | "stopped", MessageKey>
> = {
  running: "agent.panel.runningNow",
  settled: "agent.panel.finishedToday",
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
