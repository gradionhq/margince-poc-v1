/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { OnboardingScreen } from "../onboarding";
import { resolutionsFromAnswers } from "./company-proposal";
import type { ConversationState } from "./conversation-machine";
import {
  conversationReducer,
  initialConversationState,
} from "./conversation-machine";
import { QuestionCard } from "./entries";

// Humans outrank the reader: every clarify carries a local dismiss escape,
// so an implausible question (page chrome glued into entity names) can never
// become an unanswerable gate in front of Accept all. Dismissal writes
// nothing; a human_conflict dismissal still sends the explicit keep_current
// resolution the server requires, while a census dismissal sends none.

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Comparison = components["schemas"]["CompanySiteReadComparison"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type ColdField = components["schemas"]["ColdStartField"];

const conflictComparison: Comparison = {
  key: "legal_name",
  value_kind: "profile_field",
  classification: "human_conflict",
  current_value: "My Own GmbH",
  current_source: "human",
  proposed_value: "Gradion GmbH",
};

describe("resolutionsFromAnswers with dismissals", () => {
  it("maps a dismissed human_conflict to the explicit keep_current the server requires", () => {
    expect(
      resolutionsFromAnswers(
        [conflictComparison],
        [
          {
            clarifyId: "clarify:legal_name:1",
            field: "legal_name",
            value: "",
            dismissed: true,
          },
        ],
      ),
    ).toEqual([{ key: "legal_name", action: "keep_current" }]);
  });

  it("sends NO resolution for a dismissed census question — the server rejects non-conflict keys", () => {
    expect(
      resolutionsFromAnswers(
        [],
        [
          {
            clarifyId: "clarify:legal_name:1",
            field: "legal_name",
            value: "",
            dismissed: true,
          },
        ],
      ),
    ).toEqual([]);
  });
});

describe("the machine's dismissal path", () => {
  const clarifying: ConversationState = {
    ...initialConversationState,
    act: "company",
    phase: "co.clarify",
    readCompleted: true,
    pendingQuestion: {
      id: "clarify-entity",
      i18nKey: "ob.conv.clarify.question",
      params: { question: "Which entity?" },
      dismissLabelKey: "ob.conv.clarify.dismiss",
      options: [{ value: "a", label: "Acme GmbH" }],
    },
  };

  it("clears the pending question through the ordinary answer path and notes the dismissal", () => {
    const next = conversationReducer(clarifying, {
      type: "QUESTION_ANSWERED",
      questionId: "clarify-entity",
      value: "",
      dismissed: true,
    });
    expect(next.pendingQuestion).toBeNull();
    expect(next.phase).toBe("co.review");
    expect(next.thread.at(-1)).toMatchObject({
      kind: "user",
      i18nKey: "ob.conv.clarify.dismiss",
    });
  });

  it("rejects a dismissal of a question that offers no escape", () => {
    const speakerAsk: ConversationState = {
      ...clarifying,
      act: "voice",
      phase: "vo.speaker",
      pendingQuestion: {
        id: "speaker",
        i18nKey: "ob.conv.voice.speakerQuestion",
        options: [{ value: "Speaker 1", label: "Speaker 1" }],
      },
    };
    expect(
      conversationReducer(speakerAsk, {
        type: "QUESTION_ANSWERED",
        questionId: "speaker",
        value: "",
        dismissed: true,
      }),
    ).toBe(speakerAsk);
  });
});

describe("option chip clamping", () => {
  it("is presentation-only: the full value stays the accessible name and title", () => {
    const garbage =
      "Gradion GmbH Imprint Privacy Cookie Settings Accept All Continue Reading Hauptstrasse 1";
    rtlRender(
      <LocaleProvider initial="en">
        <QuestionCard
          question={{
            id: "q",
            i18nKey: "ob.conv.clarify.question",
            params: { question: "Which entity?" },
            options: [{ value: "g", label: garbage }],
          }}
          onAnswer={() => undefined}
        />
      </LocaleProvider>,
    );
    const chip = screen.getByRole("button", { name: garbage });
    expect(chip.title).toBe(garbage);
    expect(chip.className).toContain("ob-conv-option");
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

function readyRead(comparisons: Comparison[]): CompanySiteRead {
  return {
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
      grounded(
        "offer_summary",
        "Revenue software",
        "We build revenue software",
      ),
      grounded("icp", "Mid-market manufacturers", "We serve manufacturers"),
    ],
    facts: [],
    comparisons,
    people: [],
    legal_entities: [],
    warnings: [],
    draft_version: 2,
    proposal_hash: "proposal-2",
    created_at: "2026-07-22T08:00:00Z",
    updated_at: "2026-07-22T08:00:01Z",
  };
}

function proposalWithQuestion(read: CompanySiteRead): Proposal {
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
    open_questions: [
      {
        id: "clarify:legal_name:1",
        question: "Which legal entity is this installation for?",
        field: "legal_name",
        options: [
          {
            value: "Gradion GmbH Imprint Privacy Cookie Settings",
            label: "Gradion GmbH Imprint Privacy Cookie Settings",
            evidence_url: "https://gradion.com/impressum",
            evidence_snippet: "chrome glued into the entity name",
            detail: null,
          },
        ],
        allow_free_text: false,
      },
    ],
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

function stubApi(read: CompanySiteRead, proposal: Proposal) {
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
      if (path.includes("/company/site-reads/") && path.endsWith("/confirm")) {
        return jsonResponse({
          organization_id: "018f3a1b-0000-7000-8000-0000000000a1",
          display_name: "Gradion",
        });
      }
      if (path.endsWith("/company/site-reads") && request.method === "POST") {
        return jsonResponse(read, 202);
      }
      if (path.includes("/company/site-reads/") && request.method === "GET") {
        return jsonResponse(read);
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

async function submitWebsite() {
  const composer = await screen.findByRole("textbox", {
    name: /Type your website address/,
  });
  await userEvent.type(composer, "gradion.com{Enter}");
}

async function confirmBody(calls: Request[]): Promise<Record<string, unknown>> {
  const writes = calls.filter(
    (request) => request.url.includes("/confirm") && request.method === "POST",
  );
  expect(writes.length).toBe(1);
  return (await writes[0].clone().json()) as Record<string, unknown>;
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

describe("dismissing a clarify in the company act", () => {
  it("unblocks Accept all and the next-step bar, and the census confirm sends no resolution", async () => {
    const read = readyRead([]);
    const calls = stubApi(read, proposalWithQuestion(read));
    render(<OnboardingScreen />);

    await submitWebsite();
    expect(
      await screen.findByText(/Which legal entity is this installation for\?/),
    ).toBeTruthy();
    expect(
      await screen.findByRole("button", { name: "1 decision open" }),
    ).toBeTruthy();

    await userEvent.click(
      screen.getByRole("button", { name: "Skip this - I will set it myself" }),
    );

    // The dismissal is noted, the gate opens, nothing was written.
    const accept = (await screen.findByRole("button", {
      name: /Accept all/,
    })) as HTMLButtonElement;
    expect(accept.disabled).toBe(false);
    expect(
      await screen.findByRole("button", { name: "Your review is ready" }),
    ).toBeTruthy();
    expect(screen.getByText(/You skipped: Registered legal name/)).toBeTruthy();

    await userEvent.click(accept);
    const body = await confirmBody(calls);
    expect(body.resolutions).toEqual([]);
    expect(await screen.findByText(/Company profile confirmed/)).toBeTruthy();
  });

  it("labels a human_conflict dismissal as keeping the value and sends keep_current", async () => {
    const read = readyRead([conflictComparison]);
    const calls = stubApi(read, proposalWithQuestion(read));
    render(<OnboardingScreen />);

    await submitWebsite();
    await screen.findByText(/Which legal entity is this installation for\?/);

    await userEvent.click(
      await screen.findByRole("button", { name: "Keep my value" }),
    );

    const accept = (await screen.findByRole("button", {
      name: /Accept all/,
    })) as HTMLButtonElement;
    expect(accept.disabled).toBe(false);
    await userEvent.click(accept);

    const body = await confirmBody(calls);
    expect(body.resolutions).toEqual([
      { key: "legal_name", action: "keep_current" },
    ]);
  });
});
