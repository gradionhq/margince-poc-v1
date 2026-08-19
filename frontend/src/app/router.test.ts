import { describe, expect, it } from "vitest";
import { parseHash, routeHash } from "./router";

// The hash router's parse/serialize round-trip, pinned so a 3rd path segment
// (the share screen's #/share/<type>/<id>) can be added without silently
// breaking the existing 0/1/2-segment routes every other screen depends on.

describe("parseHash", () => {
  it("parses a bare screen with no id", () => {
    expect(parseHash("#/home")).toEqual({
      screen: "home",
      id: undefined,
      id2: undefined,
    });
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

  it("parses a four-segment route (the consent return's provider)", () => {
    expect(parseHash("#/onboarding/connect/ok/graph")).toEqual({
      screen: "onboarding",
      id: "connect",
      id2: "ok",
      id3: "graph",
    });
  });

  it("falls back to home when the hash is empty", () => {
    expect(parseHash("")).toEqual({ screen: "home" });
    expect(parseHash("#/")).toEqual({ screen: "home" });
  });

  // A hash is text a human can type, so a screen name that no longer typechecks
  // in source still arrives here. It is a not-found PAGE, not a parse failure
  // and not the "not built yet" surface a mistyped navigate() used to render.
  it("resolves a screen this app does not answer to not-found", () => {
    expect(parseHash("#/dealz")).toEqual({ screen: "not-found" });
  });

  // The segments below an unanswered screen addressed arguments of a page that
  // is not there, so they do not ride along: a not-found route carrying an id
  // would offer the shell a record to look up on a screen nobody has.
  it("drops the segments under a screen this app does not answer", () => {
    expect(parseHash("#/dealz/01J9ZK/tab")).toEqual({ screen: "not-found" });
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

  it("serializes a four-segment route", () => {
    expect(
      routeHash({
        screen: "onboarding",
        id: "connect",
        id2: "ok",
        id3: "graph",
      }),
    ).toBe("#/onboarding/connect/ok/graph");
  });

  it("round-trips share hashes through parse and back", () => {
    const hash = "#/share/organization/o-1";
    expect(routeHash(parseHash(hash))).toBe(hash);
  });
});
