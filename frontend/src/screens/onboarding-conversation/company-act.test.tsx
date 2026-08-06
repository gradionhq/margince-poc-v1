/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
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

// Every case below waits on the guide's own sentence rather than the board's
// heading. The heading arrives with the read-only fallback proposal, which is
// a render or two BEFORE the draft is prefilled from the read — so a board
// waited for that way can still be showing every field empty and everything
// blocking. The sentence is rendered from `blockingCount` itself, the number
// each case is about, so it cannot read as settled before the board is.
describe("the rail's review to-do list", () => {
  it("carries exactly as many items as the board's own section tallies add up to", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS);

    await screen.findByText(/2 fields block confirm/);
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
    await screen.findByText(/2 fields block confirm/);

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
    // Two waits, because two things have to have landed: the decision item
    // proves the REAL proposal is in (the fallback carries no open question),
    // and "3" proves the draft is prefilled — 2 blocking fields plus the one
    // decision. Before the prefill the same sentence would read 4.
    await screen.findByText("needs a decision");
    await screen.findByText(/3 fields block confirm/);

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
    // that actually carries the still-open clarify — and then for the count
    // that says the draft is prefilled too.
    await screen.findByText("needs a decision");
    await screen.findByText(/3 fields block confirm/);

    expect(
      document.querySelectorAll(
        ".ob-conv-attention button[data-kind='decision']",
      ),
    ).toHaveLength(1);
    expect(document.querySelectorAll(".ob-conv-attention li")).toHaveLength(9);
  });

  it("names the header whenever the list itself renders", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS);
    await screen.findByText(/2 fields block confirm/);

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
    await screen.findByText(/1 field blocks confirm/);
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
    await screen.findByText(/Nothing blocks you/);
    expect(
      document.querySelectorAll(".ob-conv-attention [data-kind='blocks']"),
    ).toHaveLength(0);
    expect(screen.queryByText("Needed before you can continue")).toBeNull();
  });

  it("puts exactly the required-and-empty fields in the blocking group, nothing else", async () => {
    renderReview(REVIEW_STATE, REVIEW_FIELDS);
    await screen.findByText(/2 fields block confirm/);

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
    // The clean sentence IS the assertion's own state: nothing blocking and
    // nothing advisory. Before the prefill the guide is still counting gaps.
    await screen.findByText(/looks clean/);

    expect(document.querySelector(".ob-conv-attention")).toBeNull();
  });
});

