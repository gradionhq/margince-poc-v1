/** @vitest-environment jsdom */
import { renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import {
  DEFAULT_PACE_MS,
  diffCorpus,
  diffSiteRead,
  diffVoiceBuild,
  type NarrationEvent,
  useNarrationQueue,
} from "./narration";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type ColdStartField = components["schemas"]["ColdStartField"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];

// The diffs are the honesty layer: every narration event must trace back to
// a server snapshot delta. These tests pin the deltas and the queue's pacing
// and urgent-flush behaviour with fake timers only.

function field(name: ColdStartField["field"], value: string): ColdStartField {
  return {
    field: name,
    value,
    evidence_snippet: `snippet for ${name}`,
    source_kind: "url",
    confidence: 0.8,
  };
}

function read(overrides: Partial<CompanySiteRead>): CompanySiteRead {
  return {
    id: "8b6e2a34-0000-4000-8000-000000000001",
    target_kind: "onboarding",
    root_url: "https://acme.example",
    status: "reading",
    status_code: null,
    status_detail: null,
    next_attempt_at: null,
    pages: [],
    profile_fields: [],
    facts: [],
    comparisons: [],
    people: [],
    warnings: [],
    draft_version: 1,
    proposal_hash: "h1",
    created_at: "2026-07-22T09:00:00Z",
    updated_at: "2026-07-22T09:00:00Z",
    ...overrides,
  };
}

describe("diffSiteRead", () => {
  it("narrates page growth, new fields, and the crawl-to-extract handover", () => {
    const prev = read({
      pages_read: 2,
      phase: "crawling",
      profile_fields: [field("display_name", "Acme")],
    });
    const next = read({
      pages_read: 5,
      phase: "extracting",
      profile_fields: [
        field("display_name", "Acme"),
        field("industry", "Robotics"),
      ],
    });
    const events = diffSiteRead(prev, next);
    expect(events).toEqual([
      {
        id: "pages:5",
        i18nKey: "ob.conv.read.pages",
        params: { pages: 5 },
      },
      { id: "phase:extracting", i18nKey: "ob.conv.read.extracting" },
      {
        id: "field:industry",
        i18nKey: "ob.conv.read.learnedField",
        params: { field: "industry", value: "Robotics" },
        findingIds: ["industry"],
      },
    ]);
  });

  it("emits one event per field on the first snapshot and none on a repeat", () => {
    const snapshot = read({
      profile_fields: [field("display_name", "Acme"), field("icp", "SMB ops")],
    });
    expect(diffSiteRead(null, snapshot)).toHaveLength(2);
    expect(diffSiteRead(snapshot, snapshot)).toEqual([]);
  });

  it("truncates long field values to the 80-character preview", () => {
    const long = "x".repeat(120);
    const events = diffSiteRead(
      null,
      read({ profile_fields: [field("history", long)] }),
    );
    const value = String(events[0]?.params?.value);
    expect(value.length).toBe(80);
    expect(value.endsWith("…")).toBe(true);
  });

  it("narrates new warnings but never repeats known ones", () => {
    const prev = read({ warnings: ["robots.txt blocked /careers"] });
    const next = read({
      warnings: ["robots.txt blocked /careers", "sitemap missing"],
    });
    expect(diffSiteRead(prev, next)).toEqual([
      {
        id: "warning:sitemap missing",
        i18nKey: "ob.conv.read.warning",
        params: { warning: "sitemap missing" },
      },
    ]);
  });

  it("flags terminal statuses urgent, with the server-side finding count", () => {
    const prev = read({
      status: "reading",
      profile_fields: [field("display_name", "Acme")],
    });
    const next = read({
      status: "ready",
      profile_fields: [field("display_name", "Acme")],
    });
    const events = diffSiteRead(prev, next);
    expect(events).toEqual([
      {
        id: "status:ready",
        i18nKey: "ob.conv.read.done",
        params: { count: 1 },
        urgent: true,
      },
    ]);
    // Polling the same terminal snapshot again stays silent.
    expect(diffSiteRead(next, next)).toEqual([]);
  });

  it("narrates failed and deferred reads as urgent too", () => {
    expect(diffSiteRead(read({}), read({ status: "failed" }))[0]).toMatchObject(
      { i18nKey: "ob.conv.read.failed", urgent: true },
    );
    expect(
      diffSiteRead(read({}), read({ status: "deferred" }))[0],
    ).toMatchObject({ i18nKey: "ob.conv.read.deferred", urgent: true });
  });
});

describe("diffVoiceBuild", () => {
  it("narrates each stage change exactly once", () => {
    expect(diffVoiceBuild(null, "snapshot", "running")).toEqual([
      { id: "stage:snapshot", i18nKey: "ob.conv.build.snapshot" },
    ]);
    expect(diffVoiceBuild("snapshot", "extract", "running")).toEqual([
      { id: "stage:extract", i18nKey: "ob.conv.build.extract" },
    ]);
    expect(diffVoiceBuild("extract", "extract", "running")).toEqual([]);
  });

  it("terminal statuses win over stage changes and are urgent", () => {
    expect(diffVoiceBuild("evaluate", "activate", "succeeded")).toEqual([
      {
        id: "build:succeeded",
        i18nKey: "ob.conv.build.succeeded",
        urgent: true,
      },
    ]);
    expect(diffVoiceBuild(null, null, "failed")[0]).toMatchObject({
      i18nKey: "ob.conv.build.failed",
      urgent: true,
    });
  });
});

describe("diffCorpus", () => {
  const summary = (
    total_words: number,
    quality_band: VoiceCorpusSummary["quality_band"],
  ): VoiceCorpusSummary => ({
    total_words,
    target_words: 30000,
    maturity: "collecting",
    quality_band,
    source_count: 1,
    register_words: {},
  });

  it("narrates word growth and band changes from server numbers only", () => {
    expect(diffCorpus(summary(400, "thin"), summary(1240, "good"))).toEqual([
      {
        id: "words:1240",
        i18nKey: "ob.conv.corpus.words",
        params: { words: 1240 },
      },
      {
        id: "band:good",
        i18nKey: "ob.conv.corpus.band",
        params: { band: "good" },
      },
    ]);
    expect(diffCorpus(summary(1240, "good"), summary(1240, "good"))).toEqual(
      [],
    );
  });
});

describe("useNarrationQueue", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  const event = (id: string, urgent?: boolean): NarrationEvent => ({
    id,
    i18nKey: "ob.conv.read.pages",
    params: { pages: 1 },
    ...(urgent ? { urgent } : {}),
  });

  it("emits the first event immediately and then one per pace window", () => {
    const onEmit = vi.fn();
    const { result } = renderHook(() => useNarrationQueue({ onEmit }));

    result.current.push([event("a"), event("b"), event("c")]);
    expect(onEmit.mock.calls.map(([e]) => e.id)).toEqual(["a"]);

    vi.advanceTimersByTime(DEFAULT_PACE_MS);
    expect(onEmit.mock.calls.map(([e]) => e.id)).toEqual(["a", "b"]);

    vi.advanceTimersByTime(DEFAULT_PACE_MS);
    expect(onEmit.mock.calls.map(([e]) => e.id)).toEqual(["a", "b", "c"]);
  });

  it("keeps pacing across separate pushes inside the same window", () => {
    const onEmit = vi.fn();
    const { result } = renderHook(() =>
      useNarrationQueue({ onEmit, paceMs: 1000 }),
    );
    result.current.push([event("a")]);
    result.current.push([event("b")]);
    expect(onEmit).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1000);
    expect(onEmit).toHaveBeenCalledTimes(2);
  });

  it("flushes everything queued the moment an urgent event arrives", () => {
    const onEmit = vi.fn();
    const { result } = renderHook(() => useNarrationQueue({ onEmit }));

    result.current.push([event("a"), event("b")]);
    expect(onEmit).toHaveBeenCalledTimes(1);

    result.current.push([event("terminal", true)]);
    expect(onEmit.mock.calls.map(([e]) => e.id)).toEqual([
      "a",
      "b",
      "terminal",
    ]);

    // Nothing left ticking afterwards.
    vi.advanceTimersByTime(DEFAULT_PACE_MS * 3);
    expect(onEmit).toHaveBeenCalledTimes(3);
  });

  it("cancels the pending timer on unmount", () => {
    const onEmit = vi.fn();
    const { result, unmount } = renderHook(() => useNarrationQueue({ onEmit }));
    result.current.push([event("a"), event("b")]);
    unmount();
    vi.advanceTimersByTime(DEFAULT_PACE_MS * 2);
    expect(onEmit).toHaveBeenCalledTimes(1);
  });
});
