import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import type { MessageKey } from "../../i18n/en";
import type { CompanyDraft, CompanyFieldName } from "../onboarding";
import { EMPTY_DRAFT } from "../onboarding";
import { rowFor } from "./company-review-state";

type ProposalField = components["schemas"]["OnboardingCompanyProposalField"];

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
    expect(row.state).toBe("chosen");
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
    expect(row.state).toBe("chosen");
    expect(row.evidence).toBeNull();
  });
});
