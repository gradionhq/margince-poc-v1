import type { Dispatch } from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import type { components } from "../../api/schema";
import type { MessageKey } from "../../i18n/en";
import { problemMessage } from "../common";
import type { IntakeOutcome, RefusalReason } from "../voice-intake-core";
import {
  intakePaste,
  intakeTranscript,
  intakeUpload,
  isAcceptedCorpusFile,
  sourceRef,
} from "../voice-intake-core";
import type {
  ConversationEvent,
  ConversationState,
} from "./conversation-machine";
import { diffCorpus, useNarrationQueue } from "./narration";

// The voice corpus of the conversational shell as one hook: intake (files
// and pasted text), the preview probe that decides what a source honestly
// IS, the speaker question for conversational material, and ingestion at
// add-time. Every count the conversation shows is a server number — the
// preview's per-speaker words, the ingest's kept-of-total stats, and the
// corpus summary the meter renders. Client-side word counting only
// pre-gates empty files; it never reaches the thread.

type CorpusPreview = components["schemas"]["VoiceCorpusPreviewResult"];
type CorpusSummary = components["schemas"]["VoiceCorpusSummary"];
type IngestStats = components["schemas"]["VoiceIngestStats"];

export type CorpusManifestEntry = Readonly<{
  ref: string;
  label: string;
  /** Server-counted words that survived the speaker filter. */
  keptWords: number;
  /** Server-counted words of the whole source before filtering. */
  inputWords: number;
  /** Kept-of-total is shown only where filtering actually discarded turns. */
  transcript: boolean;
}>;

type SpeakerAsk = Readonly<{
  ref: string;
  label: string;
  content: string;
  preview: CorpusPreview;
}>;

type UseVoiceCorpusArgs = Readonly<{
  state: ConversationState;
  dispatch: Dispatch<ConversationEvent>;
  /** The restore probe's server meter, so a resumed session's word count
   * (and the build gate) is honest before the first new ingest. */
  initialSummary?: CorpusSummary | null;
}>;

const refusalKeys: Record<RefusalReason, MessageKey> = {
  unattributed: "ob.conv.voice.refusalUnattributed",
  speaker: "ob.conv.voice.refusalSpeaker",
  unsupported: "ob.conv.voice.refusalUnsupported",
};

// The refusal category the core read off the 422 picks the honest line; a
// refusal it did not recognize falls back to the server's own safe detail.
function refusalEntry(
  ref: string,
  reason: RefusalReason | null,
  problem: unknown,
): Extract<ConversationEvent, { type: "NARRATION" }>["entry"] {
  if (reason !== null) {
    return {
      kind: "narration",
      id: `refuse:${ref}`,
      i18nKey: refusalKeys[reason],
    };
  }
  return {
    kind: "narration",
    id: `refuse:${ref}`,
    i18nKey: "ob.conv.voice.ingestFailed",
    params: { detail: problemMessage(problem) },
  };
}

