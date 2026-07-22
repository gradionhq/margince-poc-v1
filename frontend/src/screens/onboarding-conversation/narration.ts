import { useCallback, useEffect, useRef } from "react";
import type { components } from "../../api/schema";
import type { MessageKey } from "../../i18n/en";
import type { BuildStage, BuildTerminalStatus } from "./conversation-machine";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];
type VoiceBuildStatus = components["schemas"]["VoiceBuild"]["status"];

// Narration is derived, never invented: every event below is a delta between
// two server snapshots, and every parameter it carries comes from the server
// payload. The queue paces delivery so a burst of findings reads like a
// conversation, but terminal states always flush immediately; pacing may
// delay honesty, never withhold it.

export type NarrationEvent = {
  /** Stable across repeated diffs of the same snapshot pair. */
  id: string;
  i18nKey: MessageKey;
  params?: Record<string, string | number>;
  findingIds?: string[];
  /** Terminal states bypass pacing; the queue flushes everything queued. */
  urgent?: boolean;
};

const VALUE_PREVIEW_LIMIT = 80;

function previewValue(value: string): string {
  if (value.length <= VALUE_PREVIEW_LIMIT) return value;
  return `${value.slice(0, VALUE_PREVIEW_LIMIT - 1)}…`;
}

const readTerminalEvents: Partial<
  Record<CompanySiteRead["status"], MessageKey>
> = {
  ready: "ob.conv.read.done",
  confirmed: "ob.conv.read.done",
  partial: "ob.conv.read.partial",
  failed: "ob.conv.read.failed",
  abandoned: "ob.conv.read.failed",
  deferred: "ob.conv.read.deferred",
};

export function diffSiteRead(
  prev: CompanySiteRead | null,
  next: CompanySiteRead,
): NarrationEvent[] {
  const events: NarrationEvent[] = [];

  // Page progress coalesces: one event per poll that saw growth, carrying the
  // running total rather than one bubble per page.
  const prevPages = prev?.pages_read ?? 0;
  const nextPages = next.pages_read ?? 0;
  if (nextPages > prevPages) {
    events.push({
      id: `pages:${nextPages}`,
      i18nKey: "ob.conv.read.pages",
      params: { pages: nextPages },
    });
  }

  if (next.phase === "extracting" && prev?.phase !== "extracting") {
    events.push({ id: "phase:extracting", i18nKey: "ob.conv.read.extracting" });
  }

  const knownFields = new Set(
    (prev?.profile_fields ?? []).map((field) => field.field),
  );
  for (const field of next.profile_fields) {
    if (knownFields.has(field.field)) continue;
    events.push({
      id: `field:${field.field}`,
      i18nKey: "ob.conv.read.learnedField",
      params: { field: field.field, value: previewValue(field.value) },
      findingIds: [field.field],
    });
  }

  const knownWarnings = new Set(prev?.warnings ?? []);
  for (const warning of next.warnings) {
    if (knownWarnings.has(warning)) continue;
    events.push({
      id: `warning:${warning}`,
      i18nKey: "ob.conv.read.warning",
      params: { warning },
    });
  }

  const terminalKey = readTerminalEvents[next.status];
  if (terminalKey && prev?.status !== next.status) {
    events.push({
      id: `status:${next.status}`,
      i18nKey: terminalKey,
      params: {
        count: next.profile_fields.length + next.facts.length,
      },
      urgent: true,
    });
  }

  return events;
}

const buildStageEvents: Record<BuildStage, MessageKey> = {
  snapshot: "ob.conv.build.snapshot",
  extract: "ob.conv.build.extract",
  evaluate: "ob.conv.build.evaluate",
  activate: "ob.conv.build.activate",
};

const buildTerminalEvents: Partial<Record<VoiceBuildStatus, MessageKey>> = {
  succeeded: "ob.conv.build.succeeded",
  failed: "ob.conv.build.failed",
  deferred: "ob.conv.build.deferred",
};

export function diffVoiceBuild(
  prevStage: BuildStage | null,
  nextStage: BuildStage | null,
  status: VoiceBuildStatus,
): NarrationEvent[] {
  const terminalKey = buildTerminalEvents[status];
  if (terminalKey) {
    return [
      {
        id: `build:${status as BuildTerminalStatus}`,
        i18nKey: terminalKey,
        urgent: true,
      },
    ];
  }
  if (nextStage && nextStage !== prevStage) {
    return [{ id: `stage:${nextStage}`, i18nKey: buildStageEvents[nextStage] }];
  }
  return [];
}

export function diffCorpus(
  prev: VoiceCorpusSummary | null,
  next: VoiceCorpusSummary,
): NarrationEvent[] {
  const events: NarrationEvent[] = [];
  const prevWords = prev?.total_words ?? 0;
  if (next.total_words > prevWords) {
    events.push({
      id: `words:${next.total_words}`,
      i18nKey: "ob.conv.corpus.words",
      params: { words: next.total_words },
    });
  }
  if (prev && next.quality_band !== prev.quality_band) {
    events.push({
      id: `band:${next.quality_band}`,
      i18nKey: "ob.conv.corpus.band",
      params: { band: next.quality_band },
    });
  }
  return events;
}

export const DEFAULT_PACE_MS = 1800;

type NarrationQueueOptions = {
  onEmit: (event: NarrationEvent) => void;
  paceMs?: number;
};

// Paces narration to one bubble per paceMs so bursts stay readable. An urgent
// event (a terminal state) drains the whole queue synchronously; the user is
// never left watching a paced trickle after the outcome is already known.
export function useNarrationQueue({
  onEmit,
  paceMs = DEFAULT_PACE_MS,
}: NarrationQueueOptions) {
  const queue = useRef<NarrationEvent[]>([]);
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
      if (events.length === 0) return;
      queue.current.push(...events);
      if (events.some((event) => event.urgent)) {
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
