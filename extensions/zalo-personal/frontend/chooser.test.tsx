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
import ZaloPersonalScreen from "./screen";

// THE CHOOSER: which Zalo conversations go into the CRM.
//
// Its own file, and the seam is a real one — screen.test.tsx holds the
// CONNECTION lane (scan, confirm, withdraw, a stranded credential, no session
// material rendered) and this holds the CONSENT lane (which conversations, and
// what the rep is told about them). They share a screen and nothing else: no
// case here scans a code, and no case there chooses anybody.
//
// The split is not tidiness. The two lanes together ran past the 1000-line
// ceiling this repo holds test files to, and the chooser is where the copy is
// argued about — a reviewer reading the words a rep will read should not have to
// walk a QR handshake first.
//
// WHAT IS DUPLICATED, deliberately and with the cost stated: the transport stub,
// `renderScreen` and `flush` are copied from the connection lane rather than
// shared. A shared module would have to import @testing-library and vitest,
// which `check-ext-imports.sh` allows only in a file named `*.test.*` — and a
// `*.test.*` module imported by two suites re-registers its own cases inside
// both. Two small copies that can drift is the lesser defect; if either grows,
// the answer is the harness the gate would then justify.
//
// THE WHOLE FILE RUNS ON FAKE TIMERS: the picker debounces, and a suite sharing
// a machine may not WAIT. Every step is `flush()` or the debounce advance inside
// `addPerson`, so nothing here depends on how busy the runner is.

/** A seat that may choose. */
const FULL_GRANT = {
  seat_type: "full",
  objects: {
    ext_zalo_personal_connection: { read: true, update: true, delete: true },
  },
};

/** A seat that may look and not touch. */
const READ_ONLY_GRANT = {
  seat_type: "full",
  objects: { ext_zalo_personal_connection: { read: true } },
};

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

const NOT_CONNECTED = { connected: false, session_deposited: false };

/** Two people on the rep's own Zalo, by Zalo's own ids. */
const CONTACT_IDS = { mai: "8801", tuan: "8802" } as const;

const CONTACT_NAMES: Readonly<Record<string, string>> = {
  [CONTACT_IDS.mai]: "Chi Mai",
  [CONTACT_IDS.tuan]: "Anh Tuan",
};

type Handler = (body: unknown) => unknown | Promise<unknown>;

function stubTransport(
  authorization: unknown,
  handlers: Readonly<Record<string, Handler>>,
) {
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
      // A route nobody scripted answers 503 rather than something plausible.
      return json({ code: "unavailable" }, 503);
    }
    // AWAITED, so a handler may return a promise the case resolves itself: that
    // is the only way to hold a request in flight on a fake clock, and "the save
    // is still going" is a state a rep sees and a control has to reflect.
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
 * Render, and let BOTH reads a connected screen makes land: the chooser mounts
 * only once the status read has answered, so its roster read starts a beat
 * later, and one drain leaves every assertion below about a skeleton.
 */
async function openChooser() {
  renderScreen();
  await flush();
  await flush();
}

/**
 * How long RecordPicker debounces a search, over-stated on purpose.
 *
 * The design system owns the real number and does not export it, so this
 * advances the fake clock by MORE than it can be: over-advancing still fires the
 * timer, while a copy that fell behind would not — the candidate list would
 * never arrive and a case would assert on an empty search while reporting green.
 */
const PICKER_DEBOUNCE_MS = 1_000;

/** The one control that says what shape the answer takes. */
function shapeSelect() {
  return screen.getByRole("combobox", { name: "Choose one of these two" });
}

/**
 * Pick one of the two shapes, the way a member does.
 *
 * The core's own `pickOption` is not on the published extension surface, so the
 * two steps live here — on the ROLES, so this holds while the control keeps its
 * semantics rather than its markup.
 */
function chooseShape(label: string) {
  fireEvent.click(shapeSelect());
  const listbox = screen.getByRole("listbox");
  fireEvent.click(within(listbox).getByRole("option", { name: label }));
}

/**
 * Find somebody in the roster and put them on the list.
 *
 * The search is a filter over the roster the screen already holds, so the only
 * wait here is the picker's own debounce — no request is made, which is the
 * property the case at the end of this file asserts.
 */
