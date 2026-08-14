/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import {
  intakePaste,
  intakeTranscript,
  intakeUpload,
  isAcceptedCorpusFile,
  refusalOf,
  routePreview,
  sourceRef,
} from "./voice-intake-core";

// The intake core is what both surfaces that collect writing samples run on.
// What it decides — what a file honestly is, which body says so, and what the
// server answered — is the same question on the onboarding act and in
// Settings, so it is proven once here rather than twice through two UIs.

type CorpusPreview = components["schemas"]["VoiceCorpusPreviewResult"];

// A preview with nothing attributed to anyone by default; a case that cares
// about attribution passes its own speakers and the words left over.
function preview(over: Partial<CorpusPreview> = {}): CorpusPreview {
  return {
    total_words: 1000,
    detected_format: "txt",
    ingestible_as_transcript: false,
    unattributed_words: 1000,
    speakers: [],
    ...over,
  };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const SUMMARY: components["schemas"]["VoiceCorpusSummary"] = {
  total_words: 900,
  target_words: 30000,
  maturity: "provisional",
  quality_band: "thin",
  source_count: 1,
  register_words: { general: 900 },
};

const STATS: components["schemas"]["VoiceIngestStats"] = {
  input_words: 1000,
  kept_words: 900,
  kept_turns: 4,
  discarded_turns: 6,
  speakers_seen: ["Lars", "Sam"],
};

// The server as the core actually meets it: one profile already exists, the
// preview answers what the test asked for, and every ingest body is recorded
// so the request SHAPE can be asserted rather than assumed.
function stubApi(
  previewResult: CorpusPreview | { status: number; body: unknown },
) {
  const bodies: Record<string, unknown>[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const path = new URL(request.url).pathname.replace(/^\/v1/, "");
      if (path === "/voice-profiles") {
        return jsonResponse({
          data: [{ id: "vp-1" }],
          page: { next_cursor: null, has_more: false },
        });
      }
      if (path === "/voice-profiles/vp-1/sources/preview") {
        if ("status" in previewResult) {
          return jsonResponse(previewResult.body, previewResult.status);
        }
        return jsonResponse(previewResult);
      }
      if (path === "/voice-profiles/vp-1/sources") {
        bodies.push(JSON.parse(await request.text()));
        return jsonResponse(
          { source: {}, summary: SUMMARY, ingest_stats: STATS },
          201,
        );
      }
      return jsonResponse({});
    }),
  );
  return bodies;
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("what a previewed source honestly is", () => {
  it("asks who is speaking when a transcript-named file is attributable", () => {
    expect(
      routePreview(
        "standup.vtt",
        preview({ ingestible_as_transcript: true, speakers: [] }),
      ),
    ).toBe("ask-speaker");
  });

  // The server's verdict decides, not the file extension: a .txt that carries
  // dialogue is a conversation however it was named.
  it("asks who is speaking when a .txt turns out to carry dialogue", () => {
    expect(
      routePreview(
        "notes.txt",
        preview({
          ingestible_as_transcript: true,
          unattributed_words: 100,
          speakers: [
            { label: "Lars", words: 500, turns: 10 },
            { label: "Sam", words: 400, turns: 8 },
          ],
        }),
      ),
    ).toBe("ask-speaker");
  });

  // A .txt of 68% attributed dialogue plus narration used to fall under a
  // client-side share threshold and ingest as prose — crediting the
  // counterparty's words to the owner. The server already says it is
  // ingestible as a transcript, and that answer is the one that counts.
  it("asks who is speaking even when much of the source is unattributed", () => {
    expect(
      routePreview(
        "mixed.txt",
        preview({
          total_words: 888,
          ingestible_as_transcript: true,
          unattributed_words: 288,
          speakers: [
            { label: "Lars", words: 325, turns: 25 },
            { label: "Sam", words: 275, turns: 25 },
          ],
        }),
      ),
    ).toBe("ask-speaker");
  });

  it("refuses a transcript nobody can be attributed in", () => {
    // Nothing in it can be proven the owner's own words, and a transcript is
    // refused whole rather than ingested as if one person wrote it.
    expect(
      routePreview("meeting.srt", preview({ ingestible_as_transcript: false })),
    ).toBe("refuse");
  });

  // Named speakers the server could not make ingestible are still speakers:
  // taking the file as prose would credit all of them to the owner.
  it("refuses a source with speakers it cannot attribute, whatever its name", () => {
    expect(
      routePreview(
        "notes.txt",
        preview({
          ingestible_as_transcript: false,
          speakers: [{ label: "Sam", words: 400, turns: 8 }],
        }),
      ),
    ).toBe("refuse");
  });

  it("takes single-author prose as a document", () => {
    expect(routePreview("letter.txt", preview())).toBe("document");
  });
});

