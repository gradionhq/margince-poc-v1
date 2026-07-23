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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { OnboardingScreen } from "../onboarding";

// The restore matrix of the conversational shell: which act a reload lands
// in, that the landing is derived from the wizard state's `path` and `step`
// (never from company-exists alone), that recap turns are derived summaries
// rather than replayed narration, and that finishing connect writes the
// completion BEFORE any navigation.

type OnboardingState = components["schemas"]["OnboardingState"];

const savedProfile = {
  organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
  display_name: "Gradion",
  website: "gradion.com",
  offer_summary: "Revenue software for manufacturers",
  icp: "Mid-market manufacturers",
};

function stateRow(overrides: Partial<OnboardingState> = {}): OnboardingState {
  return {
    path: "creator",
    step: "read",
    source_mode: "website",
    website_url: "https://gradion.com",
    site_read_id: null,
    company_draft: {},
    selected_fact_keys: [],
    voice_skipped: false,
    connect_skipped: false,
    version: 3,
    completed_at: null,
    created_at: "2026-07-22T08:00:00Z",
    updated_at: "2026-07-22T09:00:00Z",
    ...overrides,
  };
}

type StubOptions = {
  /** GET /onboarding/state; null answers 404 (nothing persisted). */
  state?: OnboardingState | null;
  /** GET /company; null answers 404 (no company confirmed yet). */
  company?: typeof savedProfile | null;
  /** GET /voice-profiles items (the restore probe's first hop). */
  voiceProfiles?: { id: string }[];
  voiceVersions?: { profile_version: number; status: string }[];
  corpusWords?: number;
  /** Mutable: set to make PUT /onboarding/state fail with this status. */
  putStatus?: number;
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(options: StubOptions = {}) {
  const calls: Request[] = [];
  let version = options.state?.version ?? 0;
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
          configured_models: [],
        });
      }
      if (path.endsWith("/company/context/capabilities")) {
        return jsonResponse({
          onboarding_enabled: true,
          read_enabled: true,
          rollout: "ga",
        });
      }
      if (path.endsWith("/onboarding/state") && request.method === "GET") {
        const row = options.state ?? null;
        return row === null
          ? jsonResponse({ detail: "not started" }, 404)
          : jsonResponse({ ...row, version });
      }
      if (path.endsWith("/onboarding/state") && request.method === "PUT") {
        if (options.putStatus !== undefined) {
          return jsonResponse({ detail: "write failed" }, options.putStatus);
        }
        const body = (await request.clone().json()) as Record<string, unknown>;
        version += 1;
        return jsonResponse({
          ...body,
          path: options.state?.path ?? "creator",
          version,
          completed_at: null,
          created_at: "2026-07-22T08:00:00Z",
          updated_at: "2026-07-22T09:01:00Z",
        });
      }
      if (path.endsWith("/company") && request.method === "GET") {
        return options.company
          ? jsonResponse(options.company)
          : jsonResponse({ detail: "no company yet" }, 404);
      }
      if (path.endsWith("/voice-profiles") && request.method === "GET") {
        return jsonResponse({ data: options.voiceProfiles ?? [], page: {} });
      }
      if (path.includes("/voice-profiles/") && path.endsWith("/versions")) {
        return jsonResponse({ data: options.voiceVersions ?? [], page: {} });
      }
      if (path.includes("/voice-profiles/") && path.endsWith("/sources")) {
        return jsonResponse({
          data: [],
          summary: {
            total_words: options.corpusWords ?? 0,
            target_words: 30000,
            maturity: "collecting",
            quality_band: "thin",
            source_count: options.corpusWords ? 1 : 0,
            register_words: {},
          },
          page: {},
        });
      }
      throw new Error(`unstubbed request: ${request.method} ${request.url}`);
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

function requestsTo(calls: Request[], path: string, method: string) {
  return calls.filter(
    (request) => request.url.includes(path) && request.method === method,
  );
}

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
  window.localStorage.setItem("margince.conv", "1");
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.localStorage.removeItem("margince.conv");
  window.location.hash = "";
});

