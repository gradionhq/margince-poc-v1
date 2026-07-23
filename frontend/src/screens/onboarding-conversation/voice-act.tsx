import { Paperclip, Send, Sparkles } from "lucide-react";
import type { ChangeEvent, Dispatch } from "react";
import { useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import { Button } from "../../design-system/atoms";
import { useT } from "../../i18n";
import { ACCEPTED_CORPUS_ATTR, VOICE_MIN_WORDS } from "../onboarding";
import { parseVoiceInsights, VoiceInsights } from "../voice-insights";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { NarrationBubble } from "./entries";
import { NextStepBar } from "./next-step-bar";
import { presenceFor } from "./presence";
import { ConversationThread } from "./thread";
import { useVoiceBuild } from "./use-voice-build";
import { useVoiceCorpus } from "./use-voice-corpus";
import { VoiceActArtifact } from "./voice-artifact";
import { ConversationWorkbench } from "./workbench";

// The voice act driver: intake and ingestion live in useVoiceCorpus, the
// build lifecycle in useVoiceBuild; this component owns the composer (paste
// offer), the drop target, and the chips — all expressed as machine events,
// so the pure reducer stays the single truth about where the act is.

// Below this many client-counted words a composer submit is a message, not
// corpus material; the client count only routes the offer, the server
// counts what is ingested.
const PASTE_OFFER_MIN_WORDS = 40;

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
  const build = useVoiceBuild({
    dispatch,
    machine,
    sharedProfileId: corpus.sharedProfileId,
  });
  const [draft, setDraft] = useState("");
  const [pendingPaste, setPendingPaste] = useState<string | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const composer = useRef<HTMLTextAreaElement>(null);

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

  // The hint promises "drop files anywhere in this conversation", so the
  // WHOLE window is the drop target — a file landing on the composer, the
  // artifact panel, or a layout gap must feed the corpus, and outside the
  // collecting phases a stray drop must still be neutralized: the browser's
  // default is to NAVIGATE to the dropped file, which would tear the user
  // out of the onboarding mid-act.
  const { addFiles } = corpus;
  useEffect(() => {
    const onDragOver = (event: globalThis.DragEvent) => {
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

  const submitComposer = () => {
    const text = draft.trim();
    if (text === "" || state.phase !== "vo.collecting") {
      return;
    }
    // Sending must not strand focus on the send button: the composer stays
    // the keyboard home while the conversation continues.
    composer.current?.focus();
    if (text.split(/\s+/).length >= PASTE_OFFER_MIN_WORDS) {
      setPendingPaste(text);
    } else {
      setPendingPaste(null);
      dispatch({
        type: "NARRATION",
        entry: {
          kind: "narration",
          id: "paste:short",
          i18nKey: "ob.conv.voice.pasteTooShort",
        },
      });
    }
    setDraft("");
  };

  const handleAnswer = (questionId: string, value: string) => {
    dispatch({ type: "QUESTION_ANSWERED", questionId, value });
    corpus.answerSpeaker(questionId, value);
  };

  const presence = presenceFor(state);

  return (
    <ConversationWorkbench
      core={presence.core}
      progress={presence.progress}
      status={t(
        state.phase === "vo.building"
          ? "ob.conv.voice.statusBuilding"
          : "ob.ai.ready",
      )}
      artifact={
        <VoiceActArtifact
          summary={corpus.summary}
          manifest={corpus.manifest}
          stage={build.stage}
          building={state.phase === "vo.building"}
        />
      }
    >
      <div className={`mw-thread${dragOver ? " ob-conv-dragover" : ""}`}>
        <ConversationThread
          entries={state.thread}
          pendingQuestionId={state.pendingQuestion?.id ?? null}
          onAnswer={handleAnswer}
        >
          {state.phase === "vo.invite" && <InviteChips dispatch={dispatch} />}
          {state.phase === "vo.collecting" && (
            <CollectingControls
              dispatch={dispatch}
              serverWords={serverWords}
              canBuild={canBuild}
              startPending={build.start.isPending}
              onBuild={() => build.start.mutate()}
              startError={
                build.start.isError ? build.start.error.message : null
              }
            />
          )}
          {pendingPaste !== null && state.phase === "vo.collecting" && (
            <PasteOffer
              onAdd={() => {
                corpus.addPaste(pendingPaste, t("ob.conv.voice.pasteSource"));
                setPendingPaste(null);
              }}
              onDiscard={() => setPendingPaste(null)}
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
      {collecting && (
        <div className="mw-composer">
          <input
            ref={fileRef}
            type="file"
            multiple
            hidden
            accept={ACCEPTED_CORPUS_ATTR}
            onChange={onFiles}
          />
          <Button
            aria-label={t("ob.conv.voice.attach")}
            onClick={() => fileRef.current?.click()}
          >
            <Paperclip aria-hidden />
          </Button>
          <textarea
            ref={composer}
            value={draft}
            maxLength={100_000}
            rows={2}
            placeholder={t("ob.conv.voice.composer")}
            aria-label={t("ob.conv.voice.composer")}
            onChange={(event) => setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (
                event.key === "Enter" &&
                !event.shiftKey &&
                !event.nativeEvent.isComposing
              ) {
                event.preventDefault();
                submitComposer();
              }
            }}
          />
          <Button
            variant="primary"
            aria-label={t("ob.ai.send")}
            disabled={!draft.trim()}
            onClick={submitComposer}
          >
            <Send aria-hidden />
          </Button>
          <small>{t("ob.conv.voice.dropHint")}</small>
        </div>
      )}
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

function InviteChips({
  dispatch,
}: Readonly<{ dispatch: Dispatch<ConversationEvent> }>) {
  const t = useT();
  return (
    <>
      <NarrationBubble
        entry={{
          kind: "narration",
          id: "voice:invite",
          i18nKey: "ob.conv.voice.invite",
        }}
      />
      <div className="ob-conv-chips">
        <Button
          small
          variant="primary"
          onClick={() => dispatch({ type: "VOICE_OPT_IN" })}
        >
          {t("ob.conv.voice.optIn")}
        </Button>
        <Button
          small
          variant="ghost"
          onClick={() => dispatch({ type: "VOICE_SKIPPED" })}
        >
          {t("ob.conv.voice.skipped")}
        </Button>
      </div>
    </>
  );
}

function CollectingControls({
  dispatch,
  serverWords,
  canBuild,
  startPending,
  onBuild,
  startError,
}: Readonly<{
  dispatch: Dispatch<ConversationEvent>;
  serverWords: number;
  canBuild: boolean;
  startPending: boolean;
  onBuild: () => void;
  startError: string | null;
}>) {
  const t = useT();
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
      {startError !== null && (
        <p className="mw-send-error" role="alert">
          {startError}
        </p>
      )}
      <div className="ob-conv-chips">
        {(canBuild || startPending) && (
          <Button
            small
            variant="primary"
            className="ob-conv-build-chip"
            disabled={startPending}
            onClick={onBuild}
          >
            <Sparkles aria-hidden /> {t("ob.conv.voice.buildChip")}
          </Button>
        )}
        <Button
          small
          variant="ghost"
          onClick={() => dispatch({ type: "VOICE_SKIPPED" })}
        >
          {t("ob.conv.voice.skipped")}
        </Button>
      </div>
    </>
  );
}

function PasteOffer({
  onAdd,
  onDiscard,
}: Readonly<{ onAdd: () => void; onDiscard: () => void }>) {
  const t = useT();
  return (
    <>
      <NarrationBubble
        entry={{
          kind: "narration",
          id: "paste:offer",
          i18nKey: "ob.conv.voice.pasteOffer",
        }}
      />
      <div className="ob-conv-chips">
        <Button small variant="primary" onClick={onAdd}>
          {t("ob.conv.voice.pasteAdd")}
        </Button>
        <Button small variant="ghost" onClick={onDiscard}>
          {t("ob.conv.voice.pasteDiscard")}
        </Button>
      </div>
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
  const version = build.builtVersion.data ?? null;
  return (
    <>
      {state.lastBuildStatus === "succeeded" && (
        <div className="ob-conv-voice-result">
          <h2>{t("ob.conv.voice.resultTitle")}</h2>
          {build.builtVersion.isPending && (
            <p>{t("ob.conv.voice.resultLoading")}</p>
          )}
          {!build.builtVersion.isPending && version === null && (
            <p>{t("ob.conv.voice.resultEmpty")}</p>
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
        </div>
      )}
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