async function addPerson(query: string, name: string) {
  fireEvent.change(screen.getByRole("searchbox"), {
    target: { value: query },
  });
  await act(async () => {
    await vi.advanceTimersByTimeAsync(PICKER_DEBOUNCE_MS);
  });
  fireEvent.click(screen.getByRole("button", { name }));
}

/** Take one named person off the list, by the words their button announces. */
function takeOff(name: string) {
  fireEvent.click(
    screen.getByRole("button", { name: `Take ${name} off the list` }),
  );
}

/** The save, by the words on it. */
function saveButton() {
  return screen.getByRole("button", { name: "Save" });
}

/**
 * What the card says is going into the CRM right now, read from the fact's own
 * row: a bare `getByText("1")` would pass on any stray digit on the screen.
 */
function nowRow(): string {
  const row = screen
    .getByText("Going into the CRM right now")
    .closest(".factlist-row");
  if (!row) {
    throw new Error("the saved-state fact rendered outside a fact row");
  }
  return row.textContent ?? "";
}

/**
 * What the save stated, validated rather than assumed: a save that posted the
 * wrong shape would otherwise read as an empty document, and every assertion
 * about it would pass vacuously.
 */
type SavedDocument = {
  capture_mode: string;
  entries?: { channel_user_id: string; mode: string; display_name?: string }[];
};

function savedDocument(body: unknown): SavedDocument {
  if (
    typeof body !== "object" ||
    body === null ||
    !("capture_mode" in body) ||
    typeof body.capture_mode !== "string"
  ) {
    throw new Error("the save carried no `capture_mode`");
  }
  const entries = "entries" in body ? body.entries : undefined;
  if (entries !== undefined && !Array.isArray(entries)) {
    throw new Error("the save carried a non-array `entries`");
  }
  // Rebuilt rather than asserted: a cast here would let a shape this function
  // never checked reach an assertion, which is how a save that posted the wrong
  // document passes a case about the right one.
  return { capture_mode: body.capture_mode, entries };
}

/**
 * A server that remembers what it was told: the screen shows what the RE-READ
 * says, so a fixed body would let a broken save look like a working one.
 *
 * `modes` is the stored placement per person — `allow` on the pick-list, `block`
 * on the leave-out list, `none` on neither — which is the shape the roster read
 * returns and NOT anything the screen shows a reader.
 */
