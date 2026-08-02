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

// Every fixture carries the RFC 8707 audience param the server armed the
// request with: a redirect fragment that omits it cannot prove the screen
// carries it forward, and `resource` is the one authorize param whose loss
// would be silent — the flow still completes, bound to the wrong audience.
const RESOURCE = "https://margince.example/mcp";

// The whole authorize request minus the nonce — what a re-entry (the mint
// detour's stash, the post-sign-in retry) must carry, spelled once so both
// assertions read from the same list.
const AUTHORIZE_KEYS = [
  "response_type",
  "client_id",
  "redirect_uri",
  "scope",
  "code_challenge",
  "code_challenge_method",
  "resource",
  "state",
];

function hashWith(overrides: Record<string, string> = {}): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: "client-1",
    redirect_uri: "https://client.example/cb",
    scope: "read",
    code_challenge: "abc123",
    code_challenge_method: "S256",
    resource: RESOURCE,
    state: "night-state",
    consent: NONCE,
    ...overrides,
  });
  return `#/oauth-consent?${params.toString()}`;
}

// A TERMINAL refusal: the request comes back with a marker and no nonce,
// because nothing the human could submit would be accepted any more. The same
// shape as the not-signed-in redirect (which carries no marker either), and the
// shape a nonce is never minted client-side to fill in.
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
    resource: RESOURCE,
    state: "night-state",
    error: errorCode,
    ...overrides,
  });
  return `#/oauth-consent?${params.toString()}`;
}

// A RECOVERABLE refusal: the pending authorization is untouched, so the server
// hands the nonce back with the marker and the human's next choice is submitted
// inside the same window. This is the only refusal shape that may render a form.
function hashWithRetry(errorCode: string): string {
  return hashWithError(errorCode, { consent: NONCE });
}

function hashWithoutNonce(overrides: Record<string, string> = {}): string {
  const params = new URLSearchParams({
    response_type: "code",
    client_id: "client-1",
    redirect_uri: "https://client.example/cb",
    scope: "read",
    code_challenge: "abc123",
    code_challenge_method: "S256",
    resource: RESOURCE,
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

// Hands the client back for the one case that has to make the screen read the
// consent request a SECOND time (a passport revoked while the screen was open).
function renderWithClient(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
  return client;
}

function render(ui: ReactNode) {
  renderWithClient(ui);
}

type ConsentPayload = {
  client_name: string;
  offline: boolean;
  passports: Array<{
    id: string;
    label: string;
    scopes: string[];
  }>;
};

// Fills every passport's expires_at so the screen's own read of that
// (required) field never breaks a test whose payload didn't spell it out.
function withExpiry(payload: ConsentPayload) {
  return {
    ...payload,
    passports: payload.passports.map((p) => ({
      ...p,
      expires_at: "2026-12-31T00:00:00Z",
    })),
  };
}

// Answers the consent-request read from whatever `current()` returns AT THE
// TIME OF THE CALL, so a test can change what the server offers between two
// reads — the shape a passport revoked in another tab actually arrives in.
function stubConsentReads(current: () => ConsentPayload) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      if (url.pathname === "/v1/oauth/consent-request") {
        return jsonResponse(withExpiry(current()));
      }
      return jsonResponse({ title: "not found" }, 404);
    }),
  );
}

function stubConsent(payload: ConsentPayload) {
  stubConsentReads(() => payload);
}

// Every read this screen makes fails. The states that need no server data must
// still render: a refusal whose whole content IS the refusal cannot be gated
// behind a fetch that the same cause often breaks.
function stubConsentUnavailable() {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => jsonResponse({ title: "not found" }, 404)),
  );
}

// Answers GET /v1/me — the ONE signal the re-entry effect is allowed to act
// on. hasSession=false 401s exactly like an anonymous visitor: the same shape
// useMe() maps to "no session" for the app's own auth gate, so a test that
// starts from here proves the effect reads the real signal rather than
// assuming one.
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
      const form = event.target;
      if (!(form instanceof HTMLFormElement)) {
        throw new Error("submit fired on something that is not a form");
      }
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

