import type { ChangeEvent, Dispatch, RefObject } from "react";
import { useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { useT } from "../../i18n";
import { problemMessageOf } from "../common";
import { VOICE_MIN_WORDS } from "../onboarding";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { presenceFor } from "./presence";
import { railStops } from "./rail";
import { ConversationThread, selectionFor } from "./thread";
import { useVoiceBuild } from "./use-voice-build";
import { useVoiceCorpus } from "./use-voice-corpus";
import type { VoiceContinueReason } from "./voice-artifact";
import { VoiceActArtifact } from "./voice-artifact";
import {
  VoiceBuildScene,
  VoiceCollectScene,
  VoiceResultScene,
  VoiceSpeakerScene,
} from "./voice-scenes";
import { ConversationWorkbench, useConfiguredModel } from "./workbench";

// The voice act driver: intake and ingestion live in useVoiceCorpus, the
// build lifecycle in useVoiceBuild. Every source — a browsed file, a window
// drop, a pasted text — lands through the collect scene; the rail beside it
// narrates and never offers a way to add material of its own. Every
// consequence a scene already shows (a source in the sources list, a
// decision on its own surface, Continue in its own foot) is filtered out of
// the rail's thread below — a fact live on the surface has no business
// repeating itself as a rail bubble.

type CorpusSummary = components["schemas"]["VoiceCorpusSummary"];

type VoiceActProps = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  /** The restore probe's server meter for a resumed session; null fresh. */
  initialSummary?: CorpusSummary | null;
}>;

export function VoiceAct({ state, dispatch, initialSummary }: VoiceActProps) {
  const t = useT();
  const machine = useRef(state);
  machine.current = state;
  const corpus = useVoiceCorpus({ state, dispatch, initialSummary });
  const build = useVoiceBuild({ dispatch, machine });
  const [dragOver, setDragOver] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);

  const collecting =
    state.phase === "vo.collecting" || state.phase === "vo.speaker";
  const serverWords = corpus.summary?.total_words ?? 0;
  const canBuild =
    state.phase === "vo.collecting" &&
    serverWords >= VOICE_MIN_WORDS &&
    !corpus.busy &&
    !build.start.isPending;

  const onFiles = (event: ChangeEvent<HTMLInputElement>) => {
    corpus.addFiles(Array.from(event.target.files ?? []));
    event.target.value = "";
  };

  // The scene promises "drop files anywhere in this conversation", so the
  // WHOLE window is the drop target — a file landing on the rail, the
  // artifact panel, or a layout gap must feed the corpus, and outside the
  // collecting phases a stray drop must still be neutralized: the browser's
  // default is to NAVIGATE to the dropped file, which would tear the user
  // out of the onboarding mid-act.
  const { addFiles } = corpus;
  useEffect(() => {
    // Only FILE drags are claimed: dragging selected text elsewhere on the
    // page is a native interaction this act must not swallow.
    const isFileDrag = (event: globalThis.DragEvent) =>
      event.dataTransfer?.types.includes("Files") ?? false;
    const onDragOver = (event: globalThis.DragEvent) => {
      if (!isFileDrag(event)) {
        return;
      }
      event.preventDefault();
      setDragOver(collecting);
    };
    const onDragLeave = (event: globalThis.DragEvent) => {
      // relatedTarget is null only when the drag exits the window; moving
      // between elements inside it must not flicker the affordance off.
      if (event.relatedTarget === null) {
        setDragOver(false);
      }
    };
    const onDrop = (event: globalThis.DragEvent) => {
      if (!isFileDrag(event)) {
        return;
      }
      event.preventDefault();
      setDragOver(false);
      if (collecting) {
        addFiles(Array.from(event.dataTransfer?.files ?? []));
      }
    };
    window.addEventListener("dragover", onDragOver);
    window.addEventListener("dragleave", onDragLeave);
    window.addEventListener("drop", onDrop);
    return () => {
      window.removeEventListener("dragover", onDragOver);
      window.removeEventListener("dragleave", onDragLeave);
      window.removeEventListener("drop", onDrop);
    };
  }, [collecting, addFiles]);

  const handleAnswer = (questionId: string, value: string) => {
    dispatch({ type: "QUESTION_ANSWERED", questionId, value });
    corpus.answerSpeaker(questionId, value);
  };

  const presence = presenceFor(state);
  const configuredModel = useConfiguredModel();

  // Where the journey stands, in the rail's own counting.
  const stops = railStops(state.memberPath);
  const eyebrow = t("ob.conv.scene.step", {
    n: stops.findIndex((stop) => stop.key === "voice") + 1,
    m: stops.length,
    label: t("ob.rail.voice"),
  });

  const scene = (
    <VoiceSurface
      state={state}
      dispatch={dispatch}
      eyebrow={eyebrow}
      corpus={corpus}
      build={build}
      canBuild={canBuild}
      fileRef={fileRef}
      onFiles={onFiles}
      onAnswer={handleAnswer}
      model={configuredModel}
    />
  );

  // Upload consequences (a source's own word count, the corpus meter, the
  // speaker decision itself) render on the scene, not twice — an unresolved
  // question is EVERY caller's own filter (see thread.tsx's `selectionFor`
  // docstring); the id patterns below are the whole of what useVoiceCorpus
  // narrates for an ingest already ON the collect scene's sources list.
  const threadEntries = state.thread.filter((entry, index) => {
    if (entry.kind === "question") {
      return selectionFor(state.thread, index) !== null;
    }
    return !isSurfaceRedundant(entry.id);
  });

  return (
    <ConversationWorkbench
      core={presence.core}
      progress={presence.progress}
      railState={state}
      status={t(
        state.phase === "vo.building"
          ? "ob.conv.voice.statusBuilding"
          : "ob.ai.ready",
      )}
      artifact={scene}
    >
      <div className={`mw-thread${dragOver ? " ob-conv-dragover" : ""}`}>
        <ConversationThread
          entries={threadEntries}
          pendingQuestionId={state.pendingQuestion?.id ?? null}
          onAnswer={handleAnswer}
        >
          {state.phase === "vo.collecting" && (
            // The controls live on the scene now; the rail says only what
            // the machine wants and why.
            <CollectingNarration
              serverWords={serverWords}
              canBuild={canBuild}
            />
          )}
          {state.phase === "vo.speaker" && (
            <NarrationBubble
              entry={{
                kind: "narration",
                id: "voice:guide-speaker",
                i18nKey: "ob.conv.voice.guideSpeaker",
              }}
            />
          )}
        </ConversationThread>
      </div>
    </ConversationWorkbench>
  );
}

