import type { components } from "../../api/schema";
import type { MarginceCoreState } from "../../design-system/margince-core";
import type { BuildStage, ConversationState } from "./conversation-machine";

// How the Margince orb accompanies the conversation: ONE pure mapping from
// machine state to the Core scene's presence and progress ring, shared by
// every act so the choreography reads as one performer. The company read's
// live snapshot rides in as an extra argument because the machine holds only
// run identity, never poll payloads.
//
// The grammar is the agent's own lifecycle, and each phase takes the state that
// names what is actually happening: dormant while the human owes the next move,
// ingesting (with a progress ring) while pages or corpus material arrive,
// reasoning while the agent works over what it has, drafting while it composes,
// applied on a confirmation, error on a failed run. Anything waiting on a person
// is the agent at REST: the surface's own card is what asks for the answer. Nothing here claims the agent is listening — it reads captured
// activity and never holds a conversation.

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
    // Deferred is not broken and not busy: the run has not started, so the Core
    // is at rest rather than reaching for something.
    return { core: "dormant" };
  }
  if (
    state.phase === "co.reading" &&
    read !== null &&
    (read.status === "queued" || read.status === "reading")
  ) {
    // The two halves of a read are two different states, and the phase is what
    // tells them apart: crawling is pages ARRIVING, extracting is the agent
    // working over what arrived.
    return {
      core: read.phase === "extracting" ? "reasoning" : "ingesting",
      progress: readProgress(read),
    };
  }
  if (state.phase === "co.clarify") {
    // The read found something it cannot resolve alone — a contradiction, a
    // field it may not fill in for you. Held, not progressing.
    return { core: "flagged" };
  }
  if (state.phase === "co.review") {
    // Proposals sitting in front of a person: the agent has stopped, so the orb
    // rests rather than claiming work nobody asked it to keep doing.
    return { core: "dormant" };
  }
  if (state.phase === "co.confirmed") {
    return { core: "applied" };
  }
  return { core: "dormant" };
}

/**
 * Which state a voice build is in, by the stage the server last reported.
 *
 * The four stages are not one activity: a snapshot and an extraction are
 * material ARRIVING, an evaluation is the agent working over it, and activation
 * is the agent producing the thing. One `working` for all four was the Core
 * saying "busy" through a process the reader can actually follow.
 */
function buildCore(stage: BuildStage | null): MarginceCoreState {
  if (stage === "evaluate") {
    return "reasoning";
  }
  return stage === "activate" ? "drafting" : "ingesting";
}

function voicePresence(state: ConversationState): OrbPresence {
  if (state.phase === "vo.building") {
    const stage = state.lastBuildStage;
    return {
      core: buildCore(stage),
      progress:
        stage === null
          ? READ_RING_FLOOR
          : (buildStageOrder.indexOf(stage) + 1) / buildStageOrder.length,
    };
  }
  if (state.phase === "vo.speaker") {
    // A question card: the build needs a person to say which voice is theirs,
    // and until they do the agent is not working.
    return { core: "dormant" };
  }
  if (state.phase === "vo.result") {
    if (state.lastBuildStatus === "succeeded") {
      return { core: "applied" };
    }
    return { core: state.lastBuildStatus === "failed" ? "error" : "dormant" };
  }
  return { core: "dormant" };
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
      return { core: "dormant" };
    case "company":
      return companyPresence(
        state,
        company.read ?? null,
        company.readBroken ?? false,
      );
    case "voice":
      return voicePresence(state);
    case "results":
      return { core: "applied" };
    case "connect":
      return { core: "dormant" };
    case "done":
      return { core: "applied" };
  }
}
