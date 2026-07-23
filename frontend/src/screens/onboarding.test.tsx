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

// The onboarding invariants that survived the conversational flip, driven
// through the ONE onboarding surface (the conversational shell): honest
// source choice, the manual interview's required gate, whitespace-is-missing,
// typing outranking a newer dossier, the fact-selection cap, honest failure
// and deferral, and exact run transparency with apply-only-after-approval.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type MessageReply = components["schemas"]["OnboardingCompanyMessageReply"];
type ColdField = components["schemas"]["ColdStartField"];

const READ_ID = "018f3a1b-0000-7000-8000-0000000000b2";

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

function grounded(
  field: ColdField["field"],
  value: string,
  snippet: string,
): ColdField {
  return {
    field,
    value,
    evidence_snippet: snippet,
    source_kind: "url",
    source_url: "https://gradion.com",
    confidence: 0.9,
  };
}

const readingRead = {
  id: READ_ID,
  target_kind: "onboarding",
  organization_id: null,
  root_url: "https://gradion.com",
  status: "reading",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: "crawling",
  pages_read: 1,
  pages: [{ url: "https://gradion.com", status: "fetched", kind: "home" }],
  profile_fields: [
    grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
  ],
  facts: [],
  comparisons: [],
  people: [],
  legal_entities: [],
  warnings: [],
  draft_version: 1,
  proposal_hash: "proposal-1",
  created_at: "2026-07-22T08:00:00Z",
  updated_at: "2026-07-22T08:00:01Z",
} as const satisfies CompanySiteRead;

