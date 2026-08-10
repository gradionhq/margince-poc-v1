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
import { useReducer } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import type { ConversationState } from "./conversation-machine";
import {
  conversationReducer,
  initialConversationState,
} from "./conversation-machine";
import { run } from "./test-fixtures";
import { VoiceAct } from "./voice-act";

type CorpusPreview = components["schemas"]["VoiceCorpusPreviewResult"];
type CorpusSummary = components["schemas"]["VoiceCorpusSummary"];
type IngestStats = components["schemas"]["VoiceIngestStats"];

const PROFILE_ID = "018f3a1b-0000-7000-8000-0000000000d1";
const BUILD_IDS = [
  "018f3a1b-0000-7000-8000-0000000000e1",
  "018f3a1b-0000-7000-8000-0000000000e2",
];

function summaryOf(totalWords: number): CorpusSummary {
  return {
    total_words: totalWords,
    target_words: 30000,
    maturity: "collecting",
    quality_band: totalWords >= 800 ? "good" : "thin",
    source_count: 1,
    register_words: { spoken: totalWords },
  };
}

const conversationalPreview: CorpusPreview = {
  detected_format: "vtt",
  total_words: 5400,
  speakers: [
    { label: "Speaker 1", turns: 12, words: 1240 },
    { label: "Speaker 2", turns: 14, words: 4160 },
  ],
  unattributed_words: 0,
  ingestible_as_transcript: true,
};

const unattributedPreview: CorpusPreview = {
  detected_format: "vtt",
  total_words: 5400,
  speakers: [],
  unattributed_words: 5400,
  ingestible_as_transcript: false,
};

const documentPreview: CorpusPreview = {
  detected_format: "txt",
  total_words: 900,
  speakers: [],
  unattributed_words: 900,
  ingestible_as_transcript: false,
};

const transcriptStats: IngestStats = {
  input_words: 5400,
  kept_words: 1240,
  kept_turns: 12,
  discarded_turns: 14,
  speakers_seen: ["Speaker 1", "Speaker 2"],
};

const documentStats: IngestStats = {
  input_words: 900,
  kept_words: 900,
  kept_turns: 1,
  discarded_turns: 0,
  speakers_seen: [],
};

// Build poll rows: only the fields the hook reads; the stub serves them as
// plain JSON exactly like the server would.
type BuildRow = { id: string; status: string; stage: string | null };

const candidateVersion = {
  profile_version: 3,
  status: "candidate",
  model_name: "test-model",
  profile_json: {
    inference: { identity_summary: "Direct, concrete, first person." },
  },
  stats_json: { word_count: 1240 },
};

// A version carrying every optional field parseVoiceInsights reads — the
// result scene's own structure (the sample card, the measured dimensions,
// the reading) is pinned against this, independent of the parser's own
// tests.
const richVersion = {
  profile_version: 5,
  status: "active",
  model_name: "test-model",
  profile_json: {
    inference: {
      identity_summary: "Direct and warm.",
      thinking_pattern: "Leads with the ask, then the context.",
      signature_moves: [
        { move: "Opens with 'Hey'", quote: "Hey Jordan," },
        { move: "Closes with Cheers", quote: "Cheers, Alex" },
      ],
      avoid: ["Corporate jargon", "Em dashes"],
    },
    sample_drafts: [
      {
        subject: "Following up on Tuesday",
        body: "Hey Jordan, just circling back on this.",
        voice_score: 0.9,
      },
      {
        subject: "Quick nudge",
        body: "Hey Jordan, any update on your end?",
        voice_score: 0.8,
      },
    ],
    guidance: { next_best: "Add two more sent emails." },
  },
  stats_json: { word_count: 4200, mean_sentence_words: 9.5, sample_count: 4 },
};

type IngestFixture = {
  stats: IngestStats;
  summary: CorpusSummary;
  /** Response latency, to force out-of-order settlement in tests. */
  delayMs?: number;
};

