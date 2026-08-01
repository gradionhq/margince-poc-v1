/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render as rtlRender, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearPendingAuthorize,
  readPendingAuthorize,
} from "../app/pendingauthorize";
import { formatDate } from "../format/format";
import { LocaleProvider } from "../i18n";
import { OAuthConsent } from "./oauthconsent";

// The consent screen is where a human hands an agent their own authority.
// The nonce that proves the redirect was real is NOT in the endpoint's
// response — the consent cookie that pairs with it is Path=/oauth/authorize
// and never reaches any endpoint the SPA calls — so every test arrives via
// the same route the server actually uses: a realistic redirect fragment.

const NONCE = "n1";

function hashWith(overrides: Record<string, string> = {}): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: "client-1",
    redirect_uri: "https://client.example/cb",
    scope: "read",
    code_challenge: "abc123",
    code_challenge_method: "S256",
    state: "night-state",
    consent: NONCE,
    ...overrides,
  });
  return `#/oauth-consent?${params.toString()}`;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

type ConsentPayload = {
  client_name: string;
  requested: string[];
  offline: boolean;
  passports: Array<{
    id: string;
    label: string;
    scopes: string[];
    granted: string[];
  }>;
};

// Fills every passport's expires_at so the screen's own read of that
// (required) field never breaks a test whose payload didn't spell it out.
function stubConsent(payload: ConsentPayload) {
  const withExpiry = {
    ...payload,
    passports: payload.passports.map((p) => ({
      ...p,
      expires_at: "2026-12-31T00:00:00Z",
    })),
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      if (url.pathname === "/v1/oauth/consent-request") {
        return jsonResponse(withExpiry);
      }
      return jsonResponse({ title: "not found" }, 404);
    }),
  );
}

// Captures a real <form method="post" action="/oauth/authorize"> submit —
// the flow is a native browser POST, not fetch, so a test observes it by
// listening for the submit event and reading the form's own data, exactly as
// the browser would send it. preventDefault avoids jsdom's unimplemented
// navigation from firing on a submit this suite doesn't let complete.
function stubAuthorizePost(): { body: URLSearchParams } {
  const posted = { body: new URLSearchParams() };
  document.addEventListener(
    "submit",
    (event) => {
      event.preventDefault();
      const form = event.target as HTMLFormElement;
      posted.body = new URLSearchParams(
        [...new FormData(form).entries()].map(([key, value]) => [
          key,
          String(value),
        ]),
      );
    },
    { once: true },
  );
  return posted;
}

beforeEach(() => {
  globalThis.location.hash = hashWith();
  clearPendingAuthorize();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  clearPendingAuthorize();
});

describe("OAuthConsent", () => {
  it("guides the human to mint a passport when they have none, and offers no way to approve", async () => {
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    expect(
      await screen.findByText(/need an agent passport first/i),
    ).toBeTruthy();
    // I7: not a disabled button — no approve control at all.
    expect(screen.queryByRole("button", { name: /authorise/i })).toBeNull();
  });

  it("stashes the pending authorize request before sending the human to mint one", async () => {
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /mint a passport/i }),
    );
    const stashed = readPendingAuthorize();
    expect(stashed?.url).toContain("/oauth/authorize?");
    expect(stashed?.clientName).toBe("Claude Code");
    expect(globalThis.location.hash).toBe("#/settings/ai");

    // I8: the nonce must NOT survive into the stash — it is single-use and
    // cookie-bound, and the mint trip navigates away from /oauth/authorize
    // entirely, so replaying it on return would only get a refusal at the
    // very end of the detour. Compare the actual parameter SET rather than
    // grepping for a handful of substrings, so a future edit that drops one
    // of the other required params (or reintroduces the nonce under a
    // different key) fails here too.
    const stashedParams = new URLSearchParams(
      (stashed?.url ?? "").split("?")[1] ?? "",
    );
    expect(stashedParams.has("consent")).toBe(false);
    expect(stashedParams.toString().includes(NONCE)).toBe(false);
    expect([...stashedParams.keys()].sort()).toEqual(
      [
        "response_type",
        "client_id",
        "redirect_uri",
        "scope",
        "code_challenge",
        "code_challenge_method",
        "state",
      ].sort(),
    );
  });

  it("names the client from the server, never from the URL", async () => {
    globalThis.location.hash = hashWith({ client_name: "EVIL" });
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [
        { id: "p1", label: "night agent", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    expect(await screen.findByText(/Claude Code/)).toBeTruthy();
    expect(screen.queryByText(/EVIL/)).toBeNull();
  });

  it("posts the chosen passport and the nonce the redirect handed it", async () => {
    const posted = stubAuthorizePost();
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [
        { id: "p1", label: "night agent", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /authorise/i }),
    );
    expect(posted.body.get("passport_id")).toBe("p1");
    // The nonce comes from the fragment, not from the endpoint: the cookie
    // that holds its counterpart is Path=/oauth/authorize and reaches
    // nothing else.
    expect(posted.body.get("consent")).toBe(NONCE);
  });

  it("shows when the lent passport expires", async () => {
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [
        { id: "p1", label: "night agent", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    // stubConsent always fills expires_at with this instant (see its own
    // comment); the screen renders it through the same formatDate the
    // passport list elsewhere in this app already uses.
    const expected = formatDate("2026-12-31T00:00:00Z", "en", "Europe/Berlin");
    expect(await screen.findByText(new RegExp(expected))).toBeTruthy();
  });

  it("discloses a self-renewing connection separately from the scopes", async () => {
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: true,
      passports: [
        { id: "p1", label: "night agent", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    expect(
      await screen.findByText(/stay connected without asking again/i),
    ).toBeTruthy();
    // I4: offline_access is never an item in a scope list.
    expect(screen.queryByText("offline_access")).toBeNull();
  });
});