// Upload consequences the collect scene's own sources list and meter already
// show: the "Added {name}." turn UPLOAD_ADDED appends, its per-source
// reaction ("Words kept/counted: …"), and the corpus-growth counter
// (diffCorpus's stable "words"/"band:<band>" ids). `withEntries` stamps
// every id with a `<seq>:` prefix, so the match is a suffix test on the
// SHAPE those three narrations always take, not the reaction's own text.
function isSurfaceRedundant(id: string): boolean {
  return (
    /^\d+:upload:/.test(id) ||
    /^\d+:react:/.test(id) ||
    /^\d+:words$/.test(id) ||
    /^\d+:band:/.test(id)
  );
}

// What the machine wants while it collects, and nothing it can press: the
// drop target, the sources and the build action are the scene's.
function CollectingNarration({
  serverWords,
  canBuild,
}: Readonly<{ serverWords: number; canBuild: boolean }>) {
  return (
    <>
      <NarrationBubble
        entry={{
          kind: "narration",
          id: "voice:collect",
          i18nKey: "ob.conv.voice.collectAsk",
        }}
      />
      {serverWords > 0 && serverWords < VOICE_MIN_WORDS && (
        <NarrationBubble
          entry={{
            kind: "narration",
            id: "voice:floor",
            i18nKey: "ob.conv.voice.buildFloor",
            params: { words: serverWords, min: VOICE_MIN_WORDS },
          }}
        />
      )}
      {canBuild && (
        <NarrationBubble
          entry={{
            kind: "narration",
            id: "voice:nudge",
            i18nKey: "ob.conv.voice.buildNudge",
          }}
        />
      )}
    </>
  );
}