type StubOptions = {
  preview?: CorpusPreview;
  /** Ingest responses in order: each carries its stats + resulting summary. */
  ingests?: readonly IngestFixture[];
  /** Ingest responses keyed by source_label; wins over the ordered list. */
  ingestsBySource?: Readonly<Record<string, IngestFixture>>;
  /** Poll snapshots per build id, consumed one per GET (last one repeats). */
  builds?: Readonly<Record<string, BuildRow[]>>;
  /** Error status for the build poll GET (resilience tests). */
  buildPollStatus?: number;
  /** RFC 7807 body + status the build POST is refused with, for the cases
   * about what a refused start is allowed to say on the collect scene. */
  buildStartRefusal?: Readonly<{ body: unknown; status: number }>;
  /** The built version the versions GET returns; defaults to the thin
   * candidateVersion fixture most tests never look past. */
  version?: unknown;
};

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => {
    globalThis.setTimeout(resolve, ms);
  });
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(options: StubOptions = {}) {
  const calls: Request[] = [];
  let ingestIndex = 0;
  let buildIndex = 0;
  const buildPolls = new Map<string, BuildRow[]>(
    Object.entries(options.builds ?? {}),
  );
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const path = new URL(request.url).pathname;
      if (path.endsWith("/ai/profile")) {
        return jsonResponse({
          name: "Margince",
          kind: "ai",
          state: "configured",
          inference_mode: "cloud",
          providers: ["gemini"],
          configured_models: [
            {
              tier: "cheap_cloud",
              provider: "gemini",
              model: "gemini-3.5-flash",
            },
          ],
        });
      }
      if (path.endsWith("/voice-profiles") && request.method === "GET") {
        return jsonResponse({
          data: [{ id: PROFILE_ID }],
          page: { next_cursor: null },
        });
      }
      if (path.endsWith("/sources/preview")) {
        return jsonResponse(options.preview ?? documentPreview);
      }
      if (path.endsWith("/sources") && request.method === "POST") {
        const body = (await request.clone().json()) as Record<string, unknown>;
        const label =
          typeof body.source_label === "string" ? body.source_label : "";
        const bySource = options.ingestsBySource?.[label];
        const ingest = bySource ?? (options.ingests ?? [])[ingestIndex];
        if (!ingest) {
          throw new Error("unexpected ingest: no fixture left");
        }
        ingestIndex += 1;
        if (ingest.delayMs !== undefined) {
          await delay(ingest.delayMs);
        }
        return jsonResponse(
          {
            source: { id: `source-${ingestIndex}` },
            summary: ingest.summary,
            ingest_stats: ingest.stats,
          },
          201,
        );
      }
      if (path.endsWith("/builds") && request.method === "POST") {
        const refusal = options.buildStartRefusal;
        if (refusal !== undefined) {
          return jsonResponse(refusal.body, refusal.status);
        }
        const id = BUILD_IDS[buildIndex];
        buildIndex += 1;
        return jsonResponse({ id, status: "queued", stage: null }, 202);
      }
      if (path.includes("/builds/") && request.method === "GET") {
        if (options.buildPollStatus !== undefined) {
          return jsonResponse(
            { detail: "build fetch failed" },
            options.buildPollStatus,
          );
        }
        const buildId = path.slice(path.lastIndexOf("/") + 1);
        const polls = buildPolls.get(buildId) ?? [];
        const row = polls.length > 1 ? polls.shift() : polls[0];
        if (!row) {
          throw new Error(`unstubbed build poll: ${buildId}`);
        }
        return jsonResponse(row);
      }
      if (path.endsWith("/versions") && request.method === "GET") {
        return jsonResponse({
          data: [options.version ?? candidateVersion],
          page: { next_cursor: null },
        });
      }
      throw new Error(`unstubbed request: ${request.method} ${request.url}`);
    }),
  );
  return calls;
}

function collectingState(): ConversationState {
  return { ...initialConversationState, act: "voice", phase: "vo.collecting" };
}

