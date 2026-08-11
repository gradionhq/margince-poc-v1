/** @vitest-environment jsdom */
import { cleanup, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  memoryStorage,
  renderApp,
  sessionOnlyFetch,
} from "./testing/appharness";

// The THIRD extension-route case, beside App.test.tsx's vanilla lane and
// App.composed.test.tsx's descriptor lane: a composed unit that also has a
// bespoke core screen listed in the composed screen registry.
//
// Both aliased modules are swapped, because both are what a composed build
// swaps: "@composition/extensions" is the descriptor registry the route
// resolves against, and "@composition/screens" is the registry it then looks
// the unit's screen up in. The order matters and is what this file pins — the
// screen renders only AFTER the descriptor resolves, so a registry entry for a
// unit the installation did not compose is inert rather than a route into a
// surface with no server behind it.
//
// The screen itself is a stub. The real one (src/screens/ext/notes.tsx) is
// outside the vanilla TypeScript program and is exercised by its own suite;
// what is under test here is App.tsx's dispatch, and mounting the real screen
// would drag its six network calls into a routing test.
vi.mock("@composition/extensions", () => ({
  extensions: [
    {
      name: "notes",
      verbs: [
        {
          operationId: "notesList",
          route: "/ext/notes/list",
          method: "POST",
          title: "List demo notes",
          version: "1.0.0",
          rbacObject: "ext_notes_note",
        },
      ],
    },
    // A composed unit with NO screen of its own, named to collide with a member
    // of Object.prototype. The unit-name grammar
    // (^[a-z0-9]+(-[a-z0-9]+)*$, extension.Name.Validate) admits "constructor",
    // and a registry that is a plain object literal answers that key from the
    // prototype chain — so the miss path has to be a real miss, not a truthy
    // inherited function.
    {
      name: "constructor",
      verbs: [
        {
          operationId: "constructorPing",
          route: "/ext/constructor/ping",
          method: "POST",
          title: "Ping",
          version: "1.0.0",
          rbacObject: "ext_constructor_thing",
        },
      ],
    },
  ],
}));

vi.mock("@composition/screens", () => ({
  extensionScreens: {
    notes: () => <h1>Demo Notepad</h1>,
    // A unit this installation did NOT compose. It must never render: the
    // descriptor lookup is the gate, and an entry here is not one.
    "crm-ghost": () => <h1>Ghost</h1>,
  },
}));

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  vi.stubGlobal("fetch", vi.fn(sessionOnlyFetch()));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("extension routes (composed screen registry)", () => {
  it("renders the unit's own screen in place of the descriptor card", async () => {
    window.location.hash = "#/ext/notes";
    renderApp();
    expect(
      await screen.findByRole("heading", { name: "Demo Notepad" }),
    ).toBeTruthy();
    // The generic card must be GONE, not merely outranked: rendering both
    // would put a second, contradictory account of the unit on one page.
    expect(screen.queryByText("Published operations")).toBeNull();
  });

  // The registry lookup must answer for the unit's OWN entries only. Read off a
  // plain object, `extensionScreens["constructor"]` is Object itself — truthy,
  // and a function, so the dispatch mounts it and the route dies where it
  // should have shown the descriptor card.
  it("falls back to the descriptor card for a unit whose name is an Object.prototype member", async () => {
    window.location.hash = "#/ext/constructor";
    renderApp();
    expect(await screen.findByText("Published operations")).toBeTruthy();
    expect(await screen.findByText(/Ping/)).toBeTruthy();
  });

  it("does not render a screen for a unit the installation did not compose", async () => {
    window.location.hash = "#/ext/crm-ghost";
    renderApp();
    expect(
      await screen.findByText(
        "No extension named “crm-ghost” is enabled on this installation.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Ghost" })).toBeNull();
  });
});
