import type { MessageKey } from "../../i18n/en";
import type {
  BuildStage,
  BuildTerminalStatus,
  ConversationEvent,
  ConversationPhase,
  ConversationState,
  OutcomeTone,
  ReadTerminalStatus,
  ThreadEntry,
} from "./conversation-types";

// The onboarding conversation as a pure reducer. Every legal move lives in
// the transition table below; an event that is not legal in the current
// phase returns the state unchanged, so a stale poll or a double click can
// never corrupt the conversation. React effects hold no hidden state: the
// thread, the pending question, and the act/phase pair ARE the conversation.
// The vocabulary (acts, phases, entries, events) lives in
// conversation-types.ts and is re-exported here so callers have one import.

export type {
  BuildStage,
  BuildTerminalStatus,
  ConversationAct,
  ConversationEvent,
  ConversationPhase,
  ConversationQuestion,
  ConversationState,
  NarrationEntry,
  OutcomeTone,
  QuestionOption,
  ReadTerminalStatus,
  ThreadEntry,
} from "./conversation-types";

export const initialConversationState: ConversationState = {
  act: "welcome",
  phase: "co.intro",
  memberPath: false,
  pendingQuestion: null,
  thread: [],
  seq: 0,
  activeReadId: null,
  lastBuildStage: null,
  lastBuildStatus: null,
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

const buildTerminalTones: Record<BuildTerminalStatus, OutcomeTone> = {
  succeeded: "success",
  failed: "failure",
  deferred: "deferred",
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
  patch: Partial<Omit<ConversationState, "thread" | "seq">>,
  entries: readonly ThreadEntry[] = [],
): ConversationState {
  if (entries.length === 0) {
    return { ...state, ...patch };
  }
  const stamped = entries.map((entry, offset) => ({
    ...entry,
    id: `${state.seq + offset}:${entry.id}`,
  }));
  return {
    ...state,
    ...patch,
    seq: state.seq + entries.length,
    thread: appendEntries(state.thread, stamped),
  };
}

// The transition table: the phases in which each event is legal. On top of
// this, isLegal rejects everything but START in the welcome act and
// eventGuards adds the per-event conditions (read-run identity, pending
// question identity, build retry/dedupe). An event outside its row is
// ignored.
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
  RESUME: new Set(["co.confirmed"]),
  VOICE_OPT_IN: new Set(["vo.invite"]),
  VOICE_SKIPPED: new Set(["vo.invite", "vo.collecting"]),
  UPLOAD_ADDED: new Set(["vo.collecting"]),
  SPEAKER_NEEDED: new Set(["vo.collecting"]),
  BUILD_STARTED: new Set(["vo.collecting", "vo.result"]),
  BUILD_STAGE: new Set(["vo.building"]),
  BUILD_TERMINAL: new Set(["vo.building"]),
  RESULTS_CONTINUE: new Set(["vo.result", "vo.skipped", "re.recap"]),
  CONNECT_DONE: new Set(["cn.consent"]),
};

function isLegal(state: ConversationState, event: ConversationEvent): boolean {
  // The welcome act admits exactly one move: starting the conversation.
  if (state.act === "welcome") {
    return event.type === "START";
  }
  if (event.type === "START") {
    return false;
  }
  if (state.memberPath && creatorOnlyEvents.has(event.type)) {
    return false;
  }
  if (!legalPhases[event.type].has(state.phase)) {
    return false;
  }
  return eventGuards(state, event);
}

function eventGuards(
  state: ConversationState,
  event: ConversationEvent,
): boolean {
  switch (event.type) {
    // A read event from a superseded run must never advance or mis-record
    // the current one; only the active run's events count.
    case "READ_TERMINAL":
    case "CLARIFY":
      return event.readId === state.activeReadId;
    case "NARRATION":
      // Narration without a run id (voice corpus, build) is run-agnostic.
      return event.readId === undefined || event.readId === state.activeReadId;
    case "QUESTION_ANSWERED":
      // Both the question and the chosen option must be the pending ones; an
      // unknown value is never echoed into the thread.
      return (
        state.pendingQuestion?.id === event.questionId &&
        state.pendingQuestion.options.some(
          (option) => option.value === event.value,
        )
      );
    case "BUILD_STARTED":
      // From vo.result only a FAILED build may be retried; a succeeded or
      // deferred build is not restartable from here.
      return state.phase !== "vo.result" || state.lastBuildStatus === "failed";
    case "BUILD_STAGE":
      // A poll repeating the current stage narrates nothing new.
      return event.stage !== state.lastBuildStage;
    default:
      return true;
  }
}

export function conversationReducer(
  state: ConversationState,
  event: ConversationEvent,
): ConversationState {
  return isLegal(state, event) ? applyEvent(state, event) : state;
}

function applyReadTerminal(
  state: ConversationState,
  event: Extract<ConversationEvent, { type: "READ_TERMINAL" }>,
): ConversationState {
  const done = event.status === "ready" || event.status === "partial";
  if (done) {
    // A pending clarify question is never stranded: the outcome queues into
    // the thread while co.clarify stays put until the question is answered.
    return withEntries(state, {}, [
      {
        kind: "outcome",
        id: `read:${event.status}`,
        i18nKey: readTerminalKeys[event.status],
        params: { count: event.findings },
        tone: "success",
      },
    ]);
  }
  // A failed or deferred read moots its clarify question and waits in
  // co.reading for a new URL or the manual path.
  return withEntries(state, { phase: "co.reading", pendingQuestion: null }, [
    {
      kind: "outcome",
      id: `read:${event.status}`,
      i18nKey: readTerminalKeys[event.status],
      tone: event.status === "deferred" ? "deferred" : "failure",
    },
  ]);
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
      return withEntries(state, {
        phase: "co.reading",
        activeReadId: event.readId,
      });
    case "NARRATION":
      return withEntries(state, {}, [event.entry]);
    case "READ_TERMINAL":
      return applyReadTerminal(state, event);
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
            tone: "success",
          },
        ],
      );
    case "RESUME":
      // Restore normalization out of co.confirmed: the same routing the live
      // confirmation takes, without repeating the confirmation outcome.
      return withEntries(
        state,
        state.memberPath
          ? { act: "connect", phase: "cn.consent" }
          : { act: "voice", phase: "vo.invite" },
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
      return withEntries(state, {
        phase: "vo.building",
        lastBuildStage: null,
        lastBuildStatus: null,
      });
    case "BUILD_STAGE":
      return withEntries(state, { lastBuildStage: event.stage }, [
        {
          kind: "narration",
          id: `stage:${event.stage}`,
          i18nKey: buildStageKeys[event.stage],
        },
      ]);
    case "BUILD_TERMINAL":
      return withEntries(
        state,
        { phase: "vo.result", lastBuildStatus: event.status },
        [
          {
            kind: "outcome",
            id: `build:${event.status}`,
            i18nKey: buildTerminalKeys[event.status],
            tone: buildTerminalTones[event.status],
          },
        ],
      );
    case "RESULTS_CONTINUE":
      return state.phase === "re.recap"
        ? withEntries(state, { act: "connect", phase: "cn.consent" })
        : withEntries(state, { act: "results", phase: "re.recap" }, [
            { kind: "narration", id: "recap", i18nKey: "ob.conv.recap" },
          ]);
    case "CONNECT_DONE":
      return withEntries(state, { act: "done", phase: "cn.done" }, [
        {
          kind: "outcome",
          id: "connect:done",
          i18nKey: "ob.conv.done",
          tone: "success",
        },
      ]);
  }
}
