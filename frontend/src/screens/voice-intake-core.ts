import { api } from "../api/client";
import type { components } from "../api/schema";
import { ensureProfileId } from "./voice-profile";

// The voice-corpus intake, spelled once for both surfaces that collect writing
// samples: the onboarding voice act and the Settings Voice DNA card. What lives
// here is everything that is TRUE of an intake regardless of who is asking —
// what a file honestly is, which request body says so, and what the server
// answered. Nothing here renders or knows about a conversation machine; each
// surface adapts the outcomes below into its own vocabulary (narration events
// there, notices and query invalidation here).
//
// Two surfaces previously carried their own copies of half of this. The copies
// disagreed: Settings never previewed an upload at all, so a meeting transcript
// was ingested with every speaker's words counted as the owner's own.

type CorpusPreview = components["schemas"]["VoiceCorpusPreviewResult"];
type CorpusSummary = components["schemas"]["VoiceCorpusSummary"];
type IngestStats = components["schemas"]["VoiceIngestStats"];
type IngestRequest = components["schemas"]["IngestVoiceCorpusSourceRequest"];

// The file extensions the corpus accepts, and the subset that is conversational
// by name. These are a CLIENT-side accept list, not the wire enum: the contract
// carries `format: text | transcript`, and the concrete detected format appears
// only in a preview result.
export const ACCEPTED_CORPUS_FILE = /\.(txt|md|vtt|srt|json)$/i;
export const ACCEPTED_CORPUS_ATTR = ".txt,.md,.vtt,.srt,.json";
export const TRANSCRIPT_EXT = /\.(vtt|srt|json)$/i;

// 800 mirrors the server's build floor ("at least 800 eligible own-authored
// words"): gating the build action turns that 422 into a clear, up-front ask.
export const VOICE_MIN_WORDS = 800;

// A .txt is treated as a transcript when the preview attributes at least this
// share of its spoken words to labelled speakers.
const ATTRIBUTED_SHARE = 0.8;

// source_ref is the contract's stable natural key: the ingest is idempotent on
// it, so retrying an upload that failed halfway updates the one source rather
// than adding a second copy. It is capped at 512 and source_label at 255, and a
// long filename is truncated rather than refused by the server for length.
const SOURCE_REF_MAX = 512;
const SOURCE_LABEL_MAX = 255;

function clamp(value: string, max: number): string {
  return value.length <= max ? value : value.slice(0, max);
}

export function uploadRef(surface: string, name: string): string {
  return clamp(`${surface}:upload:${name}`, SOURCE_REF_MAX);
}

export function pasteRef(surface: string, seq: number): string {
  return clamp(`${surface}:paste:${seq}`, SOURCE_REF_MAX);
}

