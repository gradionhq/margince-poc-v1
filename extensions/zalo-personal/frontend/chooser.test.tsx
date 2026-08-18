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

/** One of the two answers, by the sentence a rep reads. */
function shapeRadio(label: string | RegExp) {
  return screen.getByRole("radio", { name: label });
}

/** Which answer currently carries the dot, or "" while nobody has answered. */
function chosenShape(): string {
  const chosen = screen
    .getAllByRole("radio")
    .find((radio) => radio.matches(":checked"));
  return chosen?.getAttribute("value") ?? "";
}

/** Answer the question, the way a rep does: press the sentence. */
function chooseShape(label: string | RegExp) {
  fireEvent.click(shapeRadio(label));
}

/**
 * Find somebody in the roster and put them on the list.
 *
 * The search is a filter over the roster the screen already holds, so the only
 * wait here is the picker's own debounce — no request is made, which is the
 * property one of the cases below asserts.
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

/** Take one named person off the list, by the words their control announces. */
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

/** The way out of a quiet-period wait, by the words on it. */
function checkNowButton() {
  return screen.getByRole("button", { name: "Check now" });
}

/** The same control when a case expects it to be absent. */
function checkNowQuery() {
  return screen.queryByRole("button", { name: "Check now" });
}

/**
 * A rep who is sitting inside a quiet-period wait, on a server that REMEMBERS
 * being asked to look sooner.
 *
 * The wait clearing is the server's answer and not this fixture's opinion: a
 * screen that showed the confirmation without the row having moved would pass a
 * case about a control that did nothing.
 */
