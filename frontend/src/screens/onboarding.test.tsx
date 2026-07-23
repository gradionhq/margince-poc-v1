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
import type { components } from "../api/schema";
import { LocaleProvider } from "../i18n";
import { OnboardingScreen } from "./onboarding";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type MessageReply = components["schemas"]["OnboardingCompanyMessageReply"];

const savedProfile = {
  organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
  display_name: "Gradion",
  website: "gradion.com",
  legal_name: "Gradion GmbH",
  registered_address: "Hauptstrasse 1, 10115 Berlin",
  register_vat: "DE123456789",
  industry: "Robotics",
  offer_summary: "Revenue software for manufacturers",
  icp: "Mid-market manufacturers",
};

const readyRead = {
  id: "018f3a1b-0000-7000-8000-0000000000b2",
  target_kind: "onboarding",
  organization_id: null,
  root_url: "https://gradion.com",
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: null,
  pages_read: 2,
  pages: [
    { url: "https://gradion.com", status: "fetched", kind: "home" },
    { url: "https://gradion.com/about", status: "fetched", kind: "about" },
  ],
  profile_fields: [
    {
      field: "legal_name",
      value: "Gradion GmbH",
      evidence_snippet: "© 2026 Gradion GmbH",
      source_kind: "url",
      source_url: "https://gradion.com",
      confidence: 0.9,
    },
    {
      field: "registered_address",
      value: "Hauptstrasse 1, 10115 Berlin",
      evidence_snippet:
        "Gradion GmbH, Hauptstrasse 1, 10115 Berlin, HRB 12345, DE123456789",
      source_kind: "url",
      source_url: "https://gradion.com/impressum",
      confidence: 1,
    },
    {
      field: "register_vat",
      value: "HRB 12345 · DE123456789",
      evidence_snippet:
        "Gradion GmbH, Hauptstrasse 1, 10115 Berlin, HRB 12345, DE123456789",
      source_kind: "url",
      source_url: "https://gradion.com/impressum",
      confidence: 1,
    },
    {
      field: "icp",
      value: "Mid-market manufacturers",
      evidence_snippet: "We serve mid-market manufacturers",
      source_kind: "url",
      source_url: "https://gradion.com/about",
      confidence: 0.8,
    },
  ],
  facts: [
    {
      category: "company",
      field: "founded_year",
      value: "2021",
      value_key: "founded_year:2021",
      evidence_snippet: "Founded in 2021",
      evidence_url: "https://gradion.com/about",
      confidence: 0.88,
    },
  ],
  legal_entities: [
    {
      name: "Gradion GmbH",
      registered_address: "Hauptstrasse 1, 10115 Berlin",
      register_number: "HRB 12345 · DE123456789",
      evidence_snippet:
        "Gradion GmbH, Hauptstrasse 1, 10115 Berlin, HRB 12345, DE123456789",
      source_url: "https://gradion.com/impressum",
    },
  ],
  comparisons: [],
  people: [],
  warnings: [],
  draft_version: 1,
  proposal_hash: "proposal-1",
  created_at: "2026-07-19T08:00:00Z",
  updated_at: "2026-07-19T08:00:01Z",
} as const satisfies CompanySiteRead;

const manyFactsRead = {
  ...readyRead,
  facts: Array.from({ length: 101 }, (_, index) => ({
    ...readyRead.facts[0],
    value: `Fact ${index}`,
    value_key: `founded_year:fact-${index}`,
  })),
} satisfies CompanySiteRead;

const populatedReadingRead = {
  ...readyRead,
  status: "reading",
  phase: "extracting",
  profile_fields: [
    ...readyRead.profile_fields,
    {
      field: "display_name",
      value: "Gradion",
      evidence_snippet: "Gradion",
      source_kind: "url",
      source_url: "https://gradion.com",
      confidence: 1,
    },
    {
      field: "offer_summary",
      value: "Revenue software",
      evidence_snippet: "Revenue software",
      source_kind: "url",
      source_url: "https://gradion.com",
      confidence: 1,
    },
  ],
} as const satisfies CompanySiteRead;

