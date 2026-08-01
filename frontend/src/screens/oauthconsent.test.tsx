/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  clearPendingAuthorize,
  readPendingAuthorize,
  stashPendingAuthorize,
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

// The two redirects the backend now hands this screen with no nonce at
// all: a §2 refusal (carries `error`) and the not-signed-in re-entry case
// (carries neither `error` nor `consent`) — the nonce is only ever minted
// server-side, so replaying a spent or nonexistent one is never on offer.
function hashWithError(
  errorCode: string,
  overrides: Record<string, string> = {},
): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: "client-1",
    redirect_uri: "https://client.example/cb",
    scope: "read",
    code_challenge: "abc123",
    code_challenge_method: "S256",
    state: "night-state",
    error: errorCode,
    ...overrides,
  });
  return `#/oauth-consent?${params.toString()}`;
}

function hashWithoutNonce(overrides: Record<string, string> = {}): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: "client-1",
    redirect_uri: "https://client.example/cb",
    scope: "read",
    code_challenge: "abc123",
    code_challenge_method: "S256",
    state: "night-state",
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

// Answers GET /v1/me — the ONE signal the re-entry effect (Finding 3) is
// allowed to act on. hasSession=false 401s exactly like an anonymous
// visitor: the same shape useMe() maps to "no session" for the app's own
// auth gate, so a test that starts from here proves the effect reads the
// real signal rather than assuming one.
function stubSession(hasSession: boolean) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      if (url.pathname === "/v1/me") {
        return hasSession
          ? jsonResponse({ user: { id: "u1" }, roles: [], teams: [] })
          : jsonResponse({ title: "unauthorized" }, 401);
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

  it("posts the SECOND passport when that's the one chosen — not just whatever renders first", async () => {
    const posted = stubAuthorizePost();
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [
        { id: "p1", label: "night agent", scopes: ["read"], granted: ["read"] },
        { id: "p2", label: "day agent", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    await userEvent.selectOptions(await screen.findByRole("combobox"), "p2");
    await userEvent.click(
      await screen.findByRole("button", { name: /authorise/i }),
    );
    // A component that ignored onChange and always posted options[0] would
    // pass a single-passport test but fail this one.
    expect(posted.body.get("passport_id")).toBe("p2");
  });

  it("posts the deny marker when the human refuses the connection", async () => {
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
      await screen.findByRole("button", { name: /deny access/i }),
    );
    expect(posted.body.get("deny")).toBe("1");
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

  it("shows a scope the client asked for that this passport cannot grant at all", async () => {
    stubConsent({
      client_name: "Claude Code",
      requested: ["read", "write"],
      offline: false,
      passports: [
        { id: "p1", label: "night agent", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    await screen.findByRole("button", { name: /authorise/i });
    // "write" is in neither this passport's scopes nor its granted set, so
    // the earlier bug's ScopeChips (fed only `selected.scopes`) never
    // rendered it at all — a client asking for more than the passport can
    // give looked identical to one whose whole request was satisfied.
    expect(screen.getByText("write").textContent).toBe("write not granted");
  });

  it("gives an unlabelled passport a fallback that still identifies it", async () => {
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [
        { id: "p1anonymous", label: "", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    // The server maps a NULL label to "" deliberately — a blank <option>
    // makes two such passports indistinguishable on the one screen where
    // knowing which credential you're lending is the point.
    expect(
      await screen.findByRole("option", { name: /unnamed passport/i }),
    ).toBeTruthy();
  });
});

describe("OAuthConsent — the stash outlives only the request it represents", () => {
  it("clears a stashed pending authorize once the screen has a usable passport list", async () => {
    stashPendingAuthorize({
      url: "/oauth/authorize?client_id=old&scope=read",
      clientName: "Old Client",
    });
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [
        { id: "p1", label: "night agent", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    await screen.findByRole("button", { name: /authorise/i });
    // I9: arriving here with a usable list means any mint detour is over —
    // a stash surviving past this point is what makes Settings offer to
    // "finish" a connection that already went through.
    expect(readPendingAuthorize()).toBeNull();
  });

  it("leaves an existing stash alone while the screen still has nothing to lend", async () => {
    stashPendingAuthorize({
      url: "/oauth/authorize?client_id=old&scope=read",
      clientName: "Old Client",
    });
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    await screen.findByText(/need an agent passport first/i);
    // The other direction of I9: an empty list is still a guide sending the
    // human to mint one, and a stash cleared here would strand that trip —
    // Settings would never offer to finish a connection actually pending.
    expect(readPendingAuthorize()?.clientName).toBe("Old Client");
  });
});

describe("OAuthConsent — the §2 error contract", () => {
  it("renders stale_consent as a dead end with no approve control", async () => {
    globalThis.location.hash = hashWithError("stale_consent");
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    expect(await screen.findByText(/request has expired/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /authorise/i })).toBeNull();
    // The nonce is spent forever, so the recovery is the client, never a
    // reload of this page — the copy says so rather than staying silent.
    expect(
      await screen.findByText(/reloading this page will not help/i),
    ).toBeTruthy();
  });

  it("gives the stale_consent dead end a way back into the app", async () => {
    globalThis.location.hash = hashWithError("stale_consent");
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /back to margince/i }),
    );
    // A rail-less screen with no forward action still needs an exit — this
    // is the app's own "home" route, never the client's callback.
    expect(globalThis.location.hash).toBe("#/home");
  });

  it("renders invalid_request the same way: start again from the client, no approve control", async () => {
    globalThis.location.hash = hashWithError("invalid_request");
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    expect(await screen.findByText(/could not be completed/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /authorise/i })).toBeNull();
    expect(
      screen.getByRole("button", { name: /back to margince/i }),
    ).toBeTruthy();
  });

  it("re-renders the selector for unlendable_passport when other passports remain", async () => {
    globalThis.location.hash = hashWithError("unlendable_passport");
    stubConsent({
      client_name: "Claude Code",
      requested: ["read"],
      offline: false,
      passports: [
        { id: "p2", label: "day agent", scopes: ["read"], granted: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    expect(
      await screen.findByRole("button", { name: /authorise/i }),
    ).toBeTruthy();
    expect(await screen.findByText(/no longer be lent/i)).toBeTruthy();
  });

  it("falls back to the guide for unlendable_passport when nothing remains lendable", async () => {
    globalThis.location.hash = hashWithError("unlendable_passport");
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
    expect(screen.queryByRole("button", { name: /authorise/i })).toBeNull();
  });
});

describe("OAuthConsent — re-entering after sign-in (Finding 3)", () => {
  it("re-enters /oauth/authorize once signed in, when the fragment carries no nonce", async () => {
    globalThis.location.hash = hashWithoutNonce();
    stubSession(true);
    const assigned: string[] = [];
    vi.stubGlobal("location", {
      ...globalThis.location,
      assign: (url: string) => assigned.push(url),
    });
    render(<OAuthConsent />);
    await waitFor(() => expect(assigned).toHaveLength(1));
    const reentered = new URLSearchParams(assigned[0].split("?")[1] ?? "");
    expect(assigned[0]).toContain("/oauth/authorize?");
    // Same exclusion as the mint-detour stash (I8): a fresh nonce is only
    // ever minted server-side, so replaying an absent one is not on offer.
    expect(reentered.has("consent")).toBe(false);
    expect([...reentered.keys()].sort()).toEqual(
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

  it("never re-enters without a session — the effect's own signal never fires", async () => {
    globalThis.location.hash = hashWithoutNonce();
    stubSession(false);
    const assigned: string[] = [];
    vi.stubGlobal("location", {
      ...globalThis.location,
      assign: (url: string) => assigned.push(url),
    });
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    rtlRender(
      <QueryClientProvider client={client}>
        <LocaleProvider initial="en">
          <OAuthConsent />
        </LocaleProvider>
      </QueryClientProvider>,
    );
    // Wait for the /me probe to fully settle to its OTHER terminal state
    // (error) before asserting the negative — proving the effect had every
    // chance to fire and still didn't, not just that it hasn't caught up
    // yet. Loop-freedom rests on this: `me.data` is the only signal the
    // effect reacts to, and an anonymous visitor never produces it.
    await waitFor(() =>
      expect(client.getQueryState(["me"])?.status).toBe("error"),
    );
    expect(assigned).toEqual([]);
  });
});
