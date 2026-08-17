/** @vitest-environment jsdom */

import { LocaleProvider } from "@margince/frontend/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ZaloOaScreen from "./screen";

// The connector's screen, over a stubbed transport.
//
// It is compiled by tsconfig.composed-tests.json — so the fixtures below are held
// against the MERGED contract — and run by `make fe-test-ext`, which
// `make check-fe` calls. What it cannot see is what the server actually sends:
// every body here is a fixture, so a screen and a stub that are wrong in the same
// direction agree. The Go suite is the other half of that pair.

/** The grants a seat holds on the unit's one object. */
const FULL_GRANT = {
  seat_type: "full",
  objects: { ext_zalo_oa_connection: { read: true, update: true, delete: true } },
};

/** A seat that may look and not touch: the setup form must not render. */
const READ_ONLY_GRANT = {
  seat_type: "full",
  objects: { ext_zalo_oa_connection: { read: true } },
};

/** A seat granted nothing on this unit at all. */
const NO_GRANT = { seat_type: "full", objects: {} };

const CONNECTION = {
  id: "3d5f8a10-7c42-4e19-9b03-1f6a2d8c5e74",
  oa_id: "4033837145949898046",
  app_id: "app-1",
  redirect_uri: "https://crm.example.com/zalo",
  authorized_by: "9f1d0c4a-3b2e-4f57-9a10-2c8e6b5d4f31",
  status: "connected",
  account_label: "NFQ",
  package_name: "Tăng trưởng",
  package_valid_through: "12/08/2027",
  access_token_expires_at: "2026-08-18T09:00:00Z",
  high_water_mark: 1786689951020,
  backfill_offset: 0,
  version: 3,
  poll_request_budget: 40,
};

const CONNECTED = { connected: true, connection: CONNECTION };

type Handler = (body: unknown) => unknown;