function rosterServer(
  modes: Map<string, string>,
  shape: { mode?: string } = { mode: "only_chosen" },
) {
  const allowed = () => [...modes.values()].filter((m) => m === "allow").length;
  return {
    "/ext/zalo-personal/status": () => ({
      connected: true,
      session_deposited: true,
      allowed_count: allowed(),
      connection: {
        ...CONNECTION,
        capture_enabled: allowed() > 0,
        // Omitted, not nulled, when this rep has not answered: absent is how the
        // contract says "no shape chosen", and nothing is captured in that state.
        ...(shape.mode === undefined ? {} : { capture_mode: shape.mode }),
      },
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
      const document = savedDocument(body);
      shape.mode = document.capture_mode;
      for (const entry of document.entries ?? []) {
        modes.set(entry.channel_user_id, entry.mode);
      }
      return { saved: document.entries?.length ?? 0, capture_armed: true };
    },
  };
}

/** The button that takes one named person off the list. */
function takeOffButton(name: string) {
  return screen.getByRole("button", { name: `Take ${name} off the list` });
}

/** The same button when the case expects it to be absent. */
function takeOffQuery(name: string) {
  return screen.queryByRole("button", { name: `Take ${name} off the list` });
}

beforeEach(() => {
  vi.useFakeTimers();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("choosing which Zalo conversations go into the CRM", () => {
  // NO DEFAULT SHAPE. A connection nobody has answered for captures nothing, and
  // the contract has no default precisely because defaulting to the permissive
  // one would read somebody's whole personal chat life without anybody deciding
  // to. So the card asks, and offers nothing to save until it is answered.
  it("pre-selects nothing, and offers no save until the rep answers", async () => {
    const modes = new Map([[CONTACT_IDS.mai, "none"]]);
    const { calls, fetchStub } = stubTransport(
      FULL_GRANT,
      rosterServer(modes, {}),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(shapeSelect().textContent).toContain("You have not chosen yet");
    expect(nowRow()).toContain("Nothing — you have not chosen yet");
    // No list and no search until the shape gives them a meaning.
    expect(screen.getByText(/Pick one of the two above/)).toBeTruthy();
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();

    chooseShape("Only the people I choose");
    await flush();

    expect(screen.getByRole("searchbox")).toBeTruthy();
    fireEvent.click(saveButton());
    await flush();
    await flush();

    expect(
      savedDocument(
        calls.find((call) => call.path === "/ext/zalo-personal/allowlist")
          ?.body,
      ),
    ).toEqual({ capture_mode: "only_chosen" });
  });

  // THE DEFECT THIS CARD EXISTS FOR, twice over: it first promised a chooser and
  // shipped none, and then shipped one dropdown per contact — which on a real
  // roster of 68 people is a wall nobody can find anybody in.
  it("offers the two shapes as whole sentences, starting on the saved one", async () => {
    const { fetchStub } = stubTransport(
      FULL_GRANT,
      rosterServer(new Map([[CONTACT_IDS.mai, "allow"]])),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(
      screen.getByText("Which Zalo conversations go into the CRM?"),
    ).toBeTruthy();
    // The shape the server saved, on the control's face rather than guessed.
    expect(shapeSelect().textContent).toContain("Only the people I choose");
    fireEvent.click(shapeSelect());
    const listbox = screen.getByRole("listbox");
    expect(
      within(listbox).getByRole("option", {
        name: "Everyone I talk to — except the people I leave out",
      }),
    ).toBeTruthy();
    expect(
      within(listbox).getByRole("option", { name: "Only the people I choose" }),
    ).toBeTruthy();
    // One search box, not one control per person.
    expect(screen.getAllByRole("searchbox")).toHaveLength(1);
  });

  // EVERYTHING-EXCEPT IS ALLOWED, AND WARNED ABOUT. Not a modal and not a
  // confirm step — one sentence at the moment of choosing, saying what actually
  // happens to a rep's family, friends and doctor.
  it("warns plainly before everything goes in, and then allows it", async () => {
    const { calls, fetchStub } = stubTransport(
      FULL_GRANT,
      rosterServer(new Map([[CONTACT_IDS.mai, "none"]])),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    // Nothing to warn about while only chosen people go in.
    expect(
      screen.queryByText(/your family, your friends, your doctor/),
    ).toBeNull();

    chooseShape("Everyone I talk to — except the people I leave out");
    await flush();

    const warning = screen.getByText(/your family, your friends, your doctor/);
    expect(warning.textContent).toContain("Careful");
    expect(warning.textContent).toContain("into the company CRM");
    // Colleagues reading those chats is the part a rep cannot infer.
    expect(warning.textContent).toContain("Colleagues who can open the CRM");
    // The empty list now says the OPPOSITE of what it says in the other shape:
    // nobody left out means everything goes in.
    expect(screen.getByText(/Nobody is left out yet/)).toBeTruthy();
    // Warned, then allowed: the save is offered rather than withheld.
    fireEvent.click(saveButton());
    await flush();
    await flush();

    const saved = calls.find(
      (call) => call.path === "/ext/zalo-personal/allowlist",
    );
    expect(saved?.method).toBe("PUT");
    // A shape-only change states the shape and names nobody: an empty list of
    // people is not one of the things this save is saying.
    expect(savedDocument(saved?.body)).toEqual({
      capture_mode: "everyone_except",
    });
    expect(nowRow()).toContain("Everyone you talk to");
  });

  it("adds somebody through the search, and saves them onto the list", async () => {
    const modes = new Map([
      [CONTACT_IDS.mai, "none"],
      [CONTACT_IDS.tuan, "none"],
    ]);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, rosterServer(modes));
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    expect(screen.getByText(/Nobody is on the list yet/)).toBeTruthy();
    expect(nowRow()).toContain("0");

    await addPerson("Chi", "Chi Mai");
    await flush();

    // On the list, and NOT yet in force — the screen says which.
    expect(screen.getByText("People you chose")).toBeTruthy();
    expect(takeOffButton("Chi Mai")).toBeTruthy();
    expect(screen.getByText(/changes that are not saved yet/)).toBeTruthy();
    expect(nowRow()).toContain("0");

    fireEvent.click(saveButton());
    await flush();
    await flush();

    expect(
      savedDocument(
        calls.find((call) => call.path === "/ext/zalo-personal/allowlist")
          ?.body,
      ),
    ).toEqual({
      capture_mode: "only_chosen",
      entries: [
        {
          channel_user_id: CONTACT_IDS.mai,
          mode: "allow",
          // The name the screen was showing, stored so the list still reads as
          // people the next time Zalo cannot be reached.
          display_name: "Chi Mai",
        },
      ],
    });
    expect(nowRow()).toContain("1");
    expect(screen.queryByText(/changes that are not saved yet/)).toBeNull();
  });

  it("takes somebody off the list, and saves that too", async () => {
    const modes = new Map([[CONTACT_IDS.mai, "allow"]]);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, rosterServer(modes));
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    expect(nowRow()).toContain("1");

    takeOff("Chi Mai");
    await flush();
    expect(takeOffQuery("Chi Mai")).toBeNull();

    fireEvent.click(saveButton());
    await flush();
    await flush();

    expect(
      savedDocument(
        calls.find((call) => call.path === "/ext/zalo-personal/allowlist")
          ?.body,
      ).entries,
    ).toEqual([
      {
        channel_user_id: CONTACT_IDS.mai,
        mode: "none",
        display_name: "Chi Mai",
      },
    ]);
    expect(screen.getByText(/Nobody is on the list yet/)).toBeTruthy();
    expect(nowRow()).toContain("0");
  });

  // In everything-except the SAME roster reads the other way round: the list on
  // screen is the people left out, and a pick puts somebody on it.
  it("leaves people out in the everything-except shape", async () => {
    const modes = new Map([
      [CONTACT_IDS.mai, "block"],
      [CONTACT_IDS.tuan, "none"],
    ]);
    const { calls, fetchStub } = stubTransport(
      FULL_GRANT,
      rosterServer(modes, { mode: "everyone_except" }),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(screen.getByText("People you leave out")).toBeTruthy();
    expect(takeOffButton("Chi Mai")).toBeTruthy();
    // The person on NEITHER list is not shown as left out.
    expect(takeOffQuery("Anh Tuan")).toBeNull();

    await addPerson("Anh", "Anh Tuan");
    await flush();
    fireEvent.click(saveButton());
    await flush();
    await flush();

    expect(
      savedDocument(
        calls.find((call) => call.path === "/ext/zalo-personal/allowlist")
          ?.body,
      ),
    ).toEqual({
      capture_mode: "everyone_except",
      entries: [
        {
          channel_user_id: CONTACT_IDS.tuan,
          mode: "block",
          display_name: "Anh Tuan",
        },
      ],
    });
  });

  // NOTHING IS SAVED BEFORE A SAVE, and the search does not reach the server
  // either: the roster is already held, so finding somebody in it is a filter.
  it("saves nothing until the save, and searches without asking the server", async () => {
    const modes = new Map([[CONTACT_IDS.mai, "none"]]);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, rosterServer(modes));
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    const before = calls.length;

    chooseShape("Everyone I talk to — except the people I leave out");
    await flush();
    chooseShape("Only the people I choose");
    await flush();
    await addPerson("Chi", "Chi Mai");
    await flush();

    expect(
      calls.some((call) => call.path === "/ext/zalo-personal/allowlist"),
    ).toBe(false);
    // No request AT ALL: not a search endpoint, not a re-read of the roster.
    expect(calls).toHaveLength(before);
    // And the card still reports the saved state rather than the draft.
    expect(nowRow()).toContain("0");
    expect(screen.getByText(/Nothing yet/)).toBeTruthy();
  });

  // The honest limits, in the plain words a rep reads: forward only, both sides,
  // and theirs to take back.
  it("promises no history, captures both sides, and stays the rep's to undo", async () => {
    const { fetchStub } = stubTransport(
      FULL_GRANT,
      rosterServer(new Map([[CONTACT_IDS.mai, "none"]])),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(
      screen.getByText(/Nothing changes until you press save/),
    ).toBeTruthy();
    expect(
      screen.getByText(/your earlier conversations are not fetched/),
    ).toBeTruthy();
    expect(screen.getByText(/From that same moment/)).toBeTruthy();
    expect(
      screen.getByText(/the way it sends it to your other devices/),
    ).toBeTruthy();
    expect(screen.getByText(/This stays yours/)).toBeTruthy();
  });

  // A ROSTER CALL THAT FAILED UPSTREAM. The server degrades to the stored
  // entries, so what arrives is the people this rep already listed and no names
  // — and the one thing they must never lose is the ability to take one off.
  it("still lists and edits the people on the list when Zalo did not answer", async () => {
    const modes = new Map([["7788", "allow"]]);
    const server = rosterServer(modes);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, {
      ...server,
      "/ext/zalo-personal/contacts": () => ({
        roster_available: false,
        contacts: [...modes].map(([id, mode]) => ({
          channel_user_id: id,
          mode,
        })),
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    // The card says why the search will find nobody new, rather than presenting
    // a short list as this rep's whole contact list.
    expect(
      screen.getByText(/searching will not find anybody new/),
    ).toBeTruthy();
    // Named by its channel id rather than drawn blank: somebody nobody can name
    // is still somebody this rep put on the list.
    expect(takeOffButton("7788")).toBeTruthy();

    takeOff("7788");
    await flush();
    fireEvent.click(saveButton());
    await flush();
    await flush();

    // NO display_name is sent, because none was known: posting the channel id
    // back would store an id as somebody's name, and that stored name is what
    // the next degraded list reads people by.
    expect(
      savedDocument(
        calls.find((call) => call.path === "/ext/zalo-personal/allowlist")
          ?.body,
      ).entries,
    ).toEqual([{ channel_user_id: "7788", mode: "none" }]);
  });

  // A save in flight, and one that never came back: the first must not invite a
  // second press, and the second must not leave a rep believing it landed.
  it("holds the changes while a save is in flight, and admits one that failed", async () => {
    // A holder rather than a bare `let`: the compiler cannot see the promise
    // executor run, so a nulled variable narrows to `never` at the guard below.
    const inFlight: { fail?: () => void } = {};
    const modes = new Map([[CONTACT_IDS.mai, "none"]]);
    const server = rosterServer(modes);
    const { fetchStub } = stubTransport(FULL_GRANT, {
      ...server,
      "/ext/zalo-personal/allowlist": () =>
        new Promise((_resolve, reject) => {
          inFlight.fail = () => reject(new Error("the connection was lost"));
        }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    await addPerson("Chi", "Chi Mai");
    await flush();
    fireEvent.click(saveButton());
    await flush();

    expect(
      screen.getByRole("button", { name: "Saving…" }).hasAttribute("disabled"),
    ).toBe(true);
    expect(shapeSelect().hasAttribute("disabled")).toBe(true);
    // Inert, and still THERE: a rep whose save comes back refused has to be
    // looking at the same search box and the same list they pressed it over.
    expect(screen.getByRole("searchbox").hasAttribute("disabled")).toBe(true);

    if (!inFlight.fail) {
      throw new Error("the save never reached the transport");
    }
    inFlight.fail();
    await flush();
    await flush();

    // It says what a rep can act on, and their change is still there to press
    // again rather than silently dropped.
    expect(screen.getByRole("alert").textContent).toContain(
      "may not have been saved",
    );
    expect(takeOffButton("Chi Mai")).toBeTruthy();
    expect(screen.getByText(/changes that are not saved yet/)).toBeTruthy();
    expect(saveButton().hasAttribute("disabled")).toBe(false);
  });

  // A seat that may read and not write sees the list and cannot touch it: a
  // control that leads to a 403 is worse than one that is not there.
  it("shows a read-only seat the list without a way to change it", async () => {
    const { fetchStub } = stubTransport(
      READ_ONLY_GRANT,
      rosterServer(new Map([[CONTACT_IDS.mai, "allow"]])),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(screen.getByText("Chi Mai")).toBeTruthy();
    expect(shapeSelect().hasAttribute("disabled")).toBe(true);
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(takeOffQuery("Chi Mai")).toBeNull();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  // The chooser belongs to an account: beside the invitation to scan, it would
  // ask a rep to rule on a contact list that does not exist.
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

    expect(
      screen.queryByText("Which Zalo conversations go into the CRM?"),
    ).toBeNull();
    expect(
      calls.some((call) => call.path === "/ext/zalo-personal/contacts"),
    ).toBe(false);
  });
});
