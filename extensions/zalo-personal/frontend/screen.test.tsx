/** @vitest-environment jsdom */

import { LocaleProvider } from "@margince/frontend/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ZaloPersonalScreen, { CONNECT_POLL_MS } from "./screen";

// The personal-Zalo screen, over a stubbed transport.
//
// It is compiled by tsconfig.composed-tests.json — so the fixtures below are
// held against the MERGED contract — and run by `make fe-test-ext`, which
// `make check-fe` calls. What it cannot see is what the server actually sends:
// every body here is a fixture, so a screen and a stub that are wrong in the
// same direction agree. The Go conformance tests are the other half of that
// pair.
//
// THE WHOLE FILE RUNS ON FAKE TIMERS, because the QR handshake is a poll: the
// screen asks how the login is going on an interval, and an interval armed on
// the real clock cannot be advanced — a test would have to WAIT, which is the
// one thing a suite sharing a machine may not do. Every step below is `flush()`
// or `poll()`, both of which advance the fake clock and drain the microtask
// chains behind the stubbed fetches, so what a case proves does not depend on
// how busy the runner is.

/** The grants a seat holds on the unit's one object. */
const FULL_GRANT = {
  seat_type: "full",
  objects: {
    ext_zalo_personal_connection: { read: true, update: true, delete: true },
  },
};

/** A seat that may look and not touch: no code to scan, nothing to withdraw. */
const READ_ONLY_GRANT = {
  seat_type: "full",
  objects: { ext_zalo_personal_connection: { read: true } },
};

/** A seat granted nothing on this unit at all. */
const NO_GRANT = { seat_type: "full", objects: {} };

/** A QR as the provider sends it: a data URL the screen renders untouched. */
const QR_IMAGE = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB";

const CONNECTION = {
  id: "11111111-1111-4111-8111-111111111111",
  user_id: "9f1d0c4a-3b2e-4f57-9a10-2c8e6b5d4f31",
  status: "connected",
  zalo_uid: "3684092176",
  display_name: "Tin Nguyen",
  capture_enabled: false,
  connected_at: "2026-08-13T09:14:00Z",
  version: 1,
};

const CONNECTED = {
  connected: true,
  session_deposited: true,
  allowed_count: 0,
  connection: CONNECTION,
};

const NOT_CONNECTED = { connected: false, session_deposited: false };

/**
 * A session as the server would never send it.
 *
 * Every fixture that leaks one uses this exact string, so a case can assert on
 * the rendered markup — attributes included — rather than on the screen's
 * intentions.
 */
const LEAKED_SESSION = "zpw_sek_leaked_by_the_server";

type Handler = (body: unknown) => unknown | Promise<unknown>;

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
    // AWAITED, so a handler may return a promise the case resolves itself:
    // that is the only way to hold a request in flight on a fake clock, and
    // "the save is still going" is a state a member sees and a control has to
    // reflect.
    return json(await handler(raw === "" ? null : JSON.parse(raw)));
  };
  return { calls, fetchStub };
}

function renderScreen() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <ZaloPersonalScreen />
      </LocaleProvider>
    </QueryClientProvider>,
  );
}

/** Drain what is already in flight: due timers plus the promises behind them. */
async function flush() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(1);
  });
}

/**
 * Let the handshake ask once more, and settle whatever the answer starts.
 *
 * The advance is the screen's own {@link CONNECT_POLL_MS}, imported rather than
 * restated: a second copy that fell behind would under-advance the fake clock,
 * the poll would not fire, and every case below would assert on a state that
 * never changed while still reporting green.
 */
async function poll() {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(CONNECT_POLL_MS);
  });
  await flush();
}

