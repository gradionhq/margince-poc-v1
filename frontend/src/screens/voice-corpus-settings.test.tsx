/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { VoiceCorpusIntake } from "./voice-corpus-settings";

// Building a Voice DNA from Settings has to reach the same bar the onboarding
// act reaches: a file can be handed over, and a file that turns out to be a
// conversation is not counted as the owner's own writing until they say which
// speaker they are. These tests are the ones that were missing while Settings
// could only accept pasted text.

type CorpusPreview = components["schemas"]["VoiceCorpusPreviewResult"];

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
  kept_words: 640,
  kept_turns: 4,
  discarded_turns: 6,
  speakers_seen: ["Lars", "Sam"],
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

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

function render(ui: ReactNode, client?: QueryClient) {
  return rtlRender(
    <QueryClientProvider
      client={
        client ??
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

// A client whose corpus manifest is already populated, the way the card's own
// query populates it before anyone drops a file.
function clientHolding(refs: readonly string[]): QueryClient {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  client.setQueryData(["voice-sources", "vp-1"], {
    sources: refs.map((source_ref, i) => ({
      id: `vs-${i}`,
      source_ref,
      source_label: "already here",
      word_count: 100,
      included: true,
      register: "general",
    })),
    summary: SUMMARY,
  });
  return client;
}

function fileOf(name: string, text: string): File {
  return new File([text], name, { type: "text/plain" });
}

// A file that reports when its contents are read. Holding a file's text is
// what the concurrency bound is protecting memory against, so the tests count
// reads directly rather than inferring them from request traffic.
function countingFileOf(name: string, text: string, onRead: () => void): File {
  const file = fileOf(name, text);
  return Object.assign(file, {
    text: async () => {
      onRead();
      return text;
    },
  });
}

// The hidden input the "Choose files" button clicks. userEvent.upload needs
// the input itself, which carries no accessible name by design.
function fileInput(): HTMLInputElement {
  const input = document.querySelector('input[type="file"]');
  if (!(input instanceof HTMLInputElement)) {
    throw new Error("the intake rendered no file input");
  }
  return input;
}

// The paste box lives in the dialog the row's verb opens, so a test whose
// subject is that box opens it first and scopes its queries to the dialog.
async function openPaste(): Promise<HTMLElement> {
  await userEvent.click(screen.getByRole("button", { name: "Paste writing" }));
  return screen.getByRole("dialog");
}

const PROSE: CorpusPreview = {
  total_words: 1000,
  detected_format: "txt",
  ingestible_as_transcript: false,
  unattributed_words: 1000,
  speakers: [],
};

const CONVERSATION: CorpusPreview = {
  total_words: 1000,
  detected_format: "vtt",
  ingestible_as_transcript: true,
  unattributed_words: 0,
  speakers: [
    { label: "Lars", words: 640, turns: 12 },
    { label: "Sam", words: 360, turns: 9 },
  ],
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("handing a file to the Settings voice card", () => {
  it("offers a file control at all — the thing paste-only Settings never had", async () => {
    stubApi(PROSE);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);
    expect(screen.getByRole("button", { name: /Choose files/ })).toBeTruthy();
    expect(fileInput().accept).toBe(".txt,.md,.vtt,.srt,.json");
  });

  // source_ref is persisted, and two earlier spellings are already in
  // installations that have been running. Re-adding a file that has a row
  // under an older key must UPDATE that row — writing the current key instead
  // would leave a duplicate sample and count its words twice.
  it("re-adds a file under the key its existing row already carries", async () => {
    const bodies = stubApi(PROSE);
    const text = "Short sentences. Concrete nouns.";
    // The earlier content-only spelling: no name half.
    const existing = "voice:upload:d506ebb7-32";
    render(
      <VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />,
      clientHolding([existing]),
    );

    await userEvent.upload(fileInput(), fileOf("letter.txt", text));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]?.source_ref).toBe(existing);
  });

  it("ingests single-author prose and reports what the server kept", async () => {
    const bodies = stubApi(PROSE);
    const onChanged = vi.fn();
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={onChanged} />);

    await userEvent.upload(
      fileInput(),
      fileOf("letter.txt", "Short sentences. Concrete nouns."),
    );

    expect(await screen.findByText(/kept 640 of 1,000 words/)).toBeTruthy();
    expect(bodies[0]).toMatchObject({ kind: "document", format: "text" });
    // The manifest on screen is the server's, so an ingest has to invalidate it.
    expect(onChanged).toHaveBeenCalled();
  });

  // This is the defect the card shipped with: a meeting transcript was posted
  // straight through as kind:"other", counting every speaker's words as the
  // owner's own. Nothing may reach the corpus before the question is answered.
  it("asks who is speaking before a conversation becomes the owner's voice", async () => {
    const bodies = stubApi(CONVERSATION);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    await userEvent.upload(
      fileInput(),
      fileOf("standup.vtt", "Lars: we ship Friday. Sam: agreed."),
    );

    expect(await screen.findByText(/Which speaker are you in/)).toBeTruthy();
    expect(screen.getByText(/Lars · 640 words, 12 turns/)).toBeTruthy();
    expect(bodies).toHaveLength(0);
  });

  it("ingests the chosen speaker's turns as an attributed transcript", async () => {
    const bodies = stubApi(CONVERSATION);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    await userEvent.upload(
      fileInput(),
      fileOf("standup.vtt", "Lars: we ship Friday."),
    );
    await screen.findByText(/Which speaker are you in/);
    await userEvent.click(screen.getByRole("radio", { name: /^Lars/ }));
    await userEvent.click(
      screen.getByRole("button", { name: "That one is me" }),
    );

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0]).toMatchObject({
      kind: "transcript",
      register: "spoken",
      format: "transcript",
      speaker_label: "Lars",
    });
  });

  // Pasting used to skip the preview, so the whole speaker protection could be
  // walked around by pasting a transcript instead of uploading one.
  //
  // The paste box is a form behind a verb now, so the claim is unchanged and
  // the route to it is one click longer: the row opens the dialog, the dialog
  // holds the box, and the question still has to be asked before anything
  // reaches the corpus.
  it("asks who is speaking when a transcript is PASTED, not uploaded", async () => {
    const bodies = stubApi(CONVERSATION);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    const dialog = await openPaste();
    await userEvent.type(
      within(dialog).getByPlaceholderText(
        "Paste an email, post, or anything you've written…",
      ),
      "Lars: we ship Friday. Sam: agreed.",
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Add sample" }),
    );

    expect(await screen.findByText(/Which speaker are you in/)).toBeTruthy();
    expect(bodies).toHaveLength(0);
  });

  // Two conversational files queue two questions. The second must be asked
  // fresh: carrying the first answer over would submit a speaker the reader
  // never chose for that file — silently ingesting the wrong person's words
  // whenever that name happens to appear in both.
  it("does not carry one file's chosen speaker into the next file's question", async () => {
    const bodies = stubApi(CONVERSATION);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    await userEvent.upload(fileInput(), [
      fileOf("one.vtt", "Lars: first. Sam: ok."),
      fileOf("two.vtt", "Lars: second. Sam: ok."),
    ]);
    await screen.findByText(/Which speaker are you in/);

    await userEvent.click(screen.getByRole("radio", { name: /^Lars/ }));
    await userEvent.click(
      screen.getByRole("button", { name: "That one is me" }),
    );

    // The next question is a question, not a pre-filled answer.
    await waitFor(() => {
      expect(
        screen
          .getAllByRole("radio")
          .every((r) => !(r as HTMLInputElement).checked),
      ).toBe(true);
    });
    expect(
      screen
        .getByRole("button", { name: "That one is me" })
        .hasAttribute("disabled"),
    ).toBe(true);
    // Only the answered file was written.
    expect(bodies).toHaveLength(1);
  });

  it("drops a conversation the owner declines to claim", async () => {
    const bodies = stubApi(CONVERSATION);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    await userEvent.upload(fileInput(), fileOf("standup.vtt", "Lars: hi."));
    await screen.findByText(/Which speaker are you in/);
    await userEvent.click(
      screen.getByRole("button", { name: "Skip this file" }),
    );

    expect(
      await screen.findByText(/nothing in it could be attributed to you/),
    ).toBeTruthy();
    expect(bodies).toHaveLength(0);
  });

  // The browse dialog filters by `accept`, but a DROP does not: an unreadable
  // file only ever arrives this way, and it has to be named rather than
  // silently ignored.
  it("names a dropped file it cannot read instead of uploading it", async () => {
    const bodies = stubApi(PROSE);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);
    const zone = document.querySelector(".vdna-intake");
    if (!(zone instanceof HTMLElement)) {
      throw new Error("no drop zone rendered");
    }

    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: {
        types: ["Files"],
        files: [new File(["binary"], "photo.png", { type: "image/png" })],
      },
    });
    Object.defineProperty(drop, "target", { value: zone });
    window.dispatchEvent(drop);

    expect(
      await screen.findByText(/photo\.png was skipped — only text files/),
    ).toBeTruthy();
    expect(bodies).toHaveLength(0);
  });

  it("quotes the server when it refuses for a reason the client does not know", async () => {
    stubApi({
      status: 422,
      body: {
        code: "brand_new_rule",
        title: "Unprocessable",
        detail: "That source is not eligible.",
      },
    });
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    await userEvent.upload(fileInput(), fileOf("notes.txt", "some words"));

    // An unrecognized refusal must still say something true, never disappear.
    expect(
      await screen.findByText(/notes\.txt could not be added/),
    ).toBeTruthy();
  });
});

