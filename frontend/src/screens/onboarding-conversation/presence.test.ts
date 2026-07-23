import { describe, expect, it } from "vitest";
import {
  type ConversationState,
  initialConversationState,
} from "./conversation-machine";
import { presenceFor } from "./presence";

// The orb choreography as a spec: for every phase the conversation can be
// in, exactly one presence — and a progress ring only while a read or build
// is actually running, fed by server counters, never invented.

function state(patch: Partial<ConversationState>): ConversationState {
  return { ...initialConversationState, ...patch };
}

const reading = state({ act: "company", phase: "co.reading" });

describe("presenceFor: welcome and company act", () => {
  it("idles before the restore settles", () => {
    expect(presenceFor(initialConversationState)).toEqual({ core: "idle" });
  });

  it("listens while the human owes the URL", () => {
    expect(presenceFor(state({ act: "company", phase: "co.intro" }))).toEqual({
      core: "listening",
    });
  });

  it("works with a pages-driven ring while the read runs", () => {
    const presence = presenceFor(reading, {
      read: { status: "reading", phase: "crawling", pages_read: 10 },
    });
    expect(presence.core).toBe("working");
    expect(presence.progress).toBeCloseTo(0.25);
  });

  it("keeps the ring inside its honest band: floor, crawl cap, extracting", () => {
    const floor = presenceFor(reading, {
      read: { status: "queued", phase: null, pages_read: 0 },
    });
    expect(floor.progress).toBeCloseTo(0.08);
    const capped = presenceFor(reading, {
      read: { status: "reading", phase: "crawling", pages_read: 400 },
    });
    expect(capped.progress).toBeCloseTo(0.78);
    const extracting = presenceFor(reading, {
      read: { status: "reading", phase: "extracting", pages_read: 400 },
    });
    expect(extracting.progress).toBeCloseTo(0.84);
  });

  it("asks for attention while a clarify question waits", () => {
    expect(
      presenceFor(state({ act: "company", phase: "co.clarify" })).core,
    ).toBe("attention");
  });

  it("marks review and confirmed as success", () => {
    expect(
      presenceFor(state({ act: "company", phase: "co.review" })).core,
    ).toBe("success");
    expect(
      presenceFor(state({ act: "company", phase: "co.confirmed" })).core,
    ).toBe("success");
  });

  it("shows error on a broken or failed read, quiet on deferred", () => {
    expect(presenceFor(reading, { readBroken: true }).core).toBe("error");
    expect(
      presenceFor(reading, {
        read: { status: "failed", phase: null, pages_read: 3 },
      }).core,
    ).toBe("error");
    expect(
      presenceFor(reading, {
        read: { status: "deferred", phase: null, pages_read: 3 },
      }).core,
    ).toBe("quiet");
  });
});

describe("presenceFor: voice, results, connect", () => {
  it("rings the build stages as quarters while building", () => {
    const base = state({ act: "voice", phase: "vo.building" });
    expect(presenceFor(base)).toEqual({ core: "working", progress: 0.08 });
    expect(
      presenceFor({ ...base, lastBuildStage: "snapshot" }).progress,
    ).toBeCloseTo(0.25);
    expect(
      presenceFor({ ...base, lastBuildStage: "activate" }).progress,
    ).toBeCloseTo(1);
  });

  it("asks for attention on the invite and the speaker question", () => {
    expect(presenceFor(state({ act: "voice", phase: "vo.invite" })).core).toBe(
      "attention",
    );
    expect(presenceFor(state({ act: "voice", phase: "vo.speaker" })).core).toBe(
      "attention",
    );
  });

  it("maps the build result to its honest presence", () => {
    const result = state({ act: "voice", phase: "vo.result" });
    expect(presenceFor({ ...result, lastBuildStatus: "succeeded" }).core).toBe(
      "success",
    );
    expect(presenceFor({ ...result, lastBuildStatus: "failed" }).core).toBe(
      "error",
    );
    expect(presenceFor({ ...result, lastBuildStatus: "deferred" }).core).toBe(
      "quiet",
    );
  });

  it("listens while collecting and after a skip", () => {
    expect(
      presenceFor(state({ act: "voice", phase: "vo.collecting" })).core,
    ).toBe("listening");
    expect(presenceFor(state({ act: "voice", phase: "vo.skipped" })).core).toBe(
      "listening",
    );
  });

  it("celebrates the recap, listens through consent, succeeds on done", () => {
    expect(presenceFor(state({ act: "results", phase: "re.recap" }))).toEqual({
      core: "success",
    });
    expect(presenceFor(state({ act: "connect", phase: "cn.consent" }))).toEqual(
      { core: "listening" },
    );
    expect(presenceFor(state({ act: "done", phase: "cn.done" }))).toEqual({
      core: "success",
    });
  });
});
