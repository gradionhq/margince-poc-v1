import { useMutation, useQuery } from "@tanstack/react-query";
import type { Dispatch } from "react";
import { useEffect, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import { problemMessage } from "../common";
import { pickBuiltVersion } from "../onboarding";
import type {
  BuildStage,
  BuildTerminalStatus,
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import type { VoiceBuildSnapshot } from "./narration";
import { diffVoiceBuild, useNarrationQueue } from "./narration";

// The voice build lifecycle as one hook: start the build, poll its durable
// row, narrate stage deltas, and conclude with a correlated terminal event.
// diffVoiceBuild computes what is new per snapshot; its stage events ride
// the paced queue and are re-dispatched as BUILD_STAGE machine events, so
// the machine's correlation guard (stale build ids, deferred resume from
// vo.result) stays the single authority on what may speak.

type VoiceBuild = components["schemas"]["VoiceBuild"];
type VoiceProfileVersion = components["schemas"]["VoiceProfileVersion"];

const POLL_MS = 1200;
const DEFERRED_POLL_MS = 60_000;

// Type-honest narrowing of a polled status to the machine's terminal union;
// queued/running are not terminals and map to null.
function terminalOf(status: VoiceBuild["status"]): BuildTerminalStatus | null {
  return status === "succeeded" || status === "failed" || status === "deferred"
    ? status
    : null;
}

const BUILD_STAGES: readonly BuildStage[] = [
  "snapshot",
  "extract",
  "evaluate",
  "activate",
];

type UseVoiceBuildArgs = Readonly<{
  dispatch: Dispatch<ConversationEvent>;
  /** Live view of the machine, for the poll-failure fallback guards. */
  machine: Readonly<{ current: ConversationState }>;
  /** The corpus hook's single-flight profile resolution. */
  sharedProfileId: () => Promise<string>;
}>;

export function useVoiceBuild({
  dispatch,
  machine,
  sharedProfileId,
}: UseVoiceBuildArgs) {
  const [profileId, setProfileId] = useState<string | null>(null);
  const [buildId, setBuildId] = useState<string | null>(null);
  const prevSnapshot = useRef<VoiceBuildSnapshot | null>(null);

  const queue = useNarrationQueue({
    onEmit: (event) => {
      // diffVoiceBuild only says stages, under `<buildId>:stage:<stage>` ids
      // (a build id is a UUID, so the first colon splits it off cleanly).
      const [runId, , stagePart] = event.id.split(":");
      const stage = BUILD_STAGES.find((known) => known === stagePart);
      if (runId !== undefined && stage !== undefined) {
        dispatch({ type: "BUILD_STAGE", buildId: runId, stage });
      }
    },
  });

  const start = useMutation({
    mutationFn: async (): Promise<{ profileId: string; buildId: string }> => {
      const id = await sharedProfileId();
      const { data, error } = await api.POST("/voice-profiles/{id}/builds", {
        params: { path: { id } },
        body: { reason: "onboarding" },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return { profileId: id, buildId: data.id };
    },
    onSuccess: ({ profileId: profile, buildId: build }) => {
      prevSnapshot.current = null;
      setProfileId(profile);
      setBuildId(build);
      dispatch({ type: "BUILD_STARTED", buildId: build });
    },
  });

  const poll = useQuery({
    queryKey: ["voice-build", profileId, buildId],
    enabled: profileId !== null && buildId !== null,
    queryFn: async (): Promise<VoiceBuild> => {
      const { data, error } = await api.GET(
        "/voice-profiles/{id}/builds/{buildId}",
        { params: { path: { id: profileId ?? "", buildId: buildId ?? "" } } },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      if (status === "queued" || status === "running") {
        return POLL_MS;
      }
      // A deferred build resumes on its own (budget window); keep a slow
      // poll so the resume re-enters vo.building without a reload.
      return status === "deferred" ? DEFERRED_POLL_MS : false;
    },
  });

  useEffect(() => {
    const next = poll.data;
    if (!next) {
      return;
    }
    const snapshot: VoiceBuildSnapshot = {
      id: next.id,
      status: next.status,
      stage: next.stage,
    };
    const events = diffVoiceBuild(prevSnapshot.current, snapshot);
    prevSnapshot.current = snapshot;
    const flushed = events.some((event) => event.kind === "flush");
    // Stage narration first (the flush drains it), then the terminal — so
    // progress always lands before the outcome.
    queue.push(events);
    const terminal = terminalOf(next.status);
    if (flushed && terminal !== null) {
      dispatch({ type: "BUILD_TERMINAL", buildId: next.id, status: terminal });
    }
  }, [poll.data, queue, dispatch]);

  // A persistently failing poll must not strand the act in vo.building:
  // isError flips only after react-query exhausted its retries (a transient
  // error that recovers never lands here), and only the still-active,
  // still-building run is concluded — a run whose real terminal already
  // moved the machine to vo.result (or a superseded build id) keeps its
  // recorded outcome. The failed conclusion re-arms the retry chip; the
  // durable build keeps running server-side either way.
  useEffect(() => {
    if (!poll.isError || buildId === null) {
      return;
    }
    const { phase, activeBuildId } = machine.current;
    if (phase !== "vo.building" || activeBuildId !== buildId) {
      return;
    }
    queue.flush();
    dispatch({
      type: "NARRATION",
      buildId,
      entry: {
        kind: "narration",
        id: `${buildId}:poll-failed`,
        i18nKey: "ob.conv.voice.buildPollFailed",
      },
    });
    dispatch({ type: "BUILD_TERMINAL", buildId, status: "failed" });
  }, [poll.isError, buildId, machine, queue, dispatch]);

  // What the finished build produced: the just-built version carries the
  // structured insights (candidate when it awaits review). A failed version
  // read degrades the result card to its honest empty line, never blocks.
  const builtVersion = useQuery({
    queryKey: ["voice-built-version", profileId, buildId],
    enabled: profileId !== null && poll.data?.status === "succeeded",
    queryFn: async (): Promise<VoiceProfileVersion | null> => {
      const { data, error } = await api.GET("/voice-profiles/{id}/versions", {
        params: { path: { id: profileId ?? "" } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return pickBuiltVersion(data.data);
    },
  });

  return {
    /** Starts (or after a failure, restarts) a build. */
    start,
    /** Current build stage for the artifact tracker; null outside a run. */
    stage: poll.data?.status === "running" ? (poll.data.stage ?? null) : null,
    builtVersion,
  };
}
