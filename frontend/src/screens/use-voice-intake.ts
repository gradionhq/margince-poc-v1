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
    | "dismissed"
    | "askQueueFull";
  keptWords?: number;
  inputWords?: number;
  reason?: RefusalReason | null;
  problem?: unknown;
}>;

// A long Settings session can add many sources; the list keeps only the most
// recent results so it stays a readable summary of what just happened rather
// than an ever-growing log.
const MAX_NOTICES = 6;

// How many sources are read and previewed at once. Selecting a folder's worth
// of files used to start every read and every preview simultaneously, which
// hands the server a burst it has no reason to receive and holds every file's
// text in memory at the same time. Three keeps a multi-file add feeling
// immediate while the rest wait their turn.
const MAX_CONCURRENT_INTAKE = 3;

// How many unanswered speaker questions may be held. Each one retains the full
// text of its source until it is answered or dismissed, and a reader can only
// answer them one at a time anyway — a queue longer than this is memory held
// against a question nobody is going to reach soon. Further conversational
// files are declined with a notice rather than silently dropped.
const MAX_PENDING_ASKS = 5;

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
  const [queued, setQueued] = useState(0);
  const mounted = useRef(true);
  // The work still waiting for a slot, and how many slots are taken. Both live
  // in refs because the queue is driven from promise callbacks: reading them
  // from state would read whatever the closure captured when the intake
  // started, not what is true when it finishes.
  const pending = useRef<(() => Promise<void>)[]>([]);
  const running = useRef(0);
  // The pending questions as they stand RIGHT NOW. Several previews can answer
  // between two renders, so a count read off `asks` would be the same stale
  // number for all of them and the cap below would never bite. Every change
  // goes through this ref and `setAsks` together; it is never re-synced from
  // the rendered value, which would undo changes made since that render.
  const asksRef = useRef<readonly SpeakerAsk[]>(asks);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      // Work that never got a slot is abandoned with the card: its results
      // would have nowhere to go.
      pending.current = [];
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
        case "speaker-needed": {
          // A re-upload of the same writing supersedes its pending question;
          // the ingest is idempotent on source_ref server-side.
          const supersedes = asksRef.current.some(
            (ask) => ask.ref === outcome.ref,
          );
          // Each unanswered question holds its source's full text, and they
          // are answered one at a time. Past the cap the file is declined
          // outright rather than queued into memory nobody will reach.
          if (!supersedes && asksRef.current.length >= MAX_PENDING_ASKS) {
            note({
              ref: outcome.ref,
              label: outcome.label,
              tone: "warn",
              kind: "askQueueFull",
            });
            return;
          }
          // The ref is advanced HERE, not only on the next render: several
          // previews can answer before React re-renders once, and each would
          // otherwise read the same stale length and queue past the cap.
          asksRef.current = [
            ...asksRef.current.filter((ask) => ask.ref !== outcome.ref),
            {
              ref: outcome.ref,
              label: outcome.label,
              content: outcome.content,
              preview: outcome.preview,
            },
          ];
          setAsks(asksRef.current);
          return;
        }
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
      const work = async (): Promise<void> => {
        setInFlight((count) => count + 1);
        try {
          applyOutcome(await start());
        } catch (err: unknown) {
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
        } finally {
          if (mounted.current) {
            setInFlight((count) => count - 1);
          }
        }
      };

      // Take a slot if one is free, otherwise wait for one. `pump` is what
      // hands the slot on, so it runs whether the work succeeded or failed —
      // a source that could not be read must not strand the queue behind it.
      const pump = () => {
        const next = pending.current.shift();
        if (next === undefined) {
          running.current -= 1;
          return;
        }
        setQueued(pending.current.length);
        void next().finally(pump);
      };

      if (running.current < MAX_CONCURRENT_INTAKE) {
        running.current += 1;
        void work().finally(pump);
        return;
      }
      pending.current.push(work);
      setQueued(pending.current.length);
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

  // Settling a question frees one of the capped slots, so the ref moves with
  // the state — otherwise answering questions would never restore capacity
  // and every later conversational file would be declined.
  const settleAsk = useCallback((ref: string) => {
    asksRef.current = asksRef.current.filter((ask) => ask.ref !== ref);
    setAsks(asksRef.current);
  }, []);

  const answerSpeaker = useCallback(
    (speakerLabel: string) => {
      const ask = asks[0];
      if (ask === undefined) {
        return;
      }
      settleAsk(ask.ref);
      runIntake(ask.label, () =>
        intakeTranscript(ask.ref, ask.label, ask.content, speakerLabel),
      );
    },
    [asks, runIntake, settleAsk],
  );

  // Declining to attribute a transcript drops it: none of it can be proven to
  // be the owner's own words, so ingesting it anyway is what corrupts a voice.
  const dismissAsk = useCallback(() => {
    const ask = asks[0];
    if (ask === undefined) {
      return;
    }
    settleAsk(ask.ref);
    note({
      ref: ask.ref,
      label: ask.label,
      tone: "warn",
      kind: "dismissed",
    });
  }, [asks, note, settleAsk]);

  return {
    addFiles,
    addPaste,
    pendingAsk,
    answerSpeaker,
    dismissAsk,
    notices,
    /** True while any preview, ingest, or unanswered speaker question is open
     * — a build started now would misrepresent what the voice is made of. */
    busy: inFlight > 0 || queued > 0 || asks.length > 0,
    /** The profile the caller was rendered for; the core mints one on the
     * first add when this is null. */
    profileId,
  };
}