/** The one control that opens a login, by the words on it. */
function connectButton() {
  return screen.getByRole("button", { name: "Connect Zalo" });
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("the personal Zalo screen", () => {
  it("names the page in the one level-1 heading a unit screen owns", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => NOT_CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();
    const h1 = screen.getByRole("heading", { level: 1 });
    expect(h1.textContent).toBe("Zalo (your own account)");
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  // The consent panel is the screen, not decoration around the button: a member
  // who scans without reading these four sentences has connected their family
  // and their doctor to a CRM on the strength of a green button.
  it("states what connecting does and does not do before offering a code", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => NOT_CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    expect(screen.getByText("Not connected")).toBeTruthy();
    // Nothing goes into the CRM until the member says so — this screen must not
    // imply that anything has started.
    expect(
      screen.getByText(
        /Nothing from your Zalo goes into the CRM until you say/,
      ),
    ).toBeTruthy();
    // And it names the two shapes they will be offered, because the first
    // version of this screen promised a choice and shipped no way to make one.
    expect(
      screen.getByText(/except the people you leave out, or only the people/),
    ).toBeTruthy();
    // From now on, never "your history": personal Zalo has no history API, and a
    // member expecting last month's conversation files a bug that is real.
    expect(
      screen.getByText(/nothing from before today will appear/),
    ).toBeTruthy();
    // Their own account, withdrawable whenever they like.
    expect(
      screen.getByText(/your own personal account, not a company one/),
    ).toBeTruthy();
    // And what they actually get: a few days at most, stated as a CEILING rather
    // than as the 3-day figure the server reports, because that figure is one
    // observation from one account and "3 days" read as a promise is a bug report.
    expect(screen.getByText(/which is a few days at most/)).toBeTruthy();

    expect(connectButton()).toBeTruthy();
    // No code before they ask for one, and nothing to withdraw.
    expect(screen.queryByAltText(/QR code/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  // THE TWO STANDING DISCLOSURES, asserted in BOTH states on purpose: each one is
  // a fact about the connector rather than about a moment in it, and a case that
  // only checked the connected screen would pass while the not-connected screen
  // quietly lost them — which is precisely how the browser conflict came to be
  // stated only in a panel that disappears the moment somebody connects.
  it("states the browser conflict and the disclaimer whether or not anybody is connected", async () => {
    for (const connected of [false, true]) {
      const { fetchStub } = stubTransport(FULL_GRANT, {
        "/ext/zalo-personal/status": () =>
          connected ? CONNECTED : NOT_CONNECTED,
      });
      vi.stubGlobal("fetch", vi.fn(fetchStub));

      renderScreen();
      await flush();

      // WHAT TO DO, not "stop using Zalo": the phone and Zalo PC do not conflict,
      // and a rep who finds that half false discounts everything else here.
      const browser = screen.getByText(
        /Do not sign in to Zalo in a web browser/,
      );
      expect(browser.textContent).toContain("phone and Zalo PC");
      // Both directions, because it was measured in both: our login evicts the
      // browser and the browser evicts us.
      expect(browser.textContent).toContain("signs this connection out");
      expect(browser.textContent).toContain("signs your browser out");
      // And the cost, which is what makes it worth avoiding rather than a curio.
      expect(browser.textContent).toContain("new QR scan");

      // The disclaimer claims only what we can support. Nothing about bans or
      // terms of service: we have no evidence either way, and a frightening claim
      // we cannot substantiate is worse than the silence.
      const unofficial = screen.getByText(/not built or endorsed by Zalo/);
      expect(unofficial.textContent).toContain("stop working without notice");
      expect(unofficial.textContent).toContain(
        "disconnect it here at any time",
      );
      expect(screen.queryByText(/ban|terms of service|suspend/i)).toBeNull();

      cleanup();
      vi.unstubAllGlobals();
    }
  });

  it("walks a member from the code on screen to a connected account", async () => {
    // The handshake advances one step per call, exactly as the server's own
    // bounded poll does.
    const steps = ["waiting", "scanned", "confirmed"];
    let connected = false;
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () =>
        connected ? CONNECTED : NOT_CONNECTED,
      // Connecting reveals the chooser, which reads the roster: a member who has
      // just scanned lands on an account with nothing chosen yet.
      "/ext/zalo-personal/contacts": () => ({
        roster_available: true,
        contacts: [],
      }),
      "/ext/zalo-personal/connect/start": () => ({
        qr_image: QR_IMAGE,
        expires_at: "2026-08-13T09:20:00Z",
      }),
      "/ext/zalo-personal/connect/status": () => {
        const state = steps.shift() ?? "confirmed";
        if (state === "confirmed") {
          connected = true;
        }
        return state === "waiting"
          ? { state }
          : { state, display_name: "Tin Nguyen" };
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();
    fireEvent.click(connectButton());
    await flush();

    // The provider's own data URL, in an img the member can actually scan.
    const code = screen.getByAltText(/QR code/);
    // Narrowed by a real check rather than an assertion: the claim under test is
    // that this IS an <img>, so a cast would assume the thing being proved.
    if (!(code instanceof HTMLImageElement)) {
      throw new Error(
        `the QR code rendered as <${code.tagName.toLowerCase()}>, not an <img> the member can scan`,
      );
    }
    expect(code.src).toBe(QR_IMAGE);
    expect(screen.getByText(/Waiting for you to scan/)).toBeTruthy();
    expect(
      calls.some(
        (call) =>
          call.path === "/ext/zalo-personal/connect/start" &&
          call.method === "PUT",
      ),
    ).toBe(true);

    // Scanned: the phone now holds the decision, and the member is told WHICH
    // of their accounts is about to be connected.
    await poll();
    expect(screen.getByText("Scanned — confirm on your phone.")).toBeTruthy();
    expect(screen.getByText("Connecting as Tin Nguyen.")).toBeTruthy();

    // Confirmed: the connection exists, so the screen re-reads and flips.
    await poll();
    await flush();
    expect(screen.getByText("Connected")).toBeTruthy();
    expect(screen.getByText("3684092176")).toBeTruthy();
    // Capture has NOT started, and the screen says so rather than leaving a
    // member to assume their conversations are already arriving.
    expect(screen.getByText("Not yet — nothing saved")).toBeTruthy();
    // And the note now POINTS AT the chooser rather than promising one.
    expect(
      screen.getByText(/You choose what goes in, just below/),
    ).toBeTruthy();
    // The code is spent, so it is off the screen rather than sitting there as
    // an instruction.
    expect(screen.queryByAltText(/QR code/)).toBeNull();
  });

  // Declined and expired are kept apart: one means somebody pressed no, the
  // other means nobody pressed anything, and only the first is worth a second
  // thought before scanning again.
  it("says a login was turned down on the phone, and offers a fresh code", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => NOT_CONNECTED,
      "/ext/zalo-personal/connect/start": () => ({
        qr_image: QR_IMAGE,
        expires_at: "2026-08-13T09:20:00Z",
      }),
      "/ext/zalo-personal/connect/status": () => ({ state: "declined" }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();
    fireEvent.click(connectButton());
    await flush();

    expect(screen.getByText(/was turned down on the phone/)).toBeTruthy();
    // A dead code is not left on screen to be scanned.
    expect(screen.queryByAltText(/QR code/)).toBeNull();
    expect(screen.getByRole("button", { name: "Start over" })).toBeTruthy();
  });

  it("says a code expired unscanned, and offers a fresh one", async () => {
    let started = 0;
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => NOT_CONNECTED,
      "/ext/zalo-personal/connect/start": () => {
        started += 1;
        return { qr_image: QR_IMAGE, expires_at: "2026-08-13T09:20:00Z" };
      },
      "/ext/zalo-personal/connect/status": () =>
        started > 1 ? { state: "waiting" } : { state: "expired" },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();
    fireEvent.click(connectButton());
    await flush();
    expect(
      screen.getByText(/This code expired before it was scanned/),
    ).toBeTruthy();

    // Starting over is a DIFFERENT login: the finished one must not keep
    // answering for the new code.
    fireEvent.click(screen.getByRole("button", { name: "Start over" }));
    await flush();
    expect(started).toBe(2);
    expect(screen.getByAltText(/QR code/)).toBeTruthy();
    expect(screen.getByText(/Waiting for you to scan/)).toBeTruthy();
    expect(screen.queryByText(/This code expired/)).toBeNull();
  });

  it("withdraws the account, and re-reads rather than asserting the outcome", async () => {
    let connected = true;
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () =>
        connected ? CONNECTED : NOT_CONNECTED,
      "/ext/zalo-personal/disconnect": () => {
        connected = false;
        return { disconnected: true };
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();
    expect(screen.getByText("Connected")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Disconnect" }));
    await flush();
    await flush();

    const withdrawn = calls.find(
      (call) => call.path === "/ext/zalo-personal/disconnect",
    );
    expect(withdrawn?.method).toBe("DELETE");
    // The screen shows what the RE-READ says, not what the click hoped for.
    expect(screen.getByText("Not connected")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  // A dead session cannot be repaired from this screen, so the only thing worth
  // saying is what the member has to do with their phone.
  it("says a session Zalo stopped accepting needs another scan", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => ({
        connected: false,
        session_deposited: true,
        connection: { ...CONNECTION, status: "needs_reconnect" },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    expect(screen.getByText("Reconnect needed")).toBeTruthy();
    expect(screen.getByText(/Scan a new code with your phone/)).toBeTruthy();
    // Both verbs are offered: another scan, or withdraw the account outright.
    expect(connectButton()).toBeTruthy();
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeTruthy();
  });

  // THE STRANDED CREDENTIAL. The server seals the Zalo session before it writes
  // the connection row, so a failure between the two leaves a fully valid login
  // — cookies, device identity, everything needed to read that member's entire
  // personal chat life — on deposit with nothing pointing at it.
  //
  // Read off `connected` alone the screen would call that "not connected",
  // invite another scan, and offer NO way to withdraw the access this
  // installation is holding. The member would have to ask an administrator to
  // revoke their own privacy. So the read carries `session_deposited`, and the
  // withdraw verb is gated on it as well as on the row.
  it("offers a way out when a session is on deposit with no connection behind it", async () => {
    let deposited = true;
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => ({
        connected: false,
        session_deposited: deposited,
      }),
      "/ext/zalo-personal/disconnect": () => {
        deposited = false;
        return { disconnected: false };
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    // Honest about both halves: no working connection, and an access that is
    // nonetheless real.
    expect(screen.getByText("Not connected")).toBeTruthy();
    expect(screen.getByText(/A sign-in did not finish/)).toBeTruthy();
    expect(screen.getByText(/still held by this installation/)).toBeTruthy();

    const withdraw = screen.getByRole("button", { name: "Disconnect" });
    fireEvent.click(withdraw);
    await flush();
    await flush();

    // Disconnect deletes the sealed session whether or not a row points at it,
    // so `disconnected: false` is a success here rather than a refusal — and
    // the re-read is what the screen believes, not the click.
    expect(
      calls.find((call) => call.path === "/ext/zalo-personal/disconnect")
        ?.method,
    ).toBe("DELETE");
    expect(screen.queryByText(/A sign-in did not finish/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  // The same stranding, but with the withdrawn row still on the table: a
  // `disconnected` connection is not something to withdraw, and a session
  // deposited beside one still is.
  it("offers the same way out when the stranded session sits beside a withdrawn row", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => ({
        connected: false,
        session_deposited: true,
        connection: { ...CONNECTION, status: "disconnected" },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    expect(screen.getByText("Not connected")).toBeTruthy();
    expect(screen.getByText(/A sign-in did not finish/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeTruthy();
  });

  // An explicit null where the field is merely absent today. The handler omits
  // `connection` rather than nulling it, so this shape is a producer detail away
  // — and an undefined-only guard would let it through and then throw on
  // `.status` DURING RENDER, which does not degrade the card, it kills it: the
  // whole surface fails and the member is left with an error where the honest
  // answer was "not connected".
  it("reads a null connection as no connection rather than failing to render", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => ({
        connected: false,
        session_deposited: false,
        connection: null,
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    // The card is alive and says the true thing.
    expect(screen.getByText("Not connected")).toBeTruthy();
    expect(connectButton()).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  // The same null WITH a session on deposit, which is where the crash would
  // cost a member something they cannot get back another way: `stranded` is
  // the negation of `held`, so a throw inside that expression takes the withdraw
  // verb down with the rest of the card and leaves them asking an
  // administrator to revoke access to their own private life.
  it("still offers the way out when the stranded deposit comes with a null connection", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => ({
        connected: false,
        session_deposited: true,
        connection: null,
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    expect(screen.getByText("Not connected")).toBeTruthy();
    expect(screen.getByText(/A sign-in did not finish/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Disconnect" })).toBeTruthy();
  });

  // And the ordinary unconnected state says none of it: a member who never
  // connected has nothing on deposit, and telling them otherwise would be the
  // same false claim in the other direction.
  it("says nothing about a deposit when there is none", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => NOT_CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    expect(screen.getByText("Not connected")).toBeTruthy();
    expect(screen.queryByText(/A sign-in did not finish/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  // A control that leads to a 403 is worse than one that is not there.
  it("offers no controls to a seat that may only read", async () => {
    const { fetchStub } = stubTransport(READ_ONLY_GRANT, {
      "/ext/zalo-personal/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    expect(screen.getByText("Connected")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Connect Zalo" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
  });

  // An ungranted seat is told so, and — the part that matters — no request is
  // fired: a refused read on a twenty-second timer is a failing screen where the
  // honest answer is "you were not granted this".
  it("tells an ungranted seat, and asks the server nothing", async () => {
    const { calls, fetchStub } = stubTransport(NO_GRANT, {
      "/ext/zalo-personal/status": () => CONNECTED,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    expect(screen.getByText(/have not been granted access/)).toBeTruthy();
    expect(calls.filter((call) => call.path.startsWith("/ext/"))).toHaveLength(
      0,
    );
  });

  // No operation returns a Zalo session, and the screen must not display one it
  // was handed anyway. The assertion is on the rendered MARKUP rather than on
  // visible text, so a credential smuggled into an attribute fails here too.
  it("renders no session material, in any state the server can produce", async () => {
    const leak = { session: LEAKED_SESSION, cookie: LEAKED_SESSION };
    let connected = false;
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => ({
        ...(connected ? CONNECTED : NOT_CONNECTED),
        ...leak,
        connection: connected ? { ...CONNECTION, ...leak } : undefined,
      }),
      "/ext/zalo-personal/connect/start": () => ({
        qr_image: QR_IMAGE,
        expires_at: "2026-08-13T09:20:00Z",
        ...leak,
      }),
      "/ext/zalo-personal/connect/status": () => {
        connected = true;
        return { state: "confirmed", display_name: "Tin Nguyen", ...leak };
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();
    expect(document.body.innerHTML).not.toContain(LEAKED_SESSION);

    fireEvent.click(connectButton());
    await flush();
    expect(document.body.innerHTML).not.toContain(LEAKED_SESSION);

    await poll();
    await flush();
    expect(screen.getByText("Connected")).toBeTruthy();
    expect(document.body.innerHTML).not.toContain(LEAKED_SESSION);
    // And nothing on this screen ever asks for one: the member's Zalo login is
    // typed on their phone and nowhere else. The capture card's own radios are
    // inputs too, so the property is stated as what a FIELD could carry — no box
    // to type into, and no input holding session material — rather than as the
    // absence of every input on the page.
    expect(document.querySelector("input[type='password']")).toBeNull();
    expect(document.querySelector("input[type='text']")).toBeNull();
    for (const input of document.querySelectorAll("input")) {
      expect(input.value).not.toContain(LEAKED_SESSION);
    }
  });
});