type StubOptions = {
  company?: typeof savedProfile | null;
  state?: Record<string, unknown> | null;
  read?: CompanySiteRead;
  readSequence?: CompanySiteRead[];
  readStartError?: { detail: string; status: number };
  saveError?: { detail: string; status: number };
  conflictOnce?: boolean;
  messageReply?: MessageReply;
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function requestPath(request: Request) {
  return new URL(request.url).pathname;
}

function stubApi(options: StubOptions = {}) {
  const calls: Request[] = [];
  let version = 0;
  let conflictPending = options.conflictOnce ?? false;
  let readIndex = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const path = requestPath(request);
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
      if (path.endsWith("/onboarding/state") && request.method === "GET") {
        return options.state
          ? jsonResponse(options.state)
          : jsonResponse({ detail: "not started" }, 404);
      }
      if (path.endsWith("/onboarding/state") && request.method === "PUT") {
        if (conflictPending) {
          conflictPending = false;
          return jsonResponse({ detail: "state changed elsewhere" }, 409);
        }
        const body = (await request.clone().json()) as Record<string, unknown>;
        version += 1;
        return jsonResponse({
          ...body,
          path: "creator",
          version,
          completed_at:
            body.step === "complete" ? "2026-07-19T08:03:00Z" : null,
          created_at: "2026-07-19T08:00:00Z",
          updated_at: "2026-07-19T08:01:00Z",
        });
      }
      if (path.endsWith("/company/site-reads") && request.method === "POST") {
        if (options.readStartError) {
          return jsonResponse(
            { detail: options.readStartError.detail },
            options.readStartError.status,
          );
        }
        return jsonResponse(
          options.readSequence?.[0] ?? options.read ?? readyRead,
          202,
        );
      }
      if (path.includes("/company/site-reads/") && path.endsWith("/confirm")) {
        return jsonResponse(savedProfile);
      }
      if (
        path.endsWith("/onboarding/company/messages") &&
        request.method === "POST"
      ) {
        return jsonResponse(
          options.messageReply ?? {
            kind: "answer",
            message: "I can help you review what I found.",
            proposed_changes: [],
            citations: [],
            next_required_field: null,
            remaining_required_fields: [],
            available_action: "confirm_company",
            ai_runtime: {
              currency: "USD",
              call_attempts: 1,
              tokens_in: 120,
              tokens_out: 30,
              latency_ms: 800,
              estimated_cost_microusd: 0,
              unpriced_calls: 0,
              models: [],
            },
          },
        );
      }
      if (path.includes("/company/site-reads/") && request.method === "GET") {
        const sequence = options.readSequence;
        if (sequence && sequence.length > 0) {
          const read = sequence[Math.min(readIndex, sequence.length - 1)];
          readIndex += 1;
          return jsonResponse(read);
        }
        return jsonResponse(options.read ?? readyRead);
      }
      if (path.endsWith("/company") && request.method === "GET") {
        return options.company
          ? jsonResponse(options.company)
          : jsonResponse({ detail: "no company yet" }, 404);
      }
      if (path.endsWith("/company") && request.method === "PUT") {
        if (options.saveError) {
          return jsonResponse(
            { detail: options.saveError.detail },
            options.saveError.status,
          );
        }
        return jsonResponse(savedProfile);
      }
      throw new Error(`unstubbed request: ${request.method} ${request.url}`);
    }),
  );
  return calls;
}

