import { describe, expect, it } from "vitest";
import {
  type ConversationEvent,
  conversationReducer,
} from "./conversation-machine";
import { entityQuestion, run } from "./test-fixtures";

// Run-correlation and dedupe semantics: a stale poll from a superseded or
// concluded read/build run must never advance, duplicate, or mis-record the
// conversation, while the active run's events (and a deferred build's
// self-resume) keep working.

describe("read-run correlation", () => {
  const midRead = () =>
    run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "URL_SUBMITTED", url: "https://other.example" },
      { type: "READ_STARTED", readId: "r2" },
    ]);

  it("ignores terminal, clarify, and narration events from a superseded read", () => {
    const state = midRead();
    expect(state.activeReadId).toBe("r2");
    const stale: ConversationEvent[] = [
      { type: "READ_TERMINAL", readId: "r1", status: "ready", findings: 4 },
      { type: "CLARIFY", readId: "r1", question: entityQuestion },
      {
        type: "NARRATION",
        readId: "r1",
        entry: {
          kind: "narration",
          id: "pages:9",
          i18nKey: "ob.conv.read.pages",
          params: { pages: 9 },
        },
      },
    ];
    for (const event of stale) {
      expect(conversationReducer(state, event)).toBe(state);
    }
  });

  it("still accepts the active read's events", () => {
    const state = midRead();
    const advanced = conversationReducer(state, {
      type: "READ_TERMINAL",
      readId: "r2",
      status: "ready",
      findings: 2,
    });
    expect(advanced).not.toBe(state);
  });

  it("drops uncorrelated narration during company phases", () => {
    const state = midRead();
    expect(
      conversationReducer(state, {
        type: "NARRATION",
        entry: { kind: "narration", id: "recap", i18nKey: "ob.conv.recap" },
      }),
    ).toBe(state);
  });

  it("retires the run once its terminal is recorded: late events are stale", () => {
    const state = midRead();
    const concluded = conversationReducer(state, {
      type: "READ_TERMINAL",
      readId: "r2",
      status: "ready",
      findings: 2,
    });
    expect(concluded.activeReadId).toBeNull();
    const late: ConversationEvent[] = [
      { type: "READ_TERMINAL", readId: "r2", status: "ready", findings: 2 },
      { type: "CLARIFY", readId: "r2", question: entityQuestion },
      {
        type: "NARRATION",
        readId: "r2",
        entry: {
          kind: "narration",
          id: "pages:12",
          i18nKey: "ob.conv.read.pages",
          params: { pages: 12 },
        },
      },
    ];
    for (const event of late) {
      expect(conversationReducer(concluded, event)).toBe(concluded);
    }
  });

  it("treats read events as stale in the URL_SUBMITTED-to-READ_STARTED gap", () => {
    const gap = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "URL_SUBMITTED", url: "https://other.example" },
    ]);
    expect(gap.activeReadId).toBeNull();
    expect(
      conversationReducer(gap, {
        type: "READ_TERMINAL",
        readId: "r1",
        status: "ready",
        findings: 4,
      }),
    ).toBe(gap);
  });
});

describe("clarify interplay with read terminals", () => {
  const clarifying = () =>
    run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "CLARIFY", readId: "r1", question: entityQuestion },
    ]);

  it("a finished read never strands the pending question: co.clarify holds, outcome queues", () => {
    let state = conversationReducer(clarifying(), {
      type: "READ_TERMINAL",
      readId: "r1",
      status: "ready",
      findings: 3,
    });
    expect(state.phase).toBe("co.clarify");
    expect(state.pendingQuestion?.id).toBe("clarify-entity");
    expect(state.readCompleted).toBe(true);
    expect(state.thread.at(-1)).toMatchObject({
      kind: "outcome",
      i18nKey: "ob.conv.read.done",
    });

    // The completion is never lost: with no questions left, the answer
    // proceeds straight to review instead of back to a finished read.
    state = conversationReducer(state, {
      type: "QUESTION_ANSWERED",
      questionId: "clarify-entity",
      value: "acme-holding",
    });
    expect(state.phase).toBe("co.review");
    expect(state.pendingQuestion).toBeNull();
  });

  it("a premature REVIEW_READY mid-read is ignored: review needs a recorded outcome", () => {
    const midRead = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
    ]);
    expect(conversationReducer(midRead, { type: "REVIEW_READY" })).toBe(
      midRead,
    );
  });

  it("a REVIEW_READY right after the terminal is legal from co.reading", () => {
    const state = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "READ_TERMINAL", readId: "r1", status: "ready", findings: 5 },
      { type: "REVIEW_READY" },
    ]);
    expect(state.phase).toBe("co.review");
  });

  it("a failed read moots the question explicitly", () => {
    const state = conversationReducer(clarifying(), {
      type: "READ_TERMINAL",
      readId: "r1",
      status: "failed",
      findings: 0,
    });
    expect(state.phase).toBe("co.reading");
    expect(state.pendingQuestion).toBeNull();
    expect(state.thread.at(-1)).toMatchObject({ tone: "failure" });
  });
});

describe("question answering guards", () => {
  it("ignores an answer to a question that is not pending", () => {
    const state = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "CLARIFY", readId: "r1", question: entityQuestion },
    ]);
    expect(
      conversationReducer(state, {
        type: "QUESTION_ANSWERED",
        questionId: "some-other-question",
        value: "acme-gmbh",
      }),
    ).toBe(state);
  });

  it("ignores a value outside the pending question's options", () => {
    const state = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "CLARIFY", readId: "r1", question: entityQuestion },
    ]);
    expect(
      conversationReducer(state, {
        type: "QUESTION_ANSWERED",
        questionId: "clarify-entity",
        value: "not-an-option",
      }),
    ).toBe(state);
  });
});

