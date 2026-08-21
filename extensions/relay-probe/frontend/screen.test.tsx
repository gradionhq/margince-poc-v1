/** @vitest-environment jsdom */

import { LocaleProvider } from "@margince/frontend/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import RelayProbeScreen from "./screen";

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
    ext_relay_probe_connection: {
      read: true,
      update: true,
      delete: true,
    },
  },
};

/** A seat that may look and not touch: the connect form must not render. */
const READ_ONLY_GRANT = {
  seat_type: "full",
  objects: { ext_relay_probe_connection: { read: true } },
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
        <RelayProbeScreen />
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

describe("the Relay connector screen", () => {
  it("names the page in the one level-1 heading a unit screen owns", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const h1 = await screen.findByRole("heading", { level: 1 });
    expect(h1.textContent).toBe("Relay");
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  // Not having connected is the ordinary state of this screen, and it is a
  // state rather than a failure.
  it("says an account is not connected, and offers the form to connect one", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("Not connected")).toBeTruthy();
    expect(screen.getByLabelText("Relay URL")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Connect" })).toBeTruthy();
    // Nothing to disconnect, so no control that would 404 on the way.
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  it("sends the deployment and the token, and keeps neither on screen after", async () => {
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => ({ connected: false }),
      "/ext/relay-probe/connect": () => CONNECTED.connection,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("Relay URL"),
      "https://workspace.example.com",
    );
    await user.type(screen.getByLabelText("Access token"), "pat_secret");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => {
      const connect = calls.find(
        (call) => call.path === "/ext/relay-probe/connect",
      );
      expect(connect?.method).toBe("PUT");
      expect(connect?.body).toEqual({
        base_url: "https://workspace.example.com",
        token: "pat_secret",
      });
    });
    // The token field is cleared whatever happened, so a live credential is not
    // left sitting in a form field on an unattended screen.
    await waitFor(() => {
      expect(
        (screen.getByLabelText("Access token") as HTMLInputElement).value,
      ).toBe("");
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
      "/ext/relay-probe/status": () => ({ connected: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("Relay URL"),
      "https://workspace.example.com",
    );
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
      "/ext/relay-probe/status": () => ({
        connected: true,
        connection: {
          ...CONNECTED.connection,
          token: "pat_leaked_by_the_server",
        },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await screen.findByText("Connected");
    expect(screen.queryByText(/pat_leaked_by_the_server/)).toBeNull();
  });

  it("shows how far the poll has read, and offers to disconnect", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => CONNECTED,
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
      "/ext/relay-probe/status": () => ({
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
    expect(screen.getByText(/Relay rejected the token/)).toBeTruthy();
  });

  // A seat that may look and not touch gets no controls: a control that leads
  // to a 403 is worse than one that is not there.
  it("offers no controls to a seat that may only read", async () => {
    const { fetchStub } = stubTransport(READ_ONLY_GRANT, {
      "/ext/relay-probe/status": () => CONNECTED,
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
      "/ext/relay-probe/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(
      await screen.findByText(/have not been granted access/),
    ).toBeTruthy();
    expect(calls.filter((call) => call.path.startsWith("/ext/"))).toHaveLength(
      0,
    );
  });

  // A deposited credential must LOOK deposited. An empty enabled token box
  // beside a working connection reads as "no token set", which is the one
  // thing it is not, and what it invites is pasting over a credential that is
  // already polling.
  it("shows a stored token as stored, and refuses to be typed into", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await screen.findByText("Connected");
    const token = screen.getByLabelText("Access token") as HTMLInputElement;
    expect(token.disabled).toBe(true);
    expect(token.value).not.toBe("");
    expect(screen.getByText(/A token is stored/)).toBeTruthy();
    // And the stored deployment is the one on screen, not the example: a
    // connected account whose URL renders as a placeholder states that nothing
    // is set.
    expect(
      (screen.getByLabelText("Relay URL") as HTMLInputElement).value,
    ).toBe("https://workspace.example.com");
    // Depositing is the only edit `PUT /connect` can express, so the deposit
    // button is not offered while a credential is in place.
    expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();
  });

  // The mask is not the token, and it is not the token's LENGTH either — a
  // field sized to the secret publishes how long it is.
  it("masks a stored token without echoing it or its length", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => ({
        connected: true,
        connection: {
          ...CONNECTED.connection,
          token: "pat_leaked_by_the_server",
        },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await screen.findByText("Connected");
    const token = screen.getByLabelText("Access token") as HTMLInputElement;
    expect(token.value).toBe("••••••••••••");
    expect(token.value).not.toContain("pat_leaked_by_the_server");
  });

  it("opens an empty token field when the member asks to replace one", async () => {
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => CONNECTED,
      "/ext/relay-probe/connect": () => CONNECTED.connection,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await screen.findByText("Connected");
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Replace token" }));

    const token = screen.getByLabelText("Access token") as HTMLInputElement;
    expect(token.disabled).toBe(false);
    // Empty, never the mask: a member who typed after the mask would send it.
    expect(token.value).toBe("");

    await user.type(token, "pat_rotated");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    // The stored deployment travels with the new token — the contract requires
    // both on every call, so a replacement that dropped the URL would move the
    // account to an empty one.
    await waitFor(() => {
      const connect = calls.find(
        (call) => call.path === "/ext/relay-probe/connect",
      );
      expect(connect?.body).toEqual({
        base_url: "https://workspace.example.com",
        token: "pat_rotated",
      });
    });
  });

  // The deployment can move under an open screen — connect upserts on
  // (workspace, member), so a URL change keeps the SAME row id. A form that
  // re-seeded only on the id would keep showing the URL it opened with, and the
  // next "Replace token" would submit that stale one. The server reads a
  // different base_url as a DEPLOYMENT change and resets high_water_mark to 0,
  // so the silent revert would also wipe the member's read cursor.
  //
  // The re-read is driven by a connect's own invalidation rather than by the
  // 20s poll: what is under test is that a CHANGED base_url re-seeds the form,
  // and waiting on a real interval would buy the same assertion at the price of
  // a wall-clock test.
  it("re-seeds the deployment when it moves under the same connection", async () => {
    let moved = false;
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => ({
        connected: true,
        connection: {
          ...CONNECTED.connection,
          base_url: moved
            ? "https://moved.example.com"
            : CONNECTED.connection.base_url,
        },
      }),
      "/ext/relay-probe/connect": () => CONNECTED.connection,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    const urlField = () =>
      screen.getByLabelText("Relay URL") as HTMLInputElement;

    renderScreen();
    await screen.findByText("Connected");
    expect(urlField().value).toBe("https://workspace.example.com");

    // The same row, a different deployment — as another tab would leave it.
    moved = true;
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Replace token" }));
    await user.type(screen.getByLabelText("Access token"), "pat_first");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    // The form re-seeds off the re-read the connect triggered, and comes back
    // in its stored state — the deployment the server now holds, and a token
    // field that is masked again rather than still open over the old one.
    await waitFor(() => {
      expect(urlField().value).toBe("https://moved.example.com");
    });
    expect(
      (screen.getByLabelText("Access token") as HTMLInputElement).disabled,
    ).toBe(true);

    // And the next replacement travels with the deployment on screen, never
    // the one this screen was opened with.
    await user.click(screen.getByRole("button", { name: "Replace token" }));
    await user.type(screen.getByLabelText("Access token"), "pat_second");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => {
      const sent = calls.filter(
        (call) => call.path === "/ext/relay-probe/connect",
      );
      expect(sent.at(-1)?.body).toEqual({
        base_url: "https://moved.example.com",
        token: "pat_second",
      });
    });
  });

  // A read that has said nothing yet must not draw a deposit form: an empty
  // form claims "not connected" before anything established it, and that claim
  // is the one that gets a working credential overwritten.
  it("offers no deposit form until the status read has answered", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => ({ something_else: true }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await waitFor(() => {
      expect(screen.queryByLabelText("Access token")).toBeNull();
    });
    expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();
  });

  // A body this screen cannot read is an error, not "not connected" — the
  // second would invite a member to paste a token over a working connection.
  it("does not read an unreadable answer as an unconnected account", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/relay-probe/status": () => ({ something_else: true }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await waitFor(() => {
      expect(screen.queryByText("Not connected")).toBeNull();
    });
  });
});
