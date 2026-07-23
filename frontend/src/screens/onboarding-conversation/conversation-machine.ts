import type { MessageKey } from "../../i18n/en";
import { isLegal } from "./conversation-legality";
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
// the transition table in conversation-legality.ts; an event that is not
// legal in the current phase returns the state unchanged, so a stale poll or
// a double click can never corrupt the conversation. React effects hold no
// hidden state: the thread, the pending question, and the act/phase pair ARE
// the conversation. The vocabulary (acts, phases, entries, events) lives in
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
  readCompleted: false,
  activeBuildId: null,
  lastBuildStage: null,
  lastBuildStatus: null,
};

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

export function conversationReducer(
  state: ConversationState,
  event: ConversationEvent,
): ConversationState {
  return isLegal(state, event) ? applyEvent(state, event) : state;
}

// Where an answered question lands: the speaker question back to collecting;
// a clarify to review when the read already finished (its completion must
// never be lost), otherwise back to the still-running read.
function answeredPhase(state: ConversationState): ConversationPhase {
  if (state.phase === "vo.speaker") return "vo.collecting";
  return state.readCompleted ? "co.review" : "co.reading";
}

function applyReadTerminal(
  state: ConversationState,
  event: Extract<ConversationEvent, { type: "READ_TERMINAL" }>,
): ConversationState {
  const done = event.status === "ready" || event.status === "partial";
  if (done) {
    // A pending clarify question is never stranded: the outcome queues into
    // the thread while co.clarify stays put until the question is answered.
    // readCompleted records the completion so the final answer (or a
    // REVIEW_READY from co.reading) proceeds straight to review; the
    // concluded run's id retires so its late events are stale.
    return withEntries(state, { activeReadId: null, readCompleted: true }, [
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
  return withEntries(
    state,
    {
      phase: "co.reading",
      pendingQuestion: null,
      activeReadId: null,
      readCompleted: false,
    },
    [
      {
        kind: "outcome",
        id: `read:${event.status}`,
        i18nKey: readTerminalKeys[event.status],
        tone: event.status === "deferred" ? "deferred" : "failure",
      },
    ],
  );
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
      // Until READ_STARTED names the new run, read events for ANY run are
      // stale — the previous run is retired here, not at READ_STARTED.
      return withEntries(state, { activeReadId: null, readCompleted: false }, [
        { kind: "user", id: `url:${event.url}`, text: event.url },
      ]);
    case "READ_STARTED":
      return withEntries(state, {
        phase: "co.reading",
        activeReadId: event.readId,
        readCompleted: false,
      });
    case "NARRATION": {
      // A monotonic counter (pages read, corpus words) narrates under ONE
      // stable semantic id with the count in params: a fresh emission
      // REPLACES the earlier bubble in place — same position, same stamped
      // id (so React keys hold) — instead of stacking near-identical lines.
      const index = state.thread.findIndex(
        (entry) =>
          entry.kind === "narration" &&
          entry.id.slice(entry.id.indexOf(":") + 1) === event.entry.id,
      );
      if (index !== -1) {
        const thread = [...state.thread];
        thread[index] = { ...event.entry, id: state.thread[index].id };
        return { ...state, thread };
      }
      return withEntries(state, {}, [event.entry]);
    }
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
      const answerId = `answer:${event.questionId}`;
      const answered: ThreadEntry =
        option?.labelKey !== undefined
          ? {
              kind: "user",
              id: answerId,
              i18nKey: option.labelKey,
              params: option.params,
            }
          : {
              kind: "user",
              id: answerId,
              text: option?.label ?? event.value,
              params: option?.params,
            };
      return withEntries(
        state,
        {
          phase: answeredPhase(state),
          pendingQuestion: null,
        },
        [answered],
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
        activeBuildId: event.buildId,
        lastBuildStage: null,
        lastBuildStatus: null,
      });
    case "BUILD_STAGE":
      // A stage from vo.result means a deferred build resumed on its own:
      // re-enter vo.building and clear the deferred status.
      return withEntries(
        state,
        {
          phase: "vo.building",
          lastBuildStage: event.stage,
          lastBuildStatus: null,
        },
        [
          {
            kind: "narration",
            id: `stage:${event.stage}`,
            i18nKey: buildStageKeys[event.stage],
          },
        ],
      );
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
