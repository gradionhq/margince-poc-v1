/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../../api/schema";
import { LocaleProvider } from "../../i18n";
import { installFetchStub, jsonResponse, type RouteMap } from "../story-utils";
import { CompanyAct } from "./company-act";
import type {
  ConversationQuestion,
  ConversationState,
} from "./conversation-machine";
import { initialConversationState } from "./conversation-machine";

// A live decision owns the whole work surface (DecisionScene): the rail
// beside it is a narrator, never a second copy of the same choice. This is
// the one invariant this suite guards — every way a "question" thread entry
// can reach the rail is swept here, not just the machine's current
// pendingQuestion.

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function entityQuestion(id: string): ConversationQuestion {
  return {
    id,
    i18nKey: "ob.conv.clarify.question",
    params: {
      question:
        "The legal notice names more than one legal entity. Which one is your company?",
    },
    dismissLabelKey: "ob.conv.clarify.dismiss",
    options: [
      { value: "Gradion GmbH", label: "Gradion GmbH" },
      { value: "Gradion Holding GmbH", label: "Gradion Holding GmbH" },
    ],
  };
}

function renderCompanyAct(state: ConversationState) {
  installFetchStub({});
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <CompanyAct
          state={state}
          dispatch={vi.fn()}
          profile={null}
          persist={vi.fn(async () => true)}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

it("shows a live legal-entity decision on the surface, never as a QuestionCard in the rail", () => {
  const live = entityQuestion("clarify:legal_name:3");
  renderCompanyAct({
    ...initialConversationState,
    act: "company",
    phase: "co.clarify",
    activeReadId: null,
    readCompleted: true,
    pendingQuestion: live,
    thread: [
      { kind: "question", id: "question:clarify:legal_name:3", question: live },
    ],
    seq: 1,
  });

  // The scene: one heading, one radio per candidate.
  expect(
    screen.getByRole("heading", { level: 2, name: /legal entity/ }),
  ).toBeInTheDocument();
  expect(
    screen.getAllByRole("radio", { name: "Gradion Holding GmbH" }),
  ).toHaveLength(1);

  // No fieldset-based question card reaches the rail while the scene owns
  // this decision — the candidate list lives on the surface, once.
  expect(document.querySelectorAll(".ob-conv-question")).toHaveLength(0);
});

it("keeps a superseded, never-answered re-ask out of the rail once a fresh one takes over", () => {
  // The server re-issues a clarify with a new id across a background poll
  // (a new draft version); the machine appends the new question without
  // retiring the old thread entry, which is exactly the entry that must
  // never render as a second, disabled copy of the same candidate list.
  const stale = entityQuestion("clarify:legal_name:2");
  const live = entityQuestion("clarify:legal_name:3");
  renderCompanyAct({
    ...initialConversationState,
    act: "company",
    phase: "co.clarify",
    activeReadId: null,
    readCompleted: true,
    pendingQuestion: live,
    thread: [
      {
        kind: "question",
        id: "question:clarify:legal_name:2",
        question: stale,
      },
      {
        kind: "question",
        id: "question:clarify:legal_name:3",
        question: live,
      },
    ],
    seq: 2,
  });

  expect(screen.getAllByRole("radio", { name: "Gradion GmbH" })).toHaveLength(
    1,
  );
  // The stale re-ask's own candidate list must not survive as a rail card,
  // answered or not — its answer can never be recorded, so an inert card
  // would be a dead end that looks exactly like the live one.
  expect(document.querySelectorAll(".ob-conv-question")).toHaveLength(0);
  expect(
    screen.queryAllByRole("button", { name: "Gradion Holding GmbH" }),
  ).toHaveLength(0);
});

it("carries no composer or free-text control in the rail during manual entry — typed fields stay on the surface", () => {
  renderCompanyAct({
    ...initialConversationState,
    act: "company",
    phase: "co.manual",
    activeReadId: null,
    readCompleted: false,
  });

  const rail = document.querySelector(".mw-thread");
  expect(rail).not.toBeNull();
  // The manual form's own fields are real textboxes — on the SURFACE
  // (.mw-artifact), never inside the rail this query is scoped to.
  expect(within(rail as HTMLElement).queryAllByRole("textbox")).toHaveLength(0);
  expect(document.querySelector(".mw-composer")).toBeNull();
});

// The rail's to-do list during co.review: it must name exactly what the
// review board itself counts as outstanding, no more and no fewer — the bug
// this guards was two surfaces reading the same draft through two different
// ideas of "needs attention".

type CompanySiteRead = components["schemas"]["CompanySiteRead"];
type Proposal = components["schemas"]["OnboardingCompanyProposal"];
type ProposalField = components["schemas"]["OnboardingCompanyProposalField"];
type ColdField = components["schemas"]["ColdStartField"];

const REVIEW_READ_ID = "018f3a1b-0000-7000-8000-0000000000e1";

// The wire's own `field` is a bare string on ProposalField (the proposal
// endpoint names any field), but ColdStartField's is the closed literal
// union — so a fixture built to satisfy BOTH shapes has to start from the
// narrow one and widen, never the other way around.
type FieldFixture = Readonly<{
  field: ColdField["field"];
  value: string;
  confidence: number;
}>;

function proposedField(
  field: ColdField["field"],
  value: string,
  confidence: number,
): FieldFixture {
  return { field, value, confidence };
}

function toProposalField(fixture: FieldFixture): ProposalField {
  return {
    field: fixture.field,
    value: fixture.value,
    confidence: fixture.confidence,
    evidence_snippet: "seen on the site",
    source_url: "https://gradion.com",
  };
}

function toColdField(fixture: FieldFixture): ColdField {
  return {
    field: fixture.field,
    value: fixture.value,
    evidence_snippet: "seen on the site",
    source_kind: "url",
    source_url: "https://gradion.com",
    confidence: fixture.confidence,
  };
}

const REVIEW_READ: CompanySiteRead = {
  id: REVIEW_READ_ID,
  target_kind: "onboarding",
  organization_id: null,
  root_url: "https://gradion.com",
  status: "ready",
  status_code: null,
  status_detail: null,
  next_attempt_at: null,
  phase: null,
  pages_read: 3,
  pages: [{ url: "https://gradion.com", status: "fetched", kind: "home" }],
  profile_fields: [],
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

// One field left high-confidence or human-typed in every group (so each
// section has a settled row too), the rest spread across the states the
// rail must fold into its two buckets: no value (required or optional) and
// a weak-confidence value worth a second look.
const REVIEW_FIELDS: readonly FieldFixture[] = [
  proposedField("display_name", "Gradion", 0.95),
  proposedField("industry", "B2B software", 0.6),
  proposedField("history", "Founded 2019", 0.9),
  // legal_name, registered_address, register_vat: left blank on purpose.
  proposedField("value_proposition", "Faster onboarding", 0.9),
  proposedField("usp", "AI-native from day one", 0.9),
  // offer_summary: left blank on purpose (also required).
  proposedField("customer_pains", "Manual onboarding takes weeks", 0.9),
  proposedField("buying_center", "Ops and RevOps leads", 0.4),
  // icp: left blank on purpose (also required); desired_outcomes too.
  proposedField("buying_intents", "Evaluating CRM replacements", 0.9),
  proposedField("common_objections", "Migration risk", 0.9),
  proposedField("sales_motion", "Sales-assisted", 0.9),
];

function reviewProposal(
  fields: readonly FieldFixture[],
  openQuestions: Proposal["open_questions"] = [],
): Proposal {
  return {
    ready: true,
    fields: fields.map(toProposalField),
    facts: [],
    open_questions: openQuestions,
    remaining_required_fields: [],
    draft_version: REVIEW_READ.draft_version,
    proposal_hash: REVIEW_READ.proposal_hash,
  };
}

// The read's own profile_fields is what actually prefills the draft
// (`useCompanyRead`'s `handleSnapshot` → `prefill`); the proposal endpoint
// only supplies confidence and evidence for a value the draft already
// carries. A row only reads as filled if BOTH agree, so every scenario below
// builds the read's snapshot from the exact same field list as the proposal.
function reviewRoutes(
  fields: readonly FieldFixture[],
  proposal: Proposal,
): RouteMap {
  const read: CompanySiteRead = {
    ...REVIEW_READ,
    profile_fields: fields.map(toColdField),
  };
  return {
    [`GET /company/site-reads/${REVIEW_READ_ID}`]: () => jsonResponse(read),
    "GET /onboarding/company/proposal": () => jsonResponse(proposal),
  };
}

function renderReview(
  state: ConversationState,
  fields: readonly FieldFixture[],
  openQuestions: Proposal["open_questions"] = [],
) {
  installFetchStub(reviewRoutes(fields, reviewProposal(fields, openQuestions)));
  return render(
    <QueryClientProvider
      client={
        new QueryClient({ defaultOptions: { queries: { retry: false } } })
      }
    >
      <LocaleProvider initial="en">
        <CompanyAct
          state={state}
          dispatch={vi.fn()}
          profile={null}
          persist={vi.fn(async () => true)}
        />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

const REVIEW_STATE: ConversationState = {
  ...initialConversationState,
  act: "company",
  phase: "co.review",
  activeReadId: REVIEW_READ_ID,
  readCompleted: true,
  pendingQuestion: null,
};

describe("the rail's review to-do list", () => {
  it("carries exactly as many items as the board's own section tallies add up to", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS);

    await screen.findByRole("heading", { level: 2, name: /Correct me/ });
    // The board's own nav names every outstanding field as its own
    // `.ob-triage-nav-item`, blocking and advisory alike (only the badge
    // above them is blocking-only now) — the same two-tier split the rail's
    // list makes, so the count of named fields is the one number both
    // surfaces must agree on.
    const nav = document.querySelector(".ob-triage-nav") as HTMLElement;
    const boardTotal = nav.querySelectorAll(".ob-triage-nav-item").length;

    const items = document.querySelectorAll(".ob-conv-attention li");
    expect(items).toHaveLength(boardTotal);
    // The scenario's own arithmetic (0 blocking + 4 advisory in identity, 1
    // blocking in offer, 1 blocking + 2 advisory in customer, 0 in sales):
    // pinned so a change to the fixture above cannot silently stop
    // exercising the invariant.
    expect(boardTotal).toBe(8);
  });

  // The board's own nav badges (`.ob-triage-nav-badge b`, confirm-card.tsx)
  // already sum to `missingRequired.length` — the exact same required-empty
  // rows the rail calls "blocks confirm" (both read the same predicate,
  // `isRequired(field) && value === ""`). Two surfaces, one number: if
  // either drifts from `isRequired`/REQUIRED_FIELDS, this fails.
  it("counts exactly as many blocking rows as the nav's own blocking badges", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS);
    await screen.findByRole("heading", { level: 2, name: /Correct me/ });

    const nav = document.querySelector(".ob-triage-nav") as HTMLElement;
    const boardBlocking = [
      ...nav.querySelectorAll(".ob-triage-nav-badge[data-blocking='true'] b"),
    ].reduce((sum, badge) => sum + Number(badge.textContent), 0);
    const railBlocking = document.querySelectorAll(
      ".ob-conv-attention button[data-kind='blocks']",
    );
    expect(railBlocking).toHaveLength(boardBlocking);
    // offer_summary and icp are the only two REQUIRED_FIELDS left empty in
    // the fixture; industry/buying_center are merely weak-confidence, not
    // required, so they must not count as blocking.
    expect(boardBlocking).toBe(2);
  });

  it("sorts the blocking rows before decisions, and decisions before the merely advisory ones", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS, [
      {
        id: "clarify:register_vat:1",
        field: "register_vat",
        question: "Which VAT ID is current?",
        options: [],
      },
    ]);
    await screen.findByText("needs a decision");

    const kinds = [
      ...document.querySelectorAll(".ob-conv-attention button"),
    ].map((button) => button.getAttribute("data-kind"));
    const lastBlocks = kinds.lastIndexOf("blocks");
    const decision = kinds.indexOf("decision");
    const firstAdvisory = kinds.findIndex(
      (kind) => kind === "empty" || kind === "check",
    );
    expect(lastBlocks).toBeGreaterThan(-1);
    expect(decision).toBeGreaterThan(-1);
    expect(firstAdvisory).toBeGreaterThan(-1);
    expect(lastBlocks).toBeLessThan(decision);
    expect(decision).toBeLessThan(firstAdvisory);
  });

  it("folds a still-open clarify decision into the same list, alongside the fields", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS, [
      {
        id: "clarify:register_vat:1",
        field: "register_vat",
        question: "Which VAT ID is current?",
        options: [],
      },
    ]);
    // The board itself renders from the same proposal fetch and settles
    // first via the read-only fallback (no open questions yet); waiting for
    // the decision button itself is waiting for the REAL proposal, the one
    // that actually carries the still-open clarify.
    await screen.findByText("needs a decision");

    expect(
      document.querySelectorAll(
        ".ob-conv-attention button[data-kind='decision']",
      ),
    ).toHaveLength(1);
    expect(document.querySelectorAll(".ob-conv-attention li")).toHaveLength(9);
  });

  it("names the header whenever the list itself renders", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS);
    await screen.findByRole("heading", { level: 2, name: /Correct me/ });

    const attention = document.querySelector(
      ".ob-conv-attention",
    ) as HTMLElement;
    expect(attention).not.toBeNull();
    expect(
      within(attention).getByRole("heading", {
        name: "These need your input",
      }),
    ).toBeInTheDocument();
  });

  it("renders no blocking styling once nothing left blocks confirm", async () => {
    // Fill icp too (offer_summary is filled by the base fixture below): one
    // field, offer_summary, is left the ONLY thing that still blocks — the
    // one case that may ever paint the blocking colour.
    renderReview(
      REVIEW_STATE,
      REVIEW_FIELDS.concat([proposedField("icp", "Mid-market B2B", 0.9)]),
    );
    await screen.findByRole("heading", { level: 2, name: /Correct me/ });
    expect(
      document.querySelectorAll(".ob-conv-attention [data-kind='blocks']"),
    ).toHaveLength(1);

    // Now fill offer_summary too: nothing left blocks, so the whole blocking
    // group — heading, red dot and all — must be absent, not merely empty.
    cleanup();
    renderReview(
      REVIEW_STATE,
      REVIEW_FIELDS.concat([
        proposedField("icp", "Mid-market B2B", 0.9),
        proposedField("offer_summary", "Revenue software", 0.9),
      ]),
    );
    await screen.findByRole("heading", { level: 2, name: /Correct me/ });
    expect(
      document.querySelectorAll(".ob-conv-attention [data-kind='blocks']"),
    ).toHaveLength(0);
    expect(screen.queryByText("Needed before you can continue")).toBeNull();
  });

  it("puts exactly the required-and-empty fields in the blocking group, nothing else", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS);
    await screen.findByRole("heading", { level: 2, name: /Correct me/ });

    const blockingGroup = screen
      .getByText("Needed before you can continue")
      .closest(".ob-conv-attention-group") as HTMLElement;
    const fields = [
      ...blockingGroup.querySelectorAll(".ob-conv-attention-field"),
    ].map((span) => span.textContent);
    // offer_summary and icp are the only two REQUIRED_FIELDS the fixture
    // leaves empty; industry and buying_center are merely weak-confidence
    // and must never turn up here.
    expect(fields.sort()).toEqual(["Ideal customer", "What do you sell?"]);
  });

  it("says the review is clean, with no empty list container, once nothing is outstanding", async () => {
    const allSettled = REVIEW_FIELDS.map((field) =>
      field.field === "industry" || field.field === "buying_center"
        ? { ...field, confidence: 0.95 }
        : field,
    ).concat([
      proposedField("legal_name", "Gradion GmbH", 0.9),
      proposedField("registered_address", "Berlin, Germany", 0.9),
      proposedField("register_vat", "DE123456789", 0.9),
      proposedField("offer_summary", "Revenue software", 0.9),
      proposedField("icp", "Mid-market B2B", 0.9),
      proposedField("desired_outcomes", "Faster ramp", 0.9),
    ]);
    renderReview(REVIEW_STATE, allSettled);
    await screen.findByRole("heading", { level: 2, name: /Correct me/ });

    expect(document.querySelector(".ob-conv-attention")).toBeNull();
    expect(screen.getByText(/looks clean/)).toBeInTheDocument();
  });
});