function VoiceHarness({ initial }: Readonly<{ initial: ConversationState }>) {
  const [state, dispatch] = useReducer(conversationReducer, initial);
  return <VoiceAct state={state} dispatch={dispatch} />;
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

async function uploadFile(name: string, content: string) {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]');
  expect(input).not.toBeNull();
  if (input) {
    await userEvent.upload(
      input,
      new File([content], name, { type: "text/plain" }),
    );
  }
}

// Path-suffix matching so "/sources" never also counts "/sources/preview".
function requestsTo(calls: Request[], path: string, method: string) {
  return calls.filter(
    (request) =>
      new URL(request.url).pathname.endsWith(path) && request.method === method,
  );
}

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the conversational voice act", () => {
  it("asks the speaker question for a conversational file and ingests with the chosen speaker_label", async () => {
    const calls = stubApi({
      preview: conversationalPreview,
      ingests: [{ stats: transcriptStats, summary: summaryOf(1240) }],
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("call.vtt", "WEBVTT transcript content");

    // The decision is the whole work surface, not a rail card: the question
    // and its server-derived speaker options render there.
    expect(
      await screen.findByText(/Which one is you\? Only your own words count/),
    ).toBeTruthy();
    expect(screen.getByText("words: 1240 · turns: 12")).toBeTruthy();
    expect(screen.getByText("words: 4160 · turns: 14")).toBeTruthy();

    await userEvent.click(screen.getByRole("radio", { name: /Speaker 1/ }));
    await userEvent.click(
      screen.getByRole("button", { name: "Use this speaker" }),
    );

    await waitFor(() => {
      expect(requestsTo(calls, "/sources", "POST").length).toBe(1);
    });
    const body = (await requestsTo(calls, "/sources", "POST")[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.format).toBe("transcript");
    expect(body.speaker_label).toBe("Speaker 1");
    expect(body.register).toBe("spoken");

    // The server's kept-of-total stats land on the collect scene's own
    // sources list, once — never a second copy of the same fact in the rail.
    expect(await screen.findByText(/Kept 1240 of 5400 words/)).toBeTruthy();
  });

  it("ingests a document directly and reacts with the server word count", async () => {
    const calls = stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(900) }],
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("notes.md", "Plain prose I wrote myself.");

    // The server's word count lands on the collect scene's own sources
    // list, not as a second bubble in the rail.
    expect(await screen.findByText("900 words")).toBeTruthy();
    const body = (await requestsTo(calls, "/sources", "POST")[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.format).toBe("text");
    expect(body.speaker_label).toBeNull();
    // No speaker question was ever asked for single-author prose.
    expect(screen.queryByText(/Which one is you/)).toBeNull();
  });

  it("accepts a drop anywhere in the window — composer, artifact, gaps included — and neutralizes the browser default", async () => {
    const calls = stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(900) }],
    });
    render(<VoiceHarness initial={collectingState()} />);

    // The drop lands on window, NOT on the thread div: the hint promises
    // "anywhere in this conversation", and an unhandled drop would navigate
    // the browser to the file, destroying the onboarding session.
    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: {
        types: ["Files"],
        files: [
          new File(["Plain prose I wrote myself."], "notes.md", {
            type: "text/plain",
          }),
        ],
      },
    });
    window.dispatchEvent(drop);

    expect(drop.defaultPrevented).toBe(true);
    expect(await screen.findByText("900 words")).toBeTruthy();
    expect(requestsTo(calls, "/sources", "POST").length).toBe(1);

    // A text-selection drag is NOT claimed: the composer's native
    // drag-to-insert must keep working.
    const textDrag = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(textDrag, "dataTransfer", {
      value: { types: ["text/plain"], files: [] },
    });
    window.dispatchEvent(textDrag);
    expect(textDrag.defaultPrevented).toBe(false);
  });

  it("neutralizes a stray drop outside the collecting phases without ingesting", () => {
    const calls = stubApi({});
    render(
      <VoiceHarness
        initial={{
          ...initialConversationState,
          act: "voice",
          phase: "vo.skipped",
        }}
      />,
    );

    const drop = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(drop, "dataTransfer", {
      value: { types: ["Files"], files: [new File(["text"], "stray.txt")] },
    });
    window.dispatchEvent(drop);

    expect(drop.defaultPrevented).toBe(true);
    expect(requestsTo(calls, "/sources", "POST").length).toBe(0);
  });

  it("refuses an unattributed transcript honestly and counts nothing", async () => {
    const calls = stubApi({ preview: unattributedPreview });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("raw.vtt", "unlabelled transcript text");

    expect(
      await screen.findByText(
        /I cannot tell which words are yours, so I counted none/,
      ),
    ).toBeTruthy();
    expect(requestsTo(calls, "/sources", "POST").length).toBe(0);
    expect(screen.queryByText(/words kept/)).toBeNull();
  });

  // The scene shows the build action from the start and states its
  // precondition beside it: an affordance that appears out of nowhere at 800
  // words is one nobody could plan for. Enabled is what the floor gates.
  it("enables the build only at the server floor of 800 words", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [
        { stats: documentStats, summary: summaryOf(500) },
        { stats: documentStats, summary: summaryOf(820) },
      ],
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "First document.");
    expect(
      await screen.findByText(/Own words so far: 500\. I need at least 800/),
    ).toBeTruthy();
    const build = screen.getByRole("button", {
      name: /Build my voice profile/,
    }) as HTMLButtonElement;
    expect(build.disabled).toBe(true);

    await uploadFile("two.md", "Second document.");
    await waitFor(() => {
      expect(
        (
          screen.getByRole("button", {
            name: /Build my voice profile/,
          }) as HTMLButtonElement
        ).disabled,
      ).toBe(false);
    });
    expect(screen.queryByText(/I need at least 800/)).toBeNull();
  });

  it("narrates build stages and lands the succeeded result card with the candidate note", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      builds: {
        [BUILD_IDS[0]]: [
          { id: BUILD_IDS[0], status: "running", stage: "extract" },
          { id: BUILD_IDS[0], status: "succeeded", stage: null },
        ],
      },
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    expect(
      await screen.findByText(/Finding your signature moves/, undefined, {
        timeout: 4000,
      }),
    ).toBeTruthy();
    expect(
      await screen.findByText(/Your voice profile is ready\./, undefined, {
        timeout: 4000,
      }),
    ).toBeTruthy();
    expect(
      await screen.findByText(/Here is your voice, in your own words\./),
    ).toBeTruthy();
    expect(
      await screen.findByText(/needs your review before it goes live/),
    ).toBeTruthy();
    expect(await screen.findByText(/Direct, concrete/)).toBeTruthy();
  });

  it("offers a retry after a failed build and a second build proceeds", async () => {
    const calls = stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      builds: {
        [BUILD_IDS[0]]: [{ id: BUILD_IDS[0], status: "failed", stage: null }],
        [BUILD_IDS[1]]: [
          { id: BUILD_IDS[1], status: "succeeded", stage: null },
        ],
      },
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    expect(
      await screen.findByText(/The build did not finish/, undefined, {
        timeout: 4000,
      }),
    ).toBeTruthy();

    await userEvent.click(
      screen.getByRole("button", { name: /Try the build again/ }),
    );

    expect(
      await screen.findByText(/Your voice profile is ready\./, undefined, {
        timeout: 4000,
      }),
    ).toBeTruthy();
    expect(requestsTo(calls, "/builds", "POST").length).toBe(2);
  });

  it("keeps the newest-by-request-order summary when ingest responses settle out of order", async () => {
    stubApi({
      preview: documentPreview,
      // The first upload's response is held back past the second's: the
      // stale 500-word summary settles last and must not roll the meter
      // (or the build gate) back below the floor.
      ingestsBySource: {
        "one.md": {
          stats: documentStats,
          summary: summaryOf(500),
          delayMs: 150,
        },
        "two.md": { stats: documentStats, summary: summaryOf(820) },
      },
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "First document.");
    await uploadFile("two.md", "Second document.");

    // Both sources land in the scene's own list regardless of settlement
    // order — the rail carries neither reaction.
    await waitFor(() => {
      expect(screen.getAllByText("900 words").length).toBe(2);
    });
    // The one-time floor-reached announcement echoes this same sentence into
    // a second, visually-hidden node, so the visible line is queried by its
    // own class rather than by text (which would now match both).
    await waitFor(() => {
      expect(document.querySelector(".ob-voice-meter-line")?.textContent).toBe(
        "820 words — enough to build. More still sharpens it.",
      );
    });
    expect(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    ).toBeTruthy();
    expect(screen.queryByText(/I need at least 800/)).toBeNull();
    expect(screen.queryByText("500 of 800 words")).toBeNull();
  });

  it("concludes as failed with the retry chip when the build poll keeps erroring", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      buildPollStatus: 500,
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    // The act never sits silent in vo.building: one honest correlated turn,
    // the failed outcome, and the retry chip back on offer.
    expect(
      await screen.findByText(/I lost the connection during the build/),
    ).toBeTruthy();
    expect(await screen.findByText(/The build did not finish/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Try the build again/ }),
    ).toBeTruthy();
  });

  // What a refused build start may say on the collect scene. The two halves
  // of the same rule: the server's own sentence is the best thing a reader
  // can be given, and a failure the server put no words to is answered in
  // the reader's language rather than in whatever the exception happened to
  // carry.
  it("shows the server's own refusal on the collect scene when the build cannot start", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      buildStartRefusal: {
        status: 429,
        body: {
          code: "budget_exhausted",
          detail: "This month's model budget is spent. It resets on the 1st.",
        },
      },
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    expect(
      await screen.findByText(
        "This month's model budget is spent. It resets on the 1st.",
      ),
    ).toBeTruthy();
  });

  it("answers a refusal carrying no words for a reader with the shared line", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      buildStartRefusal: { status: 502, body: { status: 502 } },
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe("The request failed. No cause reported.");
  });

  it("keeps late events from a superseded build inert after the retry", () => {
    // Machine-level correlation, reusing the shared fold helper: once build
    // two is the active run, build one's late terminal changes nothing.
    const retried = run(
      [
        { type: "BUILD_STARTED", buildId: BUILD_IDS[0] },
        { type: "BUILD_TERMINAL", buildId: BUILD_IDS[0], status: "failed" },
        { type: "BUILD_STARTED", buildId: BUILD_IDS[1] },
      ],
      collectingState(),
    );
    expect(retried.phase).toBe("vo.building");

    const afterStale = conversationReducer(retried, {
      type: "BUILD_TERMINAL",
      buildId: BUILD_IDS[0],
      status: "succeeded",
    });
    expect(afterStale).toBe(retried);
  });
});

