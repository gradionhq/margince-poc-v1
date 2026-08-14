/** @vitest-environment jsdom */
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import {
  intakePaste,
  intakeTranscript,
  intakeUpload,
  isAcceptedCorpusFile,
  pasteRef,
  refusalOf,
  routePreview,
  uploadRef,
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

describe("what a previewed file honestly is", () => {
  it("asks who is speaking when a transcript-named file is attributable", () => {
    expect(
      routePreview(
        "standup.vtt",
        preview({ ingestible_as_transcript: true, speakers: [] }),
      ),
    ).toBe("ask-speaker");
  });

  it("asks who is speaking when prose is mostly attributed dialogue", () => {
    // Above the 0.8 attributed share a .txt is a conversation whatever its
    // extension claims.
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

  it("refuses a transcript nobody can be attributed in", () => {
    // Nothing in it can be proven the owner's own words, and a transcript is
    // refused whole rather than ingested as if one person wrote it.
    expect(
      routePreview("meeting.srt", preview({ ingestible_as_transcript: false })),
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
    const outcome = await intakeUpload(
      uploadRef("settings", "letter.txt"),
      "letter.txt",
      "Short sentences. Concrete nouns.",
    );
    expect(outcome.kind).toBe("ingested");
    expect(bodies).toHaveLength(1);
    expect(bodies[0]).toMatchObject({
      kind: "document",
      register: "general",
      format: "text",
      speaker_label: null,
      source_ref: "settings:upload:letter.txt",
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

  it("sends pasted writing as prose the owner claimed as their own", async () => {
    const bodies = stubApi(preview());
    await intakePaste(pasteRef("settings", 1), "Pasted writing", "Some words.");
    expect(bodies[0]).toMatchObject({
      kind: "other",
      register: "general",
      format: "text",
      source_ref: "settings:paste:1",
    });
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

describe("source_ref, the key the ingest is idempotent on", () => {
  it("names the surface and the file, so a retry updates one source", () => {
    expect(uploadRef("settings", "letter.txt")).toBe(
      "settings:upload:letter.txt",
    );
    expect(uploadRef("settings", "letter.txt")).toBe(
      uploadRef("settings", "letter.txt"),
    );
  });

  it("stays inside the contract's 512-character cap", () => {
    expect(uploadRef("settings", "x".repeat(900)).length).toBeLessThanOrEqual(
      512,
    );
  });
});
