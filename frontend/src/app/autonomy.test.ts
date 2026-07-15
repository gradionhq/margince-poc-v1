import { describe, expect, it } from "vitest";
import {
  approvalDotTier,
  dotTier,
  KIND_TO_VERB,
  type VerbTierMap,
} from "./autonomy";

describe("autonomy tier helpers", () => {
  it("maps green to auto and everything else to confirm", () => {
    expect(dotTier("green")).toBe("auto");
    expect(dotTier("yellow")).toBe("confirm");
    expect(dotTier("dynamic")).toBe("confirm");
    expect(dotTier(undefined)).toBe("confirm");
  });

  it("resolves an approval kind's tier via its verb, falling back to confirm", () => {
    const map: VerbTierMap = { send_email: "yellow", search_records: "green" };
    expect(approvalDotTier("send_email", map)).toBe("confirm");
    // a kind with no known verb is confirm-first by definition (it was staged)
    expect(approvalDotTier("overnight", map)).toBe("confirm");
  });

  it("knows the approval kinds", () => {
    expect(KIND_TO_VERB.send_email).toBe("send_email");
    expect(KIND_TO_VERB.advance_deal).toBeDefined();
  });
});
