/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { en } from "../../i18n/en";
import { installFetchStub, jsonResponse, type RouteMap } from "../story-utils";
import { CompanyAct } from "./company-act";
import type { ConversationState } from "./conversation-machine";
import { initialConversationState } from "./conversation-machine";

// WHERE a refused confirm says so. The board carries Continue, so the board is
// where its refusal belongs: the sentence used to render in the conversation
// rail, a scroller the reader who just pressed a button on the other pane is
// not looking at, and a press that earned a 409 therefore had no on-screen life
// at all. company-act.test.tsx pins which notice each server code produces and
// when Continue re-arms; this file pins only the one thing those cases never
// asked — that a reader can tell their press was rejected.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type ColdField = components["schemas"]["ColdStartField"];

const SITE_URL = "https://gradion.com";
const READ_ID = "018f3a1b-0000-7000-8000-0000000000e2";
const CONFIRM_PATH = `POST /company/site-reads/${READ_ID}/confirm`;

function grounded(field: ColdField["field"], value: string): ColdField {
  return {
    field,
    value,
    evidence_snippet: "seen on the site",
    source_kind: "url",
    source_url: SITE_URL,
    confidence: 0.95,
  };
}

// The three fields `confirmCompanySiteRead` 422s without. Filled, so nothing
// but the server's own refusal can hold Continue down.
const REQUIRED_TRIO: ColdField[] = [
  grounded("display_name", "Gradion"),
  grounded("offer_summary", "CRM software"),
  grounded("icp", "Mid-market B2B"),
];

const READ: CompanySiteRead = {
  id: READ_ID,
  target_kind: "onboarding",
  organization_id: null,
  root_url: SITE_URL,
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: null,
  pages_read: 1,
  pages: [{ url: SITE_URL, status: "fetched", kind: "home" }],
  profile_fields: REQUIRED_TRIO,
  facts: [],
  comparisons: [],
  people: [],
  legal_entities: [],
  warnings: [],
  draft_version: 1,
  proposal_hash: "proposal-1",
  created_at: "2026-07-22T08:00:00Z",
  updated_at: "2026-07-22T08:00:01Z",
};

function proposal(hash: string): Proposal {
  return {
    ready: true,
    // The proposal states a definite source for every field it carries, and
    // `grounded` supplies one — so the fixture asserts that rather than
    // widening the contract's own type to admit a missing URL.
    fields: REQUIRED_TRIO.map((field) => ({
      field: field.field,
      value: field.value,
      confidence: field.confidence,
      evidence_snippet: field.evidence_snippet ?? "",
      source_url: field.source_url ?? SITE_URL,
    })),
    facts: [],
    open_questions: [],
    remaining_required_fields: [],
    draft_version: READ.draft_version,
    proposal_hash: hash,
  };
}

const REVIEW_STATE: ConversationState = {
  ...initialConversationState,
  act: "company",
  phase: "co.review",
  activeReadId: READ_ID,
  readCompleted: true,
  pendingQuestion: null,
};

function renderReview(routes: RouteMap): void {
  installFetchStub({
    [`GET /company/site-reads/${READ_ID}`]: () => jsonResponse(READ),
    "GET /onboarding/company/proposal": () =>
      jsonResponse(proposal("proposal-1")),
    ...routes,
  });
  render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <CompanyAct
          state={REVIEW_STATE}
          dispatch={vi.fn()}
          profile={null}
          persist={vi.fn(async () => true)}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

// The two panes by name, so every assertion below is about WHICH of them a
// sentence landed on. Throwing rather than asserting: a missing pane means the
// shell never rendered, and every case after it would report the wrong failure.
function pane(selector: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(selector);
  if (found === null) {
    throw new Error(`the conversation shell rendered no ${selector}`);
  }
  return found;
}

/** The work surface: the review board and the Continue it carries. */
function surface(): HTMLElement {
  return pane(".mw-artifact");
}

/** The conversation rail: narration and history, and a scroller. */
function rail(): HTMLElement {
  return pane(".mw-thread");
}

async function pressContinue(): Promise<HTMLElement> {
  const button = await screen.findByRole("button", { name: /Continue/ });
  expect(button).toBeEnabled();
  fireEvent.click(button);
  return button;
}

it("says on the work surface, not in the rail, that the server refused the press", async () => {
  renderReview({
    [CONFIRM_PATH]: () =>
      jsonResponse({ title: "conflict", code: "not_confirmable" }, 409),
  });

  await pressContinue();

  const notice = await within(surface()).findByText(
    en["ob.conv.review.confirmNotReady"],
  );
  // Once, and on the pane that carries the button. A second copy in the rail
  // would leave the reader deciding which of the two is about their press.
  expect(
    screen.getAllByText(en["ob.conv.review.confirmNotReady"]),
  ).toHaveLength(1);
  expect(
    within(rail()).queryByText(en["ob.conv.review.confirmNotReady"]),
  ).toBeNull();
  // A DIRECT child of the pane's own surface, because that is what pins it:
  // conversation.css sticks `.ob-conv-artifact > .ob-conv-refusal` to the pane's
  // head, and a selector that quietly stops matching would leave the notice
  // scrolling away again with nothing failing.
  const strip = notice.closest(".ob-conv-refusal");
  expect(strip?.parentElement?.classList.contains("ob-conv-artifact")).toBe(
    true,
  );
  // An alert, because it appeared in answer to a press the reader has already
  // made and may not be looking for.
  expect(notice.closest('[role="alert"]')).not.toBeNull();
});

it("puts the one look that could end the refusal on the same surface as the refusal", async () => {
  renderReview({
    [CONFIRM_PATH]: () =>
      jsonResponse({ title: "conflict", code: "not_confirmable" }, 409),
  });

  await pressContinue();

  await within(surface()).findByText(en["ob.conv.review.confirmNotReady"]);
  // Continue is blocked here, so the re-check IS the route forward — it may
  // not sit one pane away from the sentence that tells the reader to take it.
  expect(
    await within(surface()).findByRole("button", { name: "Retry" }),
  ).toBeInTheDocument();
  expect(within(rail()).queryByRole("button", { name: "Retry" })).toBeNull();
});

it("keeps the skew sentence on the surface after the refetch re-arms Continue", async () => {
  // The whole race in the field report: the refetch lands a genuinely newer
  // draft, Continue becomes safe to press again, and the reader still has to
  // be able to see why their first press did nothing.
  let proposalCalls = 0;
  renderReview({
    "GET /onboarding/company/proposal": () => {
      proposalCalls += 1;
      return jsonResponse(
        proposal(proposalCalls === 1 ? "proposal-1" : "proposal-2"),
      );
    },
    [CONFIRM_PATH]: () =>
      jsonResponse({ title: "conflict", code: "version_skew" }, 409),
  });

  const button = await pressContinue();

  await within(surface()).findByText(en["ob.conv.review.confirmVersionSkew"]);
  await waitFor(() => expect(button).toBeEnabled());
  expect(
    within(surface()).getByText(en["ob.conv.review.confirmVersionSkew"]),
  ).toBeInTheDocument();
});
