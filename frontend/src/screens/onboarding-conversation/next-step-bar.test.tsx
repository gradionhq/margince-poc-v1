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
import { NextStepBar } from "./next-step-bar";

// The pinned next-step bar: it exists exactly while the current act has a
// blocking affordance out of view, one click brings that affordance back
// and focuses it, and it disappears when the observer reports the target
// visible. The integration half drives the real company act to the states
// the bar names.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type ColdField = components["schemas"]["ColdStartField"];

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

describe("NextStepBar", () => {
  function mountTarget(): { card: HTMLElement; option: HTMLButtonElement } {
    const card = document.createElement("fieldset");
    card.className = "ob-conv-question";
    const option = document.createElement("button");
    option.textContent = "Acme GmbH";
    card.append(option);
    document.body.append(card);
    return { card, option };
  }

  it("scrolls the target into view and focuses its first option on click", async () => {
    const { card, option } = mountTarget();
    const scrolled = vi.fn();
    card.scrollIntoView = scrolled;
    render(
      <NextStepBar
        label="1 decision open"
        targetSelector="fieldset.ob-conv-question:not([disabled])"
        revision={1}
      />,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "1 decision open" }),
    );

    expect(scrolled).toHaveBeenCalledWith(
      expect.objectContaining({ block: "center" }),
    );
    expect(document.activeElement).toBe(option);
    card.remove();
  });

  it("hides itself while the observer reports the target visible", async () => {
    const { card } = mountTarget();
    class VisibleObserver {
      constructor(
        private readonly callback: (
          entries: { isIntersecting: boolean }[],
        ) => void,
      ) {}
      observe() {
        this.callback([{ isIntersecting: true }]);
      }
      disconnect() {}
    }
    vi.stubGlobal("IntersectionObserver", VisibleObserver);
    render(
      <NextStepBar
        label="1 decision open"
        targetSelector="fieldset.ob-conv-question:not([disabled])"
        revision={1}
      />,
    );

    await waitFor(() => {
      expect(screen.queryByRole("status")).toBeNull();
    });
    card.remove();
  });
});

// ---- integration through the real company act ------------------------------

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

const readyRead = {
  id: READ_ID,
  target_kind: "onboarding",
  organization_id: null,
  root_url: "https://gradion.com",
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: null,
  pages_read: 3,
  pages: [],
  profile_fields: [
    grounded("legal_name", "Gradion GmbH", "© 2026 Gradion GmbH"),
    grounded("display_name", "Gradion", "Gradion"),
    grounded("offer_summary", "Revenue software", "We build revenue software"),
    grounded("icp", "Mid-market manufacturers", "We serve manufacturers"),
  ],
  facts: [],
  comparisons: [],
  people: [],
  legal_entities: [],
  warnings: [],
  draft_version: 2,
  proposal_hash: "proposal-2",
  created_at: "2026-07-22T08:00:00Z",
  updated_at: "2026-07-22T08:00:01Z",
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
    facts: [],
    open_questions: [],
    remaining_required_fields: [],
    draft_version: read.draft_version,
    proposal_hash: read.proposal_hash,
  };
}

function clarifyQuestion(id: string, question: string) {
  return {
    id,
    question,
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
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function stubApi(proposal: Proposal) {
  let version = 0;
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
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
        return jsonResponse(proposal);
      }
      if (path.endsWith("/company/site-reads") && request.method === "POST") {
        return jsonResponse(readyRead, 202);
      }
      if (path.includes("/company/site-reads/") && request.method === "GET") {
        return jsonResponse(readyRead);
      }
      if (path.endsWith("/company") && request.method === "GET") {
        return jsonResponse({ detail: "no company yet" }, 404);
      }
      throw new Error(`unstubbed request: ${request.method} ${request.url}`);
    }),
  );
}

async function submitWebsite() {
  const composer = await screen.findByRole("textbox", {
    name: /Type your website address/,
  });
  await userEvent.type(composer, "gradion.com{Enter}");
}

describe("the next-step bar in the company act", () => {
  it("is absent while nothing blocks", async () => {
    stubApi(proposalFor(readyRead));
    render(<OnboardingScreen />);

    await screen.findByText(/Where should I start reading\?/);
    expect(document.querySelector(".ob-conv-nextstep")).toBeNull();
  });

  it("announces the ready review when the proposal has no open questions", async () => {
    stubApi(proposalFor(readyRead));
    render(<OnboardingScreen />);

    await submitWebsite();

    expect(
      await screen.findByRole("button", { name: "Your review is ready" }),
    ).toBeTruthy();
  });

  it("counts the pending question plus the proposal's other open one", async () => {
    stubApi({
      ...proposalFor(readyRead),
      open_questions: [
        clarifyQuestion("clarify:legal_name:1", "Which legal entity?"),
        clarifyQuestion("clarify:address:2", "Which registered address?"),
      ],
    });
    render(<OnboardingScreen />);

    await submitWebsite();

    expect(
      await screen.findByRole("button", { name: "2 decisions open" }),
    ).toBeTruthy();
  });
});