function render(
  ui: ReactNode,
  client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  }),
) {
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

async function chooseManual() {
  await userEvent.click(
    await screen.findByRole("button", { name: /Tell me yourself/ }),
  );
  await screen.findByRole("textbox", {
    name: /What is the full registered legal name/,
  });
}

async function answerManual(value: string) {
  const input = document.querySelector<HTMLInputElement | HTMLTextAreaElement>(
    ".ob-manual-input",
  );
  expect(input).not.toBeNull();
  await userEvent.type(input as HTMLInputElement, value);
  await userEvent.click(screen.getByRole("button", { name: /Next question/ }));
}

async function skipManual() {
  await userEvent.click(screen.getByRole("button", { name: /Add later/ }));
}

async function completeManualInterview() {
  await answerManual("Gradion GmbH");
  await answerManual("Hauptstrasse 1, 10115 Berlin");
  await answerManual("HRB 12345 · DE123456789");
  await answerManual("Gradion");
  await answerManual("Revenue software for manufacturers");
  await answerManual("Mid-market manufacturers");
  await skipManual();
  await skipManual();
  await skipManual();
  await skipManual();
  await skipManual();
  await skipManual();
  await skipManual();
  await skipManual();
  await skipManual();
  await userEvent.click(
    screen.getByRole("button", { name: /Review my answers/ }),
  );
  await screen.findByLabelText(/Company name/);
}

async function readWebsite() {
  await userEvent.type(
    await screen.findByRole("textbox", { name: "Website" }),
    "gradion.com",
  );
  await userEvent.click(
    screen
      .getAllByRole("button", { name: /Read my website/ })
      .at(-1) as HTMLElement,
  );
  await screen.findByText("Legal entities I found");
  await screen.findByLabelText(/Company name/);
}

function requestTo(calls: Request[], path: string, method: string) {
  return calls.find(
    (request) => request.url.includes(path) && request.method === method,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

beforeEach(() => {
  vi.stubGlobal("scrollTo", vi.fn());
});

describe("the optional website path", () => {
  it("loads the detailed AI profile after the public login profile was cached", async () => {
    const calls = stubApi();
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    client.setQueryData(["assistant-profile"], {
      name: "Margince",
      kind: "ai",
      state: "configured",
      inference_mode: "cloud",
      providers: ["gemini"],
    });

    render(<OnboardingScreen />, client);

    expect(await screen.findByText(/gemini\/gemini-3\.5-flash/)).toBeTruthy();
    expect(requestTo(calls, "/ai/profile", "GET")).toBeTruthy();
  });

  it("offers an honest choice between website reading and manual entry", async () => {
    stubApi();
    render(<OnboardingScreen />);

    expect(
      await screen.findByRole("button", { name: /Read my website/ }),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Tell me yourself/ }),
    ).toBeTruthy();
    expect(screen.queryByLabelText(/Company name/)).toBeNull();
  });

  it("shows grounded findings, evidence and coverage before confirmation", async () => {
    stubApi();
    render(<OnboardingScreen />);

    await readWebsite();

    expect(
      (screen.getByLabelText(/Registered legal name/) as HTMLInputElement)
        .value,
    ).toBe("Gradion GmbH");
    expect(
      (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
    ).toBe("Mid-market manufacturers");
    expect(screen.getAllByText(/© 2026 Gradion GmbH/).length).toBeGreaterThan(
      0,
    );
    expect(
      (screen.getByLabelText(/Registered address/) as HTMLInputElement).value,
    ).toBe("Hauptstrasse 1, 10115 Berlin");
    expect(
      (screen.getByLabelText(/Register \/ VAT ID/) as HTMLInputElement).value,
    ).toBe("HRB 12345 · DE123456789");
    expect(screen.getAllByText(/founded year/i).length).toBeGreaterThan(0);
  });

  it("cannot confirm streamed website values before the dossier is ready", async () => {
    const calls = stubApi({ read: populatedReadingRead });
    render(<OnboardingScreen />);

    await readWebsite();
    const confirm = screen.getByRole("button", {
      name: /Confirm and save company/,
    }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await userEvent.click(confirm);
    expect(requestTo(calls, "/company", "PUT")).toBeUndefined();
    expect(requestTo(calls, "/confirm", "POST")).toBeUndefined();
  });

  it("preserves administrator typing when a newer streamed dossier arrives", async () => {
    const completedRead = {
      ...populatedReadingRead,
      status: "ready",
      phase: null,
      draft_version: 2,
      profile_fields: populatedReadingRead.profile_fields.map((field) =>
        field.field === "icp"
          ? { ...field, value: "Enterprise manufacturers" }
          : field,
      ),
    } as const satisfies CompanySiteRead;
    const calls = stubApi({
      readSequence: [populatedReadingRead, completedRead],
    });
    render(<OnboardingScreen />);

    await readWebsite();
    const icp = screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement;
    await userEvent.clear(icp);
    await userEvent.type(icp, "Owner-led manufacturers");

    await waitFor(
      () => {
        const reads = calls.filter(
          (request) =>
            request.method === "GET" &&
            request.url.includes("/company/site-reads/"),
        );
        expect(reads.length).toBeGreaterThanOrEqual(2);
      },
      { timeout: 3_000 },
    );
    expect(icp.value).toBe("Owner-led manufacturers");
  });

  it("caps the default fact selection at the server limit", async () => {
    const calls = stubApi({ read: manyFactsRead });
    render(<OnboardingScreen />);

    await readWebsite();

    await waitFor(async () => {
      const stateWrites = calls.filter(
        (request) =>
          request.url.includes("/onboarding/state") && request.method === "PUT",
      );
      const body = (await stateWrites.at(-1)?.clone().json()) as Record<
        string,
        unknown
      >;
      expect(body.selected_fact_keys).toHaveLength(100);
    });
    expect(
      (
        screen.getByRole("button", {
          name: /Fact 100/,
          hidden: true,
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
  });

  it("clears website fact selections when switching to manual entry", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    await readWebsite();
    await userEvent.click(
      screen
        .getAllByRole("button", { name: /Tell me instead/ })
        .at(-1) as HTMLElement,
    );

    await waitFor(async () => {
      const stateWrites = calls.filter(
        (request) =>
          request.url.includes("/onboarding/state") && request.method === "PUT",
      );
      const body = (await stateWrites.at(-1)?.clone().json()) as Record<
        string,
        unknown
      >;
      expect(body.source_mode).toBe("manual");
      expect(body.selected_fact_keys).toEqual([]);
    });
  });

  it("keeps manual entry available when the read cannot start", async () => {
    stubApi({
      readStartError: { detail: "site blocked automated access", status: 422 },
    });
    render(<OnboardingScreen />);

    await userEvent.type(
      await screen.findByRole("textbox", { name: "Website" }),
      "gradion.com",
    );
    await userEvent.click(
      screen
        .getAllByRole("button", { name: /Read my website/ })
        .at(-1) as HTMLElement,
    );

    expect(
      await screen.findByText("site blocked automated access"),
    ).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: /Tell me instead/ }),
    );
    expect(
      await screen.findByRole("textbox", {
        name: /What is the full registered legal name/,
      }),
    ).toBeTruthy();
  });

  it("offers manual recovery after website research fails", async () => {
    const calls = stubApi({
      read: {
        ...populatedReadingRead,
        status: "failed",
        phase: null,
        status_code: null,
        status_detail: "The website stopped responding.",
      },
    });
    render(<OnboardingScreen />);

    await readWebsite();
    await userEvent.click(
      screen
        .getAllByRole("button", { name: /Tell me yourself/ })
        .at(-1) as HTMLElement,
    );
    await waitFor(async () => {
      const stateWrites = calls.filter(
        (request) =>
          request.url.includes("/onboarding/state") && request.method === "PUT",
      );
      const body = (await stateWrites.at(-1)?.clone().json()) as Record<
        string,
        unknown
      >;
      expect(body.source_mode).toBe("manual");
      expect(body.site_read_id).toBeNull();
    });
  });

  it("shows a deferred read as scheduled work and keeps the manual path available", async () => {
    stubApi({
      read: {
        ...readyRead,
        status: "deferred",
        phase: null,
        status_code: "budget_deferred",
        status_detail:
          "AI budget reached its current limit. This website read will resume automatically.",
        next_attempt_at: "2026-08-01T00:00:00Z",
        profile_fields: [],
        facts: [],
      },
    });
    render(<OnboardingScreen />);

    await userEvent.type(
      await screen.findByRole("textbox", { name: "Website" }),
      "gradion.com",
    );
    await userEvent.click(
      screen
        .getAllByRole("button", { name: /Read my website/ })
        .at(-1) as HTMLElement,
    );

    expect(await screen.findByText("I'm waiting for AI budget")).toBeTruthy();
    expect(screen.getByText(/Resumes automatically/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Tell me yourself/ }),
    ).toBeTruthy();
  });

  it("shows exact run transparency and applies conversational suggestions only after approval", async () => {
    const calls = stubApi({
      read: {
        ...readyRead,
        ai_runtime: {
          currency: "USD",
          call_attempts: 41,
          tokens_in: 100_000,
          tokens_out: 7_931,
          latency_ms: 80_809,
          estimated_cost_microusd: 79_529,
          unpriced_calls: 0,
          models: [
            {
              task: "site_fact_extract",
              tier: "cheap_cloud",
              provider: "gemini",
              configured_model: "gemini-3.1-flash-lite",
              served_model: "gemini-3.1-flash-lite-2026-07",
              call_attempts: 40,
              tokens_in: 100_000,
              tokens_out: 7_931,
              cached_tokens: 0,
              cache_write_tokens: 0,
              reasoning_tokens: 0,
              latency_ms: 80_809,
              estimated_cost_microusd: 79_529,
              unpriced_calls: 0,
              last_used_at: "2026-07-19T08:00:03Z",
            },
          ],
        },
      },
      messageReply: {
        kind: "recommendation",
        act: "company",
        message:
          "I found evidence that industrial software is the clearest industry description.",
        proposed_changes: [
          {
            field: "industry",
            value: "Industrial software",
            reason: "This matches the company's stated market.",
          },
        ],
        citations: [{ label: "industry", url: "https://gradion.com/about" }],
        remaining_required_fields: ["display_name", "offer_summary"],
        ai_runtime: {
          currency: "USD",
          call_attempts: 3,
          tokens_in: 1800,
          tokens_out: 240,
          latency_ms: 2300,
          estimated_cost_microusd: 3500,
          unpriced_calls: 0,
          models: [
            {
              task: "cold_start",
              tier: "premium",
              provider: "gemini",
              configured_model: "gemini-3.5-flash",
              served_model: "gemini-3.5-flash-2026-07",
              call_attempts: 3,
              tokens_in: 1800,
              tokens_out: 240,
              cached_tokens: 0,
              cache_write_tokens: 0,
              reasoning_tokens: 0,
              latency_ms: 2300,
              estimated_cost_microusd: 3500,
              unpriced_calls: 0,
              last_used_at: "2026-07-19T08:00:02Z",
            },
          ],
        },
      },
    });
    render(<OnboardingScreen />);

    await userEvent.type(
      await screen.findByRole("textbox", { name: "Website" }),
      "gradion.com",
    );
    await userEvent.click(
      screen
        .getAllByRole("button", { name: /Read my website/ })
        .at(-1) as HTMLElement,
    );
    const composer = await screen.findByRole("textbox", {
      name: /Ask me about a finding/,
    });
    await userEvent.type(composer, "Which industry should we use?");
    await userEvent.click(
      screen.getByRole("button", { name: /Send to Margince/ }),
    );

    expect(
      await screen.findByText(/industrial software is the clearest/),
    ).toBeTruthy();
    expect(screen.getByText("gemini-3.1-flash-lite-2026-07")).toBeTruthy();
    expect(screen.getByText("$0.079529")).toBeTruthy();
    expect(screen.getByText("Industrial software")).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: /Apply to my draft/ }),
    );
    expect(
      (
        screen.getByRole("textbox", {
          name: /Industry/,
        }) as HTMLInputElement
      ).value,
    ).toBe("Industrial software");

    const messageRequest = requestTo(calls, "/messages", "POST");
    expect(await messageRequest?.clone().json()).toMatchObject({
      message: "Which industry should we use?",
      history: [],
      locale: "en",
      company_draft: {
        legal_name: "Gradion GmbH",
        registered_address: "Hauptstrasse 1, 10115 Berlin",
        register_vat: "HRB 12345 · DE123456789",
        icp: "Mid-market manufacturers",
      },
    });
  });
});

