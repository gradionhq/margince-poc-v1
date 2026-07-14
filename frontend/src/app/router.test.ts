import { describe, expect, it } from "vitest";
import { parseHash, routeHash } from "./router";

// The hash router's parse/serialize round-trip, pinned so a 3rd path segment
// (the share screen's #/share/<type>/<id>) can be added without silently
// breaking the existing 0/1/2-segment routes every other screen depends on.

describe("parseHash", () => {
  it("parses a bare screen with no id", () => {
    expect(parseHash("#/home")).toEqual({ screen: "home", id: undefined, id2: undefined });
  });

  it("parses a two-segment route (screen + id), id2 undefined", () => {
    expect(parseHash("#/deals/x")).toEqual({
      screen: "deals",
      id: "x",
      id2: undefined,
    });
  });

  it("parses a three-segment route (screen + id + id2)", () => {
    expect(parseHash("#/share/deal/abc")).toEqual({
      screen: "share",
      id: "deal",
      id2: "abc",
    });
  });

  it("falls back to home when the hash is empty", () => {
    expect(parseHash("")).toEqual({ screen: "home" });
    expect(parseHash("#/")).toEqual({ screen: "home" });
  });
});

describe("routeHash", () => {
  it("serializes a bare screen", () => {
    expect(routeHash({ screen: "home" })).toBe("#/home");
  });

  it("serializes a two-segment route", () => {
    expect(routeHash({ screen: "deals", id: "x" })).toBe("#/deals/x");
  });

  it("serializes a three-segment route", () => {
    expect(routeHash({ screen: "share", id: "deal", id2: "abc" })).toBe(
      "#/share/deal/abc",
    );
  });

  it("round-trips share hashes through parse and back", () => {
    const hash = "#/share/organization/o-1";
    expect(routeHash(parseHash(hash))).toBe(hash);
  });
});
