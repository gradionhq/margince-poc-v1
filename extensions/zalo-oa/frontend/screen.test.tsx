/** @vitest-environment jsdom */

import { LocaleProvider } from "@margince/frontend/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ZaloOaScreen, { redemptionFrom } from "./screen";

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

// The redirect is parsed rather than trusted, and a redirect missing any of the
// three values is refused: sending a request built from the pieces that ARE there
// would spend a ten-minute single-use code on a call that cannot succeed.
describe("reading the address Zalo redirected to", () => {
  it("takes the code, the account and the state off a complete redirect", () => {
    expect(
      redemptionFrom(
        " https://crm.example.com/zalo?code=abc&oa_id=123&state=s1 ",
      ),
    ).toEqual({ code: "abc", oa_id: "123", state: "s1" });
  });

  it.each([
    ["not an address at all", "the administrator pasted a sentence"],
    ["no code", "https://crm.example.com/zalo?oa_id=123&state=s1"],
    ["no account", "https://crm.example.com/zalo?code=abc&state=s1"],
    ["no state", "https://crm.example.com/zalo?code=abc&oa_id=123"],
    ["nothing at all", ""],
  ])("refuses one carrying %s", (_name, pasted) => {
    expect(redemptionFrom(pasted)).toBeNull();
  });
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
  it("says nothing is connected, and offers the first step", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("No Official Account connected")).toBeTruthy();
    expect(screen.getByLabelText("App ID")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Start authorization" })).toBeTruthy();
    // Nothing to disconnect, so no control that would 404 on the way.
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  it("sends the app credentials, and keeps the secret on screen nowhere after", async () => {
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
      "/ext/zalo-oa/authorize": () => ({
        permission_url: "https://oauth.zaloapp.com/v4/oa/permission?app_id=app-1",
        code_challenge: "the-challenge",
        connection: { ...CONNECTION, status: "pending_authorization" },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("App ID"), "app-1");
    await user.type(screen.getByLabelText("App Secret"), "the-secret");
    await user.type(screen.getByLabelText("Redirect address"), "https://crm.example.com/zalo");
    await user.click(screen.getByRole("button", { name: "Start authorization" }));

    await waitFor(() => {
      const started = calls.find((call) => call.path === "/ext/zalo-oa/authorize");
      expect(started?.method).toBe("POST");
      expect(started?.body).toEqual({
        app_id: "app-1",
        app_secret: "the-secret",
        redirect_uri: "https://crm.example.com/zalo",
      });
    });
    // The secret is cleared whatever happened, so a live credential is not left
    // sitting in a form field on an unattended screen.
    await waitFor(() => {
      expect((screen.getByLabelText("App Secret") as HTMLInputElement).value).toBe("");
    });
  });

  // The challenge has to be pasted into the developer console BEFORE the
  // permission URL is opened, because that console stores one challenge per
  // application rather than one per request. A screen that hid it would leave an
  // administrator with an authorization that fails for a reason nothing states.
  it("shows the challenge to save and the URL to open, once an authorization starts", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
      "/ext/zalo-oa/authorize": () => ({
        permission_url: "https://oauth.zaloapp.com/v4/oa/permission?app_id=app-1",
        code_challenge: "the-challenge",
        connection: { ...CONNECTION, status: "pending_authorization" },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("App ID"), "app-1");
    await user.type(screen.getByLabelText("App Secret"), "the-secret");
    await user.type(screen.getByLabelText("Redirect address"), "https://crm.example.com/zalo");
    await user.click(screen.getByRole("button", { name: "Start authorization" }));

    expect(await screen.findByText("the-challenge")).toBeTruthy();
    expect(
      screen.getByText("https://oauth.zaloapp.com/v4/oa/permission?app_id=app-1"),
    ).toBeTruthy();
  });

  it("redeems the whole redirect address the administrator came back with", async () => {
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
      "/ext/zalo-oa/connect": () => CONNECTION,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("The address Zalo sent the administrator to"),
      "https://crm.example.com/zalo?code=the-code&oa_id=4033837145949898046&state=3d5f8a10-7c42-4e19-9b03-1f6a2d8c5e74",
    );
    await user.click(screen.getByRole("button", { name: "Finish connecting" }));

    await waitFor(() => {
      const finished = calls.find((call) => call.path === "/ext/zalo-oa/connect");
      expect(finished?.method).toBe("PUT");
      expect(finished?.body).toEqual({
        code: "the-code",
        oa_id: "4033837145949898046",
        state: "3d5f8a10-7c42-4e19-9b03-1f6a2d8c5e74",
      });
    });
  });

  // The button stays shut until the pasted address actually carries a
  // redemption, because pressing it otherwise spends nothing and teaches an
  // administrator that the screen is broken.
  it("will not redeem an address that carries no code", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("The address Zalo sent the administrator to"),
      "https://crm.example.com/zalo",
    );
    expect(
      (screen.getByRole("button", { name: "Finish connecting" }) as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  // A failed step is ANNOUNCED, not merely rendered. It appears after the press
  // that caused it, so an administrator not looking at this element — a
  // screen-reader user who has just moved off the button — otherwise hears
  // nothing and is left believing the account connected.
  it("announces a failed redemption as an alert", async () => {
    // /connect is deliberately unscripted, so the stub answers 503 — a real
    // refusal shape rather than a thrown fetch, which no server produces.
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-oa/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("The address Zalo sent the administrator to"),
      "https://crm.example.com/zalo?code=c&oa_id=o&state=s",
    );
    await user.click(screen.getByRole("button", { name: "Finish connecting" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("The account was not connected");
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
      "Authorization needed",
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
    expect(screen.queryByRole("button", { name: "Start authorization" })).toBeNull();
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