// The viewer's zone as the screen asks for it. Restored by the suite's
// afterEach, so one case pretending to be elsewhere never leaks into the next.
function pretendViewerZone(timeZone: string): void {
  const real = Intl.DateTimeFormat().resolvedOptions();
  vi.spyOn(Intl.DateTimeFormat.prototype, "resolvedOptions").mockReturnValue({
    ...real,
    timeZone,
  });
}

beforeEach(() => {
  globalThis.location.hash = hashWith();
  clearPendingAuthorize();
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  clearPendingAuthorize();
});

describe("OAuthConsent", () => {
  it("guides the human to mint a passport when they have none, and offers no way to approve", async () => {
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    expect(
      await screen.findByText(/need an agent passport first/i),
    ).toBeTruthy();
    // Not a disabled button — no approve control at all.
    expect(screen.queryByRole("button", { name: /authorize/i })).toBeNull();
  });

  it("stashes the pending authorize request before sending the human to mint one", async () => {
    stubConsent({
      client_name: "Claude Code",
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

    // The nonce must NOT survive into the stash — it is single-use and
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
      [...AUTHORIZE_KEYS].sort(),
    );
    expect(stashedParams.get("resource")).toBe(RESOURCE);
  });

  it("names the client from the server, never from the URL", async () => {
    globalThis.location.hash = hashWith({ client_name: "EVIL" });
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [{ id: "p1", label: "night agent", scopes: ["read"] }],
    });
    render(<OAuthConsent />);
    expect(await screen.findByText(/Claude Code/)).toBeTruthy();
    expect(screen.queryByText(/EVIL/)).toBeNull();
  });

  it("posts the chosen passport and the nonce the redirect handed it", async () => {
    const posted = stubAuthorizePost();
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [{ id: "p1", label: "night agent", scopes: ["read"] }],
    });
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /authorize/i }),
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
      offline: false,
      passports: [
        { id: "p1", label: "night agent", scopes: ["read"] },
        { id: "p2", label: "day agent", scopes: ["read"] },
      ],
    });
    render(<OAuthConsent />);
    await userEvent.selectOptions(await screen.findByRole("combobox"), "p2");
    await userEvent.click(
      await screen.findByRole("button", { name: /authorize/i }),
    );
    // A component that ignored onChange and always posted options[0] would
    // pass a single-passport test but fail this one.
    expect(posted.body.get("passport_id")).toBe("p2");
  });

  it("posts the passport it is displaying, even after a re-read drops the chosen one", async () => {
    const posted = stubAuthorizePost();
    const night = {
      id: "p1",
      label: "night agent",
      scopes: ["read"],
    };
    const day = {
      id: "p2",
      label: "day agent",
      scopes: ["read"],
    };
    let lendable = [night, day];
    stubConsentReads(() => ({
      client_name: "Claude Code",
      offline: false,
      passports: lendable,
    }));
    const client = renderWithClient(<OAuthConsent />);
    await userEvent.selectOptions(await screen.findByRole("combobox"), "p2");

    // The day agent is revoked in another tab; the next read of the same
    // request no longer offers it, and the selector falls back to the one
    // passport that is left.
    lendable = [night];
    await client.invalidateQueries({ queryKey: ["oauth-consent-request"] });
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));

    await userEvent.click(screen.getByRole("button", { name: /authorize/i }));
    // Displayed and posted are ONE value. A form still carrying the vanished
    // p2 would let the human approve the passport on screen while lending a
    // different one.
    expect(screen.getByRole("combobox")).toHaveValue("p1");
    expect(posted.body.get("passport_id")).toBe("p1");
  });

  it("posts the deny marker when the human refuses the connection", async () => {
    const posted = stubAuthorizePost();
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [{ id: "p1", label: "night agent", scopes: ["read"] }],
    });
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /deny access/i }),
    );
    expect(posted.body.get("deny")).toBe("1");
    expect(posted.body.get("consent")).toBe(NONCE);
  });

  it("shows when the lent passport expires, on the viewer's own calendar", async () => {
    // stubConsent always fills expires_at with 2026-12-31T00:00:00Z (see its
    // own comment) — an instant that is already 31 December in Berlin but
    // still 30 December on the US west coast. Pretend the viewer is there:
    // formatDate takes its zone as an argument and never consults
    // resolvedOptions, so this spy redirects only the screen's own lookup.
    pretendViewerZone("America/Los_Angeles");
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [{ id: "p1", label: "night agent", scopes: ["read"] }],
    });
    render(<OAuthConsent />);
    // The locked locale convention (format.ts INTL_LOCALE, "A100:
    // unconfigured English is en-GB") renders DD/MM/YYYY: a fixed
    // Europe/Berlin would print 31/12/2026 to a human whose calendar still
    // says the 30th, on the one screen where the credential's lifetime is
    // the decision.
    expect(await screen.findByText(/30\/12\/2026/)).toBeTruthy();
    const fixedZoneDate = formatDate(
      "2026-12-31T00:00:00Z",
      "en",
      "Europe/Berlin",
    );
    expect(screen.queryByText(new RegExp(fixedZoneDate))).toBeNull();
  });

  it("discloses a self-renewing connection separately from the scopes", async () => {
    stubConsent({
      client_name: "Claude Code",
      offline: true,
      passports: [{ id: "p1", label: "night agent", scopes: ["read"] }],
    });
    render(<OAuthConsent />);
    expect(
      await screen.findByText(/stay connected without asking again/i),
    ).toBeTruthy();
    // offline_access is never an item in a scope list.
    expect(screen.queryByText("offline_access")).toBeNull();
  });

  it("shows the whole passport as the grant when the client asked for less", async () => {
    stubConsent({
      client_name: "Claude Code",
      // The shape every real MCP client produces: it named no scope, so the
      // server read the request as `read` alone. The passport is far wider.
      offline: false,
      passports: [
        {
          id: "p1",
          label: "night agent",
          scopes: ["read", "write", "send"],
        },
      ],
    });
    render(<OAuthConsent />);
    await screen.findByRole("button", { name: /authorize/i });
    // All three are the grant, and each reads as one: a chip the human is told
    // is withheld would be a lie about what this connection can do.
    for (const scope of ["read", "write", "send"]) {
      expect(screen.getByText(scope).textContent).toBe(scope);
    }
    // No leftover of the old narrowing disclosure in either direction — the
    // request neither dims a chip nor earns a line about what it did not get.
    expect(screen.queryByText(/not granted/i)).toBeNull();
    expect(screen.queryByText(/asked for more/i)).toBeNull();
    expect(
      screen.getByText(/gets exactly the scopes shown/i).textContent,
    ).toContain("this passport carries");
  });

  it("gives an unlabelled passport a fallback that still identifies it", async () => {
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [{ id: "p1anonymous", label: "", scopes: ["read"] }],
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
      offline: false,
      passports: [{ id: "p1", label: "night agent", scopes: ["read"] }],
    });
    render(<OAuthConsent />);
    await screen.findByRole("button", { name: /authorize/i });
    // Arriving here with a usable list means any mint detour is over —
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
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    await screen.findByText(/need an agent passport first/i);
    // The other direction of the same rule: an empty list is still a guide sending the
    // human to mint one, and a stash cleared here would strand that trip —
    // Settings would never offer to finish a connection actually pending.
    expect(readPendingAuthorize()?.clientName).toBe("Old Client");
  });
});