describe("the mandatory company minimum", () => {
  it("saves a manually entered company without requiring a website", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);
    await chooseManual();
    await completeManualInterview();

    await userEvent.click(
      screen.getByRole("button", { name: /Confirm and save company/ }),
    );

    await waitFor(() =>
      expect(requestTo(calls, "/company", "PUT")).toBeTruthy(),
    );
    const body = (await (requestTo(calls, "/company", "PUT") as Request)
      .clone()
      .json()) as Record<string, string>;
    expect(body.display_name).toBe("Gradion");
    expect(body.legal_name).toBe("Gradion GmbH");
    expect(body.registered_address).toBe("Hauptstrasse 1, 10115 Berlin");
    expect(body.register_vat).toBe("HRB 12345 · DE123456789");
    expect(body.offer_summary).toBe("Revenue software for manufacturers");
    expect(body.icp).toBe("Mid-market manufacturers");
    expect(calls.some((request) => request.url.includes("site-reads"))).toBe(
      false,
    );
  });

  it("starts with legal identity and does not advance without the required company name", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);
    await chooseManual();

    expect(screen.getByText("Your legal organization")).toBeTruthy();
    await skipManual();
    await skipManual();
    await skipManual();
    expect(
      (
        screen.getByRole("button", {
          name: /Next question/,
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(requestTo(calls, "/company", "PUT")).toBeUndefined();
  });

  it("treats whitespace as missing and keeps a failed save editable", async () => {
    stubApi({
      state: {
        path: "creator",
        step: "confirm",
        source_mode: "manual",
        website_url: null,
        site_read_id: null,
        company_draft: {
          display_name: "Gradion",
          offer_summary: "Revenue software for manufacturers",
          icp: "Mid-market manufacturers",
        },
        selected_fact_keys: [],
        voice_skipped: false,
        connect_skipped: false,
        version: 4,
        completed_at: null,
        created_at: "2026-07-19T08:00:00Z",
        updated_at: "2026-07-19T08:02:00Z",
      },
      saveError: { detail: "database unavailable", status: 503 },
    });
    render(<OnboardingScreen />);
    await screen.findByLabelText(/Company name/);
    await userEvent.clear(screen.getByLabelText(/What do you sell\?/));
    await userEvent.type(screen.getByLabelText(/What do you sell\?/), "   ");
    await userEvent.click(
      screen.getByRole("button", { name: /Confirm and save company/ }),
    );
    expect(
      screen.getByText("Fill these in before you continue: What do you sell?"),
    ).toBeTruthy();

    await userEvent.clear(screen.getByLabelText(/What do you sell\?/));
    await userEvent.type(
      screen.getByLabelText(/What do you sell\?/),
      "Revenue software",
    );
    await userEvent.click(
      screen.getByRole("button", { name: /Confirm and save company/ }),
    );
    expect(await screen.findByText("Couldn't save your company")).toBeTruthy();
    expect(screen.getByText("database unavailable")).toBeTruthy();
  });
});

