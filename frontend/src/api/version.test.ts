import { describe, expect, it } from "vitest";
import { ifMatch } from "./version";

describe("ifMatch", () => {
  it("omits the header when version is undefined", () => {
    expect(ifMatch(undefined)).toEqual({ header: {} });
  });
  it("sets If-Match from the version", () => {
    expect(ifMatch(7)).toEqual({ header: { "If-Match": "7" } });
  });
});
