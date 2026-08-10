/** @vitest-environment jsdom */
import { describe, expect, it } from "vitest";
import { worstOf } from "./company360";

// The health verdict is the WORST dimension, never an average (PO-AC-N-11,
// ADR-0095/A146).
//
// An average lets a strong relationship hide a payment problem, and payment
// problems are the ones a rep must not miss. A worst-of is also a sentence a
// reader can check — "at risk, because payment is at risk" — where a composite
// number is not.

const rated = (rating: string) => ({ rating });

describe("the health verdict is the worst dimension", () => {
  it("takes the worst rating, not the most common one", () => {
    // Two strong and one at-risk averages to "good" and reads as fine. It is
    // not fine: the account owes money.
    expect(
      worstOf([rated("strong"), rated("strong"), rated("at_risk")]).overall,
    ).toBe("at_risk");
  });

  it("is strong only when every rated dimension is", () => {
    expect(
      worstOf([rated("strong"), rated("strong"), rated("strong")]).overall,
    ).toBe("strong");
  });

  it("takes good over strong", () => {
    expect(worstOf([rated("strong"), rated("good")]).overall).toBe("good");
  });

  // A dimension with no rating is not a bad one. Counting an absent payment
  // reading as at-risk would fail every account with no accounting connection.
  it("ignores a dimension that has no rating", () => {
    const { overall, rated: count } = worstOf([
      rated("strong"),
      undefined,
      { rating: undefined },
    ]);
    expect(overall).toBe("strong");
    expect(count).toBe(1);
  });

  // Three-of-three and one-of-three are different claims and must not render
  // identically (PO-AC-N-12).
  it("says how many dimensions the verdict was read from", () => {
    expect(worstOf([rated("good"), rated("strong")]).rated).toBe(2);
    expect(worstOf([rated("good")]).rated).toBe(1);
  });

  it("has no verdict at all when nothing could be rated", () => {
    const { overall, rated: count } = worstOf([undefined, undefined]);
    expect(overall).toBeUndefined();
    expect(count).toBe(0);
  });

  // A value outside the vocabulary is not silently treated as the best one.
  it("ignores a rating it does not know", () => {
    expect(worstOf([rated("excellent"), rated("good")]).overall).toBe("good");
    expect(worstOf([rated("excellent")]).rated).toBe(0);
  });
});
