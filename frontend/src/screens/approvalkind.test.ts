import { describe, expect, it } from "vitest";
import { translate } from "../i18n";
import { approvalKindLabel, humanizeKind } from "./approvalkind";

// A proposal's kind is a wire enum. A reader deciding whether to accept
// twenty-five of something must be told what that something is.

const t = (
  key: Parameters<typeof translate>[1],
  params?: Record<string, string | number>,
) => translate("en", key, params);

describe("what a staged proposal is called", () => {
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