export function useVoiceCorpus({
  state,
  dispatch,
  initialSummary = null,
}: UseVoiceCorpusArgs) {
  const [summary, setSummary] = useState<CorpusSummary | null>(initialSummary);
  const [manifest, setManifest] = useState<readonly CorpusManifestEntry[]>([]);
  const [asks, setAsks] = useState<readonly SpeakerAsk[]>([]);
  const [probesInFlight, setProbesInFlight] = useState(0);
  const summaryRef = useRef<CorpusSummary | null>(initialSummary);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // Corpus growth narrates as a run-agnostic monotonic counter: the queue's
  // pacing keeps a multi-file burst readable, and the machine replaces the
  // words bubble in place per its stable id.
  const queue = useNarrationQueue({
    onEmit: (event) => {
      const { kind: _kind, ...entry } = event;
      dispatch({ type: "NARRATION", entry: { kind: "narration", ...entry } });
    },
  });

  const say = useCallback(
    (
      id: string,
      i18nKey: MessageKey,
      params?: Record<string, string | number>,
    ) => {
      dispatch({
        type: "NARRATION",
        entry: { kind: "narration", id, i18nKey, params },
      });
    },
    [dispatch],
  );

  // ingest/classifyUpload never THROW for a server refusal — that path
  // narrates through refusalEntry (problemMessage, already safe) and
  // resolves normally. Anything a .catch below actually receives is a
  // client-side bug that ran before the server had a chance to explain
  // itself, so its message is never trusted in front of the reader: it is
  // logged, and the narration says only that adding the source failed.
  const sayUnexpectedFailure = useCallback(
    (id: string, err: unknown) => {
      console.error("voice corpus ingest failed unexpectedly", err);
      say(id, "ob.conv.voice.ingestUnexpected");
    },
    [say],
  );

  // Concurrent ingests can settle out of order; each request is stamped at
  // issue time and only the newest-by-request-order summary may drive the
  // meter and the word-growth narration. Every response's summary is
  // authoritative for the corpus AT that request — a stale one arriving
  // late must not roll the displayed totals (and the build gate) backwards.
  const ingestSeq = useRef(0);
  const appliedSummarySeq = useRef(0);

  const recordIngest = useCallback(
    (
      seq: number,
      entry: CorpusManifestEntry,
      stats: IngestStats,
      next: CorpusSummary,
      reactionKey: MessageKey,
    ) => {
      if (!mounted.current) {
        return;
      }
      setManifest((prev) => [
        ...prev.filter((existing) => existing.ref !== entry.ref),
        entry,
      ]);
      say(`react:${entry.ref}`, reactionKey, {
        kept: stats.kept_words,
        total: stats.input_words,
        words: stats.kept_words,
      });
      if (seq <= appliedSummarySeq.current) {
        return;
      }
      appliedSummarySeq.current = seq;
      queue.push(diffCorpus(summaryRef.current, next));
      summaryRef.current = next;
      setSummary(next);
    },
    [queue, say],
  );

  // One place the conversation learns what an intake attempt ended as: the
  // core decides, this translates the verdict into what the thread says.
  const applyOutcome = useCallback(
    (seq: number, outcome: IntakeOutcome, reactionKey: MessageKey): void => {
      if (!mounted.current) {
        return;
      }
      if (outcome.kind === "skipped") {
        say(`skip:${outcome.label}`, "ob.conv.voice.fileEmpty", {
          name: outcome.label,
        });
        return;
      }
      if (outcome.kind === "refused") {
        dispatch({
          type: "NARRATION",
          entry: refusalEntry(outcome.ref, outcome.reason, outcome.problem),
        });
        return;
      }
      if (outcome.kind === "speaker-needed") {
        // A re-upload under the same name supersedes its pending question;
        // the ingest itself is idempotent on source_ref server-side.
        setAsks((prev) => [
          ...prev.filter((ask) => ask.ref !== outcome.ref),
          {
            ref: outcome.ref,
            label: outcome.label,
            content: outcome.content,
            preview: outcome.preview,
          },
        ]);
        return;
      }
      recordIngest(
        seq,
        {
          ref: outcome.ref,
          label: outcome.label,
          keptWords: outcome.stats.kept_words,
          inputWords: outcome.stats.input_words,
          transcript: outcome.transcript,
        },
        outcome.stats,
        outcome.summary,
        reactionKey,
      );
    },
    [dispatch, recordIngest, say],
  );

  // Each intake is stamped when its INGEST is issued, and only the
  // newest-stamped summary may drive the meter. A response that settles late
  // carries the corpus as it stood when the server answered it, so applying it
  // after a newer one would roll the displayed total — and the build gate that
  // reads it — backwards. The stamp is taken inside the core's ingest call
  // rather than here at intake start, because a file that reads and previews
  // slowly has not written anything yet: ordering it by when the reader picked
  // it would let a preview delay decide which summary wins.
  const runIntake = useCallback(
    (
      start: (stamp: () => number) => Promise<IntakeOutcome>,
      reactionKey: MessageKey,
      failureId: string,
    ): void => {
      setProbesInFlight((count) => count + 1);
      let seq = 0;
      start(() => {
        ingestSeq.current += 1;
        seq = ingestSeq.current;
        return seq;
      })
        .then((outcome) => applyOutcome(seq, outcome, reactionKey))
        .catch((err: unknown) => {
          if (mounted.current) {
            sayUnexpectedFailure(failureId, err);
          }
        })
        .finally(() => {
          if (mounted.current) {
            setProbesInFlight((count) => count - 1);
          }
        });
    },
    [applyOutcome, sayUnexpectedFailure],
  );

  // One intake for all three entry paths: the attach button, a drop onto the
  // thread, and (via addPaste) the composer. V1 corpus is text only;
  // anything else is refused by name.
  const addFiles = useCallback(
    (files: readonly File[]) => {
      for (const file of files) {
        if (!isAcceptedCorpusFile(file.name)) {
          say(`skip:${file.name}`, "ob.conv.voice.fileSkipped", {
            name: file.name,
          });
          continue;
        }
        // The thread shows the file the moment it is handed over, before its
        // content has been read — so the bubble is identified by name, while
        // the SOURCE it becomes is keyed by what is in it.
        dispatch({
          type: "UPLOAD_ADDED",
          id: `onboarding:upload:${file.name}`,
          name: file.name,
        });
        runIntake(
          async (stamp) => {
            const text = await file.text();
            return intakeUpload(
              sourceRef("upload", file.name, text),
              file.name,
              text,
              stamp,
            );
          },
          "ob.conv.voice.reactionDocument",
          `refuse:onboarding:upload:${file.name}`,
        );
      }
    },
    [dispatch, runIntake, say],
  );

  const addPaste = useCallback(
    (text: string, label: string) => {
      const ref = sourceRef("paste", label, text);
      dispatch({ type: "UPLOAD_ADDED", id: ref, name: label });
      runIntake(
        (stamp) => intakePaste(ref, label, text, stamp),
        "ob.conv.voice.reactionDocument",
        `refuse:${ref}`,
      );
    },
    [dispatch, runIntake],
  );

  // The machine holds ONE pending question, so speaker asks queue here and
  // step forward whenever the conversation is back in vo.collecting. A
  // duplicate dispatch (StrictMode, a re-render race) is inert: the machine
  // rejects SPEAKER_NEEDED outside vo.collecting.
  const nextAsk = asks[0];
  useEffect(() => {
    if (
      nextAsk === undefined ||
      state.phase !== "vo.collecting" ||
      state.pendingQuestion !== null
    ) {
      return;
    }
    dispatch({
      type: "SPEAKER_NEEDED",
      question: {
        id: `speaker:${nextAsk.ref}`,
        i18nKey: "ob.conv.voice.speakerQuestion",
        options: nextAsk.preview.speakers.map((speaker) => ({
          value: speaker.label,
          label: speaker.label,
          detailKey: "ob.conv.voice.speakerOptionDetail" as const,
          params: { words: speaker.words, turns: speaker.turns },
        })),
      },
    });
  }, [nextAsk, state.phase, state.pendingQuestion, dispatch]);

  // The owner named themselves: ingest with the speaker filter, so only that
  // speaker's server-counted words ever reach the meter.
  const answerSpeaker = useCallback(
    (questionId: string, value: string) => {
      const ask = asks.find(
        (candidate) => `speaker:${candidate.ref}` === questionId,
      );
      if (!ask) {
        return;
      }
      setAsks((prev) => prev.filter((candidate) => candidate.ref !== ask.ref));
      runIntake(
        (stamp) =>
          intakeTranscript(ask.ref, ask.label, ask.content, value, stamp),
        "ob.conv.voice.reactionTranscript",
        `refuse:${ask.ref}`,
      );
    },
    [asks, runIntake],
  );

  return {
    summary,
    manifest,
    addFiles,
    addPaste,
    answerSpeaker,
    /** True while any probe, ingest, or speaker question is still open —
     * a build starting now would misrepresent what the voice is made of. */
    busy: probesInFlight > 0 || asks.length > 0,
  };
}
