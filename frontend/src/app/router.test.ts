import { describe, expect, it } from "vitest";
import { parseHash, routeHash, routeIdentity } from "./router";

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

// What a screen's subtree is keyed on (App.tsx), so these are the claims that
// decide whether a navigation is a remount or a re-render.
describe("routeIdentity", () => {
  it("drops the person page's tab: six tabs, one identity", () => {
    const overview = routeIdentity({
      screen: "contacts",
      id: "p-1",
      id2: "overview",
    });
    expect(overview).toBe("#/contacts/p-1");
    for (const tab of [
      "timeline",
      "deals",
      "meetings",
      "research",
      "documents",
    ]) {
      expect(routeIdentity({ screen: "contacts", id: "p-1", id2: tab })).toBe(
        overview,
      );
    }
  });

  it("keeps the person apart from the next person", () => {
    expect(
      routeIdentity({ screen: "contacts", id: "p-1", id2: "deals" }),
    ).not.toBe(routeIdentity({ screen: "contacts", id: "p-2", id2: "deals" }));
  });

  it("keeps the contacts list apart from a person on it", () => {
    expect(routeIdentity({ screen: "contacts" })).toBe("#/contacts");
    expect(routeIdentity({ screen: "contacts", id: "p-1" })).toBe(
      "#/contacts/p-1",
    );
  });

  // Every other screen's segments name the thing, not a view of it, so its
  // identity is the address it already had. Settings is the one worth stating:
  // its sidebar looks like a tab strip and is not one — each entry is a page,
  // and the admin half lives a segment deeper.
  it("leaves a settings address whole, admin half included", () => {
    expect(routeIdentity({ screen: "settings", id: "profile" })).toBe(
      "#/settings/profile",
    );
    expect(
      routeIdentity({ screen: "settings", id: "admin", id2: "users" }),
    ).toBe("#/settings/admin/users");
  });

  it("leaves both of a share address's record segments in", () => {
    expect(routeIdentity({ screen: "share", id: "deal", id2: "d-1" })).toBe(
      "#/share/deal/d-1",
    );
  });

  it("leaves a four-segment flow address whole", () => {
    expect(
      routeIdentity({
        screen: "onboarding",
        id: "connect",
        id2: "ok",
        id3: "graph",
      }),
    ).toBe("#/onboarding/connect/ok/graph");
  });

  // The identity is an address the router can read back, not a private
  // encoding: whatever it returns has to parse to the screen it names, or the
  // key and the view could disagree about what is on screen.
  it("returns an address that parses back to the same screen", () => {
    for (const hash of [
      "#/contacts/p-1/deals",
      "#/settings/admin/users",
      "#/share/deal/d-1",
      "#/home",
    ]) {
      const route = parseHash(hash);
      expect(parseHash(routeIdentity(route)).screen).toBe(route.screen);
    }
  });
});
