/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { VoiceDnaCard } from "./voice-dna";

// The Settings Voice DNA card is the ONLY surface outside onboarding where a
// voice can be started. Its empty state promises samples can be added "below",
// so the add control has to be there — and the first add must mint the one
// profile the owner is allowed to have, never a second one beside the
// onboarding flow's.

type VoiceProfile = components["schemas"]["VoiceProfile"];
type VoiceCorpusSummary = components["schemas"]["VoiceCorpusSummary"];

const PROFILE: VoiceProfile = {
  id: "vp-1",
  owner_id: "u1",
  status: "collecting",
  maturity: "collecting",
  quality_band: "thin",
  voice_profile_md: "",
  profile_version: 0,
  personality_md: "",
  auto_learning_enabled: false,
  active_source_hash: null,
  candidate_version: null,
  last_built_at: null,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  archived_at: null,
};

const SUMMARY: VoiceCorpusSummary = {
  total_words: 420,
  target_words: 30000,
  maturity: "collecting",
  quality_band: "thin",
  source_count: 1,
  register_words: { general: 420 },
};

const SOURCE: components["schemas"]["VoiceCorpusSource"] = {
  id: "vs-1",
  origin: "manual",
  kind: "other",
  register: "general",
  weight: 1,
  source_label: "Pasted writing",
  source_ref: "settings:paste:1",
  word_count: 420,
  included: true,
  exclusion_reason: null,
  extractor_version: "1",
  occurred_at: "2026-07-01T00:00:00Z",
  retention_until: null,
  content_erased_at: null,
  source: "manual",
  captured_by: "human:u1",
  version: 1,
  created_at: "2026-07-01T00:00:00Z",
  updated_at: "2026-07-01T00:00:00Z",
  archived_at: null,
};

const STATS: components["schemas"]["VoiceIngestStats"] = {
  input_words: 420,
  kept_words: 420,
  kept_turns: 0,
  discarded_turns: 0,
  speakers_seen: [],
};

const emptyPage = { data: [], page: { next_cursor: null, has_more: false } };

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// A stub that behaves like the server does across the whole mint: the profile
// list answers empty until POST /voice-profiles creates one, so a card that
// minted twice would be visible as two creates rather than hidden behind a
// canned response.
function stubApi() {
  const calls: string[] = [];
  let profile: VoiceProfile | null = null;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      const path = new URL(request.url).pathname.replace(/^\/v1/, "");
      calls.push(`${request.method} ${path}`);
      if (path === "/voice-profiles") {
        if (request.method === "POST") {
          profile = PROFILE;
          return jsonResponse(PROFILE, 201);
        }
        return jsonResponse({
          data: profile ? [profile] : [],
          page: emptyPage.page,
        });
      }
      if (path === "/voice-profiles/vp-1/sources") {
        if (request.method === "POST") {
          return jsonResponse(
            { source: SOURCE, summary: SUMMARY, ingest_stats: STATS },
            201,
          );
        }
        return jsonResponse({ data: [SOURCE], summary: SUMMARY });
      }
      return jsonResponse(emptyPage);
    }),
  );
  return calls;
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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the Settings Voice DNA card with no profile yet", () => {
  it("offers the add control the empty state points at", async () => {
    stubApi();
    render(<VoiceDnaCard />);
    expect(await screen.findByText("No Voice DNA yet")).toBeTruthy();
    expect(screen.getByText("Your first writing sample")).toBeTruthy();
    expect(
      screen.getByPlaceholderText(
        "Paste an email, post, or anything you've written…",
      ),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "Add it and start my Voice DNA" }),
    ).toBeTruthy();
  });

  it("mints exactly one profile on the first add and then shows the build control", async () => {
    const calls = stubApi();
    render(<VoiceDnaCard />);
    const box = await screen.findByPlaceholderText(
      "Paste an email, post, or anything you've written…",
    );
    await userEvent.type(box, "Short sentences. Concrete nouns.");
    await userEvent.click(
      screen.getByRole("button", { name: "Add it and start my Voice DNA" }),
    );

    // The build control only exists inside the body that requires a profile,
    // so its appearance is the proof the card left the dead end.
    expect(
      await screen.findByRole("button", { name: /Rebuild Voice DNA/ }),
    ).toBeTruthy();
    expect(calls.filter((c) => c === "POST /voice-profiles")).toHaveLength(1);
    expect(calls).toContain("POST /voice-profiles/vp-1/sources");
  });

  it("keeps the add disabled until there is something to add", async () => {
    stubApi();
    render(<VoiceDnaCard />);
    const add = await screen.findByRole("button", {
      name: "Add it and start my Voice DNA",
    });
    expect(add.hasAttribute("disabled")).toBe(true);
  });
});

// A build is the longest-running act on this card and the one a reader is
// likeliest to report as broken, so what the card says about a failed one is
// the sentence that gets quoted. Keeping the failure itself readable on the
// console belongs to the client's mutation sink (app/queryclient.test).
describe("a build that fails", () => {
  // `collecting` is the server's "too thin to build" verdict and disables the
  // control; a provisional profile is the smallest fixture whose build button
  // can actually be pressed.
  const BUILDABLE: VoiceProfile = { ...PROFILE, maturity: "provisional" };

  function stubBuild(build: () => Response) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        const path = new URL(request.url).pathname.replace(/^\/v1/, "");
        if (path === "/voice-profiles") {
          return jsonResponse({ data: [BUILDABLE], page: emptyPage.page });
        }
        if (path === "/voice-profiles/vp-1/sources") {
          return jsonResponse({ data: [SOURCE], summary: SUMMARY });
        }
        if (path === "/voice-profiles/vp-1/builds") {
          return build();
        }
        return jsonResponse(emptyPage);
      }),
    );
  }

  async function pressRebuild() {
    render(<VoiceDnaCard />);
    await userEvent.click(
      await screen.findByRole("button", { name: /Rebuild Voice DNA/ }),
    );
  }

  it("shows the shared line and never our own internals", async () => {
    stubBuild(() => {
      throw new TypeError("Cannot read properties of undefined");
    });

    await pressRebuild();

    expect(
      await screen.findByText("The request failed. No cause reported."),
    ).toBeTruthy();
    // Our own internals never become the reader's sentence.
    expect(screen.queryByText(/Cannot read properties/)).toBeNull();
  });

  it("shows the server's own cause when the server composed one", async () => {
    stubBuild(() =>
      jsonResponse(
        { code: "budget_exhausted", detail: "The AI budget is spent." },
        429,
      ),
    );

    await pressRebuild();

    expect(await screen.findByText("The AI budget is spent.")).toBeTruthy();
  });
});