describe("which refusal the server named", () => {
  it("reads the top-level code", () => {
    expect(refusalOf({ code: "unattributed_transcript" })).toBe("unattributed");
    expect(refusalOf({ code: "speaker_not_found" })).toBe("speaker");
    expect(refusalOf({ code: "unsupported_format" })).toBe("unsupported");
  });

  it("reads a per-field code out of details.errors", () => {
    expect(
      refusalOf({
        code: "validation_failed",
        details: { errors: [{ code: "speaker_label_required" }] },
      }),
    ).toBe("unattributed");
  });

  // An unknown code must not vanish into a category it does not belong to:
  // null is what makes the caller quote the server's own detail instead.
  it("returns null for a code it does not know", () => {
    expect(refusalOf({ code: "something_new" })).toBeNull();
    expect(refusalOf(null)).toBeNull();
    expect(refusalOf("not a problem document")).toBeNull();
  });
});

describe("the request bodies the server actually receives", () => {
  it("sends single-author prose as a text document", async () => {
    const bodies = stubApi(preview());
    const text = "Short sentences. Concrete nouns.";
    const outcome = await intakeUpload(
      sourceRef("upload", "letter.txt", text),
      "letter.txt",
      text,
    );
    expect(outcome.kind).toBe("ingested");
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({
      kind: "document",
      register: "general",
      format: "text",
      speaker_label: null,
      source_label: "letter.txt",
    });
  });

  // The server infers nothing from a filename: a transcript that omits
  // format:"transcript" is read as prose and refused as unattributed, so the
  // format and the chosen speaker are asserted together.
  it("sends an attributed transcript as a transcript, with the speaker", async () => {
    const bodies = stubApi(preview());
    const outcome = await intakeTranscript(
      "settings:upload:standup.vtt",
      "standup.vtt",
      "Lars: we ship on Friday.",
      "Lars",
    );
    expect(outcome.kind).toBe("ingested");
    expect(bodies[0]).toMatchObject({
      kind: "transcript",
      register: "spoken",
      format: "transcript",
      speaker_label: "Lars",
    });
  });

  it("sends pasted prose as prose the owner claimed as their own", async () => {
    const bodies = stubApi(preview());
    const text = "Some words.";
    await intakePaste(
      sourceRef("paste", "Pasted writing", text),
      "Pasted writing",
      text,
    );
    expect(bodies[0]).toMatchObject({
      kind: "other",
      register: "general",
      format: "text",
    });
  });

  // Pasting used to skip the preview entirely, so a meeting transcript pasted
  // into the box was ingested as prose with every speaker credited to the
  // owner — the same corruption an uploaded file was protected from.
  it("previews pasted text too, so a pasted transcript still asks who is speaking", async () => {
    const bodies = stubApi(
      preview({
        ingestible_as_transcript: true,
        unattributed_words: 0,
        speakers: [
          { label: "Lars", words: 600, turns: 12 },
          { label: "Sam", words: 400, turns: 9 },
        ],
      }),
    );
    const outcome = await intakePaste(
      sourceRef("paste", "Pasted writing", "Lars: hi. Sam: hello."),
      "Pasted writing",
      "Lars: hi. Sam: hello.",
    );
    expect(outcome.kind).toBe("speaker-needed");
    expect(bodies).toHaveLength(0);
  });
});

describe("the outcomes an intake can end as", () => {
  it("reports an empty file as skipped, without asking the server", async () => {
    const bodies = stubApi(preview());
    const outcome = await intakeUpload("r", "empty.txt", "   \n  ");
    expect(outcome).toMatchObject({ kind: "skipped", reason: "empty" });
    expect(bodies).toHaveLength(0);
  });

  it("hands back a speaker question instead of ingesting", async () => {
    const bodies = stubApi(
      preview({
        ingestible_as_transcript: true,
        unattributed_words: 100,
        speakers: [{ label: "Lars", words: 900, turns: 12 }],
      }),
    );
    const outcome = await intakeUpload("r", "standup.vtt", "Lars: hello.");
    expect(outcome.kind).toBe("speaker-needed");
    // Nothing is ingested until the owner says which speaker is theirs.
    expect(bodies).toHaveLength(0);
  });

  it("resolves a server refusal as an outcome rather than throwing", async () => {
    stubApi({
      status: 422,
      body: { code: "unsupported_format", detail: "Cannot read that." },
    });
    const outcome = await intakeUpload("r", "notes.txt", "some words here");
    expect(outcome).toMatchObject({ kind: "refused", reason: "unsupported" });
  });

  it("carries the problem through when the refusal code is unknown", async () => {
    stubApi({
      status: 422,
      body: { code: "brand_new_rule", detail: "Nope." },
    });
    const outcome = await intakeUpload("r", "notes.txt", "some words here");
    expect(outcome).toMatchObject({ kind: "refused", reason: null });
    if (outcome.kind === "refused") {
      expect(outcome.problem).toBeTruthy();
    }
  });
});

