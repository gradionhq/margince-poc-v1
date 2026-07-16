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
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { PreferenceCenterScreen } from "./preferences";

// The public, anonymous preference center (G-6): no login, no session — the
// token in the URL is the whole capability. Unlike every other screen test
// in this suite, this file must NOT seed `margince.workspaceSlug` — the
// first test below proves this surface needs no workspace context at all,
// and seeding it would mask a regression where the client starts sending
// one anyway.

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

const CENTER = {
  purposes: [
    {
      key: "transactional",
      label: "Deal & service messages",
      state: "granted",
      locked: true,
    },
    {
      key: "marketing_email",
      label: "Product updates",
      state: "granted",
      locked: false,
    },
    { key: "events", label: "Events", state: "unknown", locked: false },
  ],
};

// Records every request so a test can assert what actually went to the
// server — this surface's whole authority is the token in the URL, so what
// went out on the wire IS the contract.
type Sent = { key: string; url: string; body: unknown };

// consent.test.tsx's stubRoutes, with this surface's defaults: an anonymous
// GET/PUT against tok-123 rather than a session-authed person id.
function stubCenter(
  // A response factory may itself be a pending promise — the
  // save-in-flight test below needs to control exactly when the PUT
  // resolves, to observe the "pending" state before it settles.
  overrides: Record<string, () => Response | Promise<Response>> = {},
  sent: Sent[] = [],
) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const request = input instanceof Request ? input : null;
      const url = new URL(
        request ? request.url : String(input),
        "https://test.local",
      );
      const method = request?.method ?? init?.method ?? "GET";
      const key = `${method} ${url.pathname.replace(/^\/v1/, "")}`;
      let body: unknown = null;
      if (method !== "GET") {
        try {
          body = request
            ? await request.json()
            : JSON.parse(String(init?.body));
        } catch {
          body = null;
        }
      }
      sent.push({ key, url: url.pathname + url.search, body });
      const override = overrides[key];
      if (override) return override();
      if (key === "GET /public/preferences/tok-123") {
        return jsonResponse(CENTER);
      }
      if (key === "PUT /public/preferences/tok-123") {
        return jsonResponse(CENTER);
      }
      return jsonResponse({});
    }),
  );
  return sent;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("PreferenceCenterScreen", () => {
  it("renders anonymously: no workspace header, no session probe", async () => {
    // Typed on the request param (matching real fetch's call shape) so
    // mock.calls destructures cleanly under tsc -b's stricter node config —
    // an untyped zero-arg mock would type mock.calls as empty tuples.
    const fetchSpy = vi.fn(async (_input: Request | string | URL) =>
      jsonResponse(CENTER),
    );
    vi.stubGlobal("fetch", fetchSpy);
    render(<PreferenceCenterScreen token="tok-123" />);
    await screen.findByText("Product updates");
    const requests = fetchSpy.mock.calls.map(([input]) =>
      input instanceof Request ? input : new Request(String(input)),
    );
    expect(requests.every((r) => !r.headers.has("X-Workspace-Slug"))).toBe(
      true,
    );
    expect(
      requests.every((r) => !new URL(r.url).pathname.endsWith("/me")),
    ).toBe(true);
  });

  it("locks transactional and explains why instead of silently ignoring the click", async () => {
    stubCenter();
    render(<PreferenceCenterScreen token="tok-123" />);
    const toggle = await screen.findByRole("switch", {
      name: /deal & service/i,
    });
    expect(toggle).toBeDisabled();
    expect(screen.getByText(/always on/i)).toBeInTheDocument();
  });

  it("stages changes — nothing is written until Save", async () => {
    const put = vi.fn(() => jsonResponse(CENTER));
    stubCenter({ "PUT /public/preferences/tok-123": put });
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("switch", { name: /product updates/i }),
    );
    expect(screen.getByText(/not saved yet/i)).toBeInTheDocument();
    expect(put).not.toHaveBeenCalled();
  });

  it("discards back to the saved state", async () => {
    stubCenter();
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("switch", { name: /product updates/i }),
    );
    await userEvent.click(screen.getByRole("button", { name: /discard/i }));
    expect(screen.queryByText(/not saved yet/i)).not.toBeInTheDocument();
  });

  // The invariant: the wording shown IS the wording stored. Read the string
  // off the DOM, not from a fixture — that's what makes this a passthrough
  // test rather than a restatement of the same constant twice.
  it("submits the exact wording rendered at the toggle", async () => {
    const sent = stubCenter();
    render(<PreferenceCenterScreen token="tok-123" />);
    const shown = (await screen.findByTestId("wording-marketing_email"))
      .textContent;
    await userEvent.click(
      screen.getByRole("switch", { name: /product updates/i }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /save preferences/i }),
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "PUT /public/preferences/tok-123"),
      ).toHaveLength(1),
    );
    expect(sent.at(-1)?.body).toEqual({
      choices: [
        { purpose_key: "marketing_email", state: "withdrawn", wording: shown },
      ],
    });
  });

  it("never submits a purpose the subject did not touch", async () => {
    const sent = stubCenter();
    render(<PreferenceCenterScreen token="tok-123" />);
    const shown = (await screen.findByTestId("wording-events")).textContent;
    await userEvent.click(
      await screen.findByRole("switch", { name: /^events$/i }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /save preferences/i }),
    );
    await waitFor(() =>
      expect(
        sent.filter((s) => s.key === "PUT /public/preferences/tok-123"),
      ).toHaveLength(1),
    );
    // marketing_email and transactional were never touched: exactly one
    // choice, and its content — not just its count — is the untouched
    // "events" purpose going from unknown (off) to granted.
    expect(sent.at(-1)?.body).toEqual({
      choices: [{ purpose_key: "events", state: "granted", wording: shown }],
    });
  });

  // PUT loops choices in separate transactions: a mid-list 422 leaves the
  // earlier ones committed. Re-read, never trust the optimistic draft.
  it("re-reads after a partial save rather than showing the draft as applied", async () => {
    let call = 0;
    stubCenter({
      "PUT /public/preferences/tok-123": () => {
        call += 1;
        return jsonResponse(
          {
            title: "not a tracked consent purpose",
            status: 422,
            code: "invalid",
          },
          422,
        );
      },
    });
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("switch", { name: /product updates/i }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /save preferences/i }),
    );
    expect(
      await screen.findByText(/some of your choices may have been saved/i),
    ).toBeInTheDocument();
    expect(call).toBe(1);
  });

  it("treats an unknown token as a 404 that reveals nothing", async () => {
    stubCenter({
      "GET /public/preferences/bad": () =>
        jsonResponse({ title: "not found", status: 404 }, 404),
    });
    render(<PreferenceCenterScreen token="bad" />);
    expect(
      await screen.findByText(/link is no longer valid/i),
    ).toBeInTheDocument();
  });

  it("explains a rate-limited edge instead of showing a raw 429", async () => {
    stubCenter({
      "GET /public/preferences/tok-123": () =>
        jsonResponse({ title: "too many requests", status: 429 }, 429),
    });
    render(<PreferenceCenterScreen token="tok-123" />);
    expect(await screen.findByText(/too many attempts/i)).toBeInTheDocument();
  });

  it("early-returns honestly with no token", () => {
    render(<PreferenceCenterScreen />);
    expect(screen.getByText(/link is no longer valid/i)).toBeInTheDocument();
  });

  // The PUT is non-atomic (handlers_public.go loops choices in separate
  // transactions), so a second write firing while the first is still in
  // flight could interleave with it, or land against a draft the response is
  // about to reseed out from under. Freezing every control is what rules
  // that out — pin it against a PUT this test controls the resolution of,
  // not a same-tick fake that would make "while pending" unobservable.
  it("freezes every control while a save is in flight", async () => {
    let resolvePut: (value: Response) => void = () => {};
    const putPromise = new Promise<Response>((resolve) => {
      resolvePut = resolve;
    });
    stubCenter({ "PUT /public/preferences/tok-123": () => putPromise });
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("switch", { name: /product updates/i }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: /save preferences/i }),
    );

    expect(
      screen.getByRole("switch", { name: /product updates/i }),
    ).toBeDisabled();
    expect(screen.getByRole("switch", { name: /^events$/i })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: /save preferences/i }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: /discard/i })).toBeDisabled();

    resolvePut(jsonResponse(CENTER));
    await waitFor(() =>
      expect(
        screen.getByRole("switch", { name: /product updates/i }),
      ).toBeEnabled(),
    );
  });
});

