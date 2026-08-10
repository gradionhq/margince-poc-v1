import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import type { MessageKey } from "../../i18n/en";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import { changeDraftField, EMPTY_DRAFT } from "../onboarding";
import { draftWithLegalEntity } from "./company-proposal";
import { rowFor } from "./company-review-state";

type ProposalField = components["schemas"]["OnboardingCompanyProposalField"];
type CompanySiteRead = components["schemas"]["CompanySiteRead"];

// rowFor is the one place the review board, the rail's to-do list, and the
// narration's open-decision count all derive a field's state from — see the
// file header. This suite guards its provenance rule: a row's evidence must
// support the value actually on screen, never a different value the proposal
// happens to carry an opinion about.

function stubT(key: MessageKey): string {
  return key;
}

// A stored, un-typed, un-grounded value — the shape a member-path draft's
// existing profile value takes, never having gone through changeDraftField
// (which would mark it `edited` and short-circuit rowFor before provenance
// is even considered).
function storedDraft(field: CompanyFieldName, value: string): CompanyDraft {
  return {
    values: { ...EMPTY_DRAFT.values, [field]: value },
    grounded: {},
    edited: new Set(),
  };
}

function proposalFieldFor(value: string): ProposalField {
  return {
    field: "industry",
    value,
    confidence: 0.92,
    evidence_snippet: "Robotics and industrial automation.",
    source_url: "https://acme.example/about",
  };
}

describe("rowFor provenance", () => {
  it("attaches the proposal's confidence and evidence when it proposed the value on screen", () => {
    const draft = storedDraft("industry", "Robotics");
    const byName = new Map([["industry", proposalFieldFor("Robotics")]]);
    const row = rowFor("industry", draft, byName, stubT);
    expect(row.evidence).not.toBeNull();
    expect(row.confidence).toBe(0.92);
  });

  it("never attributes the proposal's evidence to a different, still-current value", () => {
    // The draft still shows the EXISTING profile's industry; the proposal
    // is arguing for a different one the human has not accepted.
    const draft = storedDraft("industry", "Consulting");
    const byName = new Map([["industry", proposalFieldFor("Robotics")]]);
    const row = rowFor("industry", draft, byName, stubT);
    expect(row.evidence).toBeNull();
    expect(row.confidence).toBeNull();
    expect(row.state).toBe("stored");
  });

  it("never blends the draft's own grounding with the proposal's score", () => {
    // The draft carries the value the human picked off the legal notice; the
    // proposal happens to argue for the same string with a measured score.
    // Reading the quote off one and the number off the other would describe
    // one value with two provenances at once.
    const draft: CompanyDraft = {
      values: { ...EMPTY_DRAFT.values, industry: "Robotics" },
      grounded: {
        industry: {
          field: "industry",
          value: "Robotics",
          source_kind: "url",
          source_url: "https://acme.example/impressum",
        },
      },
      edited: new Set(),
    };
    const byName = new Map([["industry", proposalFieldFor("Robotics")]]);
    const row = rowFor("industry", draft, byName, stubT);
    expect(row.confidence).toBeNull();
    expect(row.evidence).toBeNull();
  });

  it("still attaches the proposal's evidence when only surrounding whitespace differs", () => {
    // A stored profile value formFromProfile copied untouched can carry
    // whitespace the proposal's own value never had — the same fact, not a
    // different one the human has not accepted.
    const draft = storedDraft("industry", "  Robotics  ");
    const byName = new Map([["industry", proposalFieldFor("Robotics")]]);
    const row = rowFor("industry", draft, byName, stubT);
    expect(row.evidence).not.toBeNull();
    expect(row.confidence).toBe(0.92);
  });
});

