import type { Dispatch } from "react";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../../api/client";
import type { components } from "../../api/schema";
import type { MessageKey } from "../../i18n/en";
import { problemMessage } from "../common";
import { ACCEPTED_CORPUS_FILE, TRANSCRIPT_EXT } from "../onboarding";
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
type IngestRequest = components["schemas"]["IngestVoiceCorpusSourceRequest"];

// A .txt is treated as a transcript when the preview attributes at least
// this share of its spoken words to labelled speakers.
const ATTRIBUTED_SHARE = 0.8;

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
}>;

const refusalKeys: Record<string, MessageKey> = {
  unattributed_transcript: "ob.conv.voice.refusalUnattributed",
  speaker_label_required: "ob.conv.voice.refusalUnattributed",
  speaker_not_found: "ob.conv.voice.refusalSpeaker",
  unsupported_format: "ob.conv.voice.refusalUnsupported",
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

// Every stable machine code an RFC 7807 body carries: the top-level `code`
// plus any per-field `details.errors[].code`.
function problemCodes(problem: unknown): string[] {
  if (!isRecord(problem)) {
    return [];
  }
  const codes: string[] = [];
  if (typeof problem.code === "string") {
    codes.push(problem.code);
  }
  const details = problem.details;
  if (isRecord(details) && Array.isArray(details.errors)) {
    for (const raw of details.errors) {
      if (isRecord(raw) && typeof raw.code === "string") {
        codes.push(raw.code);
      }
    }
  }
  return codes;
}

// The 422's stable machine code (top-level or per-field in details.errors)
// picks the honest refusal line; an unknown code falls back to the server's
// safe detail.
function refusalEntry(
  ref: string,
  problem: unknown,
): Extract<ConversationEvent, { type: "NARRATION" }>["entry"] {
  const known = problemCodes(problem).find(
    (code) => refusalKeys[code] !== undefined,
  );
  if (known !== undefined) {
    return {
      kind: "narration",
      id: `refuse:${ref}`,
      i18nKey: refusalKeys[known],
    };
  }
  return {
    kind: "narration",
    id: `refuse:${ref}`,
    i18nKey: "ob.conv.voice.ingestFailed",
    params: { detail: problemMessage(problem) },
  };
}

// What one previewed file honestly IS: a conversational source needs the
// speaker answer first; a transcript-shaped file nobody is attributable in
// is refused whole (none of it can be proven the owner's own words); and
// single-author prose ingests directly.
function routePreview(
  name: string,
  preview: CorpusPreview,
): "ask-speaker" | "refuse" | "document" {
  const attributedWords = preview.speakers.reduce(
    (sum, speaker) => sum + speaker.words,
    0,
  );
  const conversational =
    TRANSCRIPT_EXT.test(name) ||
    attributedWords >= preview.total_words * ATTRIBUTED_SHARE;
  if (conversational && preview.ingestible_as_transcript) {
    return "ask-speaker";
  }
  return TRANSCRIPT_EXT.test(name) ? "refuse" : "document";
}

export function useVoiceCorpus({ state, dispatch }: UseVoiceCorpusArgs) {
  const [summary, setSummary] = useState<CorpusSummary | null>(null);
  const [manifest, setManifest] = useState<readonly CorpusManifestEntry[]>([]);
  const [asks, setAsks] = useState<readonly SpeakerAsk[]>([]);
  const [probesInFlight, setProbesInFlight] = useState(0);
  const summaryRef = useRef<CorpusSummary | null>(null);
  const pasteSeq = useRef(0);
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

  // Parallel uploads share ONE profile resolution: concurrent creates would
  // race into the server's one-live-profile conflict. A failed resolution
  // clears the slot so the next intake retries instead of inheriting a dead
  // promise.
  const profileIdInFlight = useRef<Promise<string> | null>(null);
  const sharedProfileId = useCallback((): Promise<string> => {
    profileIdInFlight.current ??= ensureProfileId().catch((err: unknown) => {
      profileIdInFlight.current = null;
      throw err;
    });
    return profileIdInFlight.current;
  }, []);

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

  const ingest = useCallback(
    async (
      body: IngestRequest,
      transcript: boolean,
      reactionKey: MessageKey,
    ): Promise<void> => {
      ingestSeq.current += 1;
      const seq = ingestSeq.current;
      const profileId = await sharedProfileId();
      const { data, error } = await api.POST("/voice-profiles/{id}/sources", {
        params: { path: { id: profileId } },
        body,
      });
      if (error) {
        if (mounted.current) {
          dispatch({
            type: "NARRATION",
            entry: refusalEntry(body.source_ref, error),
          });
        }
        return;
      }
      recordIngest(
        seq,
        {
          ref: body.source_ref,
          label: body.source_label,
          keptWords: data.ingest_stats.kept_words,
          inputWords: data.ingest_stats.input_words,
          transcript,
        },
        data.ingest_stats,
        data.summary,
        reactionKey,
      );
    },
    [dispatch, recordIngest, sharedProfileId],
  );

  // Preview one accepted file and act on what it honestly IS (routePreview);
  // the empty pre-gate is the only client-side counting anywhere.
  const classifyUpload = useCallback(
    async (name: string, text: string): Promise<void> => {
      const ref = `onboarding:upload:${name}`;
      if (text.split(/\s+/).filter(Boolean).length === 0) {
        say(`skip:${name}`, "ob.conv.voice.fileEmpty", { name });
        return;
      }
      const profileId = await sharedProfileId();
      const { data, error } = await api.POST(
        "/voice-profiles/{id}/sources/preview",
        {
          params: { path: { id: profileId } },
          body: { format: "transcript", content: text },
        },
      );
      if (error) {
        if (mounted.current) {
          dispatch({ type: "NARRATION", entry: refusalEntry(ref, error) });
        }
        return;
      }
      if (!mounted.current) {
        return;
      }
      const route = routePreview(name, data);
      if (route === "ask-speaker") {
        // A re-upload under the same name supersedes its pending question;
        // the ingest itself is idempotent on source_ref server-side.
        setAsks((prev) => [
          ...prev.filter((ask) => ask.ref !== ref),
          { ref, label: name, content: text, preview: data },
        ]);
        return;
      }
      if (route === "refuse") {
        say(`refuse:${ref}`, "ob.conv.voice.refusalUnattributed");
        return;
      }
      await ingest(
        {
          kind: "document",
          register: "general",
          weight: 1,
          source_label: name,
          source_ref: ref,
          format: "text",
          speaker_label: null,
          content: text,
        },
        false,
        "ob.conv.voice.reactionDocument",
      );
    },
    [dispatch, ingest, say, sharedProfileId],
  );

  // One intake for all three entry paths: the attach button, a drop onto the
  // thread, and (via addPaste) the composer. V1 corpus is text only;
  // anything else is refused by name.
  const addFiles = useCallback(
    (files: readonly File[]) => {
      for (const file of files) {
        if (!ACCEPTED_CORPUS_FILE.test(file.name)) {
          say(`skip:${file.name}`, "ob.conv.voice.fileSkipped", {
            name: file.name,
          });
          continue;
        }
        dispatch({
          type: "UPLOAD_ADDED",
          id: `onboarding:upload:${file.name}`,
          name: file.name,
        });
        setProbesInFlight((count) => count + 1);
        file
          .text()
          .then((text) => classifyUpload(file.name, text))
          .catch((err: unknown) => {
            if (mounted.current) {
              say(
                `refuse:onboarding:upload:${file.name}`,
                "ob.conv.voice.ingestFailed",
                {
                  detail: err instanceof Error ? err.message : String(err),
                },
              );
            }
          })
          .finally(() => {
            if (mounted.current) {
              setProbesInFlight((count) => count - 1);
            }
          });
      }
    },
    [classifyUpload, dispatch, say],
  );

  const addPaste = useCallback(
    (text: string, label: string) => {
      pasteSeq.current += 1;
      const ref = `onboarding:paste:${pasteSeq.current}`;
      dispatch({ type: "UPLOAD_ADDED", id: ref, name: label });
      setProbesInFlight((count) => count + 1);
      void ingest(
        {
          kind: "other",
          register: "general",
          weight: 1,
          source_label: label,
          source_ref: ref,
          format: "text",
          speaker_label: null,
          content: text,
        },
        false,
        "ob.conv.voice.reactionDocument",
      )
        .catch((err: unknown) => {
          if (mounted.current) {
            say(`refuse:${ref}`, "ob.conv.voice.ingestFailed", {
              detail: err instanceof Error ? err.message : String(err),
            });
          }
        })
        .finally(() => {
          if (mounted.current) {
            setProbesInFlight((count) => count - 1);
          }
        });
    },
    [dispatch, ingest, say],
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
      setProbesInFlight((count) => count + 1);
      void ingest(
        {
          kind: "transcript",
          register: "spoken",
          weight: 1,
          source_label: ask.label,
          source_ref: ask.ref,
          format: "transcript",
          speaker_label: value,
          content: ask.content,
        },
        true,
        "ob.conv.voice.reactionTranscript",
      )
        .catch((err: unknown) => {
          if (mounted.current) {
            say(`refuse:${ask.ref}`, "ob.conv.voice.ingestFailed", {
              detail: err instanceof Error ? err.message : String(err),
            });
          }
        })
        .finally(() => {
          if (mounted.current) {
            setProbesInFlight((count) => count - 1);
          }
        });
    },
    [asks, ingest, say],
  );

  return {
    summary,
    manifest,
    addFiles,
    addPaste,
    answerSpeaker,
    sharedProfileId,
    /** True while any probe, ingest, or speaker question is still open —
     * a build starting now would misrepresent what the voice is made of. */
    busy: probesInFlight > 0 || asks.length > 0,
  };
}

// Reuse the owner's single profile (the list caps at one) or mint it.
async function ensureProfileId(): Promise<string> {
  const list = await api.GET("/voice-profiles");
  if (list.error) {
    throw new Error(problemMessage(list.error));
  }
  const existing = list.data.data[0]?.id;
  if (existing) {
    return existing;
  }
  const created = await api.POST("/voice-profiles", {
    body: { personality_md: "" },
  });
  if (created.error) {
    throw new Error(problemMessage(created.error));
  }
  return created.data.id;
}