// Intake is bounded so that selecting a large batch does not hand the server
// one request per file at once, or hold every file's text in memory at the
// same time. What the reader sees is unchanged: every file still lands, and
// the ones past the bound simply wait their turn.
describe("adding many files at once", () => {
  // Which source an ingest names, read without asserting a shape onto parsed
  // JSON: the body is whatever the client actually sent, so it is narrowed
  // rather than cast.
  function sourceLabelOf(rawBody: string): string {
    const body: unknown = JSON.parse(rawBody);
    if (
      typeof body === "object" &&
      body !== null &&
      "source_label" in body &&
      typeof body.source_label === "string"
    ) {
      return body.source_label;
    }
    throw new Error(`ingest body carried no source_label: ${rawBody}`);
  }

  // A stub whose previews stay open until the test releases them, so the
  // number in flight can actually be counted rather than inferred.
  function stubHeldPreviews(result: CorpusPreview) {
    let inFlight = 0;
    let peak = 0;
    const ingested: string[] = [];
    const release: (() => void)[] = [];
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
          inFlight += 1;
          peak = Math.max(peak, inFlight);
          await new Promise<void>((resolve) => release.push(resolve));
          inFlight -= 1;
          return jsonResponse(result);
        }
        if (path === "/voice-profiles/vp-1/sources") {
          ingested.push(sourceLabelOf(await request.text()));
          return jsonResponse(
            { source: {}, summary: SUMMARY, ingest_stats: STATS },
            201,
          );
        }
        return jsonResponse({});
      }),
    );
    return {
      peak: () => peak,
      ingested: () => ingested,
      // Drains whatever is waiting, repeatedly: releasing the first batch is
      // what lets the queue hand slots to the next one.
      releaseAll: async () => {
        for (let round = 0; round < 40 && release.length > 0; round++) {
          release.shift()?.();
          await new Promise((resolve) => setTimeout(resolve, 0));
        }
      },
    };
  }

  it("previews at most three files at a time, and still gets through all of them", async () => {
    const held = stubHeldPreviews(PROSE);
    // Reads are counted as well as requests: the bound exists to stop nine
    // files' worth of text being held at once, and a version that read every
    // file up front while only previewing three would satisfy the request
    // count alone.
    let reads = 0;
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    await userEvent.upload(
      fileInput(),
      Array.from({ length: 9 }, (_, i) =>
        countingFileOf(`s${i}.txt`, `Sample ${i}.`, () => {
          reads += 1;
        }),
      ),
    );
    await waitFor(() => expect(held.peak()).toBe(3));
    expect(reads).toBe(3);

    // Releasing lets the queue hand its slots on. Every file still lands —
    // the bound delays work, it never drops it. (The notices list is capped
    // at six, so the ingests are what proves all nine got through.)
    await held.releaseAll();
    await waitFor(
      () => {
        expect(held.ingested().length).toBe(9);
      },
      { timeout: 4000 },
    );
    expect(new Set(held.ingested()).size).toBe(9);
    expect(held.peak()).toBe(3);
  });

  // The card unmounts on a path the reader takes constantly: the first sample
  // mints the profile, which swaps the empty state for the full card. Files
  // still waiting for a slot at that moment must survive it — a new owner who
  // selects six samples must not silently end up with the three that happened
  // to start first.
  it("finishes files still queued when the card is replaced mid-intake", async () => {
    const held = stubHeldPreviews(PROSE);
    const view = render(
      <VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />,
    );

    await userEvent.upload(
      fileInput(),
      Array.from({ length: 6 }, (_, i) => fileOf(`q${i}.txt`, `Sample ${i}.`)),
    );
    await waitFor(() => expect(held.peak()).toBe(3));

    // Three are running, three are still queued when the card goes away.
    view.unmount();
    await held.releaseAll();

    await waitFor(
      () => {
        expect(held.ingested().length).toBe(6);
      },
      { timeout: 4000 },
    );
  });

  // Each unanswered question holds its source's whole text, and the reader
  // answers them one at a time. Past the cap a file is declined out loud
  // rather than queued into memory nobody will reach.
  it("declines a conversational file once five questions are already waiting", async () => {
    stubApi(CONVERSATION);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    await userEvent.upload(
      fileInput(),
      Array.from({ length: 7 }, (_, i) =>
        fileOf(`talk${i}.vtt`, `Lars: turn ${i}. Sam: ok ${i}.`),
      ),
    );

    // Five questions are held; the sixth and seventh are turned away with a
    // notice that says what to do about it.
    await waitFor(() => {
      expect(screen.getAllByText(/was not added/).length).toBe(2);
    });

    // And the five that were kept are all still reachable: answering one
    // brings up the next, five times over. Counting refusals alone would pass
    // just as happily if questions had been dropped instead of queued.
    for (let answered = 0; answered < 5; answered++) {
      await screen.findByText(/Which speaker are you in/);
      await userEvent.click(screen.getByRole("radio", { name: /^Lars/ }));
      await userEvent.click(
        screen.getByRole("button", { name: "That one is me" }),
      );
    }
    await waitFor(() => {
      expect(screen.queryByText(/Which speaker are you in/)).toBeNull();
    });
  });
});

