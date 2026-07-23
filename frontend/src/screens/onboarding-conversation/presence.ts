import type { components } from "../../api/schema";
import type { MarginceCoreState } from "../../design-system/margince-core";
import type { BuildStage, ConversationState } from "./conversation-machine";

// How the Margince orb accompanies the conversation: ONE pure mapping from
// machine state to the Core scene's presence and progress ring, shared by
// every act so the choreography reads as one performer. The company read's
// live snapshot rides in as an extra argument because the machine holds only
// run identity, never poll payloads.
//
// The grammar: listening while the human owes the next move, working (with a
// progress ring) while a read or build runs, attention when a question card
// waits, success on completions, error/quiet on failed/deferred runs.

type ReadSnapshot = Pick<
  components["schemas"]["CompanySiteRead"],
  "status" | "phase" | "pages_read"
>;

export type OrbPresence = Readonly<{
  core: MarginceCoreState;
  progress?: number;
}>;

// Mirrors the classic read screen's ring: a soft cap keeps the ring honest —
// it advances with pages but never claims completion the server has not
// reported, and the extracting phase parks near (not at) the end.
const READ_PAGES_SOFT_CAP = 40;
const READ_RING_FLOOR = 0.08;
const READ_RING_CRAWL_MAX = 0.78;
const READ_RING_EXTRACTING = 0.84;

const buildStageOrder: readonly BuildStage[] = [
  "snapshot",
  "extract",
  "evaluate",
  "activate",
];

function readProgress(read: ReadSnapshot): number {
  if (read.phase === "extracting") {
    return READ_RING_EXTRACTING;
  }
  return Math.max(
    READ_RING_FLOOR,
    Math.min(READ_RING_CRAWL_MAX, (read.pages_read ?? 0) / READ_PAGES_SOFT_CAP),
  );
}

function companyPresence(
  state: ConversationState,
  read: ReadSnapshot | null,
  readBroken: boolean,
): OrbPresence {
  if (readBroken || read?.status === "failed") {
    return { core: "error" };
  }
  if (read?.status === "deferred") {
    return { core: "quiet" };
  }
  if (
    state.phase === "co.reading" &&
    read !== null &&
    (read.status === "queued" || read.status === "reading")
  ) {
    return { core: "working", progress: readProgress(read) };
  }
  if (state.phase === "co.clarify") {
    return { core: "attention" };
  }
  if (state.phase === "co.review" || state.phase === "co.confirmed") {
    return { core: "success" };
  }
  return { core: "listening" };
}

function voicePresence(state: ConversationState): OrbPresence {
  if (state.phase === "vo.building") {
    const stage = state.lastBuildStage;
    return {
      core: "working",
      progress:
        stage === null
          ? READ_RING_FLOOR
          : (buildStageOrder.indexOf(stage) + 1) / buildStageOrder.length,
    };
  }
  if (state.phase === "vo.invite" || state.phase === "vo.speaker") {
    return { core: "attention" };
  }
  if (state.phase === "vo.result") {
    if (state.lastBuildStatus === "succeeded") {
      return { core: "success" };
    }
    return { core: state.lastBuildStatus === "failed" ? "error" : "quiet" };
  }
  return { core: "listening" };
}

export function presenceFor(
  state: ConversationState,
  company: Readonly<{
    read?: ReadSnapshot | null;
    readBroken?: boolean;
  }> = {},
): OrbPresence {
  switch (state.act) {
    case "welcome":
      return { core: "idle" };
    case "company":
      return companyPresence(
        state,
        company.read ?? null,
        company.readBroken ?? false,
      );
    case "voice":
      return voicePresence(state);
    case "results":
      return { core: "success" };
    case "connect":
      return { core: "listening" };
    case "done":
      return { core: "success" };
  }
}