describe("server-owned wizard state", () => {
  it("restores an unfinished company draft at the exact saved step", async () => {
    stubApi({
      state: {
        path: "creator",
        step: "confirm",
        source_mode: "manual",
        website_url: null,
        site_read_id: null,
        company_draft: {
          display_name: "Restored GmbH",
          offer_summary: "Industrial automation",
          icp: "Plant operators",
        },
        selected_fact_keys: [],
        voice_skipped: false,
        connect_skipped: false,
        version: 4,
        completed_at: null,
        created_at: "2026-07-19T08:00:00Z",
        updated_at: "2026-07-19T08:02:00Z",
      },
    });
    render(<OnboardingScreen />);

    expect(
      ((await screen.findByLabelText(/Company name/)) as HTMLInputElement)
        .value,
    ).toBe("Restored GmbH");
    expect(
      (screen.getByLabelText(/Ideal customer/) as HTMLTextAreaElement).value,
    ).toBe("Plant operators");
  });

  it("surfaces a stale-version conflict and reloads authoritative state", async () => {
    stubApi({ conflictOnce: true });
    render(<OnboardingScreen />);
    await chooseManual();

    expect((await screen.findByRole("alert")).textContent).toMatch(
      /changed in another tab/i,
    );
  });
});

describe("later optional steps remain honest", () => {
  it("shows confirmed understanding without fabricating a draft after voice is skipped", async () => {
    stubApi();
    render(<OnboardingScreen />);
    await chooseManual();
    await completeManualInterview();
    await userEvent.click(
      screen.getByRole("button", { name: /Confirm and save company/ }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "Skip this step" }),
    );

    expect(screen.getByText(/You skipped the voice step/)).toBeTruthy();
    expect(screen.getByText("I now understand")).toBeTruthy();
    expect(screen.queryByText(/Nordwind Robotics/)).toBeNull();
  });

  it("persists completion before a skipped inbox connection exits", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);
    await chooseManual();
    await completeManualInterview();
    await userEvent.click(
      screen.getByRole("button", { name: /Confirm and save company/ }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "Skip this step" }),
    );
    await userEvent.click(
      await screen.findByRole("button", { name: /Connect my inbox/ }),
    );
    await userEvent.click(screen.getByRole("button", { name: /Skip for now/ }));

    await waitFor(() => expect(window.location.hash).toBe("#/home"));
    const stateWrites = calls.filter(
      (request) =>
        request.url.includes("/onboarding/state") && request.method === "PUT",
    );
    const finalBody = (await stateWrites.at(-1)?.clone().json()) as Record<
      string,
      unknown
    >;
    expect(finalBody.step).toBe("complete");
    expect(finalBody.connect_skipped).toBe(true);
  });
});
