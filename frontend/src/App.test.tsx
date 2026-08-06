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
    // The language control lives in the account menu, so the switch takes opening it.
    await userEvent.click(screen.getByRole("button", { name: "Account" }));
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
    expect(screen.queryByLabelText("Email")).toBeNull();
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

// The emailed reset link must reach the reset form regardless of whatever
// session the browser already carries — a stale/live cookie must never turn
// a password-reset link into the authenticated shell's unrecognised-route
// fallback, and a token redeemed to completion must not strand the
// following sign-in off the app's normal post-login redirect.
describe("password-reset deep link", () => {
  const supportingRoutes = async (input: Request | string | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.endsWith("/v1/auth/capabilities")) {
      return new Response(
        JSON.stringify({ oidc_providers: [], password: true }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    if (url.endsWith("/v1/assistant/profile")) {
      return new Response(
        JSON.stringify({
          name: "Margince",
          kind: "ai",
          state: "unconfigured",
          inference_mode: "none",
          providers: [],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    }
    return null;
  };

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

  it("opens the reset form for an already-signed-in browser, not the pending screen", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: Request | string | URL) => {
        const supporting = await supportingRoutes(input);
        if (supporting) return supporting;
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return new Response(
            JSON.stringify({ user: { id: "u1" }, roles: [], teams: [] }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        return new Response(JSON.stringify({ data: [], page: {} }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );
    window.location.hash = "#/reset-password?token=live-session-token";
    mount();
    expect(await screen.findByLabelText("New password")).toBeTruthy();
  });

  it("reaches home on the sign-in that follows a completed reset", async () => {
    // No session at the start: the ordinary case for a password reset. Once
    // the login below succeeds, /v1/me flips to authenticated — proving the
    // sign-in actually completed the redirect rather than getting stuck
    // because the reset route was still sitting in the hash.
    let authenticated = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: Request | string | URL) => {
        const supporting = await supportingRoutes(input);
        if (supporting) return supporting;
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return authenticated
            ? new Response(
                JSON.stringify({ user: { id: "u1" }, roles: [], teams: [] }),
                {
                  status: 200,
                  headers: { "Content-Type": "application/json" },
                },
              )
            : new Response(JSON.stringify({ code: "unauthorized" }), {
                status: 401,
                headers: { "Content-Type": "application/problem+json" },
              });
        }
        if (url.endsWith("/v1/auth/reset-password")) {
          return new Response(null, { status: 204 });
        }
        if (url.endsWith("/v1/auth/login")) {
          authenticated = true;
          return new Response(JSON.stringify({}), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        if (url.endsWith("/v1/company")) {
          return new Response(
            JSON.stringify({ organization_id: "o1", display_name: "Acme" }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        return new Response(JSON.stringify({ data: [], page: {} }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );
    window.location.hash = "#/reset-password?token=completed-reset-token";
    mount();

    await userEvent.type(
      await screen.findByLabelText("New password"),
      "an entirely new password{enter}",
    );
    await userEvent.click(
      await screen.findByRole("button", { name: "Back to sign in" }),
    );
    await waitFor(() =>
      expect(window.location.hash).not.toContain("reset-password"),
    );
    await userEvent.type(await screen.findByLabelText("Email"), "a@b.com");
    await userEvent.type(
      screen.getByLabelText("Password"),
      "an entirely new password{enter}",
    );
    // The rail is proof the app reached home, not proof merely that /v1/me
    // now resolves — a stale reset hash would instead leave login re-rendered
    // with nowhere for the post-login redirect to go.
    expect(await screen.findByRole("navigation")).toBeTruthy();
  });

  it("reaches home on a sign-in from a bare reset link that never carried a token", async () => {
    // No query string at all — a stale or hand-typed "#/reset-password" with
    // nothing to reset. resetTokenFromLocation finds no token, so this mounts
    // straight into the ordinary login form (never ResetForm, and never the
    // "Back to sign in" step that would otherwise have cleared the hash) — the
    // hash stays exactly "#/reset-password" through the whole sign-in.
    let authenticated = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: Request | string | URL) => {
        const supporting = await supportingRoutes(input);
        if (supporting) return supporting;
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return authenticated
            ? new Response(
                JSON.stringify({ user: { id: "u1" }, roles: [], teams: [] }),
                {
                  status: 200,
                  headers: { "Content-Type": "application/json" },
                },
              )
            : new Response(JSON.stringify({ code: "unauthorized" }), {
                status: 401,
                headers: { "Content-Type": "application/problem+json" },
              });
        }
        if (url.endsWith("/v1/auth/login")) {
          authenticated = true;
          return new Response(JSON.stringify({}), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          });
        }
        if (url.endsWith("/v1/company")) {
          return new Response(
            JSON.stringify({ organization_id: "o1", display_name: "Acme" }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        return new Response(JSON.stringify({ data: [], page: {} }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );
    window.location.hash = "#/reset-password";
    mount();

    await userEvent.type(await screen.findByLabelText("Email"), "a@b.com");
    await userEvent.type(
      screen.getByLabelText("Password"),
      "an entirely new password{enter}",
    );
    // The rail is proof the sign-in actually landed the reader on the app —
    // the non-empty "#/reset-password" hash LoginForm's own redirect check
    // preserves would otherwise leave this render stuck on the login form.
    expect(await screen.findByRole("navigation")).toBeTruthy();
  });
});

// The onboarding gate (A107/ADR-0061 + the 0082 anchor): an installation that
// has not saved its own company has nothing for any other screen to show, so
// the shell sends the human to the company form. GET /company 404s until a
// human saves it — that 404 IS the signal, which is why the gate lives here
// rather than on the login path: a live session never passes through login, so
// a reload would otherwise walk straight past onboarding.
describe("onboarding gate", () => {
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

  // Every call the shell makes resolves; only /company's status varies, so the
  // gate is the single thing under test.
  const stubCompany = (status: number) =>
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: Request | string | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return new Response(
            JSON.stringify({ user: { id: "u1" }, roles: ["admin"], teams: [] }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        if (url.endsWith("/v1/company")) {
          return status === 200
            ? new Response(
                JSON.stringify({
                  organization_id: "o1",
                  display_name: "Acme GmbH",
                }),
                {
                  status: 200,
                  headers: { "Content-Type": "application/json" },
                },
              )
            : new Response(JSON.stringify({ code: "not_found" }), {
                status,
                headers: { "Content-Type": "application/problem+json" },
              });
        }
        return new Response(JSON.stringify({ data: [], page: {} }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );

  it("sends an installation that has not described itself to the company form", async () => {
    stubCompany(404);
    mount();
    await waitFor(() =>
      expect(window.location.hash).toBe("#/onboarding/company"),
    );
  });

  it("holds on every navigation — steering away mid-onboarding lands back on the form", async () => {
    stubCompany(404);
    mount();
    await waitFor(() =>
      expect(window.location.hash).toBe("#/onboarding/company"),
    );

    // The palette, a typed hash, a stray link: any client-side navigation
    // away from onboarding must be turned around, not just the first load.
    window.location.hash = "#/contacts";
    window.dispatchEvent(new HashChangeEvent("hashchange"));
    await waitFor(() =>
      expect(window.location.hash).toBe("#/onboarding/company"),
    );
  });

  it("leaves a described installation on the route it asked for", async () => {
    window.location.hash = "#/contacts";
    stubCompany(200);
    mount();
    // The company resolves before this settles, so a gate that redirected
    // would have replaced the hash by now.
    await screen.findByRole("navigation");
    expect(window.location.hash).toBe("#/contacts");
  });

  // A pending /oauth/authorize request lives entirely in the hash (the
  // client_id/scope/consent-nonce query string) — navigate() rewrites
  // location.hash, so a gate redirect here would destroy the request with no
  // way to recover it, unlike an ordinary screen a human can simply re-visit.
  it("does not redirect away from oauth-consent when the company is undescribed", async () => {
    const pendingHash =
      "#/oauth-consent?client_id=c1&scope=read&consent=nonce123";
    window.location.hash = pendingHash;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: Request | string | URL) => {
        const url = String(input instanceof Request ? input.url : input);
        if (url.endsWith("/v1/me")) {
          return new Response(
            JSON.stringify({ user: { id: "u1" }, roles: ["admin"], teams: [] }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        if (url.endsWith("/v1/company")) {
          return new Response(JSON.stringify({ code: "not_found" }), {
            status: 404,
            headers: { "Content-Type": "application/problem+json" },
          });
        }
        if (url.includes("/oauth/consent-request")) {
          return new Response(
            JSON.stringify({
              client_name: "Acme Client",
              offline: false,
              passports: [
                {
                  id: "p1",
                  label: "Everyday agent",
                  scopes: ["read"],
                  expires_at: "2027-01-01T00:00:00Z",
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          );
        }
        return new Response(JSON.stringify({ data: [], page: {} }), {
          status: 200,
          headers: { "Content-Type": "application/json" },
        });
      }),
    );
    mount();
    // The consent screen itself is proof the gate never fired — an
    // onboarding redirect would have replaced the hash before this renders.
    expect(
      await screen.findByRole("heading", { name: "Authorize access" }),
    ).toBeTruthy();
    expect(window.location.hash).toBe(pendingHash);
  });

  // The control for the exemption above: it must be scoped to the consent
  // route, not a gate that stopped firing. This is the third premise the gate
  // has to answer — an ordinary route named in the hash on FIRST load (the
  // cases above cover an empty hash, and a hashchange after mount).
  it("still redirects an ordinary screen away when the company is undescribed", async () => {
    window.location.hash = "#/contacts";
    stubCompany(404);
    mount();
    await waitFor(() =>
      expect(window.location.hash).toBe("#/onboarding/company"),
    );
  });
});
