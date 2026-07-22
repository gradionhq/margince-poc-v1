import { describe, expect, it } from "vitest";
import {
  type ConversationEvent,
  type ConversationQuestion,
  type ConversationState,
  conversationReducer,
  initialConversationState,
  THREAD_CAP,
} from "./conversation-machine";

// The reducer is the whole conversation: these tests walk the transition
// table end to end, prove illegal and stale events are inert, and pin the
// member path, the retry semantics, and the thread cap.

function run(
  events: readonly ConversationEvent[],
  from: ConversationState = initialConversationState,
): ConversationState {
  return events.reduce(conversationReducer, from);
}

const entityQuestion: ConversationQuestion = {
  id: "clarify-entity",
  i18nKey: "ob.conv.clarify.entity",
  options: [
    { value: "acme-gmbh", label: "Acme GmbH" },
    { value: "acme-holding", label: "Acme Holding SE" },
  ],
};

const speakerQuestion: ConversationQuestion = {
  id: "speaker",
  i18nKey: "ob.conv.voice.speakerQuestion",
  options: [
    { value: "Speaker 1", label: "Speaker 1" },
    { value: "Speaker 2", label: "Speaker 2" },
  ],
};

describe("conversationReducer happy path", () => {
  it("walks the creator journey across all five acts", () => {
    let state = run([{ type: "START", memberPath: false }]);
    expect(state).toMatchObject({ act: "company", phase: "co.intro" });

    state = run(
      [
        { type: "URL_SUBMITTED", url: "https://acme.example" },
        { type: "READ_STARTED", readId: "r1" },
        {
          type: "NARRATION",
          readId: "r1",
          entry: {
            kind: "narration",
            id: "pages:3",
            i18nKey: "ob.conv.read.pages",
            params: { pages: 3 },
          },
        },
      ],
      state,
    );
    expect(state.phase).toBe("co.reading");
    expect(state.activeReadId).toBe("r1");
    expect(state.thread.map((entry) => entry.kind)).toEqual([
      "user",
      "narration",
    ]);

    state = run(
      [{ type: "CLARIFY", readId: "r1", question: entityQuestion }],
      state,
    );
    expect(state.phase).toBe("co.clarify");
    expect(state.pendingQuestion?.id).toBe("clarify-entity");

    state = run(
      [
        {
          type: "QUESTION_ANSWERED",
          questionId: "clarify-entity",
          value: "acme-gmbh",
        },
      ],
      state,
    );
    expect(state.phase).toBe("co.reading");
    expect(state.pendingQuestion).toBeNull();
    expect(state.thread.at(-1)).toMatchObject({
      kind: "user",
      text: "Acme GmbH",
    });

    state = run(
      [
        { type: "READ_TERMINAL", readId: "r1", status: "ready", findings: 6 },
        { type: "REVIEW_READY" },
        { type: "COMPANY_CONFIRMED" },
      ],
      state,
    );
    expect(state).toMatchObject({ act: "voice", phase: "vo.invite" });
    expect(state.thread.at(-1)).toMatchObject({
      kind: "outcome",
      i18nKey: "ob.conv.company.confirmed",
      tone: "success",
    });
    const readOutcome = state.thread.find(
      (entry) => entry.kind === "outcome" && entry.id.endsWith("read:ready"),
    );
    expect(readOutcome).toMatchObject({ params: { count: 6 } });

    state = run(
      [
        { type: "VOICE_OPT_IN" },
        { type: "UPLOAD_ADDED", id: "u1", name: "call.vtt" },
        { type: "SPEAKER_NEEDED", question: speakerQuestion },
        {
          type: "QUESTION_ANSWERED",
          questionId: "speaker",
          value: "Speaker 1",
        },
        { type: "BUILD_STARTED" },
        { type: "BUILD_STAGE", stage: "snapshot" },
        { type: "BUILD_STAGE", stage: "extract" },
        { type: "BUILD_STAGE", stage: "evaluate" },
        { type: "BUILD_STAGE", stage: "activate" },
        { type: "BUILD_TERMINAL", status: "succeeded" },
      ],
      state,
    );
    expect(state).toMatchObject({ act: "voice", phase: "vo.result" });
    expect(state.thread.at(-1)).toMatchObject({
      kind: "outcome",
      i18nKey: "ob.conv.build.succeeded",
      tone: "success",
    });

    state = run([{ type: "RESULTS_CONTINUE" }], state);
    expect(state).toMatchObject({ act: "results", phase: "re.recap" });

    state = run(
      [{ type: "RESULTS_CONTINUE" }, { type: "CONNECT_DONE" }],
      state,
    );
    expect(state).toMatchObject({ act: "done", phase: "cn.done" });
  });

  it("records a failed read as a failure outcome and allows the manual path out", () => {
    let state = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "READ_TERMINAL", readId: "r1", status: "failed", findings: 0 },
    ]);
    expect(state.phase).toBe("co.reading");
    expect(state.thread.at(-1)).toMatchObject({
      kind: "outcome",
      i18nKey: "ob.conv.read.failed",
      tone: "failure",
    });

    state = run(
      [{ type: "MANUAL_CHOSEN" }, { type: "COMPANY_CONFIRMED" }],
      state,
    );
    expect(state).toMatchObject({ act: "voice", phase: "vo.invite" });
  });

  it("lets the voice act be skipped and still reach the recap", () => {
    const state = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "REVIEW_READY" },
      { type: "COMPANY_CONFIRMED" },
      { type: "VOICE_SKIPPED" },
      { type: "RESULTS_CONTINUE" },
    ]);
    expect(state).toMatchObject({ act: "results", phase: "re.recap" });
    expect(
      state.thread.some(
        (entry) =>
          entry.kind === "user" && entry.i18nKey === "ob.conv.voice.skipped",
      ),
    ).toBe(true);
  });
});

