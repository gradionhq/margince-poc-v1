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
});
