import type { MessageKey } from "../../i18n/en";

// The onboarding conversation as a pure reducer. Every legal move lives in
// the transition table below; an event that is not legal in the current
// phase returns the state unchanged, so a stale poll or a double click can
// never corrupt the conversation. React effects hold no hidden state: the
// thread, the pending question, and the act/phase pair ARE the conversation.

export type ConversationAct =
  | "welcome"
  | "company"
  | "voice"
  | "results"
  | "connect"
  | "done";

export type ConversationPhase =
  | "co.intro"
  | "co.reading"
  | "co.clarify"
  | "co.review"
  | "co.manual"
  // Reserved for the restore path: a returning session whose company is
  // already confirmed re-enters here. Live confirmation advances straight
  // to the next act because a reducer cannot self-advance out of a
  // momentary state.
  | "co.confirmed"
  | "vo.invite"
  | "vo.collecting"
  | "vo.speaker"
  | "vo.building"
  | "vo.result"
  | "vo.skipped"
  | "re.recap"
  | "cn.consent"
  | "cn.done";

export type QuestionOption = {
  value: string;
  labelKey?: MessageKey;
  label?: string;
  detailKey?: MessageKey;
  params?: Record<string, string | number>;
};

export type ConversationQuestion = {
  id: string;
  i18nKey: MessageKey;
  params?: Record<string, string | number>;
  options: QuestionOption[];
};

export type ThreadEntry =
  | {
      kind: "narration";
      id: string;
      i18nKey: MessageKey;
      params?: Record<string, string | number>;
      findingIds?: string[];
    }
  | { kind: "question"; id: string; question: ConversationQuestion }
  | {
      kind: "user";
      id: string;
      i18nKey?: MessageKey;
      text?: string;
      params?: Record<string, string | number>;
    }
  | {
      kind: "outcome";
      id: string;
      i18nKey: MessageKey;
      params?: Record<string, string | number>;
    };

export type NarrationEntry = Extract<ThreadEntry, { kind: "narration" }>;

export type ConversationState = {
  act: ConversationAct;
  phase: ConversationPhase;
  memberPath: boolean;
  pendingQuestion: ConversationQuestion | null;
  thread: ThreadEntry[];
};

export type ReadTerminalStatus = "ready" | "partial" | "failed" | "deferred";
export type BuildStage = "snapshot" | "extract" | "evaluate" | "activate";
export type BuildTerminalStatus = "succeeded" | "failed" | "deferred";

export type ConversationEvent =
  | { type: "START"; memberPath: boolean }
  | { type: "URL_SUBMITTED"; url: string }
  | { type: "READ_STARTED" }
  | { type: "NARRATION"; entry: NarrationEntry }
  | { type: "READ_TERMINAL"; status: ReadTerminalStatus }
  | { type: "CLARIFY"; question: ConversationQuestion }
  | { type: "QUESTION_ANSWERED"; questionId: string; value: string }
  | { type: "REVIEW_READY" }
  | { type: "MANUAL_CHOSEN" }
  | { type: "COMPANY_CONFIRMED" }
  | { type: "VOICE_OPT_IN" }
  | { type: "VOICE_SKIPPED" }
  | { type: "UPLOAD_ADDED"; id: string; name: string }
  | { type: "SPEAKER_NEEDED"; question: ConversationQuestion }
  | { type: "BUILD_STARTED" }
  | { type: "BUILD_STAGE"; stage: BuildStage }
  | { type: "BUILD_TERMINAL"; status: BuildTerminalStatus }
  | { type: "RESULTS_CONTINUE" }
  | { type: "CONNECT_DONE" };

export const initialConversationState: ConversationState = {
  act: "welcome",
  phase: "co.intro",
  memberPath: false,
  pendingQuestion: null,
  thread: [],
};

// A member joining an existing installation only confirms company context and
// consent; voice and results events belong to the creator path exclusively.
const creatorOnlyEvents = new Set<ConversationEvent["type"]>([
  "VOICE_OPT_IN",
  "VOICE_SKIPPED",
  "UPLOAD_ADDED",
  "SPEAKER_NEEDED",
  "BUILD_STARTED",
  "BUILD_STAGE",
  "BUILD_TERMINAL",
  "RESULTS_CONTINUE",
]);

// Narration streams only while the machine is actively reading or building;
// a late poll after a phase moved on is dropped, not displayed out of order.
const narrationPhases = new Set<ConversationPhase>([
  "co.reading",
  "co.clarify",
  "co.review",
  "vo.collecting",
  "vo.speaker",
  "vo.building",
]);