// The fallback branch's own Continue: `vo.skipped` has nothing left to
// finish, a failed build offers the retry the machine still permits, a
// deferred one has already said so honestly in its rail outcome and only
// waits for the human to move on. Every case shares the one action.
function continueBarFor(
  state: ConversationState,
  build: ReturnType<typeof useVoiceBuild>,
  dispatch: Dispatch<ConversationEvent>,
): Readonly<{
  reason: VoiceContinueReason;
  onContinue: () => void;
  retryPending?: boolean;
  onRetry?: () => void;
}> | null {
  const onContinue = () => dispatch({ type: "RESULTS_CONTINUE" });
  if (state.phase === "vo.skipped") {
    return { reason: "skipped", onContinue };
  }
  if (state.phase === "vo.result" && state.lastBuildStatus === "failed") {
    return {
      reason: "failed",
      onContinue,
      retryPending: build.start.isPending,
      onRetry: () => build.start.mutate(),
    };
  }
  if (state.phase === "vo.result" && state.lastBuildStatus === "deferred") {
    return { reason: "deferred", onContinue };
  }
  return null;
}

/**
 * Which scene the voice act's work surface is showing, and nothing else:
 * collect the writing, decide who is speaking, watch the model learn it,
 * then read what it learned. Outside those the corpus dossier stands in,
 * carrying Continue itself once there is nothing left a scene can own.
 * Extracted from the act driver so the driver stays about events, not
 * about staging.
 */
function VoiceSurface({
  state,
  dispatch,
  eyebrow,
  corpus,
  build,
  canBuild,
  fileRef,
  onFiles,
  onAnswer,
  model,
}: Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  eyebrow: string;
  corpus: ReturnType<typeof useVoiceCorpus>;
  build: ReturnType<typeof useVoiceBuild>;
  canBuild: boolean;
  fileRef: RefObject<HTMLInputElement | null>;
  onFiles: (event: ChangeEvent<HTMLInputElement>) => void;
  onAnswer: (questionId: string, value: string) => void;
  model: string;
}>) {
  const t = useT();
  if (state.phase === "vo.collecting") {
    return (
      <VoiceCollectScene
        eyebrow={eyebrow}
        summary={corpus.summary}
        manifest={corpus.manifest}
        fileRef={fileRef}
        onFiles={onFiles}
        onAddPaste={(text) =>
          corpus.addPaste(text, t("ob.conv.voice.pasteSource"))
        }
        onBuild={() => build.start.mutate()}
        onSkip={() => dispatch({ type: "VOICE_SKIPPED" })}
        canBuild={canBuild}
        startPending={build.start.isPending}
        startError={
          build.start.isError ? problemMessageOf(build.start.error, t) : null
        }
      />
    );
  }
  if (state.phase === "vo.speaker" && state.pendingQuestion !== null) {
    return (
      <VoiceSpeakerScene
        eyebrow={eyebrow}
        question={state.pendingQuestion}
        onAnswer={onAnswer}
      />
    );
  }
  if (state.phase === "vo.building") {
    return (
      <VoiceBuildScene
        stage={build.stage}
        summary={corpus.summary}
        sources={corpus.manifest.length}
        model={model}
      />
    );
  }
  if (state.phase === "vo.result" && state.lastBuildStatus === "succeeded") {
    return (
      <VoiceResultScene
        eyebrow={eyebrow}
        loading={build.builtVersion.isPending}
        version={build.builtVersion.data ?? null}
        onContinue={() => dispatch({ type: "RESULTS_CONTINUE" })}
      />
    );
  }
  // Everything a scene does not own — a failed or deferred build, the skip
  // — keeps the corpus dossier, now carrying its own pinned Continue. The
  // build scene above has already claimed every building phase, so no
  // tracker runs here.
  return (
    <VoiceActArtifact
      summary={corpus.summary}
      manifest={corpus.manifest}
      stage={build.stage}
      building={false}
      continueBar={continueBarFor(state, build, dispatch) ?? undefined}
    />
  );
}