describe("dropping a file on the page", () => {
  it("takes a file dropped on the intake area", async () => {
    const bodies = stubApi(PROSE);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);
    const zone = document.querySelector(".vdna-intake");
    if (!(zone instanceof HTMLElement)) {
      throw new Error("no drop zone rendered");
    }

    const file = fileOf("letter.txt", "Short sentences.");
    const dataTransfer = {
      types: ["Files"],
      files: [file],
    };
    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", { value: dataTransfer });
    Object.defineProperty(drop, "target", { value: zone });
    window.dispatchEvent(drop);

    await waitFor(() => expect(bodies).toHaveLength(1));
  });

  // Reported from the running product: dropping several files took only the
  // first. Every drop test here used ONE file, so nothing caught it.
  it("takes every file in a multi-file drop", async () => {
    const bodies = stubApi(PROSE);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);
    const zone = document.querySelector(".vdna-intake");
    if (!(zone instanceof HTMLElement)) {
      throw new Error("no drop zone rendered");
    }

    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: {
        types: ["Files"],
        files: [
          fileOf("one.txt", "First sample."),
          fileOf("two.txt", "Second sample."),
          fileOf("three.txt", "Third sample."),
        ],
      },
    });
    Object.defineProperty(drop, "target", { value: zone });
    window.dispatchEvent(drop);

    await waitFor(() => expect(bodies).toHaveLength(3), { timeout: 4000 });
  });

  // The real defect behind that report: the server upserts on source_ref, and
  // a key derived from content ALONE gave three files holding the same text
  // one key — so each silently replaced the last and the drop looked like it
  // had taken a single file. Copies of a sample, or several drafts exported
  // from one template, are exactly this shape.
  it("keeps files that share their text but not their name", async () => {
    const bodies = stubApi(PROSE);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);
    const zone = document.querySelector(".vdna-intake");
    if (!(zone instanceof HTMLElement)) {
      throw new Error("no drop zone rendered");
    }

    const same = "The same words in every one of these files.";
    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: {
        types: ["Files"],
        files: [
          fileOf("dup1.txt", same),
          fileOf("dup2.txt", same),
          fileOf("dup3.txt", same),
        ],
      },
    });
    Object.defineProperty(drop, "target", { value: zone });
    window.dispatchEvent(drop);

    await waitFor(() => expect(bodies).toHaveLength(3), { timeout: 4000 });
    // Three DISTINCT rows, not one row written over three times.
    const refs = new Set(bodies.map((body) => body.source_ref));
    expect(refs.size).toBe(3);
  });

  // A file dropped on the command palette or any other overlay belongs to
  // nobody. Feeding it to whatever screen happens to sit behind is how a
  // stray file silently becomes part of someone's voice.
  it("ignores a file dropped outside the card, but still stops the browser", async () => {
    const bodies = stubApi(PROSE);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);
    const elsewhere = document.createElement("div");
    document.body.append(elsewhere);

    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: { types: ["Files"], files: [fileOf("stray.txt", "words")] },
    });
    Object.defineProperty(drop, "target", { value: elsewhere });
    window.dispatchEvent(drop);

    // Claimed (so the browser cannot navigate away) but not ingested.
    expect(drop.defaultPrevented).toBe(true);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(bodies).toHaveLength(0);
  });

  it("leaves a text drag alone so native drag and drop still works", async () => {
    stubApi(PROSE);
    render(<VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />);

    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: { types: ["text/plain"], files: [] },
    });
    window.dispatchEvent(drop);

    expect(drop.defaultPrevented).toBe(false);
  });

  it("removes its window listeners when the card goes away", async () => {
    stubApi(PROSE);
    const view = render(
      <VoiceCorpusIntake profileId="vp-1" onChanged={() => {}} />,
    );
    view.unmount();

    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: { types: ["Files"], files: [fileOf("late.txt", "words")] },
    });
    window.dispatchEvent(drop);

    // Nothing is left claiming drops on behalf of a card that is gone.
    expect(drop.defaultPrevented).toBe(false);
  });
});
