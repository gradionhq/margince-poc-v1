import type {
  ConversationEvent,
  ConversationPhase,
  ConversationState,
} from "./conversation-types";

// The legality half of the conversation machine: the phase table plus the
// per-event guards (run correlation, pending-question identity, build
// retry/resume/dedupe). conversation-machine.ts consults isLegal before
// applying anything; an event rejected here leaves the state untouched.

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

// The transition table: the phases in which each event is legal. On top of
// this, isLegal rejects everything but START in the welcome act and
// eventGuards adds the per-event conditions. An event outside its row is
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
  // vo.result rows exist for the deferred-resume path; the guards restrict
  // them to the SAME build id and a deferred last status.
  BUILD_STAGE: new Set(["vo.building", "vo.result"]),
  BUILD_TERMINAL: new Set(["vo.building", "vo.result"]),
  RESULTS_CONTINUE: new Set(["vo.result", "vo.skipped", "re.recap"]),
  CONNECT_DONE: new Set(["cn.consent"]),
};

export function isLegal(
  state: ConversationState,
  event: ConversationEvent,
): boolean {
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

const companyNarrationPhases = new Set<ConversationPhase>([
  "co.reading",
  "co.clarify",
  "co.review",
]);

// During the company act only narration correlated to the ACTIVE read may
// speak; in vo.building only narration from the active build. Elsewhere
// (corpus growth while collecting) run-agnostic narration is welcome, but a
// stale run id is always dropped.
function narrationLegal(
  state: ConversationState,
  event: Extract<ConversationEvent, { type: "NARRATION" }>,
): boolean {
  if (event.readId !== undefined && event.readId !== state.activeReadId) {
    return false;
  }
  if (event.buildId !== undefined && event.buildId !== state.activeBuildId) {
    return false;
  }
  if (companyNarrationPhases.has(state.phase)) {
    return event.readId !== undefined;
  }
  if (state.phase === "vo.building") {
    return event.buildId !== undefined;
  }
  return true;
}

function eventGuards(
  state: ConversationState,
  event: ConversationEvent,
): boolean {
  switch (event.type) {
    // A read event from a superseded (or already-concluded) run must never
    // advance or mis-record the conversation; activeReadId is null between
    // URL_SUBMITTED and READ_STARTED and after a terminal was recorded, so
    // read events in those windows are all stale.
    case "READ_TERMINAL":
    case "CLARIFY":
      return event.readId === state.activeReadId;
    case "NARRATION":
      return narrationLegal(state, event);
    case "REVIEW_READY":
      // Review only follows a RECORDED ready/partial outcome: a premature
      // REVIEW_READY mid-read would move to co.review and strand the
      // eventual READ_TERMINAL there forever.
      return state.readCompleted;
    case "QUESTION_ANSWERED":
      // Both the question and the chosen option must be the pending ones; an
      // unknown value is never echoed into the thread. A dismissal carries
      // no option value and is legal exactly when the pending question
      // offers the dismiss escape.
      if (state.pendingQuestion?.id !== event.questionId) {
        return false;
      }
      if (event.dismissed === true) {
        return state.pendingQuestion.dismissLabelKey !== undefined;
      }
      return state.pendingQuestion.options.some(
        (option) => option.value === event.value,
      );
    case "BUILD_STARTED":
      // From vo.result only a FAILED build may be retried; a succeeded or
      // deferred build is not restartable by the user from here.
      return state.phase !== "vo.result" || state.lastBuildStatus === "failed";
    case "BUILD_STAGE":
      if (event.buildId !== state.activeBuildId) return false;
      // From vo.result only a deferred build resumes; while building, a poll
      // repeating the current stage narrates nothing new.
      return state.phase === "vo.result"
        ? state.lastBuildStatus === "deferred"
        : event.stage !== state.lastBuildStage;
    case "BUILD_TERMINAL":
      if (event.buildId !== state.activeBuildId) return false;
      // From vo.result only a deferred build may conclude again, and only
      // with a NEW status — a repeated deferred poll records nothing.
      return state.phase === "vo.result"
        ? state.lastBuildStatus === "deferred" && event.status !== "deferred"
        : true;
    default:
      return true;
  }
}