// A value the human settled by choosing one of the read's own candidates
// carries the page it was printed on, and that page's words when the read
// captured them — but no score, because nothing measured one. The row has to
// say that without inventing a band and without an empty evidence line.
describe("rowFor on a value with no measured confidence", () => {
  function chosenDraft(snippet: string | undefined): CompanyDraft {
    return {
      values: { ...EMPTY_DRAFT.values, legal_name: "Gradion Co., Ltd." },
      grounded: {
        legal_name: {
          field: "legal_name",
          value: "Gradion Co., Ltd.",
          evidence_snippet: snippet,
          source_kind: "url",
          source_url: "https://gradion.com/legal-notice",
        },
      },
      edited: new Set(),
    };
  }

  it("bands it nowhere and reports no score at all", () => {
    const row = rowFor(
      "legal_name",
      chosenDraft("Gradion Co., Ltd. · 0318"),
      new Map(),
      stubT,
    );
    expect(row.state).toBe("quoted");
    // Not "low", not 0: an unmeasured value is not a weak one.
    expect(row.confidence).toBeNull();
  });

  it("still shows the page's own words when the candidate printed them", () => {
    const row = rowFor(
      "legal_name",
      chosenDraft("Gradion Co., Ltd. · 0318"),
      new Map(),
      stubT,
    );
    expect(row.evidence).toEqual({
      snippet: "Gradion Co., Ltd. · 0318",
      source: "https://gradion.com/legal-notice",
    });
  });

  it("shows no evidence line at all when the candidate printed no quote", () => {
    const row = rowFor("legal_name", chosenDraft(undefined), new Map(), stubT);
    expect(row.state).toBe("quoted");
    expect(row.evidence).toBeNull();
  });
});

// The copy an empty legal row carries has to match the decision the human
// actually faces. "Choose which company is yours" is a real instruction when
// the imprint names several; on a one-company imprint it points at a picker
// that is not on the screen.
describe("rowFor's empty hint over the read's legal candidates", () => {
  const pages: CompanySiteRead["pages"] = [
    {
      url: "https://gradion.test/impressum",
      status: "fetched",
      kind: "impressum",
    },
  ];
  const candidate = {
    name: "Gradion Co., Ltd.",
    registered_address: "Bitexco Tower, Ho Chi Minh City",
    source_url: "https://gradion.test/impressum",
  };
  // The human emptied the box the sole-entity fill had put a value in: the one
  // way a field a candidate carries is still blank on that path.
  const cleared: CompanyDraft = {
    values: EMPTY_DRAFT.values,
    grounded: {},
    edited: new Set(["legal_name"]),
  };

  it("asks for the choice when the imprint names more than one company", () => {
    const row = rowFor("legal_name", cleared, new Map(), stubT, pages, [
      candidate,
      { name: "Gradion Holding GmbH", source_url: candidate.source_url },
    ]);
    expect(row.emptyHintKey).toBe("ob.conv.triage.legalUnpicked");
  });

  it("asks for no choice at all when the imprint names one", () => {
    const row = rowFor("legal_name", cleared, new Map(), stubT, pages, [
      candidate,
    ]);
    expect(row.emptyHintKey).toBe("ob.conv.triage.emptyHint");
    // The generic hint is a placeholder where a value would stand, never a
    // reason: with no gap to name, the row has no omission to state either.
    expect(row.omissionReasonKey).toBeNull();
  });

  // The gap is the ONLY thing the read accounts for per field. A blank offer
  // summary on the same read has no page and no candidate that speaks to it,
  // so the row carries a reason for nobody to state.
  it("carries no omission reason for a field outside the legal trio", () => {
    const row = rowFor("offer_summary", cleared, new Map(), stubT, pages, [
      candidate,
    ]);
    expect(row.state).toBe("required");
    expect(row.omissionReasonKey).toBeNull();
    expect(row.emptyHintKey).toBe("ob.conv.triage.emptyHint");
  });

  // A row is an omission notice, so it must not restate a decision as an open
  // one: the human picked their company off a multi-entity imprint and then
  // emptied one of the boxes that pick filled. The choice is made; the box is
  // simply blank.
  it("stops asking who the company is once the draft carries the chosen entity", () => {
    const picked = changeDraftField(
      draftWithLegalEntity(EMPTY_DRAFT, {
        name: candidate.name,
        registered_address: candidate.registered_address,
        register_number: "HRB 12345",
        source_url: candidate.source_url,
      }),
      "registered_address",
      "",
    );
    const row = rowFor("registered_address", picked, new Map(), stubT, pages, [
      candidate,
      { name: "Gradion Holding GmbH", source_url: candidate.source_url },
    ]);
    expect(row.omissionReasonKey).toBeNull();
    expect(row.emptyHintKey).toBe("ob.conv.triage.emptyHint");
  });

  it("carries the gap as the row's own omission reason when the read can name one", () => {
    const row = rowFor("register_vat", cleared, new Map(), stubT, pages, [
      candidate,
    ]);
    // The read fetched the imprint and no candidate carries a register
    // number: the page states none, which is a fact about this field.
    expect(row.omissionReasonKey).toBe("ob.conv.triage.legalNotPublished");
  });
});