describe("entry identity", () => {
  it("stamps every appended entry uniquely, so a retried URL never collides", () => {
    const state = run([
      { type: "START", memberPath: false },
      { type: "URL_SUBMITTED", url: "https://acme.example" },
      { type: "READ_STARTED", readId: "r1" },
      { type: "READ_TERMINAL", readId: "r1", status: "failed", findings: 0 },
      { type: "URL_SUBMITTED", url: "https://acme.example" },
      { type: "READ_STARTED", readId: "r2" },
      { type: "READ_TERMINAL", readId: "r2", status: "failed", findings: 0 },
    ]);
    const ids = state.thread.map((entry) => entry.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect(
      ids.filter((id) => id.endsWith(":url:https://acme.example")),
    ).toHaveLength(2);
    expect(ids.filter((id) => id.endsWith(":read:failed"))).toHaveLength(2);
  });
});

describe("voice build", () => {
  const building = () =>
    run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "READ_TERMINAL", readId: "r1", status: "ready", findings: 1 },
      { type: "REVIEW_READY" },
      { type: "COMPANY_CONFIRMED" },
      { type: "VOICE_OPT_IN" },
      { type: "BUILD_STARTED", buildId: "b1" },
    ]);
  const built = (status: "succeeded" | "failed" | "deferred") =>
    run(
      [
        { type: "BUILD_STAGE", buildId: "b1", stage: "snapshot" },
        { type: "BUILD_TERMINAL", buildId: "b1", status },
      ],
      building(),
    );

  it("dedupes a repeated stage poll of the same build", () => {
    const state = run(
      [{ type: "BUILD_STAGE", buildId: "b1", stage: "snapshot" }],
      building(),
    );
    expect(
      conversationReducer(state, {
        type: "BUILD_STAGE",
        buildId: "b1",
        stage: "snapshot",
      }),
    ).toBe(state);
    const advanced = conversationReducer(state, {
      type: "BUILD_STAGE",
      buildId: "b1",
      stage: "extract",
    });
    expect(advanced.thread.length).toBe(state.thread.length + 1);
  });

  it("ignores stage and terminal events from a superseded build across a retry", () => {
    const retried = run(
      [{ type: "BUILD_STARTED", buildId: "b2" }],
      built("failed"),
    );
    expect(retried).toMatchObject({
      phase: "vo.building",
      activeBuildId: "b2",
    });
    // A late failure from attempt 1 must never yank attempt 2 to vo.result.
    const stale: ConversationEvent[] = [
      { type: "BUILD_TERMINAL", buildId: "b1", status: "failed" },
      { type: "BUILD_STAGE", buildId: "b1", stage: "activate" },
    ];
    for (const event of stale) {
      expect(conversationReducer(retried, event)).toBe(retried);
    }
    const done = conversationReducer(retried, {
      type: "BUILD_TERMINAL",
      buildId: "b2",
      status: "succeeded",
    });
    expect(done.phase).toBe("vo.result");
  });

  it("allows retrying only a FAILED build from the result phase", () => {
    const failed = built("failed");
    const retried = conversationReducer(failed, {
      type: "BUILD_STARTED",
      buildId: "b2",
    });
    expect(retried.phase).toBe("vo.building");

    const succeeded = built("succeeded");
    expect(
      conversationReducer(succeeded, { type: "BUILD_STARTED", buildId: "b2" }),
    ).toBe(succeeded);
    const deferred = built("deferred");
    expect(
      conversationReducer(deferred, { type: "BUILD_STARTED", buildId: "b2" }),
    ).toBe(deferred);
  });

  it("a deferred build resumes on its own: same-build stage re-enters vo.building and success narrates", () => {
    const deferred = built("deferred");
    expect(deferred.phase).toBe("vo.result");

    // A repeated deferred poll records nothing new.
    expect(
      conversationReducer(deferred, {
        type: "BUILD_TERMINAL",
        buildId: "b1",
        status: "deferred",
      }),
    ).toBe(deferred);
    // Another build's events do not resume this one.
    expect(
      conversationReducer(deferred, {
        type: "BUILD_STAGE",
        buildId: "b9",
        stage: "extract",
      }),
    ).toBe(deferred);

    const resumed = conversationReducer(deferred, {
      type: "BUILD_STAGE",
      buildId: "b1",
      stage: "extract",
    });
    expect(resumed).toMatchObject({
      phase: "vo.building",
      lastBuildStatus: null,
    });

    const done = conversationReducer(resumed, {
      type: "BUILD_TERMINAL",
      buildId: "b1",
      status: "succeeded",
    });
    expect(done.phase).toBe("vo.result");
    expect(done.thread.at(-1)).toMatchObject({
      kind: "outcome",
      i18nKey: "ob.conv.build.succeeded",
      tone: "success",
    });
  });

  it("a deferred build may conclude directly from vo.result with a new status", () => {
    const deferred = built("deferred");
    const done = conversationReducer(deferred, {
      type: "BUILD_TERMINAL",
      buildId: "b1",
      status: "succeeded",
    });
    expect(done.phase).toBe("vo.result");
    expect(done.lastBuildStatus).toBe("succeeded");
  });
});
