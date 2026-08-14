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

// source_ref is the contract's stable natural key and the server upserts on it
// (ON CONFLICT (voice_profile_id, source_ref)), so two sources that share a key
// become ONE row and the later one silently replaces the earlier.
//
// The key is therefore the name AND the content together. Content alone is
// wrong: dropping several files that happen to hold the same text — three
// exported drafts from one template, or copies of a sample under different
// names — collapsed them into a single row, so a multi-file drop looked like
// it had taken only the first. Name alone is wrong the other way: two
// different files both called "meeting.txt" would overwrite each other.
//
// Together they say what a reader means: re-adding the same file updates the
// one row it already made, and anything that differs in either name or content
// keeps its own.
const SOURCE_REF_MAX = 512;
const SOURCE_LABEL_MAX = 255;

function clamp(value: string, max: number): string {
  return value.length <= max ? value : value.slice(0, max);
}

// FNV-1a over the content: short, stable, and dependency-free. This is an
// identity for rows the same person is adding to their own corpus, never a
// security boundary — a collision costs one overwritten sample, and the
// server's own uniqueness rules still apply on top.
function contentKey(content: string): string {
  let hash = 0x811c9dc5;
  for (let i = 0; i < content.length; i++) {
    hash ^= content.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return `${hash.toString(16).padStart(8, "0")}-${content.length}`;
}

/** The key one source is known by: what it is called and what it says. The
 * label is hashed rather than embedded so a long filename cannot push the
 * content half of the key past the contract's 512-character cap. */
export function sourceRef(
  kind: "upload" | "paste",
  label: string,
  content: string,
): string {
  return clamp(
    `voice:${kind}:${contentKey(label)}:${contentKey(content)}`,
    SOURCE_REF_MAX,
  );
}

// What one previewed source honestly IS.
//
// `ingestible_as_transcript` is the SERVER's answer to "does this attribute
// turns to named speakers?", and it is the only authority here. A client-side
// share threshold used to gate it, which meant a .txt whose dialogue was 68%
// attributed — the rest narration — was ingested as single-author prose with
// the counterparty's words counted as the owner's own. Whether the reader
// happened to name the file .vtt is not evidence about its contents.
//
// So: anything the server says carries speakers needs the speaker answer
// first. A transcript-shaped file it CANNOT attribute is refused whole (none
// of it can be proven the owner's own words). Everything else is prose.
export function routePreview(
  name: string,
  preview: CorpusPreview,
): "ask-speaker" | "refuse" | "document" {
  if (preview.ingestible_as_transcript) {
    return "ask-speaker";
  }
  // A source with named speakers the server could not make ingestible is not
  // prose either: ingesting it would credit every speaker to the owner. The
  // speakers list is read defensively because being WRONG here means
  // misattributing someone's words — an older or partial response must fall
  // back to refusing a transcript-shaped file, never to ingesting it.
  const speakers = preview.speakers ?? [];
  if (TRANSCRIPT_EXT.test(name) || speakers.length > 0) {
    return "refuse";
  }
  return "document";
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
  onIssue?: () => void,
): Promise<IntakeOutcome> {
  const profileId = await ensureProfileId();
  // Called at the moment this write is issued, so a caller ordering concurrent
  // ingests stamps them by when the SERVER was asked — not by when the reader
  // picked the file, which a slow read or preview would distort.
  onIssue?.();
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

// Every source is previewed before anything is written, whether it arrived as
// a file or as pasted text. Pasting used to skip this, which left the whole
// corruption intact behind the paste box: paste a meeting transcript and every
// speaker's words were credited to the owner. The two entry points differ only
// in which body prose is sent under, so they share one path.
async function intakePreviewed(
  ref: string,
  label: string,
  text: string,
  proseBody: (ref: string, label: string, content: string) => IngestRequest,
  onIssue?: () => void,
): Promise<IntakeOutcome> {
  // The empty pre-gate is the only client-side word counting anywhere; every
  // count a surface displays is a server number.
  if (text.split(/\s+/).filter(Boolean).length === 0) {
    return { kind: "skipped", ref, label, reason: "empty" };
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
      label,
      reason: refusalOf(error),
      problem: error,
    };
  }
  const route = routePreview(label, data);
  if (route === "ask-speaker") {
    return { kind: "speaker-needed", ref, label, content: text, preview: data };
  }
  if (route === "refuse") {
    return {
      kind: "refused",
      ref,
      label,
      reason: "unattributed",
      problem: null,
    };
  }
  return ingest(proseBody(ref, label, text), label, false, onIssue);
}

/** A file the owner handed over: previewed, then ingested as prose or held for
 * the speaker question. */
export function intakeUpload(
  ref: string,
  name: string,
  text: string,
  onIssue?: () => void,
): Promise<IntakeOutcome> {
  return intakePreviewed(ref, name, text, documentBody, onIssue);
}

/** Text the owner pasted. Previewed exactly like a file — a transcript is a
 * transcript however it reached the box. */
export function intakePaste(
  ref: string,
  label: string,
  content: string,
  onIssue?: () => void,
): Promise<IntakeOutcome> {
  return intakePreviewed(ref, label, content, pasteBody, onIssue);
}

/** The owner named themselves in a conversational source: ingest with the
 * speaker filter, so only that speaker's server-counted words reach the
 * corpus. */
export function intakeTranscript(
  ref: string,
  label: string,
  content: string,
  speakerLabel: string,
  onIssue?: () => void,
): Promise<IntakeOutcome> {
  return ingest(
    transcriptBody(ref, label, content, speakerLabel),
    label,
    true,
    onIssue,
  );
}

/** Whether a filename is one the corpus accepts at all. */
export function isAcceptedCorpusFile(name: string): boolean {
  return ACCEPTED_CORPUS_FILE.test(name);
}
