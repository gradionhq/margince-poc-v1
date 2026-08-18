import { describe, expect, it } from "vitest";
import { comparableRelease, releaseSkew } from "./release";

// The whole decision the release gate makes, and it has to hold two things at
// once that pull in opposite directions: a genuinely mixed set must be caught,
// and an installation whose release simply is not known must never be blanked
// out by a guard that mistook absence for disagreement.
//
// This table mirrors the Go one in internal/compose/releaseversion_test.go on
// purpose. The two tiers decide the same question in two languages, and the day
// they answer it differently is the day the api refuses to run beside a worker
// while the SPA happily renders against both.
describe("releaseSkew", () => {
  it.each([
    ["a matched set renders", "1970.42", "1970.42", false],
    ["a torn set does not", "1970.41", "1970.42", true],
    [
      "a torn set does not, whichever side is newer",
      "1970.43",
      "1970.42",
      true,
    ],
    ["an unstamped bundle never blocks", "dev", "1970.42", false],
    ["nor does a local build", "", "1970.42", false],
    [
      "an api reporting no release is not a mismatch",
      "1970.42",
      undefined,
      false,
    ],
    ["nor is one that reports the dev sentinel", "1970.42", "dev", false],
    ["two unknowns are not a mismatch either", "dev", "dev", false],
  ])("%s", (_name, mine, theirs, want) => {
    expect(releaseSkew(mine, theirs as string | undefined)).toBe(want);
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