function stubTransport(authorization: unknown, handlers: Readonly<Record<string, Handler>>) {
  const calls: { path: string; method: string; body: unknown }[] = [];
  // The client is built with `fetch: (request) => globalThis.fetch(request)`, so
  // the stub is handed ONE Request and no init — reading a body off an init
  // argument records null for every call and makes "what did the screen send"
  // vacuous.
  const fetchStub = async (input: Request | string | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    const json = (value: unknown, status = 200) =>
      new Response(JSON.stringify(value), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    if (url.endsWith("/v1/me")) {
      return json({ user: {}, roles: [], teams: [], authorization });
    }
    const parsed = new URL(url, "http://stub.invalid");
    const path = parsed.pathname.slice("/v1".length);
    const method = input instanceof Request ? input.method : "GET";
    const raw = input instanceof Request ? await input.text() : "";
    calls.push({ path, method, body: raw === "" ? null : JSON.parse(raw) });
    const handler = handlers[path];
    if (!handler) {
      // A route nobody scripted answers 503 rather than something plausible, so
      // a screen reaching for one fails here instead of rendering an error card
      // that looks like the server's.
      return json({ code: "unavailable" }, 503);
    }
    return json(handler(raw === "" ? null : JSON.parse(raw)));
  };
  return { calls, fetchStub };
}

function renderScreen() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider>
        <ZaloOaScreen />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  Object.defineProperty(globalThis.navigator, "languages", {
    value: ["en-GB"],
    configurable: true,
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the Zalo Official Account screen", () => {
  it("names the page in the one level-1 heading a unit screen owns", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const h1 = await screen.findByRole("heading", { level: 1 });
    expect(h1.textContent).toBe("Zalo Official Account");
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  // Not having connected is the ordinary state of this screen, and it is a state
  // rather than a failure.
  it("says nothing is connected, and offers the form to connect one", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("No Official Account connected")).toBeTruthy();
    for (const label of ["App ID", "App Secret", "Access token", "Refresh token"]) {
      expect(screen.getByLabelText(label)).toBeTruthy();
    }
    // Nothing to disconnect, so no control that would 404 on the way.
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  it("sends all four values, and leaves no credential in a form field after", async () => {
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
      "/ext/zalo-oa/connect": () => CONNECTION,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("App ID"), "app-1");
    await user.type(screen.getByLabelText("App Secret"), "the-secret");
    await user.type(screen.getByLabelText("Access token"), "at-1");
    await user.type(screen.getByLabelText("Refresh token"), "rt-1");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => {
      const sent = calls.find((call) => call.path === "/ext/zalo-oa/connect");
      expect(sent?.method).toBe("PUT");
      expect(sent?.body).toEqual({
        app_id: "app-1",
        app_secret: "the-secret",
        access_token: "at-1",
        refresh_token: "rt-1",
      });
    });
    // Every credential is cleared whatever happened — the refresh token above
    // all, since a successful connect has just spent it.
    await waitFor(() => {
      for (const label of ["App Secret", "Access token", "Refresh token"]) {
        expect((screen.getByLabelText(label) as HTMLInputElement).value).toBe("");
      }
    });
  });

  it("keeps the button shut until every value is present", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("App ID"), "app-1");
    await user.type(screen.getByLabelText("App Secret"), "the-secret");
    expect(
      (screen.getByRole("button", { name: "Connect" }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  // THE SERVER'S OWN SENTENCE, not a static string. This is the whole difference
  // between a screen that helps and one that does not: an account on the free
  // package and an app missing a permission group both fail to connect, and one
  // costs 2.500.000 đ a year while the other costs a click. A UAT found the
  // screen throwing both away.
  it("announces the server's own refusal, not a generic line", async () => {
    const detail =
      "this Official Account's package does not include the conversation API. Margince needs the paid tier — upgrade the account at oa.zalo.me";
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
      "/ext/zalo-oa/connect": () => {
        throw new Error("unused");
      },
    });
    // A 422 carrying an RFC-7807 detail, which is what the unit actually answers.
    const refusing = async (input: Request | string | URL) => {
      const url = String(input instanceof Request ? input.url : input);
      if (url.includes("/ext/zalo-oa/connect")) {
        return new Response(JSON.stringify({ title: "Unprocessable", status: 422, detail }), {
          status: 422,
          headers: { "Content-Type": "application/json" },
        });
      }
      return fetchStub(input);
    };
    vi.stubGlobal("fetch", vi.fn(refusing));

    renderScreen();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("App ID"), "app-1");
    await user.type(screen.getByLabelText("App Secret"), "s");
    await user.type(screen.getByLabelText("Access token"), "at");
    await user.type(screen.getByLabelText("Refresh token"), "rt");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("oa.zalo.me");
  });

  // No operation returns a credential, and the screen must not display one it was
  // handed anyway: a body carrying a token is a body this screen ignores.
  it("never renders a credential, whatever the server sends back", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({
        connected: true,
        connection: { ...CONNECTION, access_token: "leaked_by_the_server" },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await screen.findByText("Connected");
    expect(screen.queryByText(/leaked_by_the_server/)).toBeNull();
  });

  // A connection that exists is DESCRIBED, not re-offered. Four empty credential
  // boxes under a live connection say "not set up", and the credentials they
  // collect are single-use — an accidental submit costs a real token.
  it("does not re-offer the deposit form under a live connection", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await screen.findByText("Connected");
    expect(screen.queryByLabelText("Refresh token")).toBeNull();
    expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();
    expect(screen.getByRole("button", { name: "Replace the credentials" })).toBeTruthy();
  });

  it("opens the form again when somebody asks to replace what is stored", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await screen.findByText("Connected");
    await userEvent.setup().click(screen.getByRole("button", { name: "Replace the credentials" }));
    expect(screen.getByLabelText("Refresh token")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Replace" })).toBeTruthy();
  });

  it("shows which account is connected and what package it is on", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("Connected")).toBeTruthy();
    expect(screen.getByText(/Tăng trưởng/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeTruthy();
  });

  // The request ceiling is the one number on this card that governs how much of a
  // busy account each check keeps up with, and Zalo publishes no per-account rate
  // limit anywhere — so it is shown FIRST, and shown whatever it is set to.
  it("shows the request budget each check spends, ahead of the rest", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({
        connected: true,
        connection: { ...CONNECTION, poll_request_budget: 90 },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const label = await screen.findByText("Requests per check");
    // Against the ceiling, not bare: the number alone answers "how many" and not
    // "how much headroom", which is the question somebody raising it has.
    expect(label.nextElementSibling?.textContent).toContain("90 / 200");
    // And FIRST, ahead of the package.
    const list = label.closest("dl");
    const terms = [...(list?.querySelectorAll("dt") ?? [])].map((dt) => dt.textContent);
    expect(terms[0]).toBe("Requests per check");
    expect(terms).toContain("Package");
  });

  // The limit that really binds is Zalo's own per-account one, which appears in
  // no response header — so the card says so where the ceiling is read, rather
  // than letting an operator raise it believing the range is the whole story.
  it("says the provider publishes no per-account limit", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText(/does not publish a per-account limit/)).toBeTruthy();
  });

  // The two parked states send an administrator to different places, and one of
  // them costs money — so the screen has to say which.
  it.each([
    [
      "tier_lapsed",
      "package_too_low",
      "Account package too low",
      /package no longer includes the conversation API/,
    ],
    [
      "reauth_required",
      "refresh_rotation_lost",
      "Reconnection needed",
      /replacement could not be stored/,
    ],
  ])("says what to do about %s", async (status, errorClass, badge, explanation) => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({
        connected: false,
        connection: { ...CONNECTION, status, last_error_class: errorClass },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText(badge)).toBeTruthy();
    expect(screen.getByText(explanation)).toBeTruthy();
  });

  // A seat that may look and not touch gets no controls: a control that leads to
  // a 403 is worse than one that is not there.
  it("offers no controls to a seat that may only read", async () => {
    const { fetchStub } = stubTransport(READ_ONLY_GRANT, {
      "/ext/zalo-oa/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("Connected")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  // An ungranted seat is told so, and — the part that matters — no request is
  // fired: a refused read on a twenty-second timer is a failing screen where the
  // honest answer is "you were not granted this".
  it("tells an ungranted seat, and asks the server nothing", async () => {
    const { calls, fetchStub } = stubTransport(NO_GRANT, {
      "/ext/zalo-oa/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText(/have not been granted access/)).toBeTruthy();
    expect(calls.filter((call) => call.path.startsWith("/ext/"))).toHaveLength(0);
  });

  // A body this screen cannot read is an error, not "nothing is connected" — the
  // second would invite an administrator to start a second authorization over one
  // that is already working.
  it("does not read an unreadable answer as an unconnected installation", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ something_else: true }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await waitFor(() => {
      expect(screen.queryByText("No Official Account connected")).toBeNull();
    });
  });
});