describe("the welcome act", () => {
  it("rejects every event except START", () => {
    const notStart: ConversationEvent[] = [
      { type: "URL_SUBMITTED", url: "https://acme.example" },
      { type: "READ_STARTED", readId: "r1" },
      { type: "MANUAL_CHOSEN" },
      { type: "COMPANY_CONFIRMED" },
      { type: "RESUME" },
      { type: "CONNECT_DONE" },
    ];
    for (const event of notStart) {
      expect(conversationReducer(initialConversationState, event)).toBe(
        initialConversationState,
      );
    }
  });
});

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

  it("still accepts the active read's events and run-agnostic narration", () => {
    const state = midRead();
    const advanced = conversationReducer(state, {
      type: "READ_TERMINAL",
      readId: "r2",
      status: "ready",
      findings: 2,
    });
    expect(advanced).not.toBe(state);
    const narrated = conversationReducer(state, {
      type: "NARRATION",
      entry: { kind: "narration", id: "recap", i18nKey: "ob.conv.recap" },
    });
    expect(narrated.thread.length).toBe(state.thread.length + 1);
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
    expect(state.thread.at(-1)).toMatchObject({
      kind: "outcome",
      i18nKey: "ob.conv.read.done",
    });

    state = conversationReducer(state, {
      type: "QUESTION_ANSWERED",
      questionId: "clarify-entity",
      value: "acme-holding",
    });
    expect(state.phase).toBe("co.reading");
    expect(state.pendingQuestion).toBeNull();
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
  const built = (status: "succeeded" | "failed" | "deferred") =>
    run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "REVIEW_READY" },
      { type: "COMPANY_CONFIRMED" },
      { type: "VOICE_OPT_IN" },
      { type: "BUILD_STARTED" },
      { type: "BUILD_STAGE", stage: "snapshot" },
      { type: "BUILD_TERMINAL", status },
    ]);

  it("dedupes a repeated stage poll", () => {
    const state = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED", readId: "r1" },
      { type: "REVIEW_READY" },
      { type: "COMPANY_CONFIRMED" },
      { type: "VOICE_OPT_IN" },
      { type: "BUILD_STARTED" },
      { type: "BUILD_STAGE", stage: "snapshot" },
    ]);
    expect(
      conversationReducer(state, { type: "BUILD_STAGE", stage: "snapshot" }),
    ).toBe(state);
    const advanced = conversationReducer(state, {
      type: "BUILD_STAGE",
      stage: "extract",
    });
    expect(advanced.thread.length).toBe(state.thread.length + 1);
  });

  it("allows retrying only a FAILED build from the result phase", () => {
    const failed = built("failed");
    const retried = conversationReducer(failed, { type: "BUILD_STARTED" });
    expect(retried.phase).toBe("vo.building");

    const succeeded = built("succeeded");
    expect(conversationReducer(succeeded, { type: "BUILD_STARTED" })).toBe(
      succeeded,
    );
    const deferred = built("deferred");
    expect(conversationReducer(deferred, { type: "BUILD_STARTED" })).toBe(
      deferred,
    );
  });
});

