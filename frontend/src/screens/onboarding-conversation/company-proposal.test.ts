import { describe, expect, it } from "vitest";
import type { components } from "../../api/schema";
import { changeDraftField, EMPTY_DRAFT } from "../onboarding";
import { draftWithLegalEntity, legalFieldGap } from "./company-proposal";

type LegalEntity = components["schemas"]["CompanySiteReadLegalEntity"];
type SiteReadPage = components["schemas"]["CompanySiteReadPage"];

// Picking an entity is the one moment the notice's full legal block (name,
// address, registration number) is on screen at once — draftWithLegalEntity
// is what carries it into the draft instead of letting it evaporate the
// instant the human clicks past the decision.

const gradionEntity: LegalEntity = {
  name: "Gradion Co., Ltd.",
  registered_address:
    "Level 12, Bitexco Tower, 2 Hai Trieu, District 1, Ho Chi Minh City",
  register_number: "0318 447 291",
  evidence_snippet: "Gradion Co., Ltd. · Company Limited · 0318 447 291",
  source_url: "https://gradion.com/legal-notice",
};

describe("draftWithLegalEntity", () => {
  it("fills legal name, address and registration number from the chosen entity, grounded in its own source", () => {
    const next = draftWithLegalEntity(EMPTY_DRAFT, gradionEntity);
    expect(next.values.legal_name).toBe("Gradion Co., Ltd.");
    expect(next.values.registered_address).toBe(
      "Level 12, Bitexco Tower, 2 Hai Trieu, District 1, Ho Chi Minh City",
    );
    expect(next.values.register_vat).toBe("0318 447 291");
    // Grounded, not edited: the review must be able to tell this came from
    // the site's own legal notice, the same as any other scraped field.
    for (const field of [
      "legal_name",
      "registered_address",
      "register_vat",
    ] as const) {
      expect(next.grounded[field]).toMatchObject({
        source_kind: "url",
        source_url: gradionEntity.source_url,
        evidence_snippet: gradionEntity.evidence_snippet,
      });
      expect(next.edited.has(field)).toBe(false);
    }
  });

  it("claims no confidence for the block it fills, because nothing ever measured one", () => {
    const next = draftWithLegalEntity(EMPTY_DRAFT, gradionEntity);
    // The candidate carries no score on the wire. A number invented here
    // would render as the model's own certainty about a human's choice —
    // and 1 would render as the highest certainty it can express.
    for (const field of [
      "legal_name",
      "registered_address",
      "register_vat",
    ] as const) {
      expect(next.grounded[field]?.confidence).toBeUndefined();
    }
  });

  it("carries no evidence at all when the candidate printed no quote, rather than quoting the value back", () => {
    const unquoted: LegalEntity = {
      name: "Acme Holding GmbH",
      registered_address: "Musterstraße 1, 10115 Berlin",
      source_url: "https://acme.example/impressum",
    };
    const next = draftWithLegalEntity(EMPTY_DRAFT, unquoted);
    expect(next.values.legal_name).toBe("Acme Holding GmbH");
    // A value is not its own evidence: with nothing verbatim to show, the
    // grounding keeps only the page it came from.
    expect(next.grounded.legal_name?.evidence_snippet).toBeUndefined();
    expect(next.grounded.legal_name?.source_url).toBe(
      "https://acme.example/impressum",
    );
    expect(next.grounded.registered_address?.evidence_snippet).toBeUndefined();
  });

  it("treats a blank quote the same as none, so no empty evidence line can render", () => {
    const blankQuote: LegalEntity = {
      name: "Acme Holding GmbH",
      evidence_snippet: "   ",
      source_url: "https://acme.example/impressum",
    };
    const next = draftWithLegalEntity(EMPTY_DRAFT, blankQuote);
    expect(next.grounded.legal_name?.evidence_snippet).toBeUndefined();
  });

  it("fills nothing for a detail the candidate does not carry, rather than a blank or a placeholder", () => {
    const bare: LegalEntity = {
      name: "Acme Holding GmbH",
      source_url: "https://acme.example/impressum",
    };
    const seeded = {
      ...EMPTY_DRAFT,
      values: {
        ...EMPTY_DRAFT.values,
        registered_address: "An address from an earlier, richer read",
      },
    };
    const next = draftWithLegalEntity(seeded, bare);
    expect(next.values.legal_name).toBe("Acme Holding GmbH");
    // Absent on the candidate: left exactly as it was, never forced to "".
    expect(next.values.registered_address).toBe(
      "An address from an earlier, richer read",
    );
    expect(next.grounded.registered_address).toBeUndefined();
    expect(next.values.register_vat).toBe("");
    expect(next.grounded.register_vat).toBeUndefined();
  });

  it("never overwrites a field the human already typed into", () => {
    const typed = changeDraftField(
      EMPTY_DRAFT,
      "registered_address",
      "My own address, typed by hand",
    );
    const next = draftWithLegalEntity(typed, gradionEntity);
    expect(next.values.registered_address).toBe(
      "My own address, typed by hand",
    );
    expect(next.grounded.registered_address).toBeUndefined();
    expect(next.edited.has("registered_address")).toBe(true);
    // The fields the human never touched still fill normally.
    expect(next.values.register_vat).toBe("0318 447 291");
  });
});

