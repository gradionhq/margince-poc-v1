import { describe, expect, it } from "vitest";
import { ifMatch, requireVersion } from "./version";

describe("ifMatch", () => {
  it("sets If-Match from the version", () => {
    expect(ifMatch(7)).toEqual({ header: { "If-Match": "7" } });
  });

  // There is no argument that gets a write past this function without a
  // precondition, and version zero is what proves it: a truthiness check reads
  // it as "no version" and sends the write unpinned, which is the one outcome a
  // conditional write must never have.
  it("states a precondition for every version, zero included", () => {
    expect(ifMatch(0)).toEqual({ header: { "If-Match": "0" } });
  });
});

describe("requireVersion", () => {
  it("passes a real version through untouched", () => {
    expect(requireVersion(3)).toBe(3);
  });

  // The contract lets a row come back unversioned, and the honest answer is no
  // write at all: an unpinned write is last-write-wins and reports success to
  // the editor whose change it just erased.
  it("refuses a row that came back without a version", () => {
    expect(() => requireVersion(undefined)).toThrow();
  });
});