describe("which files are offered to the server at all", () => {
  it("accepts the text formats the corpus can read", () => {
    for (const name of ["a.txt", "b.md", "c.vtt", "d.srt", "e.json"]) {
      expect(isAcceptedCorpusFile(name)).toBe(true);
    }
  });

  it("rejects everything else by name, before any upload", () => {
    for (const name of ["photo.png", "deck.pdf", "sheet.xlsx", "noext"]) {
      expect(isAcceptedCorpusFile(name)).toBe(false);
    }
  });
});

// The server upserts on source_ref, so any two sources sharing a key become
// ONE row and the later silently replaces the earlier. The key is the name and
// the content together: either half alone loses real sources.
describe("source_ref, the key the ingest is idempotent on", () => {
  it("is the same for the same file, so a retry updates one row", () => {
    expect(sourceRef("upload", "letter.txt", "the same words")).toBe(
      sourceRef("upload", "letter.txt", "the same words"),
    );
  });

  // The reported bug: dropping several files that happen to hold the same
  // text — copies of a sample, or exports from one template — collapsed into
  // a single row, so the drop looked like it had taken only the first.
  it("keeps differently-named files apart even when their text is identical", () => {
    const same = "The same words in every one of these files.";
    const refs = new Set([
      sourceRef("upload", "dup1.txt", same),
      sourceRef("upload", "dup2.txt", same),
      sourceRef("upload", "dup3.txt", same),
    ]);
    expect(refs.size).toBe(3);
  });

  it("differs for different writing under the same file name", () => {
    expect(sourceRef("upload", "meeting.txt", "one meeting")).not.toBe(
      sourceRef("upload", "meeting.txt", "a different meeting"),
    );
  });

  it("does not collide when only the length matches", () => {
    expect(sourceRef("upload", "a.txt", "abcd")).not.toBe(
      sourceRef("upload", "a.txt", "dcba"),
    );
  });

  it("stays inside the contract's 512-character cap", () => {
    expect(
      sourceRef("upload", "y".repeat(9000), "x".repeat(90000)).length,
    ).toBeLessThanOrEqual(512);
  });
});

// source_ref is PERSISTED. Two earlier spellings are already in installations
// that have been running, and the server upserts on an exact string match — so
// a source whose row predates the current format has to be re-added under the
// key it already has, or the ingest writes a second copy of the same writing
// beside the first and the corpus counts those words twice.
describe("source_ref, against rows written by earlier versions", () => {
  it("reuses the content-only key a row already carries", () => {
    // The earlier content-only spelling, `voice:upload:<fnv1a>-<length>` —
    // the shape a real installation's rows carry today. Re-adding that same
    // writing must update THAT row rather than start a second copy of it.
    const content = "the words of a transcript already in the corpus";
    const v2 = "voice:upload:a730dfa1-47";

    expect(sourceRef("upload", "transcript.txt", content, new Set([v2]))).toBe(
      v2,
    );
  });

  it("reuses the name-only key a row already carries", () => {
    // The oldest spellings: the surface plus the file name.
    for (const legacy of [
      "settings:upload:letter.txt",
      "onboarding:upload:letter.txt",
    ]) {
      expect(
        sourceRef("upload", "letter.txt", "words", new Set([legacy])),
      ).toBe(legacy);
    }
  });

  it("writes the current key when the profile holds nothing matching", () => {
    const current = sourceRef("upload", "letter.txt", "words");
    expect(
      sourceRef(
        "upload",
        "letter.txt",
        "words",
        new Set(["voice:upload:somebody-else:entirely"]),
      ),
    ).toBe(current);
  });

  it("writes the current key when the caller cannot say what exists", () => {
    // An empty set is "I looked and there is nothing", which is different from
    // passing nothing at all; both must land on the current format.
    const current = sourceRef("upload", "letter.txt", "words");
    expect(sourceRef("upload", "letter.txt", "words", new Set())).toBe(current);
    expect(sourceRef("upload", "letter.txt", "words")).toBe(current);
  });

  // A legacy key is only reused for the source it actually belongs to: the v1
  // format names a file, so a DIFFERENT file's row must not be claimed.
  it("does not claim a legacy row belonging to another file", () => {
    const other = "settings:upload:someone-elses.txt";
    expect(
      sourceRef("upload", "letter.txt", "words", new Set([other])),
    ).not.toBe(other);
  });
});