// The rule this act follows: the work happens on the surface, the rail only
// narrates. These pin the three places that rule used to break.
describe("the voice act's surface/rail split", () => {
  it("keeps the speaker decision and its radio options off the rail", async () => {
    stubApi({
      preview: conversationalPreview,
      ingests: [{ stats: transcriptStats, summary: summaryOf(1240) }],
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("call.vtt", "WEBVTT transcript content");
    await screen.findByText(/Which one is you\? Only your own words count/);

    const rail = document.querySelector(".ob-conv-thread");
    expect(rail).not.toBeNull();
    expect(within(rail as HTMLElement).queryAllByRole("radio").length).toBe(0);
    // The file's own upload turn and per-source reaction never land in the
    // rail either — a fact already live on the surface stays there.
    expect(
      within(rail as HTMLElement).queryByText(/Added call.vtt/),
    ).toBeNull();
  });

  it("renders a skipped voice act's Continue action on the surface, never the rail", () => {
    stubApi({});
    render(
      <VoiceHarness
        initial={{
          ...initialConversationState,
          act: "voice",
          phase: "vo.skipped",
        }}
      />,
    );

    const rail = document.querySelector(".ob-conv-thread") as HTMLElement;
    expect(within(rail).queryByRole("button", { name: "Continue" })).toBeNull();
    const bar = document.querySelector(".ob-triage-continue");
    expect(bar).not.toBeNull();
    expect(
      within(bar as HTMLElement).getByRole("button", { name: "Continue" }),
    ).toBeTruthy();
  });

  it("renders the succeeded result as structured sections, each from the same parsed data", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      builds: {
        [BUILD_IDS[0]]: [
          { id: BUILD_IDS[0], status: "succeeded", stage: null },
        ],
      },
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    // Every section is its own bordered card, and the confirm action sits
    // in the surface's own pinned bar beside them, not the rail.
    const identity = await screen.findByText(/Direct, concrete/);
    expect(identity.closest(".ob-voice-result-card")).not.toBeNull();
    const rail = document.querySelector(".ob-conv-thread") as HTMLElement;
    expect(
      within(rail).queryByRole("button", { name: "That is my voice" }),
    ).toBeNull();
    const bar = document.querySelector(".ob-triage-continue");
    expect(bar).not.toBeNull();
    expect(
      within(bar as HTMLElement).getByRole("button", {
        name: "That is my voice",
      }),
    ).toBeTruthy();
  });

  it("shows the sample as a real draft and cycles to another one already in hand", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      builds: {
        [BUILD_IDS[0]]: [
          { id: BUILD_IDS[0], status: "succeeded", stage: null },
        ],
      },
      version: richVersion,
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    expect(await screen.findByText("Following up on Tuesday")).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: "Another scenario" }),
    );
    expect(screen.getByText("Quick nudge")).toBeTruthy();
    expect(screen.queryByText("Following up on Tuesday")).toBeNull();
  });

  it("measures only the dimension it actually scored, as a readout rather than a control", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      builds: {
        [BUILD_IDS[0]]: [
          { id: BUILD_IDS[0], status: "succeeded", stage: null },
        ],
      },
      version: richVersion,
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    expect(await screen.findByText("Sentence length")).toBeTruthy();
    expect(screen.getByText("Measured: 1")).toBeTruthy();
    expect(screen.getByText("9.5 words per sentence on average.")).toBeTruthy();
    // A readout, not a control: nothing here is focusable or draggable.
    expect(document.querySelector(".ob-voice-dim-track input")).toBeNull();
    expect(document.querySelector('[role="slider"]')).toBeNull();
    // The reference's other four axes (formality, warmth, directness,
    // vocabulary) have no server-measured equivalent, so none render.
    expect(screen.queryByText("Formality")).toBeNull();
  });

  it("keeps the how-you-think, moves, avoid, and improvement sections beside the sample", async () => {
    stubApi({
      preview: documentPreview,
      ingests: [{ stats: documentStats, summary: summaryOf(820) }],
      builds: {
        [BUILD_IDS[0]]: [
          { id: BUILD_IDS[0], status: "succeeded", stage: null },
        ],
      },
      version: richVersion,
    });
    render(<VoiceHarness initial={collectingState()} />);

    await uploadFile("one.md", "Enough material.");
    await userEvent.click(
      await screen.findByRole("button", { name: /Build my voice profile/ }),
    );

    expect(
      await screen.findByText("Leads with the ask, then the context."),
    ).toBeTruthy();
    expect(screen.getByText("Hey Jordan,")).toBeTruthy();
    expect(screen.getByText("Corporate jargon")).toBeTruthy();
    expect(screen.getByText(/Add two more sent emails\./)).toBeTruthy();
    // The sample card's own "why" line draws on the same move names, at a
    // glance rather than in full.
    expect(
      screen.getByText(/Opens with 'Hey', Closes with Cheers/),
    ).toBeTruthy();
  });
});