// The dossier's entity cards and the clarify's candidate list ask the same
// question of the same candidates, so a pick has to settle the same way on
// both: the chosen name wins over a name typed earlier, because that name
// standing above this candidate's address and registration number would put
// two companies on one card. The bug this guards was the dossier keeping the
// typed name while silently taking the rest of the block from the candidate.
describe("the dossier's legal-entity picker", () => {
  const GRADION_LTD = {
    name: "Gradion Co., Ltd.",
    registered_address: "Level 12, Bitexco Tower, Ho Chi Minh City",
    register_number: "0318 447 291",
    evidence_snippet: "Gradion Co., Ltd. · 0318 447 291",
    source_url: "https://gradion.com/legal-notice",
  };

  it("settles the chosen name over one the human typed earlier", async () => {
    // A read that reached the AI budget ceiling keeps the evidence it already
    // collected but never becomes confirmable, so no review scene takes the
    // surface — the dossier stays, with its "edit fields directly" escape
    // hatch and the entity cards behind it.
    installFetchStub({
      [`GET /company/site-reads/${REVIEW_READ_ID}`]: () =>
        jsonResponse({
          ...REVIEW_READ,
          status: "deferred",
          status_code: "budget_deferred",
          legal_entities: [
            GRADION_LTD,
            {
              name: "Gradion Holding GmbH",
              source_url: "https://gradion.com/legal-notice",
            },
          ],
        }),
      "GET /onboarding/company/proposal": () =>
        jsonResponse({ title: "not ready", code: "not_found" }, 404),
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

    fireEvent.click(
      await screen.findByRole("button", { name: "Edit fields directly" }),
    );
    const legalName = screen.getByLabelText(/Registered legal name/);
    fireEvent.change(legalName, { target: { value: "Gradion, roughly" } });

    const card = screen.getByRole("button", { name: /Gradion Co\., Ltd\./ });
    fireEvent.click(card);

    expect(legalName).toHaveValue("Gradion Co., Ltd.");
    // The picker marks a card chosen by comparing the card to legal_name, so
    // a pick that left the typed name standing also denied the very click it
    // had just honoured everywhere else.
    expect(card).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByLabelText(/Registered address/)).toHaveValue(
      GRADION_LTD.registered_address,
    );
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

// A confirm submits every required field filled and nothing else in the
// server's way — the shape a version-skew or conflict rejection is actually
// interesting to react to.
const CONFIRM_FIELDS: readonly FieldFixture[] = [
  proposedField("display_name", "Acme Inc", 0.95),
  proposedField("offer_summary", "CRM software", 0.9),
  proposedField("icp", "Mid-market B2B", 0.9),
];

const CONFIRM_PATH = `POST /company/site-reads/${REVIEW_READ_ID}/confirm`;

// The sentence a 409 recovery reads as once the driver could not find out
// which server state it was in — the one honest reading of a check that
// never completed.
const CHECK_FAILED_NOTICE =
  "I could not check where this read stands, so nothing has moved on. Check again, or press Continue in a moment.";

describe("recovering from a rejected confirm", () => {
  it("blocks a retry until the refetched proposal actually changed, never the one the server just rejected", async () => {
    let proposalCalls = 0;
    // The refetch this driver kicks off on a version-skew rejection is held
    // open deliberately, so the still-disabled window is actually
    // observable here rather than racing a mocked fetch that would
    // otherwise settle before the assertion runs.
    // Boxed rather than a bare `let`: TypeScript narrows a variable only
    // assigned inside a nested closure to `never` at the call site.
    const gate: { release: (() => void) | null } = { release: null };
    installFetchStub({
      [`GET /company/site-reads/${REVIEW_READ_ID}`]: () =>
        jsonResponse({
          ...REVIEW_READ,
          profile_fields: CONFIRM_FIELDS.map(toColdField),
        }),
      "GET /onboarding/company/proposal": async () => {
        proposalCalls += 1;
        if (proposalCalls > 1) {
          await new Promise<void>((resolve) => {
            gate.release = resolve;
          });
        }
        const hash = proposalCalls === 1 ? "proposal-1" : "proposal-2";
        return jsonResponse({
          ...reviewProposal(CONFIRM_FIELDS),
          proposal_hash: hash,
        });
      },
      [CONFIRM_PATH]: () =>
        jsonResponse({ title: "conflict", code: "version_skew" }, 409),
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

    const continueButton = await screen.findByRole("button", {
      name: /Continue/,
    });
    expect(continueButton).toBeEnabled();

    fireEvent.click(continueButton);

    // The version-skew notice names what happened; the button disables
    // while the refetch it triggered is still carrying the SAME hash the
    // server just rejected (react-query holds prior data steady in flight).
    await screen.findByText(
      "Your review just updated with newer information from the read. Have a look, then press Continue again.",
    );
    expect(continueButton).toBeDisabled();
    await vi.waitFor(() => expect(proposalCalls).toBeGreaterThan(1));
    // The refetch is in flight but has not resolved: the button stays
    // disabled on the still-stale hash, never re-armed just because a
    // refetch was ATTEMPTED.
    expect(continueButton).toBeDisabled();

    // Once the refetch actually lands a NEW hash, the block lifts on its own
    // — nothing else has to happen for Continue to become safe to press.
    expect(gate.release).not.toBeNull();
    gate.release?.();
    await vi.waitFor(() => expect(continueButton).toBeEnabled());
  });

  it("stays blocked on the no-data path too, once the refetch settles without ever producing a hash", async () => {
    // The proposal endpoint never succeeds here, so the confirm mutation
    // falls back to `proposalFromRead(prevSnapshot.current)` on every
    // attempt — the exact path the disabled-Continue guard must also cover,
    // not only the one where `proposal.data` itself carries the hash. A
    // refetch that fails is outcome (3) of the three refreshAfterSkew can
    // settle into: no new hash ever exists to resubmit, so the block does
    // NOT lift on its own — the only difference from a permanent lock is
    // that the reader is told so, and given a retry to press instead of the
    // Continue button this notice disables.
    let siteReadCalls = 0;
    const gate: { release: (() => void) | null } = { release: null };
    installFetchStub({
      [`GET /company/site-reads/${REVIEW_READ_ID}`]: async () => {
        siteReadCalls += 1;
        if (siteReadCalls > 1) {
          await new Promise<void>((resolve) => {
            gate.release = resolve;
          });
        }
        return jsonResponse({
          ...REVIEW_READ,
          profile_fields: CONFIRM_FIELDS.map(toColdField),
        });
      },
      "GET /onboarding/company/proposal": () =>
        Promise.reject(new Error("proposal endpoint unreachable")),
      [CONFIRM_PATH]: () =>
        jsonResponse({ title: "conflict", code: "version_skew" }, 409),
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

    const continueButton = await screen.findByRole("button", {
      name: /Continue/,
    });
    await vi.waitFor(() => expect(continueButton).toBeEnabled());

    fireEvent.click(continueButton);

    await screen.findByText(
      "Your review just updated with newer information from the read. Have a look, then press Continue again.",
    );
    expect(continueButton).toBeDisabled();
    await vi.waitFor(() => expect(siteReadCalls).toBeGreaterThan(1));
    // The refetch this rejection triggered is in flight, and `proposal.data`
    // will NEVER carry a hash on this path — a guard reading only that field
    // would re-arm the instant it saw `undefined`, straight after the 409.
    expect(continueButton).toBeDisabled();

    gate.release?.();
    // The refetch settled — into a failure — and Continue MUST NOT re-arm
    // onto a draft the server has never actually seen. The dedicated notice
    // swaps to naming that, with its own retry standing in for the button.
    await screen.findByText(
      "I checked again, but nothing has changed yet. Pressing Continue now would fail the same way, so have another look or check again in a moment.",
    );
    expect(continueButton).toBeDisabled();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });

  it("re-arms Continue once a skew refetch actually lands a NEW hash, whether that is the automatic one or a later manual retry", async () => {
    // The first refetch this 409 triggers lands the SAME hash — a
    // concurrent confirm elsewhere already left the draft exactly as it
    // was. A guard that re-armed on that alone (rather than on a hash that
    // actually differs) would let the very next press earn the identical
    // 409 all over again. The reader's own retry is what finally moves
    // things on, once the draft underneath has genuinely changed.
    let proposalCalls = 0;
    const gate: { release: (() => void) | null } = { release: null };
    installFetchStub({
      [`GET /company/site-reads/${REVIEW_READ_ID}`]: () =>
        jsonResponse({
          ...REVIEW_READ,
          profile_fields: CONFIRM_FIELDS.map(toColdField),
        }),
      "GET /onboarding/company/proposal": async () => {
        proposalCalls += 1;
        if (proposalCalls > 1) {
          await new Promise<void>((resolve) => {
            gate.release = resolve;
          });
        }
        const hash = proposalCalls <= 2 ? "proposal-1" : "proposal-2";
        return jsonResponse({
          ...reviewProposal(CONFIRM_FIELDS),
          proposal_hash: hash,
        });
      },
      [CONFIRM_PATH]: () =>
        jsonResponse({ title: "conflict", code: "version_skew" }, 409),
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

    const continueButton = await screen.findByRole("button", {
      name: /Continue/,
    });
    fireEvent.click(continueButton);

    await screen.findByText(
      "Your review just updated with newer information from the read. Have a look, then press Continue again.",
    );
    expect(continueButton).toBeDisabled();
    await vi.waitFor(() => expect(proposalCalls).toBeGreaterThan(1));
    expect(continueButton).toBeDisabled();

    gate.release?.();
    // The refetch settled with the SAME hash the server just rejected. The
    // block MUST stay, and the reader is told why, with a retry of their
    // own rather than the Continue button that is still disabled.
    const retry = await screen.findByRole("button", { name: "Retry" });
    expect(continueButton).toBeDisabled();

    fireEvent.click(retry);
    await vi.waitFor(() => expect(proposalCalls).toBeGreaterThan(2));
    gate.release?.();
    // This second refetch landed a genuinely NEW hash — only now may
    // Continue re-arm, onto a draft the server has not rejected.
    await vi.waitFor(() => expect(continueButton).toBeEnabled());
  });

  it("only treats a bare conflict as an already-confirmed race once THIS read's own status says confirmed", async () => {
    let readCalls = 0;
    let getCompanyCalls = 0;
    installFetchStub({
      [`GET /company/site-reads/${REVIEW_READ_ID}`]: () => {
        readCalls += 1;
        // The confirm attempt races a read that has NOT actually confirmed
        // (still "ready") — a bare conflict here means "not confirmable
        // yet", never "already landed".
        return jsonResponse({
          ...REVIEW_READ,
          status: "ready",
          profile_fields: CONFIRM_FIELDS.map(toColdField),
        });
      },
      "GET /onboarding/company/proposal": () =>
        jsonResponse(reviewProposal(CONFIRM_FIELDS)),
      "GET /company": () => {
        // An existing member company from BEFORE this attempt — present
        // regardless of whether this read's own confirmation landed, so it
        // must never be read as proof that it did.
        getCompanyCalls += 1;
        return jsonResponse({
          display_name: "Some Other Existing Co",
          offer_summary: "unrelated",
          icp: "unrelated",
        });
      },
      [CONFIRM_PATH]: () =>
        jsonResponse({ title: "conflict", code: "conflict" }, 409),
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

    const continueButton = await screen.findByRole("button", {
      name: /Continue/,
    });
    fireEvent.click(continueButton);

    // The "not ready" notice, from this read's own re-fetched status — never
    // a silent forward-advance onto the other company's stale data.
    await screen.findByText(
      "This read is not ready to confirm yet. Wait for it to finish, or start a fresh one.",
    );
    expect(readCalls).toBeGreaterThan(1);
    // GET /company is never even worth consulting once the read's own
    // status already answers the question.
    expect(getCompanyCalls).toBe(0);
  });

  // Every step of the already-confirmed recovery can fail, and a failure
  // there is exactly as stuck as the loop the recovery exists to close:
  // the confirm attempt cleared its own block on the way in, so a reader
  // left with no notice at all presses the same button and earns the same
  // 409. Both tests below hold the same line — say what actually happened,
  // never diagnose the read on the strength of a request that never landed,
  // and leave a way forward on screen.
  it("admits the already-confirmed check failed, and offers the check again, when the company probe never answers", async () => {
    let readCalls = 0;
    let companyCalls = 0;
    const dispatch = vi.fn();
    installFetchStub({
      [`GET /company/site-reads/${REVIEW_READ_ID}`]: () => {
        readCalls += 1;
        // The attempt raced a confirmation that already landed: the review
        // was built from a confirmable snapshot, and the refetch the 409
        // triggers finds this read's own status already "confirmed".
        return jsonResponse({
          ...REVIEW_READ,
          status: readCalls > 1 ? "confirmed" : "ready",
          profile_fields: CONFIRM_FIELDS.map(toColdField),
        });
      },
      "GET /onboarding/company/proposal": () =>
        jsonResponse(reviewProposal(CONFIRM_FIELDS)),
      "GET /company": () => {
        companyCalls += 1;
        // The one probe that could move the reader forward fails first: a
        // problem body, so the profile never arrives.
        return companyCalls > 1
          ? jsonResponse({
              display_name: "Acme Inc",
              offer_summary: "CRM software",
              icp: "Mid-market B2B",
            })
          : jsonResponse(
              { title: "backend unavailable", code: "internal" },
              503,
            );
      },
      [CONFIRM_PATH]: () =>
        jsonResponse({ title: "conflict", code: "conflict" }, 409),
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
            dispatch={dispatch}
            profile={null}
            persist={vi.fn(async () => true)}
          />
        </LocaleProvider>
      </QueryClientProvider>,
    );

    const continueButton = await screen.findByRole("button", {
      name: /Continue/,
    });
    fireEvent.click(continueButton);

    await screen.findByText(CHECK_FAILED_NOTICE);
    // Neither of the two confident readings: the read was never called not
    // ready, and the reader was never walked forward onto a profile that
    // never arrived.
    expect(screen.queryByText(/not ready to confirm/)).toBeNull();
    expect(dispatch).not.toHaveBeenCalledWith({ type: "COMPANY_CONFIRMED" });

    // The notice's own retry is the route forward — the same check, run
    // again, with the probe answering this time.
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await vi.waitFor(() =>
      expect(dispatch).toHaveBeenCalledWith({ type: "COMPANY_CONFIRMED" }),
    );
  });

  it("never calls the read not ready to confirm on the strength of a refetch that itself failed", async () => {
    let readCalls = 0;
    installFetchStub({
      [`GET /company/site-reads/${REVIEW_READ_ID}`]: () => {
        readCalls += 1;
        // The review renders from the first snapshot; the refetch the 409
        // triggers never lands, so this read's status is simply unknown.
        return readCalls > 1
          ? Promise.reject(new Error("site-read endpoint unreachable"))
          : Promise.resolve(
              jsonResponse({
                ...REVIEW_READ,
                profile_fields: CONFIRM_FIELDS.map(toColdField),
              }),
            );
      },
      "GET /onboarding/company/proposal": () =>
        jsonResponse(reviewProposal(CONFIRM_FIELDS)),
      [CONFIRM_PATH]: () =>
        jsonResponse({ title: "conflict", code: "conflict" }, 409),
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

    const continueButton = await screen.findByRole("button", {
      name: /Continue/,
    });
    fireEvent.click(continueButton);

    await screen.findByText(CHECK_FAILED_NOTICE);
    expect(readCalls).toBeGreaterThan(1);
    // "Not ready to confirm" is a claim about the read; a refetch that never
    // landed supports no claim about it at all.
    expect(screen.queryByText(/not ready to confirm/)).toBeNull();
    expect(screen.getByRole("button", { name: "Retry" })).toBeInTheDocument();
  });
});
