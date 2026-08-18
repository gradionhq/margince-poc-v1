/** @vitest-environment jsdom */

import { LocaleProvider } from "@margince/frontend/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
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

/**
 * The roster as the server hands it over: what Zalo reported, MERGED with the
 * verdicts this member already holds. Both start undecided, which is the state
 * default deny actually protects.
 */
const CONTACT_IDS = { mai: "8801", tuan: "8802" } as const;

const CONTACT_NAMES: Readonly<Record<string, string>> = {
  [CONTACT_IDS.mai]: "Chi Mai",
  [CONTACT_IDS.tuan]: "Anh Tuan",
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

/**
 * Render, and let BOTH reads a connected screen makes land: the chooser mounts
 * only once the status read has answered, so its roster read starts a beat
 * later, and one drain leaves every assertion below about a skeleton.
 */
async function openChooser() {
  renderScreen();
  await flush();
  await flush();
}

/** One contact's verdict control, by the name the member reads beside it. */
function verdictFor(contact: string) {
  return screen.getByRole("combobox", { name: contact });
}

/**
 * Change one contact's verdict, the way a member does. The core's own
 * `pickOption` is not on the published extension surface, so the two steps live
 * here — on the ROLES, so this holds while the control keeps its semantics.
 */
function chooseVerdict(contact: string, verdict: string) {
  fireEvent.click(verdictFor(contact));
  const listbox = screen.getByRole("listbox");
  fireEvent.click(within(listbox).getByRole("option", { name: verdict }));
}

/** The save, by the words on it. */
function saveButton() {
  return screen.getByRole("button", { name: "Save choices" });
}

/**
 * What the card reports as armed, read from the fact's own row: a bare
 * `getByText("1")` would pass on any stray digit anywhere on the screen.
 */
function armedRow(): string {
  const row = screen
    .getByText("Contacts being captured")
    .closest(".factlist-row");
  if (!row) {
    throw new Error("the armed fact rendered outside a fact row");
  }
  return row.textContent ?? "";
}

/**
 * The verdicts the screen sent, validated rather than assumed: a save that
 * posted the wrong shape would otherwise read as an empty list, and every
 * assertion about it would pass vacuously.
 */
function verdictsIn(
  body: unknown,
): { channel_user_id: string; mode: string; display_name?: string }[] {
  if (
    typeof body !== "object" ||
    body === null ||
    !("entries" in body) ||
    !Array.isArray(body.entries)
  ) {
    throw new Error("the save carried no `entries` array");
  }
  return body.entries;
}

/**
 * A server that remembers what it was told: the screen shows what the RE-READ
 * says, so a fixed body would let a broken save look like a working one.
 */
function rosterServer(modes: Map<string, string>) {
  const armed = () => [...modes.values()].filter((m) => m === "allow").length;
  return {
    "/ext/zalo-personal/status": () => ({
      connected: true,
      session_deposited: true,
      allowed_count: armed(),
      connection: { ...CONNECTION, capture_enabled: armed() > 0 },
    }),
    "/ext/zalo-personal/contacts": () => ({
      roster_available: true,
      contacts: [...modes].map(([id, mode]) => ({
        channel_user_id: id,
        display_name: CONTACT_NAMES[id],
        mode,
      })),
    }),
    "/ext/zalo-personal/allowlist": (body: unknown) => {
      const armedBefore = armed();
      const entries = verdictsIn(body);
      for (const entry of entries) {
        modes.set(entry.channel_user_id, entry.mode);
      }
      return { saved: entries.length, capture_armed: armedBefore === 0 };
    },
  };
}

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
    // Nothing is captured until the member chooses — this screen must not imply
    // that capture has already started.
    expect(
      screen.getByText(/No conversation is captured until you pick one/),
    ).toBeTruthy();
    // And it says where the picking happens, because the first version of this
    // screen promised a chooser and shipped none.
    expect(
      screen.getByText(/a list of your Zalo contacts and choose which of them/),
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
    // Zalo WEB specifically — their phone coexists, and saying otherwise would
    // tell a rep to stop using the app they work from.
    expect(screen.getByText(/rather than Zalo Web/)).toBeTruthy();

    expect(connectButton()).toBeTruthy();
    // No code before they ask for one, and nothing to withdraw.
    expect(screen.queryByAltText(/QR code/)).toBeNull();
    expect(screen.queryByRole("button", { name: "Disconnect" })).toBeNull();
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
    const code = screen.getByAltText(/QR code/) as HTMLImageElement;
    expect(code.getAttribute("src")).toBe(QR_IMAGE);
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
    expect(screen.getByText("Not started — nothing chosen yet")).toBeTruthy();
    // And the note now POINTS AT the chooser rather than promising one.
    expect(
      screen.getByText(/You choose which contacts are captured, in the list/),
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

  // THE DEFECT THIS CARD EXISTS FOR: a real account was connected, the screen
  // promised "nothing is captured until you choose", and there was no chooser.
  it("lets a member allow one contact, save, and see what that armed", async () => {
    const modes = new Map([
      [CONTACT_IDS.mai, "none"],
      [CONTACT_IDS.tuan, "none"],
    ]);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, rosterServer(modes));
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    // Default deny, stated and then shown: both contacts rest at "not
    // captured", and the count agrees with them.
    expect(screen.getByText(/Nothing is captured by default/)).toBeTruthy();
    expect(verdictFor("Chi Mai").textContent).toContain("Not chosen");
    expect(verdictFor("Anh Tuan").textContent).toContain("Not chosen");
    expect(armedRow()).toContain("0");
    expect(screen.getByText(/Nothing is being captured yet/)).toBeTruthy();
    // Nothing has been sent, and the save has nothing to send.
    expect(saveButton().hasAttribute("disabled")).toBe(true);
    expect(
      calls.some((call) => call.path === "/ext/zalo-personal/allowlist"),
    ).toBe(false);

    chooseVerdict("Chi Mai", "Capture");
    await flush();
    // Chosen and NOT yet in force: a member who walks away here has armed
    // nothing, and the screen says so.
    expect(verdictFor("Chi Mai").textContent).toContain("Capture");
    expect(screen.getByText(/Not saved yet/)).toBeTruthy();
    expect(armedRow()).toContain("0");

    fireEvent.click(saveButton());
    await flush();
    await flush();

    // Exactly one verdict, for the contact they touched: posting the untouched
    // one too would be writing a decision nobody made.
    const saved = calls.find(
      (call) => call.path === "/ext/zalo-personal/allowlist",
    );
    expect(saved?.method).toBe("PUT");
    expect(verdictsIn(saved?.body)).toEqual([
      {
        channel_user_id: CONTACT_IDS.mai,
        mode: "allow",
        // The name the screen was showing, stored so the list still reads as
        // people the next time Zalo cannot be reached.
        display_name: "Chi Mai",
      },
    ]);

    // And the screen reflects the RE-READ: one contact armed, capture on, and
    // no unsaved claim left standing.
    expect(armedRow()).toContain("1");
    expect(screen.getByText("On, for the contacts you allowed")).toBeTruthy();
    expect(screen.queryByText(/Not saved yet/)).toBeNull();
    expect(saveButton().hasAttribute("disabled")).toBe(true);
  });

  // Blocking is a refinement on top of default deny, not the thing doing the
  // work — but a member who shuts somebody out has to see that it stuck.
  it("records a blocked contact, and does not count it as armed", async () => {
    const modes = new Map([[CONTACT_IDS.mai, "none"]]);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, rosterServer(modes));
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    chooseVerdict("Chi Mai", "Never capture");
    await flush();
    fireEvent.click(saveButton());
    await flush();
    await flush();

    expect(
      verdictsIn(
        calls.find((call) => call.path === "/ext/zalo-personal/allowlist")
          ?.body,
      ),
    ).toEqual([
      {
        channel_user_id: CONTACT_IDS.mai,
        mode: "block",
        display_name: "Chi Mai",
      },
    ]);
    expect(verdictFor("Chi Mai").textContent).toContain("Never capture");
    // A block arms nothing, so the count and the connection card both still say
    // nothing is being captured.
    expect(armedRow()).toContain("0");
    expect(screen.getByText("Not started — nothing chosen yet")).toBeTruthy();
  });

  // A ROSTER CALL THAT FAILED UPSTREAM. The server degrades to the stored
  // entries, so what arrives is the member's own verdicts with no names on them
  // — and the one thing they must never lose is the ability to change a decision
  // they already made.
  it("still shows and changes stored verdicts when Zalo did not answer", async () => {
    const modes = new Map([["7788", "block"]]);
    const server = rosterServer(modes);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      ...server,
      "/ext/zalo-personal/contacts": () => ({
        // Zalo did not answer, so the server degrades to the stored entries —
        // which carry no display name, because nothing here ever named them.
        roster_available: false,
        contacts: [...modes].map(([id, mode]) => ({
          channel_user_id: id,
          mode,
        })),
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    // The card admits WHY the list is short.
    expect(screen.getByText(/Zalo did not answer just now/)).toBeTruthy();
    // Named by its channel id rather than drawn as an empty row: an entry
    // nobody can name is still an entry somebody chose.
    expect(verdictFor("7788").textContent).toContain("Never capture");

    chooseVerdict("7788", "Capture");
    await flush();
    fireEvent.click(saveButton());
    await flush();
    await flush();

    expect(
      verdictsIn(
        calls.find((call) => call.path === "/ext/zalo-personal/allowlist")
          ?.body,
      ),
      // NO display_name is sent, because none was known: posting the channel id
      // back would store an id as somebody's name, and that stored name is what
      // the next degraded list reads people by.
    ).toEqual([{ channel_user_id: "7788", mode: "allow" }]);
    expect(armedRow()).toContain("1");
  });

  // An account with no contacts is the ordinary first minute after a scan.
  it("says there is nothing to choose from yet, and offers no empty save", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => CONNECTED,
      "/ext/zalo-personal/contacts": () => ({
        roster_available: true,
        contacts: [],
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(screen.getByText(/No contacts to show yet/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save choices" })).toBeNull();
    expect(screen.queryByRole("combobox")).toBeNull();
  });

  // Capture runs FORWARD from the save, and a member who reads this card and
  // expects last month's conversation files a bug that is real. It captures both
  // SIDES from then on, which is a different claim and must not blur into that
  // one — the consent panel recommends the phone app, so the path the product
  // recommends is the path whose answers would otherwise be missing.
  it("promises no history, captures both sides, and arms nothing before a save", async () => {
    const modes = new Map([[CONTACT_IDS.mai, "none"]]);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, rosterServer(modes));
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(
      screen.getByText(/Capture runs forward from the moment you save/),
    ).toBeTruthy();
    expect(
      screen.getByText(/Your earlier conversations are not fetched/),
    ).toBeTruthy();
    // Both directions, bound to that same moment rather than to the past.
    expect(screen.getByText(/From that same moment/)).toBeTruthy();
    expect(screen.getByText(/your own replies are captured too/)).toBeTruthy();
    // And it says HOW, because the CRM does not read anybody's phone.
    expect(
      screen.getByText(/the way it delivers it to your other devices/),
    ).toBeTruthy();
    // Nothing armed, nothing sent, nothing claimed.
    expect(armedRow()).toContain("0");
    expect(
      calls.some((call) => call.path === "/ext/zalo-personal/allowlist"),
    ).toBe(false);
  });

  // A save in flight, and one that never came back: the first must not invite a
  // second press, and the second must not leave a member believing a choice
  // landed.
  it("holds the choice while a save is in flight, and admits one that failed", async () => {
    // A holder rather than a bare `let`: the compiler cannot see the promise
    // executor run, so a nulled variable narrows to `never` at the guard below.
    const inFlight: { fail?: () => void } = {};
    const modes = new Map([[CONTACT_IDS.mai, "none"]]);
    const server = rosterServer(modes);
    const { fetchStub } = stubTransport(FULL_GRANT, {
      ...server,
      // A response that never arrives, then does not arrive at all — the lost
      // answer, which is the failure this mutation is shaped around.
      "/ext/zalo-personal/allowlist": () =>
        new Promise((_resolve, reject) => {
          inFlight.fail = () => reject(new Error("the connection was lost"));
        }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    chooseVerdict("Chi Mai", "Capture");
    await flush();
    fireEvent.click(saveButton());
    await flush();

    // In flight: the verb refuses a second press, and the verdict controls stop
    // moving under a save that is already carrying them.
    expect(
      screen.getByRole("button", { name: "Saving…" }).hasAttribute("disabled"),
    ).toBe(true);
    expect(verdictFor("Chi Mai").hasAttribute("disabled")).toBe(true);

    if (!inFlight.fail) {
      throw new Error("the save never reached the transport");
    }
    inFlight.fail();
    await flush();
    await flush();

    // It says what a member can act on — the list above is what to check — and
    // their choice is still there to press again rather than silently dropped.
    expect(screen.getByRole("alert").textContent).toContain(
      "may not have been saved",
    );
    expect(verdictFor("Chi Mai").textContent).toContain("Capture");
    expect(screen.getByText(/Not saved yet/)).toBeTruthy();
    expect(armedRow()).toContain("0");
    expect(saveButton().hasAttribute("disabled")).toBe(false);
  });

  // A seat that may read and not write sees what was chosen and cannot change
  // it: a control that leads to a 403 is worse than one that is not there.
  it("shows a read-only seat the verdicts without a way to change them", async () => {
    const modes = new Map([[CONTACT_IDS.mai, "allow"]]);
    const { fetchStub } = stubTransport(READ_ONLY_GRANT, rosterServer(modes));
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(verdictFor("Chi Mai").hasAttribute("disabled")).toBe(true);
    expect(screen.queryByRole("button", { name: "Save choices" })).toBeNull();
    expect(armedRow()).toContain("1");
  });

  // The chooser belongs to an account: beside the invitation to scan, it would
  // ask a member to rule on a contact list that does not exist.
  it("offers no chooser before there is an account to choose for", async () => {
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/zalo-personal/status": () => NOT_CONNECTED,
      "/ext/zalo-personal/contacts": () => ({
        roster_available: true,
        contacts: [],
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await flush();

    expect(screen.queryByText("Which conversations are captured")).toBeNull();
    expect(
      calls.some((call) => call.path === "/ext/zalo-personal/contacts"),
    ).toBe(false);
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
    // typed on their phone and nowhere else.
    expect(document.querySelector("input")).toBeNull();
  });
});
