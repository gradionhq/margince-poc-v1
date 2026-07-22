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
  /** The read run whose events are current; stale runs are ignored. */
  activeReadId: string | null;
  /** Last narrated build stage, so a repeated stage poll appends nothing. */
  lastBuildStage: BuildStage | null;
  /** How the last voice build ended; only a failed build may be retried. */
  lastBuildStatus: BuildTerminalStatus | null;
};

export type ReadTerminalStatus = "ready" | "partial" | "failed" | "deferred";
export type BuildStage = "snapshot" | "extract" | "evaluate" | "activate";
export type BuildTerminalStatus = "succeeded" | "failed" | "deferred";

export type ConversationEvent =
  | { type: "START"; memberPath: boolean }
  | { type: "URL_SUBMITTED"; url: string }
  | { type: "READ_STARTED"; readId: string }
  | { type: "NARRATION"; readId?: string; entry: NarrationEntry }
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
  | { type: "RESUME" }
  | { type: "VOICE_OPT_IN" }
  | { type: "VOICE_SKIPPED" }
  | { type: "UPLOAD_ADDED"; id: string; name: string }
  | { type: "SPEAKER_NEEDED"; question: ConversationQuestion }
  | { type: "BUILD_STARTED" }
  | { type: "BUILD_STAGE"; stage: BuildStage }
  | { type: "BUILD_TERMINAL"; status: BuildTerminalStatus }
  | { type: "RESULTS_CONTINUE" }
  | { type: "CONNECT_DONE" };
