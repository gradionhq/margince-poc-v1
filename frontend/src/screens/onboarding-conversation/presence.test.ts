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

  it("rests while the human owes the URL", () => {
    // Not a state that claims to be listening: this agent reads captured
    // activity, and the surface's own field is what asks for the URL.
    expect(presenceFor(state({ act: "company", phase: "co.intro" }))).toEqual({
      core: "idle",
    });
  });

  it("ingests with a pages-driven ring while the read crawls", () => {
    const presence = presenceFor(reading, {
      read: { status: "reading", phase: "crawling", pages_read: 10 },
    });
    expect(presence.core).toBe("ingest");
    expect(presence.progress).toBeCloseTo(0.25);
  });

  it("turns to working once the read stops crawling and starts extracting", () => {
    // The two halves of a read are two different states: pages ARRIVING, then the
    // agent working over what arrived. One `working` for both was the Core saying
    // "busy" through a process the reader can actually follow.
    const presence = presenceFor(reading, {
      read: { status: "reading", phase: "extracting", pages_read: 40 },
    });
    expect(presence.core).toBe("working");
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

  it("flags a clarify question, because the read hit something it cannot resolve", () => {
    expect(
      presenceFor(state({ act: "company", phase: "co.clarify" })).core,
    ).toBe("warning");
  });

  it("rests at review and applies once confirmed", () => {
    // Review is proposals in front of a person: the agent has stopped, so the orb
    // must not claim work nobody asked it to keep doing.
    expect(
      presenceFor(state({ act: "company", phase: "co.review" })).core,
    ).toBe("idle");
    expect(
      presenceFor(state({ act: "company", phase: "co.confirmed" })).core,
    ).toBe("idle");
  });

  it("shows error on a broken or failed read, rest on deferred", () => {
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
    ).toBe("idle");
  });
});

describe("presenceFor: voice, results, connect", () => {
  it("rings the build stages as quarters while building", () => {
    const base = state({ act: "voice", phase: "vo.building" });
    expect(presenceFor(base)).toEqual({ core: "ingest", progress: 0.08 });
    expect(
      presenceFor({ ...base, lastBuildStage: "snapshot" }).progress,
    ).toBeCloseTo(0.25);
    expect(
      presenceFor({ ...base, lastBuildStage: "activate" }).progress,
    ).toBeCloseTo(1);
  });

  it("rests on the speaker question, because the agent is not working", () => {
    expect(presenceFor(state({ act: "voice", phase: "vo.speaker" })).core).toBe(
      "idle",
    );
  });

  it("maps the build result to its honest presence", () => {
    const result = state({ act: "voice", phase: "vo.result" });
    expect(presenceFor({ ...result, lastBuildStatus: "succeeded" }).core).toBe(
      "idle",
    );
    expect(presenceFor({ ...result, lastBuildStatus: "failed" }).core).toBe(
      "error",
    );
    expect(presenceFor({ ...result, lastBuildStatus: "deferred" }).core).toBe(
      "idle",
    );
  });

  it("rests while collecting and after a skip", () => {
    expect(
      presenceFor(state({ act: "voice", phase: "vo.collecting" })).core,
    ).toBe("idle");
    expect(presenceFor(state({ act: "voice", phase: "vo.skipped" })).core).toBe(
      "idle",
    );
  });

  it("settles on the recap, rests through consent, applies on done", () => {
    expect(presenceFor(state({ act: "results", phase: "re.recap" }))).toEqual({
      core: "idle",
    });
    expect(presenceFor(state({ act: "connect", phase: "cn.consent" }))).toEqual(
      { core: "idle" },
    );
    expect(presenceFor(state({ act: "done", phase: "cn.done" }))).toEqual({
      core: "idle",
    });
  });
});