const readyRead = {
  ...readingRead,
  status: "ready",
  phase: null,
  pages_read: 3,
  draft_version: 2,
  proposal_hash: "proposal-2",
  profile_fields: [
    grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
    grounded("display_name", "Gradion", "Gradion"),
    grounded(
      "offer_summary",
      "Revenue software for manufacturers",
      "We build revenue software",
    ),
    grounded("icp", "Mid-market manufacturers", "We serve manufacturers"),
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
} as const satisfies CompanySiteRead;

const manyFactsRead = {
  ...readyRead,
  facts: Array.from({ length: 101 }, (_, index) => ({
    ...readyRead.facts[0],
    value: `Fact ${index}`,
    value_key: `founded_year:fact-${index}`,
  })),
} satisfies CompanySiteRead;

function proposalFor(read: CompanySiteRead): Proposal {
  return {
    ready: true,
    fields: read.profile_fields.map((field) => ({
      field: field.field,
      value: field.value,
      confidence: field.confidence,
      evidence_snippet: field.evidence_snippet,
      source_url: field.source_url ?? "https://gradion.com",
    })),
    facts: [...read.facts],
    open_questions: [],
    remaining_required_fields: [],
    draft_version: read.draft_version,
    proposal_hash: read.proposal_hash,
  };
}

const zeroRuntime: MessageReply["ai_runtime"] = {
  currency: "USD",
  call_attempts: 1,
  tokens_in: 100,
  tokens_out: 20,
  latency_ms: 500,
  estimated_cost_microusd: 0,
  unpriced_calls: 0,
  models: [],
};

const defaultReply: MessageReply = {
  kind: "answer",
  act: "company",
  message: "Noted.",
  proposed_changes: [],
  citations: [],
  remaining_required_fields: [],
  available_action: "confirm_company",
  ai_runtime: zeroRuntime,
};

type StubOptions = {
  /** POST /company/site-reads reply; defaults to the reading snapshot. */
  startRead?: CompanySiteRead;
  /** GET of one site read; a sequence serves successive polls. */
  read?: CompanySiteRead;
  readSequence?: CompanySiteRead[];
  readStartError?: { detail: string; status: number };
  proposal?: Proposal;
  messageReply?: MessageReply;
  saveError?: { detail: string; status: number };
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(options: StubOptions = {}) {
  const calls: Request[] = [];
  let version = 0;
  let readIndex = 0;
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
      if (path.endsWith("/company/context/capabilities")) {
        return jsonResponse({
          onboarding_enabled: true,
          read_enabled: true,
          rollout: "ga",
        });
      }
      if (path.endsWith("/onboarding/state") && request.method === "GET") {
        return jsonResponse({ detail: "not started" }, 404);
      }
      if (path.endsWith("/onboarding/state") && request.method === "PUT") {
        const body = (await request.clone().json()) as Record<string, unknown>;
        version += 1;
        return jsonResponse({
          ...body,
          path: "creator",
          version,
          completed_at: null,
          created_at: "2026-07-22T08:00:00Z",
          updated_at: "2026-07-22T08:01:00Z",
        });
      }
      if (path.endsWith("/onboarding/company/proposal")) {
        return jsonResponse(
          options.proposal ??
            proposalFor(
              options.readSequence?.at(-1) ?? options.read ?? readyRead,
            ),
        );
      }
      if (
        path.endsWith("/onboarding/company/messages") &&
        request.method === "POST"
      ) {
        return jsonResponse(options.messageReply ?? defaultReply);
      }
      if (path.endsWith("/company/site-reads") && request.method === "POST") {
        if (options.readStartError) {
          return jsonResponse(
            { detail: options.readStartError.detail },
            options.readStartError.status,
          );
        }
        return jsonResponse(options.startRead ?? readingRead, 202);
      }
      if (path.includes("/company/site-reads/") && path.endsWith("/confirm")) {
        return jsonResponse(savedProfile);
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
        return jsonResponse({ detail: "no company yet" }, 404);
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

async function submitWebsite() {
  const composer = await screen.findByRole("textbox", {
    name: /Type your website address/,
  });
  await userEvent.type(composer, "gradion.com{Enter}");
}

async function chooseManual() {
  await userEvent.click(
    await screen.findByRole("button", {
      name: /I would rather tell you directly/,
    }),
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

describe("the conversational company act", () => {
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

  it("offers an honest choice between website reading and telling directly", async () => {
    stubApi();
    render(<OnboardingScreen />);

    expect(
      await screen.findByText(/Where should I start reading\?/),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /I would rather tell you directly/ }),
    ).toBeTruthy();
    expect(screen.queryByLabelText(/Company name/)).toBeNull();
  });

  it("preserves administrator typing when a newer streamed dossier arrives", async () => {
    const completedRead = {
      ...readyRead,
      profile_fields: readyRead.profile_fields.map((field) =>
        field.field === "icp"
          ? { ...field, value: "Enterprise manufacturers" }
          : field,
      ),
    } as const satisfies CompanySiteRead;
    const calls = stubApi({ readSequence: [readingRead, completedRead] });
    render(<OnboardingScreen />);

    await submitWebsite();
    await userEvent.click(
      (
        await screen.findAllByRole("button", { name: /Edit fields directly/ })
      )[0],
    );
    const icp = (await screen.findByLabelText(
      /Ideal customer/,
    )) as HTMLTextAreaElement;
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

  it("caps the fact selection the confirmation sends at the server limit", async () => {
    const calls = stubApi({
      startRead: manyFactsRead,
      read: manyFactsRead,
      proposal: proposalFor(manyFactsRead),
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    const accept = (await screen.findByRole("button", {
      name: /Accept all/,
    })) as HTMLButtonElement;
    await waitFor(() => {
      expect(accept.disabled).toBe(false);
    });
    await userEvent.click(accept);

    await waitFor(() => {
      expect(requestTo(calls, "/confirm", "POST")).toBeTruthy();
    });
    const body = (await (requestTo(calls, "/confirm", "POST") as Request)
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.selected_fact_keys).toHaveLength(100);
  });

  it("keeps the manual path available when the read cannot start", async () => {
    stubApi({
      readStartError: { detail: "site blocked automated access", status: 422 },
    });
    render(<OnboardingScreen />);

    await submitWebsite();

    expect(
      await screen.findByText("site blocked automated access"),
    ).toBeTruthy();
    await chooseManual();
  });

  it("shows a deferred read as scheduled work and keeps the manual path available", async () => {
    stubApi({
      read: {
        ...readingRead,
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

    await submitWebsite();

    expect(
      await screen.findByText(/The read is paused for now\./),
    ).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /I would rather tell you directly/ }),
    ).toBeTruthy();
  });

  it("shows exact run transparency and applies conversational suggestions only after approval", async () => {
    const runtimeRead = {
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
    } as const satisfies CompanySiteRead;
    stubApi({
      startRead: runtimeRead,
      read: runtimeRead,
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
        remaining_required_fields: [],
        ai_runtime: zeroRuntime,
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    await screen.findByText(/Finished reading\./);
    expect(screen.getByText("gemini-3.1-flash-lite-2026-07")).toBeTruthy();
    expect(screen.getByText("$0.079529")).toBeTruthy();

    const composer = screen.getByRole("textbox", {
      name: /Type your website address/,
    });
    await userEvent.type(composer, "Which industry should we use?");
    await userEvent.click(
      screen.getByRole("button", { name: /Send to Margince/ }),
    );

    expect(
      await screen.findByText(/industrial software is the clearest/),
    ).toBeTruthy();
    expect(screen.getByText("Industrial software")).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: /Apply to my draft/ }),
    );
    await userEvent.click(
      screen.getAllByRole("button", { name: /Edit fields directly/ })[0],
    );
    expect(
      ((await screen.findByLabelText(/Industry/)) as HTMLInputElement).value,
    ).toBe("Industrial software");
  });
});

describe("the mandatory company minimum", () => {
  it("saves a manually entered company without requiring a website", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);
    await screen.findByText(/Where should I start reading\?/);
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
    await screen.findByText(/Where should I start reading\?/);
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
    stubApi({ saveError: { detail: "database unavailable", status: 503 } });
    render(<OnboardingScreen />);
    await screen.findByText(/Where should I start reading\?/);
    await chooseManual();
    await completeManualInterview();

    await userEvent.clear(screen.getByLabelText(/What do you sell\?/));
    await userEvent.type(screen.getByLabelText(/What do you sell\?/), "   ");
    expect(
      screen.getByText("Fill these in before you continue: What do you sell?"),
    ).toBeTruthy();
    expect(
      (
        screen.getByRole("button", {
          name: /Confirm and save company/,
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);

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
    expect(
      (screen.getByLabelText(/What do you sell\?/) as HTMLTextAreaElement)
        .value,
    ).toBe("Revenue software");
  });
});
