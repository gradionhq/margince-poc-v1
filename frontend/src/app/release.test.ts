import { describe, expect, it } from "vitest";
import { comparableRelease, releaseSkew } from "./release";

// A typed table rather than it.each's positional tuples. The tuple form needed a
// cast at the call site to pass `undefined` for a `string | undefined` parameter,
// and a cast in a test is the same defect it is anywhere else: it silences the one
// check that would notice the table and the function disagreeing about a type.
const SKEW_CASES: ReadonlyArray<{
  name: string;
  mine: string | undefined;
  theirs: string | undefined;
  skew: boolean;
}> = [
  {
    name: "a matched set renders",
    mine: "1970.42",
    theirs: "1970.42",
    skew: false,
  },
  {
    name: "a torn set does not",
    mine: "1970.41",
    theirs: "1970.42",
    skew: true,
  },
  {
    name: "a torn set does not, whichever side is newer",
    mine: "1970.43",
    theirs: "1970.42",
    skew: true,
  },
  {
    name: "an unstamped bundle never blocks",
    mine: "dev",
    theirs: "1970.42",
    skew: false,
  },
  { name: "nor does a local build", mine: "", theirs: "1970.42", skew: false },
  {
    name: "an api reporting no release is not a mismatch",
    mine: "1970.42",
    theirs: undefined,
    skew: false,
  },
  {
    name: "nor is one that reports the dev sentinel",
    mine: "1970.42",
    theirs: "dev",
    skew: false,
  },
  {
    name: "two unknowns are not a mismatch either",
    mine: "dev",
    theirs: "dev",
    skew: false,
  },
];

// The whole decision the release gate makes. Two things have to hold at once and
// they pull in opposite directions: a genuinely mixed set must be caught, and an
// installation whose release simply is not known must never be blanked out by a
// guard that mistook absence for disagreement.
//
// This table mirrors the Go one in internal/compose/releaseversion_test.go on
// purpose. The two tiers decide the same question in two languages, and the day
// they answer it differently is the day the api refuses to run beside a worker
// while the SPA happily renders against both.
describe("releaseSkew", () => {
  it.each(SKEW_CASES)("$name", ({ mine, theirs, skew }) => {
    expect(releaseSkew(mine, theirs)).toBe(skew);
  });
});

describe("comparableRelease", () => {
  it("treats absent, empty and the dev sentinel alike", () => {
    // One rule, three spellings of "this build does not know" — and all three
    // must read the same, because the SPA sees `undefined` where the Go side
    // sees an empty string and neither may be a difference.
    expect(comparableRelease(undefined)).toBe(false);
    expect(comparableRelease("")).toBe(false);
    expect(comparableRelease("dev")).toBe(false);
    expect(comparableRelease("1970.42")).toBe(true);
  });
});
