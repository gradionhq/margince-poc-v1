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
  type NarrationSayEvent,
  useNarrationQueue,
  type VoiceBuildSnapshot,
} from "./narration";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type ColdStartField = components["schemas"]["ColdStartField"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];

// The diffs are the honesty layer: every narration event must trace back to
// a server snapshot delta, and terminal copy belongs to the reducer alone —
// the diffs only signal a flush. These tests pin the deltas and the queue's
// pacing and flush behaviour with fake timers only.

const READ_ID = "8b6e2a34-0000-4000-8000-000000000001";

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
    id: READ_ID,
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
        kind: "say",
        id: `${READ_ID}:pages:5`,
        i18nKey: "ob.conv.read.pages",
        params: { pages: 5 },
      },
      {
        kind: "say",
        id: `${READ_ID}:phase:extracting`,
        i18nKey: "ob.conv.read.extracting",
      },
      {
        kind: "say",
        id: `${READ_ID}:field:industry`,
        i18nKey: "ob.conv.read.learnedField",
        params: { value: "Robotics" },
        paramKeys: { field: "ob.field.industry" },
        findingIds: ["industry"],
      },
    ]);
  });

  it("carries field labels as i18n keys, never raw backend tokens", () => {
    const events = diffSiteRead(
      null,
      read({ profile_fields: [field("offer_summary", "Robots as a service")] }),
    );
    expect(events[0]).toMatchObject({
      paramKeys: { field: "ob.field.offer_summary" },
      params: { value: "Robots as a service" },
    });
  });

  it("emits one event per field on the first snapshot and none on a repeat", () => {
    const snapshot = read({
      profile_fields: [field("display_name", "Acme"), field("icp", "SMB ops")],
    });
    expect(diffSiteRead(null, snapshot)).toHaveLength(2);
    expect(diffSiteRead(snapshot, snapshot)).toEqual([]);
  });

  it("truncates long field values by code points to the 80-glyph preview", () => {
    const long = "🜁".repeat(120);
    const events = diffSiteRead(
      null,
      read({ profile_fields: [field("history", long)] }),
    );
    const first = events[0];
    if (first?.kind !== "say") throw new Error("expected a say event");
    const value = String(first.params?.value);
    expect(Array.from(value)).toHaveLength(80);
    expect(value.endsWith("…")).toBe(true);
  });

  it("narrates new warnings but never repeats known ones", () => {
    const prev = read({ warnings: ["robots.txt blocked /careers"] });
    const next = read({
      warnings: ["robots.txt blocked /careers", "sitemap missing"],
    });
    expect(diffSiteRead(prev, next)).toEqual([
      {
        kind: "say",
        id: `${READ_ID}:warning:sitemap missing`,
        i18nKey: "ob.conv.read.warning",
        params: { warning: "sitemap missing" },
      },
    ]);
  });

  it("signals a flush on a fresh terminal instead of narrating it", () => {
    const prev = read({ status: "reading" });
    const next = read({ status: "ready" });
    expect(diffSiteRead(prev, next)).toEqual([
      { kind: "flush", id: `${READ_ID}:flush:ready` },
    ]);
    // Polling the same terminal snapshot again stays silent.
    expect(diffSiteRead(next, next)).toEqual([]);
    for (const status of ["partial", "failed", "deferred"] as const) {
      expect(diffSiteRead(prev, read({ status }))).toEqual([
        { kind: "flush", id: `${READ_ID}:flush:${status}` },
      ]);
    }
  });

  it("stays silent on post-outcome lifecycle transitions (confirmed, abandoned)", () => {
    expect(
      diffSiteRead(read({ status: "ready" }), read({ status: "confirmed" })),
    ).toEqual([]);
    expect(
      diffSiteRead(read({ status: "failed" }), read({ status: "abandoned" })),
    ).toEqual([]);
  });
});