describe("OAuthConsent — what a refused consent is handed back", () => {
  it("renders stale_consent as a dead end with no approve control", async () => {
    globalThis.location.hash = hashWithError("stale_consent");
    stubConsentUnavailable();
    render(<OAuthConsent />);
    expect(await screen.findByText(/request has expired/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /authorize/i })).toBeNull();
    // The nonce is spent forever, so the recovery is the client, never a
    // reload of this page — the copy says so rather than staying silent.
    expect(
      await screen.findByText(/reloading this page will not help/i),
    ).toBeTruthy();
  });

  it("gives the stale_consent dead end a way back into the app", async () => {
    globalThis.location.hash = hashWithError("stale_consent");
    stubConsentUnavailable();
    render(<OAuthConsent />);
    await userEvent.click(
      await screen.findByRole("button", { name: /back to margince/i }),
    );
    // A rail-less screen with no forward action still needs an exit — this
    // is the app's own "home" route, never the client's callback.
    expect(globalThis.location.hash).toBe("#/home");
  });

  it("renders invalid_request even though the consent-request read fails", async () => {
    globalThis.location.hash = hashWithError("invalid_request");
    // The likeliest cause of invalid_request is a client that went unknown,
    // disabled or deleted — which is exactly what makes this read 404. A card
    // rendered behind that read becomes "couldn't load this view" with a Retry
    // button, replacing the one sentence that tells the human what to do.
    stubConsentUnavailable();
    render(<OAuthConsent />);
    expect(await screen.findByText(/could not be completed/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /authorize/i })).toBeNull();
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
    expect(
      screen.getByRole("button", { name: /back to margince/i }),
    ).toBeTruthy();
  });

  it("re-renders the selector for unlendable_passport, carrying the nonce that came back", async () => {
    globalThis.location.hash = hashWithRetry("unlendable_passport");
    const posted = stubAuthorizePost();
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [{ id: "p2", label: "day agent", scopes: ["read"] }],
    });
    render(<OAuthConsent />);
    expect(await screen.findByText(/no longer be lent/i)).toBeTruthy();
    await userEvent.click(
      await screen.findByRole("button", { name: /authorize/i }),
    );
    // The point of re-rendering the selector: the second choice is SUBMITTABLE.
    // The server left the cookie armed and handed the nonce back for this, so a
    // form posting an empty one would fail the double-submit check and land the
    // human on the stale-consent dead end instead.
    expect(posted.body.get("passport_id")).toBe("p2");
    expect(posted.body.get("consent")).toBe(NONCE);
  });

  it("offers no action at all for unlendable_passport with no nonce to submit", async () => {
    globalThis.location.hash = hashWithError("unlendable_passport");
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [{ id: "p2", label: "day agent", scopes: ["read"] }],
    });
    render(<OAuthConsent />);
    // A marker without a nonce says the pending authorization is gone, whatever
    // the marker claims. Inviting the human to "choose a different passport"
    // here is worse than saying nothing: every submission fails the nonce check,
    // so the screen looks actionable and is not.
    expect(await screen.findByText(/request has expired/i)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /authorize/i })).toBeNull();
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  it("falls back to the guide for unlendable_passport when nothing remains lendable", async () => {
    globalThis.location.hash = hashWithRetry("unlendable_passport");
    stubConsent({
      client_name: "Claude Code",
      offline: false,
      passports: [],
    });
    render(<OAuthConsent />);
    expect(
      await screen.findByText(/need an agent passport first/i),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: /authorize/i })).toBeNull();
  });
});

describe("OAuthConsent — re-entering after sign-in", () => {
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
    // Same exclusion as the mint-detour stash: a fresh nonce is only
    // ever minted server-side, so replaying an absent one is not on offer.
    expect(reentered.has("consent")).toBe(false);
    expect([...reentered.keys()].sort()).toEqual([...AUTHORIZE_KEYS].sort());
    expect(reentered.get("resource")).toBe(RESOURCE);
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
