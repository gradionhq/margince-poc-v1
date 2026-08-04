import { describe, expect, it } from "vitest";
import { translate } from "../i18n";
import {
  approvalKindLabel,
  EDITABLE_FIELDS,
  humanizeKind,
  KIND_LABEL,
} from "./approvalkind";

// A proposal's kind is a wire enum. A reader deciding whether to accept
// twenty-five of something must be told what that something is.

const t = (
  key: Parameters<typeof translate>[1],
  params?: Record<string, string | number>,
) => translate("en", key, params);

// Every kind the server can stage. This is `decisionGrants` in
// backend/internal/modules/approvals/authority.go — a kind absent from THAT
// map is refused before it is ever written, so it is the whole vocabulary a
// reader can meet. Restated here because the frontend cannot read it at
// runtime, and pinned by the test below so it cannot drift silently: two kinds
// added upstream reached a German list in English before this existed.
const STAGEABLE_KINDS = [
  "advance_deal",
  "ai_model_rate_proposal",
  "archive_record",
  "book_meeting",
  "capture_counterparty",
  "close_date_correction",
  "coldstart",
  "create_record",
  "deal_follow_up",
  "deepread",
  "enrich",
  "fx_rate_proposal",
  "lifecycle_change",
  "merge_records",
  "org_name_promotion",
  "progress_deal",
  "promote_lead",
  "send_email",
  "send_offer",
  "share_record",
  "site_lead",
  "update_record",
] as const;

describe("what a staged proposal is called", () => {
  it("has a label for every kind the server can stage, in both locales", () => {
    const missing = STAGEABLE_KINDS.filter((kind) => !(kind in KIND_LABEL));
    expect(missing, "kinds the reader would meet unlabelled").toEqual([]);
    for (const kind of STAGEABLE_KINDS) {
      for (const locale of ["en", "de"] as const) {
        const label = translate(locale, KIND_LABEL[kind]);
        expect(label.trim(), `${kind} in ${locale}`).not.toBe("");
        // The identifier itself is the thing this map exists to stop showing.
        expect(label, `${kind} in ${locale}`).not.toContain("_");
      }
    }
  });

  it("names a known kind in words, never its identifier", () => {
    expect(approvalKindLabel("site_lead", t)).toBe(
      "Add a person found on the site",
    );
    expect(approvalKindLabel("fx_rate_proposal", t)).toBe(
      "Refresh exchange rates",
    );
  });

  it("degrades an unmapped kind to its own words, not snake_case", () => {
    expect(approvalKindLabel("some_future_kind", t)).toBe("some future kind");
    expect(humanizeKind("a_b_c")).toBe("a b c");
  });
});

// What a reader may change before accepting.
//
// The inline editor's default offers every string field as free text. For a
// proposal built out of identifiers and enums that default hands a reader two
// ways to type themselves into a server refusal: re-aiming the proposal at
// another record (assertSameEntityRefs), or naming a stage that does not
// exist. A declared policy is what keeps those off the screen.
describe("what a reader may change before accepting", () => {
  it("offers only the stage on a lifecycle proposal, and only real stages", () => {
    const fields = EDITABLE_FIELDS.lifecycle_change;
    expect(fields.map((entry) => entry.field)).toEqual(["proposed_lifecycle"]);
    const [stage] = fields;
    expect(stage.as).toBe("choice");
    if (stage.as !== "choice") {
      throw new Error(
        "the stage field must be a fixed choice, never free text",
      );
    }
    expect(stage.options).toContain("former_customer");
    expect(stage.options).toContain("customer");
  });

  it("never offers an identifier, on any kind that declares a policy", () => {
    for (const [kind, fields] of Object.entries(EDITABLE_FIELDS)) {
      for (const entry of fields) {
        expect(
          entry.field.endsWith("_id"),
          `${kind} offers ${entry.field} for editing — changing an identifier ` +
            "re-aims the proposal at another record, which the server refuses",
        ).toBe(false);
      }
    }
  });

  it("declares a policy only for a kind the server can stage", () => {
    const unknown = Object.keys(EDITABLE_FIELDS).filter(
      (kind) =>
        !STAGEABLE_KINDS.includes(kind as (typeof STAGEABLE_KINDS)[number]),
    );
    expect(
      unknown,
      "a policy for a kind nobody stages asserts nothing",
    ).toEqual([]);
  });
});