describe("one-click unsubscribe (G-7)", () => {
  it("stops every non-locked purpose when no purpose is named", async () => {
    const post = vi.fn(() =>
      jsonResponse({ unsubscribed: ["marketing_email", "events"] }),
    );
    const sent = stubCenter({
      "POST /public/preferences/tok-123/unsubscribe": post,
    });
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /unsubscribe from all/i }),
    );
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(/you're off/i)).toBeInTheDocument();
    // "no purpose is named" is a claim about the request itself — pin the
    // URL, not just that some POST happened: no ?purpose= rode along.
    expect(
      sent.find((s) => s.key === "POST /public/preferences/tok-123/unsubscribe")
        ?.url,
    ).toBe("/v1/public/preferences/tok-123/unsubscribe");
  });

  // Replay is idempotent and shrinks to []. Never render "you unsubscribed
  // from 0 purposes" — and never claim a count off a retry.
  it("stays honest when a replayed unsubscribe returns nothing", async () => {
    stubCenter({
      "POST /public/preferences/tok-123/unsubscribe": () =>
        jsonResponse({ unsubscribed: [] }),
    });
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /unsubscribe from all/i }),
    );
    expect(await screen.findByText(/already off/i)).toBeInTheDocument();
  });

  // I2: the subject exercised their objection right and the public edge
  // refused it (a 429 is reachable here — this endpoint rate-limits per
  // token) — a re-enabled button with no explanation would be
  // indistinguishable from never having clicked at all.
  it("renders the failure honestly instead of going silent on a rate-limited unsubscribe", async () => {
    stubCenter({
      "POST /public/preferences/tok-123/unsubscribe": () =>
        jsonResponse({ title: "too many requests", status: 429 }, 429),
    });
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /unsubscribe from all/i }),
    );
    expect(await screen.findByText(/too many attempts/i)).toBeInTheDocument();
    expect(
      screen.queryByText(/you're off|already off/i),
    ).not.toBeInTheDocument();
  });

  it("renders the server's own explanation on a non-rate-limit unsubscribe failure", async () => {
    stubCenter({
      "POST /public/preferences/tok-123/unsubscribe": () =>
        jsonResponse(
          { title: "storage unavailable", detail: "try again shortly" },
          500,
        ),
    });
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /unsubscribe from all/i }),
    );
    expect(await screen.findByText(/try again shortly/i)).toBeInTheDocument();
  });

  // Re-subscribing must be an explicit opt-in — never a silent re-grant.
  it("stages an undo rather than immediately re-granting", async () => {
    const put = vi.fn(() => jsonResponse(CENTER));
    stubCenter({
      "POST /public/preferences/tok-123/unsubscribe": () =>
        jsonResponse({ unsubscribed: ["marketing_email"] }),
      "PUT /public/preferences/tok-123": put,
    });
    render(<PreferenceCenterScreen token="tok-123" />);
    await userEvent.click(
      await screen.findByRole("button", { name: /unsubscribe from all/i }),
    );
    await userEvent.click(await screen.findByRole("button", { name: /undo/i }));
    expect(put).not.toHaveBeenCalled();
    expect(screen.getByText(/not saved yet/i)).toBeInTheDocument();
    expect(screen.getByText(/explicit opt-in/i)).toBeInTheDocument();
  });
});