// Picking a candidate is a decision about the legal name itself, wherever the
// candidates are offered — the clarify's list and the dossier's cards are one
// gesture — so the chosen name follows the pick even over a name the human
// typed earlier, and it follows as the site's own value, never as something
// the human asserted.
describe("draftWithLegalEntity settling the chosen name", () => {
  it("replaces a name the human typed earlier, and stops counting it as theirs", () => {
    const typed = changeDraftField(
      EMPTY_DRAFT,
      "legal_name",
      "Gradion, roughly",
    );
    const next = draftWithLegalEntity(typed, gradionEntity);
    expect(next.values.legal_name).toBe("Gradion Co., Ltd.");
    expect(next.edited.has("legal_name")).toBe(false);
    expect(next.grounded.legal_name).toMatchObject({
      source_kind: "url",
      source_url: gradionEntity.source_url,
    });
    // The whole block describes ONE company: a name from the candidate and
    // an address from somewhere else is the mixture this settling prevents.
    expect(next.values.registered_address).toBe(
      gradionEntity.registered_address,
    );
  });

  it("still leaves the details the pick never asked about to the human", () => {
    const typed = changeDraftField(
      EMPTY_DRAFT,
      "registered_address",
      "My own address, typed by hand",
    );
    const next = draftWithLegalEntity(typed, gradionEntity);
    expect(next.values.registered_address).toBe(
      "My own address, typed by hand",
    );
    expect(next.edited.has("registered_address")).toBe(true);
    expect(next.grounded.registered_address).toBeUndefined();
  });

  it("leaves a typed name and its mark exactly as they were when the candidate carries no name to settle it with", () => {
    const typed = changeDraftField(EMPTY_DRAFT, "legal_name", "Gradion GmbH");
    const nameless: LegalEntity = {
      name: "",
      source_url: "https://gradion.com/legal-notice",
    };
    const next = draftWithLegalEntity(typed, nameless);
    expect(next.values.legal_name).toBe("Gradion GmbH");
    // Unmarking a value nothing replaced would show the human's own text as
    // if the site had grounded it.
    expect(next.edited.has("legal_name")).toBe(true);
    expect(next.grounded.legal_name).toBeUndefined();
  });
});

// Why a legal-trio field is blank must be exactly what the read's own crawl
// saw: a genuine "the imprint said nothing" only follows a legal page that
// actually loaded; anything short of that is an honest "I never had a page
// to check", never an accidental over-claim.
describe("legalFieldGap", () => {
  const impressumFetched: SiteReadPage = {
    url: "https://example.com/impressum",
    status: "fetched",
    kind: "impressum",
  };
  const homeFetched: SiteReadPage = {
    url: "https://example.com/",
    status: "fetched",
    kind: "home",
  };

  it("reads as genuinely not published once a fetched page was classified as the legal notice", () => {
    expect(
      legalFieldGap("registered_address", [homeFetched, impressumFetched]),
    ).toBe("not-published");
  });

  it("reads as not checked when no page in the crawl was classified as the legal notice", () => {
    expect(legalFieldGap("registered_address", [homeFetched])).toBe(
      "not-checked",
    );
    expect(legalFieldGap("registered_address", [])).toBe("not-checked");
  });

  it("names no gap at all on the manual path, where nothing ever went looking", () => {
    expect(legalFieldGap("registered_address", undefined)).toBeNull();
    expect(legalFieldGap("legal_name", undefined)).toBeNull();
  });

  it("reads as not checked when the legal page was found but never actually fetched", () => {
    const blocked: SiteReadPage = {
      url: "https://example.com/impressum",
      status: "skipped",
      kind: "impressum",
      reason: "robots",
    };
    expect(legalFieldGap("registered_address", [blocked])).toBe("not-checked");
  });

  it("names no gap at all for a field no legal page could ever have settled", () => {
    expect(legalFieldGap("offer_summary", [impressumFetched])).toBeNull();
    expect(legalFieldGap("display_name", [])).toBeNull();
  });
});