// What one previewed file honestly IS: a conversational source needs the
// speaker answer first; a transcript-shaped file nobody is attributable in is
// refused whole (none of it can be proven the owner's own words); and
// single-author prose ingests directly.
export function routePreview(
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

/** Why the server would not take a source, as a category both surfaces phrase
 * in their own words. `null` means the refusal carried no code either surface
 * knows — the caller states the server's own detail instead of inventing one. */
export type RefusalReason = "unattributed" | "speaker" | "unsupported";

const refusalReasons: Record<string, RefusalReason> = {
  unattributed_transcript: "unattributed",
  speaker_label_required: "unattributed",
  speaker_not_found: "speaker",
  unsupported_format: "unsupported",
};

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

// Every stable machine code an RFC 7807 body carries: the top-level `code` plus
// any per-field `details.errors[].code`.
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

// The refusal category a 422 names, or null when no code here recognizes it.
export function refusalOf(problem: unknown): RefusalReason | null {
  const known = problemCodes(problem).find(
    (code) => refusalReasons[code] !== undefined,
  );
  return known === undefined ? null : refusalReasons[known];
}

/** What one intake attempt ended as. Every path a source can take terminates in
 * exactly one of these — there is no silent outcome. */
export type IntakeOutcome =
  | Readonly<{
      kind: "ingested";
      ref: string;
      label: string;
      stats: IngestStats;
      summary: CorpusSummary;
      /** Kept-of-total is worth showing only where filtering discarded turns. */
      transcript: boolean;
    }>
  | Readonly<{
      kind: "speaker-needed";
      ref: string;
      label: string;
      content: string;
      preview: CorpusPreview;
    }>
  | Readonly<{
      kind: "refused";
      ref: string;
      label: string;
      /** null when the server's code is unknown here; `problem` carries it. */
      reason: RefusalReason | null;
      problem: unknown;
    }>
  | Readonly<{
      kind: "skipped";
      ref: string;
      label: string;
      reason: "type" | "empty";
    }>;

// The three request bodies, built in one place so both surfaces send the same
// shape. The server infers nothing from a filename: a transcript that omits
// `format: "transcript"` is read as prose and refused as unattributed.
function documentBody(
  ref: string,
  label: string,
  content: string,
): IngestRequest {
  return {
    kind: "document",
    register: "general",
    weight: 1,
    source_label: clamp(label, SOURCE_LABEL_MAX),
    source_ref: ref,
    format: "text",
    speaker_label: null,
    content,
  };
}

function pasteBody(ref: string, label: string, content: string): IngestRequest {
  return {
    kind: "other",
    register: "general",
    weight: 1,
    source_label: clamp(label, SOURCE_LABEL_MAX),
    source_ref: ref,
    format: "text",
    speaker_label: null,
    content,
  };
}

function transcriptBody(
  ref: string,
  label: string,
  content: string,
  speakerLabel: string,
): IngestRequest {
  return {
    kind: "transcript",
    register: "spoken",
    weight: 1,
    source_label: clamp(label, SOURCE_LABEL_MAX),
    source_ref: ref,
    format: "transcript",
    speaker_label: speakerLabel,
    content,
  };
}

// One ingest call: the profile is resolved through the shared ensureProfileId,
// so a first sample from either surface mints the one profile rather than a
// second beside it. A server refusal RESOLVES as a "refused" outcome; only a
// client-side fault (a broken fetch) rejects.
async function ingest(
  body: IngestRequest,
  label: string,
  transcript: boolean,
): Promise<IntakeOutcome> {
  const profileId = await ensureProfileId();
  const { data, error } = await api.POST("/voice-profiles/{id}/sources", {
    params: { path: { id: profileId } },
    body,
  });
  if (error) {
    return {
      kind: "refused",
      ref: body.source_ref,
      label,
      reason: refusalOf(error),
      problem: error,
    };
  }
  return {
    kind: "ingested",
    ref: body.source_ref,
    label,
    stats: data.ingest_stats,
    summary: data.summary,
    transcript,
  };
}

/** Preview one accepted file and act on what it honestly is. The empty
 * pre-gate is the only client-side word counting anywhere; every count a
 * surface displays is a server number. */
export async function intakeUpload(
  ref: string,
  name: string,
  text: string,
): Promise<IntakeOutcome> {
  if (text.split(/\s+/).filter(Boolean).length === 0) {
    return { kind: "skipped", ref, label: name, reason: "empty" };
  }
  const profileId = await ensureProfileId();
  const { data, error } = await api.POST(
    "/voice-profiles/{id}/sources/preview",
    {
      params: { path: { id: profileId } },
      body: { format: "transcript", content: text },
    },
  );
  if (error) {
    return {
      kind: "refused",
      ref,
      label: name,
      reason: refusalOf(error),
      problem: error,
    };
  }
  const route = routePreview(name, data);
  if (route === "ask-speaker") {
    return {
      kind: "speaker-needed",
      ref,
      label: name,
      content: text,
      preview: data,
    };
  }
  if (route === "refuse") {
    return {
      kind: "refused",
      ref,
      label: name,
      reason: "unattributed",
      problem: null,
    };
  }
  return ingest(documentBody(ref, name, text), name, false);
}

/** The owner named themselves in a conversational source: ingest with the
 * speaker filter, so only that speaker's server-counted words reach the
 * corpus. */
export function intakeTranscript(
  ref: string,
  label: string,
  content: string,
  speakerLabel: string,
): Promise<IntakeOutcome> {
  return ingest(transcriptBody(ref, label, content, speakerLabel), label, true);
}

/** Pasted prose. It is not previewed: the owner typed or pasted it as their
 * own writing, and there is no filename to disagree with. */
export function intakePaste(
  ref: string,
  label: string,
  content: string,
): Promise<IntakeOutcome> {
  return ingest(pasteBody(ref, label, content), label, false);
}

/** Whether a filename is one the corpus accepts at all. */
export function isAcceptedCorpusFile(name: string): boolean {
  return ACCEPTED_CORPUS_FILE.test(name);
}
