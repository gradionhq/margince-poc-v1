/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { LocaleProvider } from "./i18n";

// B-EP09.17: the locale switch flips the whole UI between DE and EN. With the
// browser asking for a language we don't ship, the app mounts in the A100
// fallback (en); one click renders the German chrome. The browser-level e2e
// twin of this test rides the 09.22 harness.
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
  // Pin the browser language to one we don't ship so mount resolves to the
  // A100 fallback deterministically, independent of the CI machine's locale.
  Object.defineProperty(globalThis.navigator, "languages", {
    value: ["fr-FR"],
    configurable: true,
  });
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

describe("custom-fields route", () => {
  it("renders the Custom fields admin screen at #/custom-fields", async () => {
    // Every query the screen fires must resolve, or QueryGate paints its error
    // card instead of the heading: /me (admin so the builder mounts), the
    // per-object field list, and the audit rail.
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: Request | string | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return new Response(
            JSON.stringify({
              user: { id: "u1" },
              roles: ["admin"],
              teams: [],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        if (url.includes("/v1/custom-fields")) {
          return new Response(JSON.stringify({ data: [], page: {} }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        if (url.includes("/v1/audit-log")) {
          return new Response(JSON.stringify({ data: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        return new Response(JSON.stringify({ code: "unavailable" }), {
          status: 503,
          headers: { "Content-Type": "application/problem+json" },
        });
      }),
    );
    window.location.hash = "#/custom-fields";
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <App />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    expect(
      await screen.findByRole("heading", { name: "Custom fields" }),
    ).toBeTruthy();
  });
});

describe("locale switch", () => {
  it("mounts in English (A100) and flips the chrome to German on switch", async () => {
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
    // English default: once the session resolves, the rail carries English labels
    expect(await screen.findByRole("link", { name: "Contacts" })).toBeTruthy();
    await userEvent.click(
      screen.getByRole("button", { name: "Switch to German" }),
    );
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Kontakte" })).toBeTruthy(),
    );
    expect(screen.queryByRole("link", { name: "Contacts" })).toBeNull();
  });
});

describe("auth boundary states (login spec §4)", () => {
  const mount = () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <App />
        </LocaleProvider>
      </QueryClientProvider>,
    );
  };
  const probe = (status: number) =>
    vi.fn(async (input: Request | string | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.endsWith("/v1/me")) {
        return new Response(JSON.stringify({ code: "x" }), {
          status,
          headers: { "Content-Type": "application/problem+json" },
        });
      }
      return new Response(JSON.stringify({}), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    });

  it("renders login on 401 — not signed in is an authentication state", async () => {
    vi.stubGlobal("fetch", probe(401));
    mount();
    expect(
      await screen.findByRole("heading", { name: "Sign in to Margince" }),
    ).toBeTruthy();
  });

  it("renders the connection problem on 5xx — an outage is never a login", async () => {
    vi.stubGlobal("fetch", probe(500));
    mount();
    expect(
      await screen.findByText("Margince couldn't be reached"),
    ).toBeTruthy();
    expect(screen.queryByLabelText("Email address")).toBeNull();
  });

  it("renders installation-unavailable on 503 and retry re-probes /me", async () => {
    const fetchMock = probe(503);
    vi.stubGlobal("fetch", fetchMock);
    mount();
    expect(await screen.findByText("Installation not ready")).toBeTruthy();
    const before = fetchMock.mock.calls.length;
    await userEvent.click(screen.getByRole("button", { name: "Try again" }));
    await waitFor(() =>
      expect(fetchMock.mock.calls.length).toBeGreaterThan(before),
    );
  });

  it("renders the connection problem when the probe cannot reach the API at all", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("network down");
      }),
    );
    mount();
    expect(
      await screen.findByText("Margince couldn't be reached"),
    ).toBeTruthy();
  });
});
