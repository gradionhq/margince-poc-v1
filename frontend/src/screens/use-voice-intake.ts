import { useCallback, useEffect, useRef, useState } from "react";
import type { components } from "../api/schema";
import type { IntakeOutcome, RefusalReason } from "./voice-intake-core";
import {
  intakePaste,
  intakeTranscript,
  intakeUpload,
  isAcceptedCorpusFile,
  sourceRef,
} from "./voice-intake-core";

// The Settings side of the shared voice-corpus intake: the same core the
// onboarding act runs on, adapted to a surface that reads its corpus from the
// server rather than narrating it. There is no local summary here — every
// successful ingest invalidates the manifest query, so the meter on screen is
// always the server's count and two concurrent ingests cannot disagree about
// the total.

type CorpusPreview = components["schemas"]["VoiceCorpusPreviewResult"];

/** A conversational source waiting for the owner to say which speaker is
 * them. Until it is answered the source is not ingested at all. */
export type SpeakerAsk = Readonly<{
  ref: string;
  label: string;
  content: string;
  preview: CorpusPreview;
}>;

/** What the card tells the owner about one finished intake attempt. */
export type IntakeNotice = Readonly<{
  ref: string;
  label: string;
  tone: "ok" | "warn";
  kind:
    | "kept"
    | "skippedType"
    | "skippedEmpty"
    | "refused"
    | "failed"
    | "dismissed";
  keptWords?: number;
  inputWords?: number;
  reason?: RefusalReason | null;
  problem?: unknown;
}>;

// A long Settings session can add many sources; the list keeps only the most
// recent results so it stays a readable summary of what just happened rather
// than an ever-growing log.
const MAX_NOTICES = 6;

type UseVoiceIntakeArgs = Readonly<{
  /** null while the owner has no profile — the first add mints it through the
   * shared ensureProfileId inside the core. */
  profileId: string | null;
  /** Called after every change the server accepted, so the caller can
   * invalidate the queries that render the corpus. */
  onChanged: () => void;
}>;

export function useVoiceIntake({ profileId, onChanged }: UseVoiceIntakeArgs) {
  const [asks, setAsks] = useState<readonly SpeakerAsk[]>([]);
  const [notices, setNotices] = useState<readonly IntakeNotice[]>([]);
  const [inFlight, setInFlight] = useState(0);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // Notices are keyed by source ref: re-adding the same file replaces its
  // previous result instead of stacking a second line about the same source.
  const note = useCallback((entry: IntakeNotice) => {
    setNotices((prev) =>
      [...prev.filter((existing) => existing.ref !== entry.ref), entry].slice(
        -MAX_NOTICES,
      ),
    );
  }, []);

  const applyOutcome = useCallback(
    (outcome: IntakeOutcome) => {
      if (!mounted.current) {
        return;
      }
      switch (outcome.kind) {
        case "ingested":
          note({
            ref: outcome.ref,
            label: outcome.label,
            tone: "ok",
            kind: "kept",
            keptWords: outcome.stats.kept_words,
            inputWords: outcome.stats.input_words,
          });
          onChanged();
          return;
        case "speaker-needed":
          // A re-upload under the same name supersedes its pending question;
          // the ingest is idempotent on source_ref server-side.
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
        case "refused":
          note({
            ref: outcome.ref,
            label: outcome.label,
            tone: "warn",
            kind: "refused",
            reason: outcome.reason,
            problem: outcome.problem,
          });
          return;
        case "skipped":
          note({
            ref: outcome.ref,
            label: outcome.label,
            tone: "warn",
            kind: outcome.reason === "empty" ? "skippedEmpty" : "skippedType",
          });
          return;
      }
    },
    [note, onChanged],
  );

  // A rejected promise here is a client-side fault, never a server refusal —
  // the core resolves those as a "refused" outcome. Its message is not shown
  // to the reader (it is not written for them); it is logged, and the card
  // says only that adding the source failed. The source's own key is not known
  // on that path (a file that could not be read has no content to key on), so
  // the notice is keyed by the label the reader chose it under.
  const runIntake = useCallback(
    (label: string, start: () => Promise<IntakeOutcome>) => {
      setInFlight((count) => count + 1);
      start()
        .then(applyOutcome)
        .catch((err: unknown) => {
          console.error("voice corpus intake failed unexpectedly", err);
          if (mounted.current) {
            note({
              ref: `failed:${label}`,
              label,
              tone: "warn",
              kind: "failed",
              problem: err,
            });
          }
        })
        .finally(() => {
          if (mounted.current) {
            setInFlight((count) => count - 1);
          }
        });
    },
    [applyOutcome, note],
  );

  const addFiles = useCallback(
    (files: readonly File[]) => {
      for (const file of files) {
        if (!isAcceptedCorpusFile(file.name)) {
          // Nothing was read, so there is no content key yet; the name is
          // enough to tell the reader which file was left out.
          note({
            ref: `skipped:${file.name}`,
            label: file.name,
            tone: "warn",
            kind: "skippedType",
          });
          continue;
        }
        runIntake(file.name, async () => {
          const text = await file.text();
          return intakeUpload(sourceRef("upload", text), file.name, text);
        });
      }
    },
    [note, runIntake],
  );

  const addPaste = useCallback(
    (text: string, label: string) => {
      runIntake(label, () =>
        intakePaste(sourceRef("paste", text), label, text),
      );
    },
    [runIntake],
  );

  const pendingAsk = asks[0] ?? null;

  const answerSpeaker = useCallback(
    (speakerLabel: string) => {
      const ask = asks[0];
      if (ask === undefined) {
        return;
      }
      setAsks((prev) => prev.filter((candidate) => candidate.ref !== ask.ref));
      runIntake(ask.label, () =>
        intakeTranscript(ask.ref, ask.label, ask.content, speakerLabel),
      );
    },
    [asks, runIntake],
  );

  // Declining to attribute a transcript drops it: none of it can be proven to
  // be the owner's own words, so ingesting it anyway is what corrupts a voice.
  const dismissAsk = useCallback(() => {
    const ask = asks[0];
    if (ask === undefined) {
      return;
    }
    setAsks((prev) => prev.filter((candidate) => candidate.ref !== ask.ref));
    note({
      ref: ask.ref,
      label: ask.label,
      tone: "warn",
      kind: "dismissed",
    });
  }, [asks, note]);

  return {
    addFiles,
    addPaste,
    pendingAsk,
    answerSpeaker,
    dismissAsk,
    notices,
    /** True while any preview, ingest, or unanswered speaker question is open
     * — a build started now would misrepresent what the voice is made of. */
    busy: inFlight > 0 || asks.length > 0,
    /** The profile the caller was rendered for; the core mints one on the
     * first add when this is null. */
    profileId,
  };
}
