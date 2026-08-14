/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
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

function render(ui: ReactNode) {
  return rtlRender(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

function fileOf(name: string, text: string): File {
  return new File([text], name, { type: "text/plain" });
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