const readTerminalKeys: Record<ReadTerminalStatus, MessageKey> = {
  ready: "ob.conv.read.done",
  partial: "ob.conv.read.partial",
  failed: "ob.conv.read.failed",
  deferred: "ob.conv.read.deferred",
};

const buildStageKeys: Record<BuildStage, MessageKey> = {
  snapshot: "ob.conv.build.snapshot",
  extract: "ob.conv.build.extract",
  evaluate: "ob.conv.build.evaluate",
  activate: "ob.conv.build.activate",
};

const buildTerminalKeys: Record<BuildTerminalStatus, MessageKey> = {
  succeeded: "ob.conv.build.succeeded",
  failed: "ob.conv.build.failed",
  deferred: "ob.conv.build.deferred",
};

export const THREAD_CAP = 200;

// The thread is a working transcript, not an archive. Past the cap the oldest
// narration goes first: questions, answers, and outcomes carry decisions and
// must outlive ambient progress chatter.
function appendEntries(
  thread: readonly ThreadEntry[],
  entries: readonly ThreadEntry[],
): ThreadEntry[] {
  const next = [...thread, ...entries];
  while (next.length > THREAD_CAP) {
    const oldestNarration = next.findIndex(
      (entry) => entry.kind === "narration",
    );
    next.splice(oldestNarration === -1 ? 0 : oldestNarration, 1);
  }
  return next;
}

function withEntries(
  state: ConversationState,
  patch: Partial<Omit<ConversationState, "thread">>,
  entries: readonly ThreadEntry[] = [],
): ConversationState {
  return {
    ...state,
    ...patch,
    thread: entries.length
      ? appendEntries(state.thread, entries)
      : state.thread,
  };
}

// The transition table: the phases in which each event is legal. Two events
// carry an extra guard in isLegal — START also requires the welcome act, and
// QUESTION_ANSWERED must name the pending question. Everything else is
// purely phase-scoped; an event outside its row is ignored.
const legalPhases: Record<
  ConversationEvent["type"],
  ReadonlySet<ConversationPhase>
> = {
  START: new Set(["co.intro"]),
  URL_SUBMITTED: new Set(["co.intro", "co.reading"]),
  READ_STARTED: new Set(["co.intro", "co.reading"]),
  NARRATION: narrationPhases,
  READ_TERMINAL: new Set(["co.reading", "co.clarify"]),
  CLARIFY: new Set(["co.reading", "co.review"]),
  QUESTION_ANSWERED: new Set(["co.clarify", "vo.speaker"]),
  REVIEW_READY: new Set(["co.reading"]),
  MANUAL_CHOSEN: new Set(["co.intro", "co.reading", "co.review"]),
  COMPANY_CONFIRMED: new Set(["co.review", "co.manual"]),
  VOICE_OPT_IN: new Set(["vo.invite"]),
  VOICE_SKIPPED: new Set(["vo.invite", "vo.collecting"]),
  UPLOAD_ADDED: new Set(["vo.collecting"]),
  SPEAKER_NEEDED: new Set(["vo.collecting"]),
  BUILD_STARTED: new Set(["vo.collecting"]),
  BUILD_STAGE: new Set(["vo.building"]),
  BUILD_TERMINAL: new Set(["vo.building"]),
  RESULTS_CONTINUE: new Set(["vo.result", "vo.skipped", "re.recap"]),
  CONNECT_DONE: new Set(["cn.consent"]),
};

function isLegal(state: ConversationState, event: ConversationEvent): boolean {
  if (state.memberPath && creatorOnlyEvents.has(event.type)) {
    return false;
  }
  if (event.type === "START" && state.act !== "welcome") {
    return false;
  }
  if (
    event.type === "QUESTION_ANSWERED" &&
    state.pendingQuestion?.id !== event.questionId
  ) {
    return false;
  }
  return legalPhases[event.type].has(state.phase);
}

export function conversationReducer(
  state: ConversationState,
  event: ConversationEvent,
): ConversationState {
  return isLegal(state, event) ? applyEvent(state, event) : state;
}

