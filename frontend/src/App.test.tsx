/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { LocaleProvider } from "./i18n";

// B-EP09.17: the locale switch flips the whole UI between DE and EN. The app
// mounts in the A24 default (de); one click renders the English chrome. The
// browser-level e2e twin of this test rides the 09.22 harness.
//
// The shell only renders behind a session: App probes GET /v1/me and shows the
// authenticated chrome once it is 200. The test seeds a workspace slug + a
// stubbed /me so the rail is reached (the signup/login gate has its own test).

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
  // Only the session probe succeeds; the home screen's own data calls fail and
  // fall to their QueryGate error state (the rail still renders — that is what
  // this test asserts). Routing by URL keeps the stub honest per endpoint.
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: Request | string | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.endsWith("/v1/me")) {
        return new Response(
          JSON.stringify({ user: {}, roles: [], teams: [] }),
          { status: 200, headers: { "Content-Type": "application/json" } },
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

describe("locale switch", () => {
  it("mounts in German (A24) and flips the chrome to English on switch", async () => {
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
    // German default: once the session resolves, the rail carries German labels
    expect(await screen.findByRole("link", { name: "Kontakte" })).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: "Auf Englisch umschalten" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Contacts" })).toBeTruthy(),
    );
    expect(screen.queryByRole("link", { name: "Kontakte" })).toBeNull();
  });
});
