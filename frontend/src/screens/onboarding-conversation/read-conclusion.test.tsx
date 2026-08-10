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
  // A page the crawl genuinely could not reach — the case most likely to
  // regress into a rail bubble again. Coverage detail belongs to the
  // review's own CoverageCard, never restated in the conversation.
  pages: [
    ...baseRead.pages,
    { url: "https://gradion.com/team", status: "skipped", reason: "robots" },
  ],
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

function stubApi(
  pollSequence: (CompanySiteRead | number)[],
  { wizardStateWritable = true }: { wizardStateWritable?: boolean } = {},
) {
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
        if (!wizardStateWritable) {
          return jsonResponse({ detail: "members begin at Voice" }, 422);
        }
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
  // The read skipped a page (robots-blocked) and still reaches the confirm
  // card with no rail commentary about it at all: the coverage detail lives
  // on the review's own CoverageCard, and the rail never restates it.
  it("a multi-poll partial terminal that skipped a page reaches the confirm card with no outcome bubble", async () => {
    stubApi([midRead, midRead, partialRead]);
    render(<OnboardingScreen />);
    const composer = await screen.findByRole("textbox", {
      name: /Your website address/,
    });
    await userEvent.type(composer, "gradion.com{Enter}");

    expect(
      await screen.findByRole(
        "button",
        { name: /Continue/ },
        {
          timeout: 8000,
        },
      ),
    ).toBeTruthy();
    expect(screen.queryByText(/I could not read/)).toBeNull();
  }, 20000);

  // The read now runs on its own full-screen stage with no composer, so a
  // question cannot be asked mid-crawl — the tree's own ob.ai.readFirst rule
  // already says an answer given before the evidence lands is the wrong answer.
  // What still has to hold is the part that was genuinely at risk: a long run
  // whose snapshots keep arriving must converge on the review rather than
  // stalling as the poll count grows. The rail itself never grows a composer
  // back — the review's own Continue is the surface's action, not the
  // rail's.
  it("a long multi-snapshot run converges on the review", async () => {
    stubApi([midRead, midRead, midRead, midRead, partialRead]);
    render(<OnboardingScreen />);
    await userEvent.type(
      await screen.findByRole("textbox", { name: /Your website address/ }),
      "gradion.com{Enter}",
    );

    // While it runs, the theatre is the surface and it reports the polled run.
    expect(
      await screen.findByRole("heading", { level: 1, name: /Reading gradion/ }),
    ).toBeTruthy();

    expect(
      await screen.findByRole(
        "button",
        { name: /Continue/ },
        {
          timeout: 8000,
        },
      ),
    ).toBeTruthy();
    expect(document.querySelector(".mw-composer")).toBeNull();
  }, 20000);

  it("a poll error mid-read that recovers into the terminal still reaches review", async () => {
    stubApi([midRead, 500, partialRead]);
    render(<OnboardingScreen />);
    const composer = await screen.findByRole("textbox", {
      name: /Your website address/,
    });
    await userEvent.type(composer, "gradion.com{Enter}");

    expect(
      await screen.findByRole(
        "button",
        { name: /Continue/ },
        {
          timeout: 8000,
        },
      ),
    ).toBeTruthy();
  }, 20000);

  // The wizard-state write is best-effort by design, so the proposal join can
  // be lost BEFORE the read finishes — the failure lands early, the terminal
  // lands minutes later, and nothing the conclude effect watches has changed
  // in between. The snapshot fallback has to reach the review anyway; without
  // it the read completes on the server and the user waits on "opening your
  // review" until they reload.
  it("a read whose wizard-state join failed before the terminal still reaches review", async () => {
    stubApi([midRead, midRead, partialRead], { wizardStateWritable: false });
    render(<OnboardingScreen />);
    await userEvent.type(
      await screen.findByRole("textbox", { name: /Your website address/ }),
      "gradion.com{Enter}",
    );

    expect(
      await screen.findByRole(
        "button",
        { name: /Continue/ },
        { timeout: 8000 },
      ),
    ).toBeTruthy();
  }, 20000);
});