describe("diffVoiceBuild", () => {
  const snap = (
    status: VoiceBuildSnapshot["status"],
    stage: VoiceBuildSnapshot["stage"],
  ): VoiceBuildSnapshot => ({ id: "b1", status, stage });

  it("narrates each stage change exactly once", () => {
    expect(diffVoiceBuild(null, snap("running", "snapshot"))).toEqual([
      {
        kind: "say",
        id: "b1:stage:snapshot",
        i18nKey: "ob.conv.build.snapshot",
      },
    ]);
    expect(
      diffVoiceBuild(snap("running", "snapshot"), snap("running", "extract")),
    ).toEqual([
      { kind: "say", id: "b1:stage:extract", i18nKey: "ob.conv.build.extract" },
    ]);
    expect(
      diffVoiceBuild(snap("running", "extract"), snap("running", "extract")),
    ).toEqual([]);
  });

  it("signals a flush on a fresh terminal, never narrating terminal copy", () => {
    expect(
      diffVoiceBuild(
        snap("running", "evaluate"),
        snap("succeeded", "activate"),
      ),
    ).toEqual([{ kind: "flush", id: "b1:flush:succeeded" }]);
    expect(diffVoiceBuild(null, snap("failed", null))).toEqual([
      { kind: "flush", id: "b1:flush:failed" },
    ]);
  });

  it("does not re-announce an unchanged terminal on repeated polls", () => {
    expect(
      diffVoiceBuild(
        snap("succeeded", "activate"),
        snap("succeeded", "activate"),
      ),
    ).toEqual([]);
    expect(diffVoiceBuild(snap("failed", null), snap("failed", null))).toEqual(
      [],
    );
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

  it("narrates word growth and band changes, band as an i18n label key", () => {
    expect(diffCorpus(summary(400, "thin"), summary(1240, "good"))).toEqual([
      {
        kind: "say",
        id: "words:1240",
        i18nKey: "ob.conv.corpus.words",
        params: { words: 1240 },
      },
      {
        kind: "say",
        id: "band:good",
        i18nKey: "ob.conv.corpus.band",
        paramKeys: { band: "settings.voice.bandGood" },
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

  const say = (id: string): NarrationSayEvent => ({
    kind: "say",
    id,
    i18nKey: "ob.conv.read.pages",
    params: { pages: 1 },
  });
  const flush = (id: string): NarrationEvent => ({ kind: "flush", id });

  it("emits the first event immediately and then one per pace window", () => {
    const onEmit = vi.fn();
    const { result } = renderHook(() => useNarrationQueue({ onEmit }));

    result.current.push([say("a"), say("b"), say("c")]);
    expect(onEmit.mock.calls.map(([event]) => event.id)).toEqual(["a"]);

    vi.advanceTimersByTime(DEFAULT_PACE_MS);
    expect(onEmit.mock.calls.map(([event]) => event.id)).toEqual(["a", "b"]);

    vi.advanceTimersByTime(DEFAULT_PACE_MS);
    expect(onEmit.mock.calls.map(([event]) => event.id)).toEqual([
      "a",
      "b",
      "c",
    ]);
  });

  it("keeps pacing across separate pushes inside the same window", () => {
    const onEmit = vi.fn();
    const { result } = renderHook(() =>
      useNarrationQueue({ onEmit, paceMs: 1000 }),
    );
    result.current.push([say("a")]);
    result.current.push([say("b")]);
    expect(onEmit).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(1000);
    expect(onEmit).toHaveBeenCalledTimes(2);
  });

  it("drains everything queued the moment a flush event arrives, emitting no terminal copy itself", () => {
    const onEmit = vi.fn();
    const { result } = renderHook(() => useNarrationQueue({ onEmit }));

    result.current.push([say("a"), say("b")]);
    expect(onEmit).toHaveBeenCalledTimes(1);

    result.current.push([flush("terminal")]);
    expect(onEmit.mock.calls.map(([event]) => event.id)).toEqual(["a", "b"]);

    // Nothing left ticking afterwards, and the flush itself emitted nothing.
    vi.advanceTimersByTime(DEFAULT_PACE_MS * 3);
    expect(onEmit).toHaveBeenCalledTimes(2);
  });

  it("cancels the pending timer on unmount", () => {
    const onEmit = vi.fn();
    const { result, unmount } = renderHook(() => useNarrationQueue({ onEmit }));
    result.current.push([say("a"), say("b")]);
    unmount();
    vi.advanceTimersByTime(DEFAULT_PACE_MS * 2);
    expect(onEmit).toHaveBeenCalledTimes(1);
  });
});
