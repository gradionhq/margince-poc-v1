/** @vitest-environment jsdom */

import { LocaleProvider } from "@margince/frontend/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import DispactConnectorScreen from "./screen";

// The connector's screen, over a stubbed transport.
//
// It is compiled by tsconfig.composed-tests.json — so the fixtures below are
// held against the MERGED contract — and run by `make fe-test-ext`, which
// `make check-fe` calls. What it cannot see is what the server actually sends:
// every body here is a fixture, so a screen and a stub that are wrong in the
// same direction agree. The Go conformance test is the other half of that pair.

/** The grants a seat holds on the unit's one object. */
const FULL_GRANT = {
  seat_type: "full",
  objects: {
    ext_dispact_connector_connection: { read: true, update: true, delete: true },
  },
};

/** A seat that may look and not touch: the connect form must not render. */
const READ_ONLY_GRANT = {
  seat_type: "full",
  objects: { ext_dispact_connector_connection: { read: true } },
};

/** A seat granted nothing on this unit at all. */
const NO_GRANT = { seat_type: "full", objects: {} };

const CONNECTED = {
  connected: true,
  connection: {
    id: "11111111-1111-4111-8111-111111111111",
    user_id: "9f1d0c4a-3b2e-4f57-9a10-2c8e6b5d4f31",
    base_url: "https://workspace.example.com",
    status: "connected",
    account_label: "Tin Nguyen",
    provider_workspace_id: "ws-7",
    high_water_mark: 768682,
    last_polled_at: "2026-08-13T09:14:00Z",
    version: 3,
  },
};

type Handler = (body: unknown) => unknown;

function stubTransport(
  authorization: unknown,
  handlers: Readonly<Record<string, Handler>>,
) {
  const calls: { path: string; method: string; body: unknown }[] = [];
  // The client is built with `fetch: (request) => globalThis.fetch(request)`,
  // so the stub is handed ONE Request and no init — reading a body off an init
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
      // A route nobody scripted answers 503 rather than something plausible,
      // so a screen reaching for one fails here instead of rendering an error
      // card that looks like the server's.
      return json({ code: "unavailable" }, 503);
    }
    return json(handler(raw === "" ? null : JSON.parse(raw)));
  };
  return { calls, fetchStub };
}

function renderScreen() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider>
        <DispactConnectorScreen />
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

describe("the Dispact connector screen", () => {
  it("names the page in the one level-1 heading a unit screen owns", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/dispact-connector/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const h1 = await screen.findByRole("heading", { level: 1 });
    expect(h1.textContent).toBe("Dispact");
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  // Not having connected is the ordinary state of this screen, and it is a
  // state rather than a failure.
  it("says an account is not connected, and offers the form to connect one", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/dispact-connector/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("Not connected")).toBeTruthy();
    expect(screen.getByLabelText("Dispact URL")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect" })).toBeTruthy();
    // Nothing to disconnect, so no control that would 404 on the way.
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  it("sends the deployment and the token, and keeps neither on screen after", async () => {
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/dispact-connector/status": () => ({ connected: false }),
      "/ext/dispact-connector/connect": () => CONNECTED.connection,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Dispact URL"), "https://workspace.example.com");
    await user.type(screen.getByLabelText("Access token"), "pat_secret");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => {
      const connect = calls.find((call) => call.path === "/ext/dispact-connector/connect");
      expect(connect?.method).toBe("PUT");
      expect(connect?.body).toEqual({
        base_url: "https://workspace.example.com",
        token: "pat_secret",
      });
    });
    // The token field is cleared whatever happened, so a live credential is not
    // left sitting in a form field on an unattended screen.
    await waitFor(() => {
      expect((screen.getByLabelText("Access token") as HTMLInputElement).value).toBe("");
    });
  });

  // A failed connect is ANNOUNCED, not merely rendered. It appears after the
  // press that caused it, so a member not looking at this element — a
  // screen-reader user, who has just moved off the button — otherwise hears
  // nothing and is left believing the account connected. The read failures
  // QueryStates renders already carry role="alert"; these are the same
  // obligation on the way back from a write.
  it("announces a failed connect as an alert", async () => {
    // /connect is deliberately unscripted, so the stub answers 503 — a real
    // refusal shape rather than a thrown fetch, which no server produces.
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/dispact-connector/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Dispact URL"), "https://workspace.example.com");
    await user.type(screen.getByLabelText("Access token"), "pat_secret");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toBe(
      "The account may not have been connected. Check the state above before trying again.",
    );
  });

  // No operation returns the token, and the screen must not display one it was
  // handed anyway: a body carrying a credential is a body this screen ignores.
  it("never renders a token, whatever the server sends back", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/dispact-connector/status": () => ({
        connected: true,
        connection: { ...CONNECTED.connection, token: "pat_leaked_by_the_server" },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await screen.findByText("Connected");
    expect(screen.queryByText(/pat_leaked_by_the_server/)).toBeNull();
  });

  it("shows how far the poll has read, and offers to disconnect", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/dispact-connector/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("Connected")).toBeTruthy();
    expect(screen.getByText("768682")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeTruthy();
  });

  // A parked connection is the one state a member has to act on, so it says so
  // in this unit's own words rather than in the provider's.
  it("says what to do about a rejected token", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/dispact-connector/status": () => ({
        connected: true,
        connection: {
          ...CONNECTED.connection,
          status: "reauth_required",
          last_error_class: "token_rejected",
        },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("Reconnect needed")).toBeTruthy();
    expect(screen.getByText(/Dispact rejected the token/)).toBeTruthy();
  });

  // A seat that may look and not touch gets no controls: a control that leads
  // to a 403 is worse than one that is not there.
  it("offers no controls to a seat that may only read", async () => {
    const { fetchStub } = stubTransport(READ_ONLY_GRANT, {
      "/ext/dispact-connector/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("Connected")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  // An ungranted seat is told so, and — the part that matters — no request is
  // fired: a refused read on a twenty-second timer is a failing screen where
  // the honest answer is "you were not granted this".
  it("tells an ungranted seat, and asks the server nothing", async () => {
    const { calls, fetchStub } = stubTransport(NO_GRANT, {
      "/ext/dispact-connector/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText(/have not been granted access/)).toBeTruthy();
    expect(calls.filter((call) => call.path.startsWith("/ext/"))).toHaveLength(0);
  });

  // A body this screen cannot read is an error, not "not connected" — the
  // second would invite a member to paste a token over a working connection.
  it("does not read an unreadable answer as an unconnected account", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/dispact-connector/status": () => ({ something_else: true }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await waitFor(() => {
      expect(screen.queryByText("Not connected")).toBeNull();
    });
  });
});
