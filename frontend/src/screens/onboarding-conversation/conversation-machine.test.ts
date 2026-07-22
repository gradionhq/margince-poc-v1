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
// table end to end, prove illegal events are inert, and pin the member path
// and the thread cap.

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
        { type: "READ_STARTED" },
        {
          type: "NARRATION",
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
    expect(state.thread.map((entry) => entry.kind)).toEqual([
      "user",
      "narration",
    ]);

    state = run([{ type: "CLARIFY", question: entityQuestion }], state);
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
        { type: "READ_TERMINAL", status: "ready" },
        { type: "REVIEW_READY" },
        { type: "COMPANY_CONFIRMED" },
      ],
      state,
    );
    expect(state).toMatchObject({ act: "voice", phase: "vo.invite" });
    expect(state.thread.at(-1)).toMatchObject({
      kind: "outcome",
      i18nKey: "ob.conv.company.confirmed",
    });

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
    });

    state = run([{ type: "RESULTS_CONTINUE" }], state);
    expect(state).toMatchObject({ act: "results", phase: "re.recap" });

    state = run(
      [{ type: "RESULTS_CONTINUE" }, { type: "CONNECT_DONE" }],
      state,
    );
    expect(state).toMatchObject({ act: "done", phase: "cn.done" });
  });

  it("records a failed read as an outcome and allows the manual path out", () => {
    let state = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED" },
      { type: "READ_TERMINAL", status: "failed" },
    ]);
    expect(state.phase).toBe("co.reading");
    expect(state.thread.at(-1)).toMatchObject({
      kind: "outcome",
      i18nKey: "ob.conv.read.failed",
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
      { type: "READ_STARTED" },
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

describe("illegal events are ignored, never thrown", () => {
  it("returns the identical state for events outside the current phase", () => {
    const started = run([{ type: "START", memberPath: false }]);
    const illegal: ConversationEvent[] = [
      { type: "BUILD_TERMINAL", status: "succeeded" },
      { type: "COMPANY_CONFIRMED" },
      { type: "VOICE_OPT_IN" },
      { type: "CONNECT_DONE" },
      { type: "REVIEW_READY" },
      { type: "START", memberPath: true },
    ];
    for (const event of illegal) {
      expect(conversationReducer(started, event)).toBe(started);
    }
  });

  it("ignores an answer to a question that is not pending", () => {
    const state = run([
      { type: "START", memberPath: false },
      { type: "READ_STARTED" },
      { type: "CLARIFY", question: entityQuestion },
    ]);
    const answered = conversationReducer(state, {
      type: "QUESTION_ANSWERED",
      questionId: "some-other-question",
      value: "acme-gmbh",
    });
    expect(answered).toBe(state);
  });
});

describe("member path", () => {
  it("confirming company jumps straight to consent, skipping voice and results", () => {
    const state = run([
      { type: "START", memberPath: true },
      { type: "READ_STARTED" },
      { type: "REVIEW_READY" },
      { type: "COMPANY_CONFIRMED" },
    ]);
    expect(state).toMatchObject({ act: "connect", phase: "cn.consent" });
  });

  it("ignores every creator-only event", () => {
    const state = run([
      { type: "START", memberPath: true },
      { type: "READ_STARTED" },
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
      { type: "READ_STARTED" },
    ]);
    for (let index = 0; index < THREAD_CAP + 20; index += 1) {
      state = conversationReducer(state, {
        type: "NARRATION",
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
      state.thread.some((entry) => entry.id === "n:0" || entry.id === "n:19"),
    ).toBe(false);
    expect(state.thread.at(-1)?.id).toBe(`n:${THREAD_CAP + 19}`);
  });
});