describe("restore normalization out of co.confirmed", () => {
  const restored = (memberPath: boolean): ConversationState => ({
    ...initialConversationState,
    act: "company",
    phase: "co.confirmed",
    memberPath,
  });

  it("routes a restored creator to the voice invite", () => {
    expect(
      conversationReducer(restored(false), { type: "RESUME" }),
    ).toMatchObject({ act: "voice", phase: "vo.invite" });
  });

  it("routes a restored member to consent", () => {
    expect(
      conversationReducer(restored(true), { type: "RESUME" }),
    ).toMatchObject({ act: "connect", phase: "cn.consent" });
  });
});

describe("member path", () => {
  it("confirming company jumps straight to consent, skipping voice and results", () => {
    const state = run([
      { type: "START", memberPath: true },
      { type: "READ_STARTED", readId: "r1" },
      { type: "REVIEW_READY" },
      { type: "COMPANY_CONFIRMED" },
    ]);
    expect(state).toMatchObject({ act: "connect", phase: "cn.consent" });
  });

  it("ignores every creator-only event", () => {
    const state = run([
      { type: "START", memberPath: true },
      { type: "READ_STARTED", readId: "r1" },
      { type: "REVIEW_READY" },
      { type: "COMPANY_CONFIRMED" },
    ]);
    const creatorOnly: ConversationEvent[] = [
      { type: "VOICE_OPT_IN" },
      { type: "VOICE_SKIPPED" },
      { type: "UPLOAD_ADDED", id: "u1", name: "notes.txt" },
      { type: "SPEAKER_NEEDED", question: speakerQuestion },
      { type: "BUILD_STARTED" },
      { type: "BUILD_STAGE", stage: "snapshot" },
      { type: "BUILD_TERMINAL", status: "succeeded" },
      { type: "RESULTS_CONTINUE" },
    ];
    for (const event of creatorOnly) {
      expect(conversationReducer(state, event)).toBe(state);
    }
    const done = conversationReducer(state, { type: "CONNECT_DONE" });
    expect(done).toMatchObject({ act: "done", phase: "cn.done" });
  });
});

describe("thread cap", () => {
  it("caps the thread and drops the oldest narration before anything else", () => {
    let state = run([
      { type: "START", memberPath: false },
      { type: "URL_SUBMITTED", url: "https://acme.example" },
      { type: "READ_STARTED", readId: "r1" },
    ]);
    for (let index = 0; index < THREAD_CAP + 20; index += 1) {
      state = conversationReducer(state, {
        type: "NARRATION",
        readId: "r1",
        entry: {
          kind: "narration",
          id: `n:${index}`,
          i18nKey: "ob.conv.read.pages",
          params: { pages: index },
        },
      });
    }
    expect(state.thread.length).toBe(THREAD_CAP);
    // The user's URL turn survives; the oldest narrations are gone.
    expect(state.thread[0]).toMatchObject({ kind: "user" });
    expect(
      state.thread.some(
        (entry) => entry.id.endsWith(":n:0") || entry.id.endsWith(":n:19"),
      ),
    ).toBe(false);
    expect(state.thread.at(-1)?.id.endsWith(`:n:${THREAD_CAP + 19}`)).toBe(
      true,
    );
  });
});
