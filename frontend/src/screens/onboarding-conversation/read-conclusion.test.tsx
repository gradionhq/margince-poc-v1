/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { OnboardingScreen } from "../onboarding";

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type ColdField = components["schemas"]["ColdStartField"];

const READ_ID = "018f3a1b-0000-7000-8000-0000000000c3";

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

const baseRead = {
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

const midRead: CompanySiteRead = {
  ...baseRead,
  pages_read: 20,
  draft_version: 2,
  profile_fields: [
    grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
    grounded("display_name", "Gradion", "Gradion"),
  ],
};

const partialRead: CompanySiteRead = {
  ...baseRead,
  status: "partial",
  phase: null,
  pages_read: 40,
  draft_version: 3,
  proposal_hash: "proposal-3",
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
};

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
    facts: [],
    open_questions: [],
    remaining_required_fields: [],
    draft_version: read.draft_version,
    proposal_hash: read.proposal_hash,
  };
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(pollSequence: (CompanySiteRead | number)[]) {
  const calls: Request[] = [];
  let version = 0;
  let poll = 0;
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
        return jsonResponse(proposalFor(partialRead));
      }
      if (
        path.endsWith("/onboarding/company/messages") &&
        request.method === "POST"
      ) {
        return jsonResponse({
          kind: "answer",
          act: "company",
          message: "Noted.",
          proposed_changes: [],
          citations: [],
          remaining_required_fields: [],
          available_action: "confirm_company",
          ai_runtime: {
            currency: "USD",
            call_attempts: 1,
            tokens_in: 100,
            tokens_out: 20,
            latency_ms: 500,
            estimated_cost_microusd: 0,
            unpriced_calls: 0,
            models: [],
          },
        });
      }
      if (path.endsWith("/company/site-reads") && request.method === "POST") {
        return jsonResponse(baseRead, 202);
      }
      if (path.includes("/company/site-reads/") && request.method === "GET") {
        const snapshot = pollSequence[Math.min(poll, pollSequence.length - 1)];
        poll += 1;
        // A number in the sequence is an error status for that poll.
        if (typeof snapshot === "number") {
          return jsonResponse({ detail: "poll blew up" }, snapshot);
        }
        return jsonResponse(snapshot);
      }
      if (path.endsWith("/company") && request.method === "GET") {
        return jsonResponse({ detail: "no company yet" }, 404);
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

// Pins the read-conclusion ordering contract end to end (see the conclude
// effect in use-company-read.ts): a completed read whose proposal has ZERO
// open questions must always reach co.review with the confirm card — via
// multi-snapshot polling, with chat interleaved, and across a poll failure
// that recovers into the terminal (the round-2 re-arm).
describe("the read conclusion ordering contract", () => {
  it("a multi-poll partial terminal with zero open questions reaches the confirm card", async () => {
    stubApi([midRead, midRead, partialRead]);
    render(<OnboardingScreen />);
    const composer = await screen.findByRole("textbox", {
      name: /Type your website address/,
    });
    await userEvent.type(composer, "gradion.com{Enter}");

    expect(
      await screen.findByText(/I could not read everything/, undefined, {
        timeout: 8000,
      }),
    ).toBeTruthy();
    expect(
      await screen.findByRole(
        "button",
        { name: /Accept all/ },
        {
          timeout: 8000,
        },
      ),
    ).toBeTruthy();
  }, 20000);

  it("chat during the read does not stall the conclusion", async () => {
    stubApi([midRead, midRead, midRead, midRead, partialRead]);
    render(<OnboardingScreen />);
    const composer = await screen.findByRole("textbox", {
      name: /Type your website address/,
    });
    await userEvent.type(composer, "gradion.com{Enter}");
    await screen.findByText(/Reading gradion\.com now/);
    await userEvent.type(composer, "what did you find so far?{Enter}");
    expect(await screen.findByText("Noted.")).toBeTruthy();

    expect(
      await screen.findByRole(
        "button",
        { name: /Accept all/ },
        {
          timeout: 8000,
        },
      ),
    ).toBeTruthy();
  }, 20000);

  it("a poll error mid-read that recovers into the terminal still reaches review", async () => {
    stubApi([midRead, 500, partialRead]);
    render(<OnboardingScreen />);
    const composer = await screen.findByRole("textbox", {
      name: /Type your website address/,
    });
    await userEvent.type(composer, "gradion.com{Enter}");

    expect(
      await screen.findByRole(
        "button",
        { name: /Accept all/ },
        {
          timeout: 8000,
        },
      ),
    ).toBeTruthy();
  }, 20000);
});
