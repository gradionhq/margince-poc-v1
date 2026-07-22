import { useCallback, useEffect, useRef } from "react";
import type { components } from "../../api/schema";
import type { MessageKey } from "../../i18n/en";
import { coldFieldLabelKey } from "../common";
import type { BuildStage } from "./conversation-machine";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];
type VoiceBuildStatus = components["schemas"]["VoiceBuild"]["status"];

// Narration is derived, never invented: every event below is a delta between
// two server snapshots, and every parameter it carries comes from the server
// payload. The queue paces delivery so a burst of findings reads like a
// conversation.
//
// Terminal copy has ONE owner: the reducer's READ_TERMINAL / BUILD_TERMINAL
// outcome entries. The diffs never narrate a terminal state themselves — on
// a fresh terminal they emit a "flush" event, which drains all queued
// progress narration immediately. Wiring contract for the shell: push the
// diff output into the queue first (the flush drains it), THEN dispatch the
// terminal machine event, so progress always lands before the outcome.

export type NarrationSayEvent = {
  kind: "say";
  /** Scoped to the run/operation, stable across repeated diffs. */
  id: string;
  i18nKey: MessageKey;
  params?: Record<string, string | number>;
  /** Params that are i18n keys themselves; the renderer translates them. */
  paramKeys?: Record<string, MessageKey>;
  findingIds?: string[];
};

export type NarrationFlushEvent = { kind: "flush"; id: string };

export type NarrationEvent = NarrationSayEvent | NarrationFlushEvent;

const VALUE_PREVIEW_LIMIT = 80;

// Truncates by Unicode code points so a surrogate pair is never split.
function previewValue(value: string): string {
  const points = Array.from(value);
  if (points.length <= VALUE_PREVIEW_LIMIT) return value;
  return `${points.slice(0, VALUE_PREVIEW_LIMIT - 1).join("")}…`;
}

// The four states a running read can freshly end in. `confirmed` and
// `abandoned` are post-outcome lifecycle states (the human already acted on
// a terminal read) — transitioning into them announces nothing new.
const freshReadTerminals = new Set<CompanySiteRead["status"]>([
  "ready",
  "partial",
  "failed",
  "deferred",
]);

function newFieldEvents(
  prev: CompanySiteRead | null,
  next: CompanySiteRead,
): NarrationEvent[] {
  const known = new Set((prev?.profile_fields ?? []).map((f) => f.field));
  return next.profile_fields
    .filter((field) => !known.has(field.field))
    .map((field): NarrationEvent => {
      const labelKey = coldFieldLabelKey(field.field);
      const params: Record<string, string | number> = {
        value: previewValue(field.value),
      };
      if (!labelKey) {
        params.field = field.field.replace(/_/g, " ");
      }
      return {
        kind: "say",
        id: `${next.id}:field:${field.field}`,
        i18nKey: "ob.conv.read.learnedField",
        params,
        ...(labelKey ? { paramKeys: { field: labelKey } } : {}),
        findingIds: [field.field],
      };
    });
}

function newWarningEvents(
  prev: CompanySiteRead | null,
  next: CompanySiteRead,
): NarrationEvent[] {
  const known = new Set(prev?.warnings ?? []);
  return next.warnings
    .filter((warning) => !known.has(warning))
    .map((warning) => ({
      kind: "say" as const,
      id: `${next.id}:warning:${warning}`,
      i18nKey: "ob.conv.read.warning" as const,
      params: { warning },
    }));
}

export function diffSiteRead(
  prev: CompanySiteRead | null,
  next: CompanySiteRead,
): NarrationEvent[] {
  const events: NarrationEvent[] = [];
  const run = next.id;

  // Page progress coalesces: one event per poll that saw growth, carrying the
  // running total rather than one bubble per page.
  const prevPages = prev?.pages_read ?? 0;
  const nextPages = next.pages_read ?? 0;
  if (nextPages > prevPages) {
    events.push({
      kind: "say",
      id: `${run}:pages:${nextPages}`,
      i18nKey: "ob.conv.read.pages",
      params: { pages: nextPages },
    });
  }

  if (next.phase === "extracting" && prev?.phase !== "extracting") {
    events.push({
      kind: "say",
      id: `${run}:phase:extracting`,
      i18nKey: "ob.conv.read.extracting",
    });
  }

  events.push(...newFieldEvents(prev, next), ...newWarningEvents(prev, next));

  if (freshReadTerminals.has(next.status) && prev?.status !== next.status) {
    events.push({ kind: "flush", id: `${run}:flush:${next.status}` });
  }

  return events;
}

