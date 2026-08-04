import type { ChangeEvent, Dispatch, RefObject } from "react";
import { useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import { VOICE_MIN_WORDS } from "../onboarding";
import { parseVoiceInsights, VoiceInsights } from "../voice-insights";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { NextStepBar } from "./next-step-bar";
import { presenceFor } from "./presence";
import { railStops } from "./rail";
import { ConversationThread } from "./thread";
import { useVoiceBuild } from "./use-voice-build";
import { useVoiceCorpus } from "./use-voice-corpus";
import { VoiceActArtifact } from "./voice-artifact";
import { VoiceBuildScene, VoiceCollectScene, VoiceScene } from "./voice-scenes";
import { ConversationWorkbench, useConfiguredModel } from "./workbench";

// The voice act driver: intake and ingestion live in useVoiceCorpus, the
// build lifecycle in useVoiceBuild. Every source — a browsed file, a window
// drop, a pasted text — lands through the collect scene; the rail beside it
// narrates and never offers a way to add material of its own.

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
      collecting={collecting}
      canBuild={canBuild}
      fileRef={fileRef}
      onFiles={onFiles}
      model={configuredModel}
    />
  );

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
          entries={state.thread}
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
          {state.phase === "vo.result" && (
            <ResultControls state={state} dispatch={dispatch} build={build} />
          )}
          {state.phase === "vo.skipped" && (
            <div className="ob-conv-chips">
              <Button
                small
                variant="primary"
                onClick={() => dispatch({ type: "RESULTS_CONTINUE" })}
              >
                {t("ob.conv.results.continue")}
              </Button>
            </div>
          )}
        </ConversationThread>
      </div>
      <VoiceNextStep state={state} canBuild={canBuild} />
    </ConversationWorkbench>
  );
}

// The pinned next-step line of the voice act: the open speaker decision
// outranks the build chip once the server corpus clears the floor.
function VoiceNextStep({
  state,
  canBuild,
}: Readonly<{ state: ConversationState; canBuild: boolean }>) {
  const t = useT();
  if (state.pendingQuestion !== null) {
    return (
      <NextStepBar
        label={t("ob.conv.next.decisionOne")}
        targetSelector="fieldset.ob-conv-question:not([disabled])"
        revision={state.seq}
      />
    );
  }
  if (canBuild) {
    return (
      <NextStepBar
        label={t("ob.conv.next.build")}
        targetSelector=".ob-conv-build-chip"
        revision={state.seq}
      />
    );
  }
  return null;
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

// The result of the act: a succeeded build shows what it learned (with the
// candidate-review note when the version awaits approval), a failed one
// offers the retry the machine permits, a deferred one has already said so
// honestly in its outcome — all continue onward the same way.
function ResultControls({
  state,
  dispatch,
  build,
}: Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  build: ReturnType<typeof useVoiceBuild>;
}>) {
  const t = useT();
  return (
    <>
      {/* What the build learned is the SCENE's; the rail keeps only the
          actions, or the same sentences would be on screen twice. */}
      <div className="ob-conv-chips">
        {state.lastBuildStatus === "failed" && (
          <Button
            small
            disabled={build.start.isPending}
            onClick={() => build.start.mutate()}
          >
            {t("ob.conv.voice.retryBuild")}
          </Button>
        )}
        <Button
          small
          variant="primary"
          onClick={() => dispatch({ type: "RESULTS_CONTINUE" })}
        >
          {t("ob.conv.results.continue")}
        </Button>
      </div>
    </>
  );
}

/**
 * Which scene the voice act's work surface is showing, and nothing else:
 * collect the writing, watch the model learn it, then read what it learned.
 * Outside those three the corpus dossier stands in. Extracted from the act
 * driver so the driver stays about events, not about staging.
 */
function VoiceSurface({
  state,
  dispatch,
  eyebrow,
  corpus,
  build,
  collecting,
  canBuild,
  fileRef,
  onFiles,
  model,
}: Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  eyebrow: string;
  corpus: ReturnType<typeof useVoiceCorpus>;
  build: ReturnType<typeof useVoiceBuild>;
  collecting: boolean;
  canBuild: boolean;
  fileRef: RefObject<HTMLInputElement | null>;
  onFiles: (event: ChangeEvent<HTMLInputElement>) => void;
  model: string;
}>) {
  const t = useT();
  if (collecting) {
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
        startError={build.start.isError ? build.start.error.message : null}
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
    const version = build.builtVersion.data ?? null;
    return (
      <VoiceScene
        eyebrow={eyebrow}
        title={t("ob.conv.voice.resultTitle")}
        sub={t("ob.conv.voice.resultSub")}
      >
        {build.builtVersion.isPending && (
          <p className="ob-conv-artifact-empty">
            {t("ob.conv.voice.resultLoading")}
          </p>
        )}
        {!build.builtVersion.isPending && version === null && (
          <p className="ob-conv-artifact-empty">
            {t("ob.conv.voice.resultEmpty")}
          </p>
        )}
        {version !== null && (
          <>
            {version.status === "candidate" && (
              <p className="t-small">{t("ob.conv.voice.candidateNote")}</p>
            )}
            <VoiceInsights
              data={parseVoiceInsights(version)}
              profileVersion={version.profile_version}
            />
          </>
        )}
      </VoiceScene>
    );
  }
  // Everything a scene does not own — a failed or deferred build, the skip
  // — keeps the corpus dossier. The build scene above has already claimed
  // every building phase, so no tracker runs here.
  return (
    <VoiceActArtifact
      summary={corpus.summary}
      manifest={corpus.manifest}
      stage={build.stage}
      building={false}
    />
  );
}