// Legality is already settled: every branch below only computes the next
// state for an event the table admitted in the current phase.
function applyEvent(
  state: ConversationState,
  event: ConversationEvent,
): ConversationState {
  switch (event.type) {
    case "START":
      return withEntries(state, {
        act: "company",
        phase: "co.intro",
        memberPath: event.memberPath,
      });
    case "URL_SUBMITTED":
      return withEntries(state, {}, [
        { kind: "user", id: `url:${event.url}`, text: event.url },
      ]);
    case "READ_STARTED":
      return withEntries(state, { phase: "co.reading" });
    case "NARRATION":
      return withEntries(state, {}, [event.entry]);
    case "READ_TERMINAL":
      return withEntries(
        state,
        // Any terminal read lands back in co.reading: a failed or deferred
        // read waits there for a new URL or the manual path (its clarify, if
        // any, is moot), a finished read waits there for REVIEW_READY.
        {
          phase: "co.reading",
          pendingQuestion:
            event.status === "ready" || event.status === "partial"
              ? state.pendingQuestion
              : null,
        },
        [
          {
            kind: "outcome",
            id: `read:${event.status}`,
            i18nKey: readTerminalKeys[event.status],
          },
        ],
      );
    case "CLARIFY":
      return withEntries(
        state,
        { phase: "co.clarify", pendingQuestion: event.question },
        [
          {
            kind: "question",
            id: `question:${event.question.id}`,
            question: event.question,
          },
        ],
      );
    case "QUESTION_ANSWERED": {
      const option = state.pendingQuestion?.options.find(
        (candidate) => candidate.value === event.value,
      );
      return withEntries(
        state,
        {
          phase: state.phase === "vo.speaker" ? "vo.collecting" : "co.reading",
          pendingQuestion: null,
        },
        [
          {
            kind: "user",
            id: `answer:${event.questionId}`,
            i18nKey: option?.labelKey,
            text: option?.labelKey ? undefined : (option?.label ?? event.value),
            params: option?.params,
          },
        ],
      );
    }
    case "REVIEW_READY":
      return withEntries(state, { phase: "co.review" });
    case "MANUAL_CHOSEN":
      return withEntries(state, { phase: "co.manual", pendingQuestion: null }, [
        { kind: "user", id: "manual:chosen", i18nKey: "ob.conv.manual.chosen" },
      ]);
    case "COMPANY_CONFIRMED":
      return withEntries(
        state,
        state.memberPath
          ? { act: "connect", phase: "cn.consent" }
          : { act: "voice", phase: "vo.invite" },
        [
          {
            kind: "outcome",
            id: "company:confirmed",
            i18nKey: "ob.conv.company.confirmed",
          },
        ],
      );
    case "VOICE_OPT_IN":
      return withEntries(state, { phase: "vo.collecting" }, [
        { kind: "user", id: "voice:optin", i18nKey: "ob.conv.voice.optIn" },
      ]);
    case "VOICE_SKIPPED":
      return withEntries(
        state,
        { phase: "vo.skipped", pendingQuestion: null },
        [
          {
            kind: "user",
            id: "voice:skipped",
            i18nKey: "ob.conv.voice.skipped",
          },
        ],
      );
    case "UPLOAD_ADDED":
      return withEntries(state, {}, [
        {
          kind: "user",
          id: `upload:${event.id}`,
          i18nKey: "ob.conv.voice.uploadAdded",
          params: { name: event.name },
        },
      ]);
    case "SPEAKER_NEEDED":
      return withEntries(
        state,
        { phase: "vo.speaker", pendingQuestion: event.question },
        [
          {
            kind: "question",
            id: `question:${event.question.id}`,
            question: event.question,
          },
        ],
      );
    case "BUILD_STARTED":
      return withEntries(state, { phase: "vo.building" });
    case "BUILD_STAGE":
      return withEntries(state, {}, [
        {
          kind: "narration",
          id: `stage:${event.stage}`,
          i18nKey: buildStageKeys[event.stage],
        },
      ]);
    case "BUILD_TERMINAL":
      return withEntries(state, { phase: "vo.result" }, [
        {
          kind: "outcome",
          id: `build:${event.status}`,
          i18nKey: buildTerminalKeys[event.status],
        },
      ]);
    case "RESULTS_CONTINUE":
      return state.phase === "re.recap"
        ? withEntries(state, { act: "connect", phase: "cn.consent" })
        : withEntries(state, { act: "results", phase: "re.recap" }, [
            { kind: "narration", id: "recap", i18nKey: "ob.conv.recap" },
          ]);
    case "CONNECT_DONE":
      return withEntries(state, { act: "done", phase: "cn.done" }, [
        { kind: "outcome", id: "connect:done", i18nKey: "ob.conv.done" },
      ]);
  }
}