function backedOffServer(state: { waiting: boolean; status?: string }) {
  return {
    ...rosterServer(new Map([[CONTACT_IDS.mai, "allow"]])),
    "/ext/zalo-personal/status": () => ({
      connected: true,
      session_deposited: true,
      allowed_count: 1,
      connection: {
        ...CONNECTION,
        status: state.status ?? "connected",
        capture_enabled: true,
        capture_mode: "only_chosen",
        last_polled_at: "2026-08-18T09:14:00Z",
        // Absent means "due now", which is exactly what the contract says: the
        // database answers the comparison, so the field is only ever present
        // while a wait is still in the future.
        ...(state.waiting
          ? { next_check_after: "2026-08-18T09:29:00Z" }
          : {}),
      },
    }),
    "/ext/zalo-personal/check-now": () => {
      const answer = { was_waiting: state.waiting };
      state.waiting = false;
      return answer;
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

    // Both answers are on screen, and NEITHER carries the dot: nothing claims to
    // be the default, which is the contract's own rule made visible.
    expect(shapeRadio(/Everyone I talk to/)).toBeTruthy();
    expect(shapeRadio(/Only the people I choose/)).toBeTruthy();
    expect(chosenShape()).toBe("");
    // No list and no search until the answer gives them a meaning.
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();

    chooseShape(/Only the people I choose/);
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

    // The card's own heading asks the question; the group's legend repeats it
    // only for assistive tech, which is why this reads the heading by role.
    expect(
      screen.getByRole("heading", {
        name: "Which Zalo conversations go into the CRM?",
      }),
    ).toBeTruthy();
    // BOTH answers readable at rest, each carrying what it does — no menu to
    // open to discover what the alternative was.
    expect(chosenShape()).toBe("only_chosen");
    expect(
      screen.getByRole("group", {
        name: "Which Zalo conversations go into the CRM?",
      }),
    ).toBeTruthy();
    expect(
      shapeRadio(
        /Everyone I talk to.*Every conversation on this Zalo account goes into the CRM/,
      ),
    ).toBeTruthy();
    expect(
      shapeRadio(/Only the people I choose.*Nothing goes in until you name/),
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

    chooseShape(/Everyone I talk to/);
    await flush();

    // The notice as a whole, so the heading and the sentence are one thing a rep
    // reads rather than two elements a test happens to find.
    const warning = screen
      .getByText(/your family, your friends, your doctor/)
      .closest(".callout");
    expect(warning?.textContent).toContain("Careful");
    expect(warning?.textContent).toContain("into the company CRM");
    // Colleagues reading those chats is the part a rep cannot infer.
    expect(warning?.textContent).toContain("Colleagues who can open the CRM");
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
    expect(chosenShape()).toBe("everyone_except");
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

    await addPerson("Chi", "Chi Mai");
    await flush();

    // On the list, and NOT yet in force — the screen says which.
    expect(screen.getByText("People you chose")).toBeTruthy();
    expect(takeOffButton("Chi Mai")).toBeTruthy();
    expect(screen.getByText(/changes that are not saved yet/)).toBeTruthy();

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
    expect(takeOffButton("Chi Mai")).toBeTruthy();
    expect(screen.queryByText(/changes that are not saved yet/)).toBeNull();
  });

  // TWO people on the list, deliberately: with one, a remove control that took
  // off "whoever is first" would pass every assertion here while being wrong.
  it("takes off the person its own control names, and saves that too", async () => {
    const modes = new Map([
      [CONTACT_IDS.mai, "allow"],
      [CONTACT_IDS.tuan, "allow"],
    ]);
    const { calls, fetchStub } = stubTransport(FULL_GRANT, rosterServer(modes));
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    expect(takeOffButton("Chi Mai")).toBeTruthy();
    expect(takeOffButton("Anh Tuan")).toBeTruthy();

    takeOff("Anh Tuan");
    await flush();
    expect(takeOffQuery("Anh Tuan")).toBeNull();
    // The one nobody touched is untouched.
    expect(takeOffButton("Chi Mai")).toBeTruthy();

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
        channel_user_id: CONTACT_IDS.tuan,
        mode: "none",
        display_name: "Anh Tuan",
      },
    ]);
    expect(takeOffButton("Chi Mai")).toBeTruthy();
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

    chooseShape(/Everyone I talk to/);
    await flush();
    chooseShape(/Only the people I choose/);
    await flush();
    await addPerson("Chi", "Chi Mai");
    await flush();

    expect(
      calls.some((call) => call.path === "/ext/zalo-personal/allowlist"),
    ).toBe(false);
    // No request AT ALL: not a search endpoint, not a re-read of the roster.
    expect(calls).toHaveLength(before);
    // And the card still shows the SAVED answer rather than the draft: the
    // second press put the dot back where the server has it.
    expect(chosenShape()).toBe("only_chosen");
  });

  // The honest limits, in the plain words a rep reads: forward only, both sides,
  // and theirs to take back.
  it("keeps every fact the cut had to preserve, beside what it constrains", async () => {
    const { fetchStub } = stubTransport(
      FULL_GRANT,
      rosterServer(new Map([[CONTACT_IDS.mai, "none"]])),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    // Forward-only and no history, beside the button it constrains.
    expect(
      screen.getByText(/Saving changes what happens from then on/),
    ).toBeTruthy();
    expect(
      screen.getByText(/Earlier conversations are not fetched/),
    ).toBeTruthy();
    // Both sides, beside the control that names people.
    expect(screen.getByText(/Both sides of a conversation go in/)).toBeTruthy();
    expect(screen.getByText(/Nothing here reads your phone/)).toBeTruthy();
    // Theirs to change or take back, in the card's own subtitle.
    expect(screen.getByText(/Your call, and yours alone/)).toBeTruthy();
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
    expect(
      shapeRadio(/Only the people I choose/).hasAttribute("disabled"),
    ).toBe(true);
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
    expect(
      shapeRadio(/Only the people I choose/).hasAttribute("disabled"),
    ).toBe(true);
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(takeOffQuery("Chi Mai")).toBeNull();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
  });

  // "I SAVED AND NOTHING HAPPENED" — reported about a system working perfectly:
  // the run had drained, found nothing and backed off, and no screen said so.
  // `Last checked` is the fact both sibling connectors already render, and the
  // wait is the state that reads as a broken feature unless it is named.
  it("says when it last looked, and names the wait that reads as broken", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      ...rosterServer(new Map([[CONTACT_IDS.mai, "allow"]])),
      "/ext/zalo-personal/status": () => ({
        connected: true,
        session_deposited: true,
        allowed_count: 1,
        connection: {
          ...CONNECTION,
          capture_enabled: true,
          capture_mode: "only_chosen",
          last_polled_at: "2026-08-18T09:14:00Z",
          next_check_after: "2026-08-18T09:29:00Z",
        },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    // The sibling connectors' own term, not a variant of it.
    expect(screen.getByText("Last checked")).toBeTruthy();
    // Both halves of the honest answer: nothing is wrong, and there is a way to
    // be looked at sooner. Without the second half a rep waits, or reconnects for
    // no reason — and the way out it names must be one that is actually on screen,
    // which is the whole reason it no longer names the save.
    const wait = screen.getByText(/All quiet, so the next check/);
    expect(wait.textContent).toContain("ask for a check sooner below");
    expect(wait.textContent).not.toContain("Saving");
    expect(checkNowButton()).toBeTruthy();
    // Nothing failed, so nothing claims anything did.
    expect(screen.queryByText(/could not be reached/)).toBeNull();
  });

  // THE DEFECT THIS CONTROL EXISTS FOR, and it was hit twice in live testing on a
  // connector that was working perfectly: the sentence above used to send a rep to
  // the save, and a rep who has already chosen the right list has nothing to save,
  // so the button carrying the remedy was greyed out at exactly that moment. The
  // control is therefore asserted WITH a save that is inert.
  it("offers a way out of the wait when there is nothing to save", async () => {
    const { calls, fetchStub } = stubTransport(
      FULL_GRANT,
      backedOffServer({ waiting: true }),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    // Nothing has been changed, so the save is refused — and the way out is not.
    expect(saveButton().hasAttribute("disabled")).toBe(true);
    const check = checkNowButton();
    expect(check.hasAttribute("disabled")).toBe(false);

    fireEvent.click(check);
    await flush();
    await flush();

    const asked = calls.filter(
      (call) => call.path === "/ext/zalo-personal/check-now",
    );
    // POST, not GET: the seat ceiling classifies a mutation by its method, and
    // this one writes the connection's schedule.
    expect(asked.map((call) => call.method)).toEqual(["POST"]);
    // The re-read now reports no wait, so the control has nothing left to do and
    // the confirmation says what was achieved. It promises no fetch: this screen
    // moved a schedule, and the scheduled run is what looks.
    expect(screen.queryByText(/All quiet, so the next check/)).toBeNull();
    expect(checkNowQuery()).toBeNull();
    const done = screen.getByText(/The next check is due/);
    expect(done.textContent).toContain("nothing is fetched by this screen");
  });

  // A seat that may only read is told the FACT and pointed at nothing: a sentence
  // naming a control that seat does not have sends them looking for a button that
  // is not there, which is the same defect one rung down.
  it("names no way out to a seat that may not ask for one", async () => {
    const { fetchStub } = stubTransport(
      READ_ONLY_GRANT,
      backedOffServer({ waiting: true }),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    const wait = screen.getByText(/All quiet, so the next check/);
    expect(wait.textContent).toContain("checks are spaced further apart");
    expect(wait.textContent).not.toContain("ask for a check");
    expect(checkNowQuery()).toBeNull();
  });

  // The remedy for a session Zalo stopped accepting is a human with a phone. A
  // scheduled check visits only a CONNECTED account, so offering the control here
  // would advertise a check the tick will never make — the server refuses it too.
  it("offers no way out for a connection no scheduled check would visit", async () => {
    const { fetchStub } = stubTransport(
      FULL_GRANT,
      backedOffServer({ waiting: true, status: "needs_reconnect" }),
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();

    expect(screen.getByText(/All quiet, so the next check/)).toBeTruthy();
    expect(checkNowQuery()).toBeNull();
  });

  // A failure is reported in the connector's OWN vocabulary — never Zalo's
  // message — and the two classes below are the two a rep can act on
  // differently: one needs their phone, the other needs nothing at all.
  it("says what went wrong last, in words a rep can act on", async () => {
    const failure = { class: "session_withdrawn" };
    const { fetchStub } = stubTransport(FULL_GRANT, {
      ...rosterServer(new Map([[CONTACT_IDS.mai, "allow"]])),
      "/ext/zalo-personal/status": () => ({
        connected: true,
        session_deposited: true,
        allowed_count: 1,
        connection: {
          ...CONNECTION,
          capture_enabled: true,
          capture_mode: "only_chosen",
          last_polled_at: "2026-08-18T09:14:00Z",
          last_error_class: failure.class,
        },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    await openChooser();
    expect(
      screen.getByText(/Scan a new code with your phone to take it up again/),
    ).toBeTruthy();

    // The other class a rep meets, which asks nothing of them.
    cleanup();
    failure.class = "provider_unavailable";
    await openChooser();
    expect(
      screen.getByText(/Zalo could not be reached on the last check/),
    ).toBeTruthy();
    expect(screen.queryByText(/Scan a new code/)).toBeNull();
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
      screen.queryByRole("heading", {
        name: "Which Zalo conversations go into the CRM?",
      }),
    ).toBeNull();
    expect(
      calls.some((call) => call.path === "/ext/zalo-personal/contacts"),
    ).toBe(false);
  });
});
