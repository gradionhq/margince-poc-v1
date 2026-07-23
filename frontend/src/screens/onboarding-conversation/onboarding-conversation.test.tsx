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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { OnboardingScreen } from "../onboarding";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type MessageReply = components["schemas"]["OnboardingCompanyMessageReply"];
type ColdField = components["schemas"]["ColdStartField"];

const READ_ID = "018f3a1b-0000-7000-8000-0000000000c3";

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
  startRead?: CompanySiteRead;
  read?: CompanySiteRead;
  proposal?: Proposal;
  /** Error status for GET /onboarding/company/proposal (resilience tests). */
  proposalStatus?: number;
  /** Error status for the site-read poll GET (resilience tests). */
  pollStatus?: number;
  messageReply?: MessageReply;
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
        if (options.proposalStatus !== undefined) {
          return jsonResponse(
            { detail: "no proposal" },
            options.proposalStatus,
          );
        }
        return jsonResponse(options.proposal ?? proposalFor(readyRead));
      }
      if (
        path.endsWith("/onboarding/company/messages") &&
        request.method === "POST"
      ) {
        return jsonResponse(options.messageReply ?? defaultReply);
      }
      if (path.endsWith("/company/site-reads") && request.method === "POST") {
        return jsonResponse(options.startRead ?? readingRead, 202);
      }
      if (path.includes("/company/site-reads/") && path.endsWith("/confirm")) {
        return jsonResponse(savedProfile);
      }
      if (path.includes("/company/site-reads/") && request.method === "GET") {
        if (options.pollStatus !== undefined) {
          return jsonResponse(
            { detail: "read fetch failed" },
            options.pollStatus,
          );
        }
        return jsonResponse(options.read ?? readyRead);
      }
      if (path.endsWith("/company") && request.method === "GET") {
        return jsonResponse({ detail: "no company yet" }, 404);
      }
      if (path.endsWith("/company") && request.method === "PUT") {
        return jsonResponse(savedProfile);
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

async function submitWebsite() {
  const composer = await screen.findByRole("textbox", {
    name: /Type your website address/,
  });
  await userEvent.type(composer, "gradion.com{Enter}");
}

function confirmCardElement(): HTMLElement {
  const card = document.querySelector(".ob-conv-confirm");
  expect(card).not.toBeNull();
  return card as HTMLElement;
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

describe("the conversational company act (behind the flag)", () => {
  it("narrates poll deltas as thread entries before the terminal outcome", async () => {
    stubApi();
    render(<OnboardingScreen />);

    await submitWebsite();

    expect(await screen.findByText(/Reading gradion\.com now/)).toBeTruthy();
    const learned = await screen.findByText(
      /Learned Registered legal name: Gradion GmbH/,
    );
    const outcome = await screen.findByText(
      /Finished reading\. Findings with sources: 4\./,
    );
    // Progress narration lands strictly before the outcome in the thread.
    expect(
      learned.compareDocumentPosition(outcome) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(
      await screen.findByText(
        /Learned Ideal customer profile|Learned Ideal customer/,
      ),
    ).toBeTruthy();
  });

  it("asks the proposal's open question and answering posts the authorizing selected_option", async () => {
    const entityClarify = {
      id: "clarify:legal_name:2",
      question: "Which legal entity is this installation for?",
      field: "legal_name",
      options: [
        {
          value: "Gradion GmbH",
          label: "Gradion GmbH",
          evidence_url: "https://gradion.com/impressum",
          evidence_snippet: "Gradion GmbH, Berlin",
          detail: null,
        },
        {
          value: "Gradion Holding GmbH",
          label: "Gradion Holding GmbH",
          evidence_url: "https://gradion.com/impressum",
          evidence_snippet: "Gradion Holding GmbH, Berlin",
          detail: null,
        },
      ],
      allow_free_text: false,
    };
    const calls = stubApi({
      proposal: { ...proposalFor(readyRead), open_questions: [entityClarify] },
      messageReply: {
        ...defaultReply,
        kind: "clarification",
        proposed_changes: [
          {
            field: "legal_name",
            value: "Gradion Holding GmbH",
            reason: "You chose this entity.",
          },
          {
            field: "industry",
            value: "Nonsense the selection never authorized",
            reason: "model overreach",
          },
        ],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();
    expect(
      await screen.findByText(/Which legal entity is this installation for\?/),
    ).toBeTruthy();

    await userEvent.click(
      screen.getByRole("button", { name: /Gradion Holding GmbH/ }),
    );

    await waitFor(() => {
      expect(
        requestsTo(calls, "/onboarding/company/messages", "POST").length,
      ).toBeGreaterThan(0);
    });
    const body = (await requestsTo(
      calls,
      "/onboarding/company/messages",
      "POST",
    )[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.act).toBe("company");
    expect(body.selected_option).toEqual({
      clarify_id: "clarify:legal_name:2",
      field: "legal_name",
      value: "Gradion Holding GmbH",
    });

    // Only the selection-authorized change lands in the review; the model's
    // extra proposal never auto-applies.
    await screen.findByText(/Company profile, prepared from sources/);
    const card = confirmCardElement();
    await waitFor(() => {
      expect(within(card).getByText("Gradion Holding GmbH")).toBeTruthy();
    });
    expect(
      screen.queryByText(/Nonsense the selection never authorized/),
    ).toBeNull();
  });

  it("disables Accept all while required fields are missing and says which", async () => {
    const thinRead = {
      ...readyRead,
      profile_fields: [
        grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
      ],
    } satisfies CompanySiteRead;
    stubApi({
      read: thinRead,
      proposal: {
        ...proposalFor(thinRead),
        remaining_required_fields: ["display_name", "offer_summary", "icp"],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();

    const accept = (await screen.findByRole("button", {
      name: /Accept all/,
    })) as HTMLButtonElement;
    expect(accept.disabled).toBe(true);
    expect(await screen.findByText(/I still need:/)).toBeTruthy();
  });

  it("confirms with the proposal's draft_version and proposal_hash", async () => {
    const calls = stubApi();
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
      expect(requestsTo(calls, "/confirm", "POST").length).toBe(1);
    });
    const body = (await requestsTo(calls, "/confirm", "POST")[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.draft_version).toBe(2);
    expect(body.proposal_hash).toBe("proposal-2");
    // The composer-typed bare domain reaches the profile as the canonical
    // URL, exactly like the classic form's website field.
    expect((body.profile as Record<string, unknown>).website).toBe(
      "https://gradion.com",
    );
    expect(await screen.findByText(/Company profile confirmed/)).toBeTruthy();
    // The machine advanced into the voice act invitation.
    expect(
      await screen.findByText(/Want me to learn how you write\?/),
    ).toBeTruthy();
  });

  it("persists wizard state on read start so the proposal endpoint can join", async () => {
    const calls = stubApi();
    render(<OnboardingScreen />);

    await submitWebsite();

    await waitFor(() => {
      expect(requestsTo(calls, "/onboarding/state", "PUT").length).toBe(1);
    });
    const body = (await requestsTo(calls, "/onboarding/state", "PUT")[0]
      .clone()
      .json()) as Record<string, unknown>;
    expect(body.site_read_id).toBe(READ_ID);
    expect(body.source_mode).toBe("website");
    expect(body.step).toBe("read");
    expect(body.website_url).toBe("https://gradion.com");
  });

  it("still concludes and reviews from the snapshot when the proposal fails", async () => {
    stubApi({ proposalStatus: 404 });
    render(<OnboardingScreen />);

    await submitWebsite();

    // The act never stalls: one honest turn, the outcome, and a review card
    // built from the site-read snapshot itself.
    expect(
      await screen.findByText(/I could not load the prepared mapping/),
    ).toBeTruthy();
    expect(
      await screen.findByText(/Finished reading\. Findings with sources: 4\./),
    ).toBeTruthy();
    expect(
      await screen.findByRole("button", { name: /Accept all/ }),
    ).toBeTruthy();
    expect(within(confirmCardElement()).getByText("Gradion GmbH")).toBeTruthy();
  });

  it("concludes as failed with the manual path when the poll keeps erroring", async () => {
    stubApi({ pollStatus: 500 });
    render(<OnboardingScreen />);

    await submitWebsite();

    // The act never sits silent: one honest turn, the failed outcome, and
    // the manual fallback back on offer.
    expect(
      await screen.findByText(/I lost the connection while reading/),
    ).toBeTruthy();
    expect(await screen.findByText(/I could not read that site/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /I would rather tell you directly/ }),
    ).toBeTruthy();
  });

  it("never renders a proposal field without evidence in the confirm card", async () => {
    stubApi({
      proposal: {
        ...proposalFor(readyRead),
        fields: [
          ...(proposalFor(readyRead).fields ?? []),
          {
            field: "parent_company",
            value: "Umbrella Holding AG",
            confidence: 0.7,
            evidence_snippet: "",
            source_url: "https://gradion.com",
          },
        ],
      },
    });
    render(<OnboardingScreen />);

    await submitWebsite();

    await screen.findByText(/Company profile, prepared from sources/);
    const card = confirmCardElement();
    expect(within(card).getByText("Gradion GmbH")).toBeTruthy();
    expect(within(card).queryByText("Umbrella Holding AG")).toBeNull();
  });
});

describe("the flag gate", () => {
  it("renders the untouched classic coordinator when the flag is off", async () => {
    window.localStorage.removeItem("margince.conv");
    stubApi();
    render(<OnboardingScreen />);

    expect(
      await screen.findByRole("button", { name: /Read my website/ }),
    ).toBeTruthy();
    expect(screen.queryByRole("log")).toBeNull();
  });
});