// Arrival is not an action the reader took: landing on co.review must show
// the scene from its own top, not wherever a leftover crawl narration last
// pointed. The bug this guards was CompanyActArtifact's highlight effect
// pulsing and scrolling to whatever field the LAST thread entry named, even
// when that entry predates the review scene entirely — a stale finding from
// the read phase, not anything that happened while the review was on screen.
describe("arriving at the review scene", () => {
  it("leaves the board unscrolled and unfocused when a field-naming entry is already the thread's last one by the time the review's own data resolves", async () => {
    // jsdom has no scrollIntoView; the real DOM always carries one.
    Element.prototype.scrollIntoView ??= () => {};
    const scrollSpy = vi
      .spyOn(Element.prototype, "scrollIntoView")
      .mockImplementation(() => {});
    installFetchStub(
      reviewRoutes(REVIEW_FIELDS, reviewProposal(REVIEW_FIELDS)),
    );
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    const tree = (state: ConversationState) => (
      <QueryClientProvider client={queryClient}>
        <LocaleProvider initial="en">
          <CompanyAct
            state={state}
            dispatch={vi.fn()}
            profile={null}
            persist={vi.fn(async () => true)}
          />
        </LocaleProvider>
      </QueryClientProvider>
    );
    // First render carries the field-naming narration already — the site
    // read and proposal queries are still in flight, so this first commit
    // never finds the row: the effect that pulses/scrolls fires once here,
    // matching nothing.
    const findingEntry = {
      kind: "narration" as const,
      id: "3:field:display_name",
      i18nKey: "ob.conv.read.learnedField" as const,
      findingIds: ["display_name"],
    };
    const { rerender } = render(
      tree({ ...REVIEW_STATE, thread: [findingEntry] }),
    );
    await screen.findByRole("heading", { level: 2, name: /Correct me/ });

    // A background poll narrates again, live, while the review is already
    // on screen with the row now actually mounted — a fresh thread array
    // that still ends on a field-naming entry, exactly the shape a
    // narrating background poll produces mid-review.
    rerender(
      tree({
        ...REVIEW_STATE,
        thread: [findingEntry, { ...findingEntry, id: "4:field:display_name" }],
      }),
    );

    expect(document.querySelectorAll(".ob-conv-pulse")).toHaveLength(0);
    expect(document.activeElement === document.body).toBe(true);
    const row = document.getElementById("ob-triage-row-display_name");
    expect(row).not.toBeNull();
    // The thread's own follow-the-bottom behaviour is a separate, legitimate
    // scroll target; the review board's rows are never among its targets.
    const scrolled: readonly unknown[] = scrollSpy.mock.instances;
    expect(scrolled.some((instance) => instance === row)).toBe(false);
  });
});
