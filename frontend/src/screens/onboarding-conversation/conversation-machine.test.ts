import { describe, expect, it } from "vitest";
import {
  type ConversationEvent,
  type ConversationQuestion,
  type ConversationState,
  conversationReducer,
  initialConversationState,
  THREAD_CAP,
  type ThreadEntry,
} from "./conversation-machine";
import { entityQuestion, run, speakerQuestion } from "./test-fixtures";

// The reducer is the whole conversation: this suite walks the transition
// table end to end and pins the welcome gate, restore routing, member path,
// the compile-time XOR contracts, and the thread cap. Run-correlation and
// build-retry semantics live in conversation-correlation.test.ts.

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
        { type: "BUILD_STARTED", buildId: "b1" },
        { type: "BUILD_STAGE", buildId: "b1", stage: "snapshot" },
        { type: "BUILD_STAGE", buildId: "b1", stage: "extract" },
        { type: "BUILD_STAGE", buildId: "b1", stage: "evaluate" },
        { type: "BUILD_STAGE", buildId: "b1", stage: "activate" },
        { type: "BUILD_TERMINAL", buildId: "b1", status: "succeeded" },
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
      { type: "BUILD_STARTED", buildId: "b1" },
      { type: "BUILD_STAGE", buildId: "b1", stage: "snapshot" },
      { type: "BUILD_TERMINAL", buildId: "b1", status: "succeeded" },
      { type: "RESULTS_CONTINUE" },
    ];
    for (const event of creatorOnly) {
      expect(conversationReducer(state, event)).toBe(state);
    }
    const done = conversationReducer(state, { type: "CONNECT_DONE" });
    expect(done).toMatchObject({ act: "done", phase: "cn.done" });
  });
});

describe("compile-time XOR contracts", () => {
  it("a question option and a user turn must each carry exactly one content source", () => {
    // @ts-expect-error an option without labelKey or label is unrepresentable
    const blank: ConversationQuestion["options"][number] = { value: "x" };
    // @ts-expect-error labelKey and label are mutually exclusive
    const both: ConversationQuestion["options"][number] = {
      value: "y",
      labelKey: "ob.conv.voice.optIn",
      label: "Yes",
    };
    // @ts-expect-error a user turn without i18nKey or text is unrepresentable
    const silent: ThreadEntry = { kind: "user", id: "u" };
    expect([blank.value, both.value, silent.kind]).toEqual(["x", "y", "user"]);
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
