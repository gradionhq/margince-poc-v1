/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { LocaleProvider } from "./i18n";

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
      name: "crm-demo",
      verbs: [
        {
          operationId: "crmDemoListNotes",
          route: "/v1/ext/crm-demo/notes",
          method: "GET",
          title: "List demo notes",
          version: "1.0.0",
          rbacObject: "ext_crm_demo_note",
        },
      ],
    },
  ],
}));

function memoryStorage(): Storage {
  const map = new Map<string, string>();
  return {
    getItem: (key) => (map.has(key) ? (map.get(key) as string) : null),
    setItem: (key, value) => {
      map.set(key, String(value));
    },
    removeItem: (key) => {
      map.delete(key);
    },
    clear: () => map.clear(),
    key: (index) => Array.from(map.keys())[index] ?? null,
    get length() {
      return map.size;
    },
  };
}

beforeEach(() => {
  vi.stubGlobal("localStorage", memoryStorage());
  globalThis.localStorage.setItem("margince.workspaceSlug", "acme");
  Object.defineProperty(globalThis.navigator, "languages", {
    value: ["fr-FR"],
    configurable: true,
  });
  // Only the session probe answers; the unit screen renders from the composed
  // registry alone and must not need a call of its own — the descriptors are
  // compiled in, exactly as the Go boot's verb literals are.
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: Request | string | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.endsWith("/v1/me")) {
        return new Response(
          JSON.stringify({ user: {}, roles: [], teams: [] }),
          {
            status: 200,
            headers: { "Content-Type": "application/json" },
          },
        );
      }
      return new Response(JSON.stringify({ code: "unavailable" }), {
        status: 503,
        headers: { "Content-Type": "application/problem+json" },
      });
    }),
  );
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.location.hash = "";
});

function mount() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider>
        <App />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

describe("extension routes (composed registry)", () => {
  it("renders the unit's screen at #/ext/crm-demo", async () => {
    window.location.hash = "#/ext/crm-demo";
    mount();
    expect(
      await screen.findByRole("heading", { name: "crm-demo" }),
    ).toBeTruthy();
    // The published operations, not decoration: this is the whole content of a
    // unit surface at this stage, and a screen that resolved the descriptor and
    // then rendered nothing from it would pass a name-only assertion.
    expect(screen.getByText("Published operations")).toBeTruthy();
    expect(
      screen.getByText("List demo notes — GET /v1/ext/crm-demo/notes"),
    ).toBeTruthy();
  });

  it("still answers not-found for a unit this installation did not compose", async () => {
    // A populated registry must not make every unit route resolve — the
    // not-found path has to survive the composed lane, or a typo'd bookmark
    // would render whichever unit happened to be first.
    window.location.hash = "#/ext/crm-hello";
    mount();
    expect(
      await screen.findByText(
        "No extension named “crm-hello” is enabled on this installation.",
      ),
    ).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "crm-demo" })).toBeNull();
  });
});