describe("restore into the conversational shell", () => {
  it("a fresh creator starts at the company welcome, no recap", async () => {
    stubApi();
    render(<OnboardingScreen />);

    expect(
      await screen.findByText(/Where should I start reading\?/),
    ).toBeTruthy();
    expect(screen.queryByText(/Welcome back/)).toBeNull();
  });

  it("a returning creator at step voice resumes the voice act with a company recap, not replayed narration", async () => {
    stubApi({
      state: stateRow({ step: "voice" }),
      company: savedProfile,
    });
    render(<OnboardingScreen />);

    // The saved company must NOT demote the creator to the member path (the
    // old proxy skipped the voice act for exactly this session).
    expect(
      await screen.findByText(/Want me to learn how you write\?/),
    ).toBeTruthy();
    expect(
      screen.getByText(/Welcome back\. Here is where we stand\./),
    ).toBeTruthy();
    expect(
      screen.getByText(/Your company profile for Gradion is confirmed\./),
    ).toBeTruthy();
    // Recap is a derived summary; the live confirmation outcome and the read
    // narration are never replayed.
    expect(
      screen.queryByText(/Everything I stored carries its source/),
    ).toBeNull();
    expect(screen.queryByText(/Finished reading/)).toBeNull();
  });

  it("a corpus already on the server resumes collecting with honest words", async () => {
    stubApi({
      state: stateRow({ step: "voice" }),
      company: savedProfile,
      voiceProfiles: [{ id: "018f3a1b-0000-7000-8000-0000000000f1" }],
      corpusWords: 1240,
    });
    render(<OnboardingScreen />);

    expect(
      await screen.findByText(
        /Your corpus already holds 1240 of your own words\./,
      ),
    ).toBeTruthy();
    // vo.collecting: the composer and the collect prompt, not the invite.
    expect(await screen.findByText(/Send me things you wrote\./)).toBeTruthy();
    expect(screen.queryByText(/Want me to learn how you write\?/)).toBeNull();
  });

  it("the member path comes from the state row and skips voice and results entirely", async () => {
    const calls = stubApi({
      state: stateRow({ path: "member", step: "connect" }),
      company: savedProfile,
    });
    render(<OnboardingScreen />);

    expect(
      await screen.findByText(/Last step: what may I capture/),
    ).toBeTruthy();
    expect(screen.getByRole("button", { name: /Google/ })).toBeTruthy();
    expect(screen.queryByText(/Want me to learn how you write\?/)).toBeNull();
    // A member restore never probes the voice surface.
    expect(requestsTo(calls, "/voice-profiles", "GET").length).toBe(0);
  });

  it("honors a recorded voice skip in the results act", async () => {
    stubApi({
      state: stateRow({ step: "results", voice_skipped: true }),
      company: savedProfile,
    });
    render(<OnboardingScreen />);

    expect(
      await screen.findByText(
        /No voice profile yet\. Drafts use a neutral starter voice/,
      ),
    ).toBeTruthy();
    expect(screen.getByText(/You skipped the voice profile\./)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Continue" })).toBeTruthy();
  });

  it("continuing out of the results act checkpoints step connect", async () => {
    const calls = stubApi({
      state: stateRow({ step: "results" }),
      company: savedProfile,
    });
    render(<OnboardingScreen />);

    await userEvent.click(
      await screen.findByRole("button", { name: "Continue" }),
    );

    expect(
      await screen.findByText(/Last step: what may I capture/),
    ).toBeTruthy();
    await waitFor(() => {
      expect(requestsTo(calls, "/onboarding/state", "PUT").length).toBe(1);
    });
    const body = (await requestsTo(calls, "/onboarding/state", "PUT")[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.step).toBe("connect");
  });

  it("a completed journey navigates straight into the workspace", async () => {
    stubApi({ state: stateRow({ step: "complete" }), company: savedProfile });
    render(<OnboardingScreen />);

    await waitFor(() => {
      expect(window.location.hash).toBe("#/home");
    });
  });
});

describe("finishing the connect act", () => {
  it("persists completion BEFORE navigating; a failed write is narrated and retryable", async () => {
    const options: StubOptions = {
      state: stateRow({ path: "member", step: "connect" }),
      company: savedProfile,
      putStatus: 500,
    };
    const calls = stubApi(options);
    render(<OnboardingScreen />);

    await userEvent.click(
      await screen.findByRole("button", { name: /Skip connecting for now/ }),
    );

    // The write failed: the failure is said out loud, nothing navigated.
    expect(
      await screen.findByText(/I could not record the finish\. Try again\./),
    ).toBeTruthy();
    expect(window.location.hash).toBe("");

    // The retry succeeds: completion lands, THEN the shell navigates.
    options.putStatus = undefined;
    await userEvent.click(
      screen.getByRole("button", { name: /Skip connecting for now/ }),
    );
    await waitFor(() => {
      expect(window.location.hash).toBe("#/home");
    });
    const writes = requestsTo(calls, "/onboarding/state", "PUT");
    expect(writes.length).toBeGreaterThan(0);
    const body = (await writes[writes.length - 1].clone().json()) as Record<
      string,
      unknown
    >;
    expect(body.step).toBe("complete");
    expect(body.connect_skipped).toBe(true);
    // The finish never rewrites the voice outcome recorded earlier.
    expect(body.voice_skipped).toBe(false);
  });
});