const buildStageEvents: Record<BuildStage, MessageKey> = {
  snapshot: "ob.conv.build.snapshot",
  extract: "ob.conv.build.extract",
  evaluate: "ob.conv.build.evaluate",
  activate: "ob.conv.build.activate",
};

const buildTerminals = new Set<VoiceBuildStatus>([
  "succeeded",
  "failed",
  "deferred",
]);

export type VoiceBuildSnapshot = {
  id: string;
  status: VoiceBuildStatus;
  stage: BuildStage | null;
};

export function diffVoiceBuild(
  prev: VoiceBuildSnapshot | null,
  next: VoiceBuildSnapshot,
): NarrationEvent[] {
  if (buildTerminals.has(next.status)) {
    // A poll repeating an already-seen terminal announces nothing.
    return prev?.status === next.status
      ? []
      : [{ kind: "flush", id: `${next.id}:flush:${next.status}` }];
  }
  if (next.stage && next.stage !== prev?.stage) {
    return [
      {
        kind: "say",
        id: `${next.id}:stage:${next.stage}`,
        i18nKey: buildStageEvents[next.stage],
      },
    ];
  }
  return [];
}

const bandLabelKeys: Record<VoiceCorpusSummary["quality_band"], MessageKey> = {
  thin: "settings.voice.bandThin",
  good: "settings.voice.bandGood",
  rich: "settings.voice.bandRich",
  sharp: "settings.voice.bandSharp",
};

export function diffCorpus(
  prev: VoiceCorpusSummary | null,
  next: VoiceCorpusSummary,
): NarrationEvent[] {
  const events: NarrationEvent[] = [];
  const prevWords = prev?.total_words ?? 0;
  if (next.total_words > prevWords) {
    events.push({
      kind: "say",
      id: `words:${next.total_words}`,
      i18nKey: "ob.conv.corpus.words",
      params: { words: next.total_words },
    });
  }
  if (prev && next.quality_band !== prev.quality_band) {
    events.push({
      kind: "say",
      id: `band:${next.quality_band}`,
      i18nKey: "ob.conv.corpus.band",
      paramKeys: { band: bandLabelKeys[next.quality_band] },
    });
  }
  return events;
}

export const DEFAULT_PACE_MS = 1800;

type NarrationQueueOptions = {
  onEmit: (event: NarrationSayEvent) => void;
  paceMs?: number;
};

// Paces narration to one bubble per paceMs so bursts stay readable. A flush
// event drains the whole queue synchronously; the user is never left
// watching a paced trickle after the outcome is already known.
export function useNarrationQueue({
  onEmit,
  paceMs = DEFAULT_PACE_MS,
}: NarrationQueueOptions) {
  const queue = useRef<NarrationSayEvent[]>([]);
  const timer = useRef<ReturnType<typeof globalThis.setTimeout> | null>(null);
  const emit = useRef(onEmit);
  emit.current = onEmit;

  const flush = useCallback(() => {
    if (timer.current !== null) {
      globalThis.clearTimeout(timer.current);
      timer.current = null;
    }
    const pending = queue.current;
    queue.current = [];
    for (const event of pending) {
      emit.current(event);
    }
  }, []);

  const pump = useCallback(() => {
    if (timer.current !== null) return;
    const next = queue.current.shift();
    if (!next) return;
    emit.current(next);
    // The timer arms even when the queue is now empty: it is the pacing
    // window for whatever arrives next, not just for the current backlog.
    timer.current = globalThis.setTimeout(() => {
      timer.current = null;
      pump();
    }, paceMs);
  }, [paceMs]);

  const push = useCallback(
    (events: readonly NarrationEvent[]) => {
      let sawFlush = false;
      for (const event of events) {
        if (event.kind === "flush") {
          sawFlush = true;
        } else {
          queue.current.push(event);
        }
      }
      if (sawFlush) {
        flush();
        return;
      }
      pump();
    },
    [flush, pump],
  );

  useEffect(
    () => () => {
      if (timer.current !== null) {
        globalThis.clearTimeout(timer.current);
        timer.current = null;
      }
    },
    [],
  );

  return { push, flush };
}
