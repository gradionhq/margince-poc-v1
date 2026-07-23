import type { MessageKey } from "../../i18n/en";

// The vocabulary of the onboarding conversation: acts, phases, thread
// entries, and the event union the reducer in conversation-machine.ts
// consumes. Types only — behaviour lives with the transition table.

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
  // The restore landing spot: a returning session whose company is already
  // confirmed is reconstructed here, and RESUME routes it onward. Live
  // confirmation advances straight to the next act because a reducer cannot
  // self-advance out of a momentary state.
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

// Exactly one label source — a blank button is unrepresentable.
type LabelSource =
  | { labelKey: MessageKey; label?: never }
  | { label: string; labelKey?: never };

export type QuestionOption = {
  value: string;
  detailKey?: MessageKey;
  params?: Record<string, string | number>;
} & LabelSource;

export type ConversationQuestion = {
  id: string;
  i18nKey: MessageKey;
  params?: Record<string, string | number>;
  options: QuestionOption[];
};

export type OutcomeTone = "success" | "deferred" | "failure";

// Entries enter the reducer with a semantic id (`read:ready`, `stage:extract`);
// withEntries stamps each appended entry with the state's monotonic sequence
// (`17:read:ready`), so a retried URL or a rebuilt stage never collides with
// an earlier occurrence as a React key.
export type ThreadEntry =
  | {
      kind: "narration";
      id: string;
      i18nKey: MessageKey;
      params?: Record<string, string | number>;
      /** Params that are i18n keys themselves; the renderer translates them. */
      paramKeys?: Record<string, MessageKey>;
      findingIds?: string[];
    }
  | { kind: "question"; id: string; question: ConversationQuestion }
  // A user turn says something — catalog copy XOR literal text, never blank.
  | ({
      kind: "user";
      id: string;
      params?: Record<string, string | number>;
    } & (
      | { i18nKey: MessageKey; text?: never }
      | { text: string; i18nKey?: never }
    ))
  | {
      kind: "outcome";
      id: string;
      i18nKey: MessageKey;
      params?: Record<string, string | number>;
      tone: OutcomeTone;
    };

export type NarrationEntry = Extract<ThreadEntry, { kind: "narration" }>;

export type ConversationState = {
  act: ConversationAct;
  phase: ConversationPhase;
  memberPath: boolean;
  pendingQuestion: ConversationQuestion | null;
  thread: ThreadEntry[];
  /** Monotonic entry-id sequence; see ThreadEntry. */
  seq: number;
  /**
   * The read run whose events are current. Null between URL_SUBMITTED and
   * READ_STARTED and after a terminal is recorded — read events for ANY run
   * are stale in those windows.
   */
  activeReadId: string | null;
  /** A ready/partial terminal was recorded; answering the last clarify (or
   * REVIEW_READY) may proceed to review without waiting on a re-read. */
  readCompleted: boolean;
  /** The build run whose events are current; stale runs are ignored. */
  activeBuildId: string | null;
  /** Last narrated build stage, so a repeated stage poll appends nothing. */
  lastBuildStage: BuildStage | null;
  /** How the last voice build ended: failed may be retried, deferred
   * resumes on its own and its later events re-enter vo.building. */
  lastBuildStatus: BuildTerminalStatus | null;
};

export type ReadTerminalStatus = "ready" | "partial" | "failed" | "deferred";
export type BuildStage = "snapshot" | "extract" | "evaluate" | "activate";
export type BuildTerminalStatus = "succeeded" | "failed" | "deferred";

/**
 * Where a restored session may land after RESUME. Only phases that are
 * stable waiting points qualify: transient phases (reading, building) cannot
 * be reconstructed from wizard state and restart from their act's entry.
 */
export type ResumePoint = Extract<
  ConversationPhase,
  "vo.invite" | "vo.collecting" | "vo.skipped" | "re.recap" | "cn.consent"
>;

export type ConversationEvent =
  | {
      type: "START";
      memberPath: boolean;
      /** Server-derived recap turns seeded on restore. Narration is never
       * persisted; these entries are recomputed from server state, so a
       * reload summarizes instead of replaying the original narration. */
      recap?: readonly NarrationEntry[];
      /** Restore landing: the server already recorded a confirmed company,
       * so the conversation reopens in co.confirmed and RESUME routes on. */
      companyConfirmed?: boolean;
    }
  | { type: "URL_SUBMITTED"; url: string }
  | { type: "READ_STARTED"; readId: string }
  // Narration carries the id of the run that produced it: readId for
  // site-read events, buildId for build events, neither for run-agnostic
  // narration (corpus growth). Company phases DROP uncorrelated narration.
  | {
      type: "NARRATION";
      readId?: string;
      buildId?: string;
      entry: NarrationEntry;
    }
  | {
      type: "READ_TERMINAL";
      readId: string;
      status: ReadTerminalStatus;
      /** Server-side finding count for the outcome copy; 0 when none. */
      findings: number;
    }
  | { type: "CLARIFY"; readId: string; question: ConversationQuestion }
  | { type: "QUESTION_ANSWERED"; questionId: string; value: string }
  | { type: "REVIEW_READY" }
  | { type: "MANUAL_CHOSEN" }
  | { type: "COMPANY_CONFIRMED" }
  // Restore routing out of co.confirmed. No target (or any target on the
  // member path) takes the same route the live confirmation takes; a target
  // fast-forwards a creator to the stable point the wizard state recorded.
  | { type: "RESUME"; target?: ResumePoint }
  | { type: "VOICE_OPT_IN" }
  | { type: "VOICE_SKIPPED" }
  | { type: "UPLOAD_ADDED"; id: string; name: string }
  | { type: "SPEAKER_NEEDED"; question: ConversationQuestion }
  | { type: "BUILD_STARTED"; buildId: string }
  | { type: "BUILD_STAGE"; buildId: string; stage: BuildStage }
  | { type: "BUILD_TERMINAL"; buildId: string; status: BuildTerminalStatus }
  | { type: "RESULTS_CONTINUE" }
  | { type: "CONNECT_DONE" };
