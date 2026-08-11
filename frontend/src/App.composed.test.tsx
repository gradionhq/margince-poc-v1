/** @vitest-environment jsdom */
import { cleanup, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  memoryStorage,
  renderApp,
  sessionOnlyFetch,
} from "./testing/appharness";

// The COMPOSED half of the extension-route pair. Its twin,
// App.test.tsx's "extension routes (vanilla registry)", renders the same URL
// against the committed empty-tree stub.
//
// The registry module itself is what is swapped here — "@composition/extensions",
// the exact specifier tsconfig.composed.json and vite.config.ts repoint at
// build/composition/frontend/. That is the real seam a composed build moves, so
// the test exercises the same substitution the build does, and it is the only
// way to render a composed unit from a suite that must also pass in a tree
// where build/composition/ does not exist. The descriptor shape below is
// pinned on the generator side by emit_frontend_test.go.
vi.mock("@composition/extensions", () => ({
  extensions: [
    {
      name: "notes",
      verbs: [
        {
          operationId: "notesList",
          route: "/ext/notes",
          method: "GET",
          title: "List demo notes",
          version: "1.0.0",
          rbacObject: "ext_notes_note",
        },
      ],
    },
  ],
}));

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  Object.defineProperty(globalThis.navigator, "languages", {
    value: ["fr-FR"],
    configurable: true,
  });
  // Only the session probe answers. The unit screen renders from the composed
  // registry alone and must not need a call of its own — the descriptors are
  // compiled in, exactly as the Go boot's verb literals are, so a screen that
  // silently fetched would fail here rather than in a deployed image.
  vi.stubGlobal("fetch", vi.fn(sessionOnlyFetch()));
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

describe("extension routes (composed registry)", () => {
  it("renders the unit's screen at #/ext/notes", async () => {
    window.location.hash = "#/ext/notes";
    renderApp();
    expect(await screen.findByRole("heading", { name: "notes" })).toBeTruthy();
    // The published operations, not decoration: this is the whole content of a
    // unit surface at this stage, and a screen that resolved the descriptor and
    // then rendered nothing from it would pass a name-only assertion.
    expect(screen.getByText("Published operations")).toBeTruthy();
    expect(screen.getByText("List demo notes — GET /ext/notes")).toBeTruthy();
  });

  it("still answers not-found for a unit this installation did not compose", async () => {
    // A populated registry must not make every unit route resolve — the
    // not-found path has to survive the composed lane, or a typo'd bookmark
    // would render whichever unit happened to be first.
    window.location.hash = "#/ext/crm-hello";
    renderApp();
    expect(
      await screen.findByText(
        "No extension named “crm-hello” is enabled on this installation.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "notes" })).toBeNull();
  });
});
