/** @vitest-environment jsdom */

import { LocaleProvider } from "@margince/frontend/app";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import NotesScreen from "./screen";

// The reference extension's screen, over a stubbed transport.
//
// It is a *.test.tsx, so it is outside the app and node TypeScript programs
// (both exclude tests) and vitest does not typecheck. It IS compiled, by
// tsconfig.composed-tests.json — the fourth project, added for exactly this
// file and run by `make fe-typecheck-composed` — so the fixtures below are
// held against the merged contract even though vitest never looks.
//
// And it is RUN, by `make fe-test-ext` (frontend/vitest.ext.config.ts), which
// `make check-fe` calls. It was not, for the whole of the first review round:
// vitest's root is frontend/, so nothing under extensions/ was ever collected —
// 2230 tests ran and none of them were these. Typechecked but never executed is
// the state that let the racy assertion in the wrapped-body case below sit here
// looking green.
//
// WHAT THIS SUITE CANNOT SEE, and did not: it stubs `fetch`, so every
// assertion here is against a fixture somebody wrote, not against a body the
// server sent. The server used to answer the governed-tool envelope while the
// contract declared the bare payload; these tests passed throughout, because
// the stub returned the contract's shape and the screen read the contract's
// shape — both wrong in the same direction. Task 14 found it by clicking (F1).
//
// The Go side is where that gap is now closed:
// backend/internal/compose/extroutes_conformance_test.go issues a real request
// through a real mounted route and asserts the body against the operation's
// declared 200 schema. `envelopeLeak` below is this file's half of the pair —
// it drives the screen against the shape the server ACTUALLY used to send, so
// a regression on either side of the seam fails somewhere.

/**
 * The grants a full seat holds on the unit's TWO objects.
 *
 * Two, not one: the secrets operations gate on `ext_notes_signing_key`
 * separately from the notes, because a role that may add a note has no business
 * rotating the installation's credential. `update` is what stores or rotates the
 * key — there is one per workspace, so setting it is never a create.
 */
const FULL_GRANT = {
  seat_type: "full",
  objects: {
    ext_notes_note: { read: true, create: true, delete: true },
    ext_notes_signing_key: { read: true, update: true },
  },
};

type Handler = (body: unknown) => unknown;

// stubTransport answers /me plus the extension's six operations, records what
// was asked, and 503s anything else — so a screen that reached for a route
// nobody scripted fails here rather than silently rendering an error card.
function stubTransport(
  authorization: unknown,
  handlers: Readonly<Record<string, Handler>>,
) {
  const calls: {
    path: string;
    method: string;
    query: Record<string, string>;
    body: unknown;
  }[] = [];
  // The client is built with `fetch: (request) => globalThis.fetch(request)`,
  // so the stub is handed ONE Request and no init — reading the body off an
  // init argument records null for every call and quietly makes a "what did
  // the screen send" assertion vacuous.
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
    // The QUERY is split off the path rather than left on it, because the unit's
    // bodyless operations carry their arguments there: keyed by the whole URL,
    // the DELETE's handler lookup misses and every remove answers the 503 below
    // — which reads as "the screen called a route nobody scripted" when what
    // happened is that the harness could not see a query string.
    const parsed = new URL(url, "http://stub.invalid");
    const path = parsed.pathname.slice("/v1".length);
    const query = Object.fromEntries(parsed.searchParams);
    const method = input instanceof Request ? input.method : "GET";
    const raw = input instanceof Request ? await input.text() : "";
    const body = raw === "" ? null : JSON.parse(raw);
    calls.push({ path, method, query, body });
    const handler = handlers[path];
    if (!handler) {
      return json({ code: "unavailable" }, 503);
    }
    return json(handler(body));
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
        <NotesScreen />
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

describe("the Demo Notepad screen", () => {
  // The app shell yields the page's name to a composed unit, so this screen's
  // own top header is the page's ONE h1 — and every card header under it stays
  // at level 2. A unit that leaves its top header at the default ships a surface
  // with no page-level heading for a reader to jump to, which is what the shell
  // used to paper over by printing "Not found" there instead.
  it("names the page in the one level-1 heading a unit screen owns", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/notes/list": () => ({ notes: [] }),
      "/ext/notes/signing-key/status": () => ({ stored: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const h1 = await screen.findByRole("heading", { level: 1 });
    expect(h1.textContent).toBe("Demo Notepad");
    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
  });

  it("lists the workspace's notes, heartbeat rows included", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/notes/list": () => ({
        notes: [
          {
            id: "11111111-1111-4111-8111-111111111111",
            body: "hello from the demo extension",
            created_at: "2026-08-09T09:14:00Z",
          },
          {
            id: "22222222-2222-4222-8222-222222222222",
            body: "⟳ heartbeat — tick #7 (workspace 0195d3f2)",
            created_at: "2026-08-09T09:10:00Z",
          },
        ],
      }),
      "/ext/notes/signing-key/status": () => ({ stored: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(
      await screen.findByText(/hello from the demo extension/),
    ).toBeTruthy();
    // The tick is the one row nobody typed, and showing it is how the jobs
    // surface becomes observable at all.
    expect(screen.getByText(/heartbeat — tick #7/)).toBeTruthy();
  });

  it("marks a note as filed only while the row still names an activity", async () => {
    // The badge is read from the ROW, which is what makes it survivable: a
    // filing is withdrawn when the activity is archived on the timeline —
    // nowhere near this screen — and the unit's subscription clears the
    // column. A badge this component remembered would go on claiming a filing
    // the notepad no longer has.
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/notes/list": () => ({
        notes: [
          {
            id: "11111111-1111-4111-8111-111111111111",
            body: "still filed",
            filed_activity_id: "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
            created_at: "2026-08-09T09:14:00Z",
          },
          {
            id: "22222222-2222-4222-8222-222222222222",
            body: "filing withdrawn",
            created_at: "2026-08-09T09:10:00Z",
          },
        ],
      }),
      "/ext/notes/signing-key/status": () => ({ stored: false }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    const filedRow = (await screen.findByText(/still filed/)).closest("li");
    expect(filedRow?.textContent).toContain("Filed");
    const withdrawnRow = screen.getByText(/filing withdrawn/).closest("li");
    expect(withdrawnRow?.textContent).not.toContain("Filed");
  });

  it("reports the signing key as absent, then present, without ever showing it", async () => {
    let stored = false;
    const { fetchStub, calls } = stubTransport(FULL_GRANT, {
      "/ext/notes/list": () => ({ notes: [] }),
      "/ext/notes/signing-key/status": () => ({ stored }),
      "/ext/notes/signing-key": () => {
        stored = true;
        return { stored: true };
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText("No key stored")).toBeTruthy();

    await userEvent.type(screen.getByLabelText("Signing key"), "s3cr3t");
    await userEvent.click(screen.getByRole("button", { name: "Store key" }));
    expect(await screen.findByText("Connected")).toBeTruthy();

    // The key went UP and never came back down: no response the screen read
    // carries it, and the field was cleared so it is not left sitting in the
    // DOM either.
    const sent = calls.find((c) => c.path === "/ext/notes/signing-key");
    expect(sent?.body).toEqual({ key: "s3cr3t" });
    // The INPUT's value, not document.body.textContent. An <input> never
    // contributes its value to textContent, so the textContent form of this
    // assertion passes whether or not the field was cleared — it was asserting
    // nothing about the one place the key is actually still sitting.
    const field = screen.getByLabelText("Signing key");
    // `instanceof`, not a cast: the runtime check and the narrowing are the
    // same expression, so a control that stopped being an <input> fails here
    // rather than reading `.value` off something that has none.
    if (!(field instanceof HTMLInputElement)) {
      throw new Error("the signing key control is not an <input>");
    }
    expect(field.value).toBe("");
    // And nowhere else on the page either — that is what textContent is good
    // for, so it stays alongside rather than instead.
    expect(document.body.textContent).not.toContain("s3cr3t");
  });

  it("returns a signature computed with the stored key", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/notes/list": () => ({ notes: [] }),
      "/ext/notes/signing-key/status": () => ({ stored: true }),
      "/ext/notes/signature": () => ({
        algorithm: "hmac-sha256",
        signature: "4f1c9ae207",
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await userEvent.type(
      await screen.findByLabelText("Payload to sign"),
      "hello",
    );
    await userEvent.click(screen.getByRole("button", { name: "Sign" }));
    expect(await screen.findByText("hmac-sha256 4f1c9ae207")).toBeTruthy();
  });

  it("adds and removes a note through the unit's own routes", async () => {
    const id = "11111111-1111-4111-8111-111111111111";
    let notes: { id: string; body: string; created_at: string }[] = [];
    const { fetchStub, calls } = stubTransport(FULL_GRANT, {
      "/ext/notes/list": () => ({ notes }),
      "/ext/notes/signing-key/status": () => ({ stored: true }),
      "/ext/notes/add": () => {
        const added = {
          id,
          body: "a note",
          created_at: "2026-08-09T09:14:00Z",
        };
        notes = [added];
        return added;
      },
      "/ext/notes/remove": () => {
        notes = [];
        return { removed: true };
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    await userEvent.type(await screen.findByLabelText("New note"), "a note");
    await userEvent.click(screen.getByRole("button", { name: "Add" }));
    // The row appears from a REFETCH, not from the mutation's own answer: the
    // heartbeat writes rows nobody clicked, so a screen that patched its cache
    // from responses alone would drift away from the table it is displaying.
    expect(await screen.findByText(/a note/)).toBeTruthy();
    const add = calls.find((c) => c.path === "/ext/notes/add");
    expect(add?.method).toBe("POST");
    expect(add?.body).toEqual({ body: "a note" });
    // The read is a GET and sends nothing. Pinned here because the METHOD is
    // what the seat ceiling classifies on: a read that goes back to being a POST
    // is refused for every read seat, and no other assertion in this suite would
    // notice — the stub answers whatever the path is keyed to either way.
    const list = calls.find((c) => c.path === "/ext/notes/list");
    expect(list?.method).toBe("GET");
    expect(list?.body).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    // The id in the QUERY, on a DELETE — the operation carries no body, so a
    // client sending one would be sending an argument the route never reads.
    // Both halves asserted: a `body` assertion alone passed while the id was
    // going nowhere, since `undefined` is what a missing body records too.
    await waitFor(() => {
      const remove = calls.find((c) => c.path === "/ext/notes/remove");
      expect(remove?.method).toBe("DELETE");
      expect(remove?.query).toEqual({ id });
      expect(remove?.body).toBeNull();
    });
    expect(await screen.findByText("No notes yet.")).toBeTruthy();
  });

  it("hides the write controls from a seat that holds only read", async () => {
    // The observable half of the demo's read-only-seat step, and the reason
    // the unit declares an RBAC object at all: the list renders, the controls
    // do not. UX honesty only — the server's gate is the authority — but a
    // screen that showed Add to a reader would send them into a refusal.
    //
    // `Store key` is in that set now and was not before. The UAT re-run found
    // this control rendered for a read-only seat and, worse, WORKED: the
    // operations declared no RBAC object, so nothing refused the write and the
    // reader replaced the installation's signing key (R1). The seat below holds
    // read on the key and not update, which is what a read-only role looks like
    // after the fix.
    const { fetchStub } = stubTransport(
      {
        seat_type: "read",
        objects: {
          ext_notes_note: { read: true },
          ext_notes_signing_key: { read: true },
        },
      },
      {
        "/ext/notes/list": () => ({
          notes: [
            {
              id: "11111111-1111-4111-8111-111111111111",
              body: "visible to a reader",
              created_at: "2026-08-09T09:14:00Z",
            },
          ],
        }),
        "/ext/notes/signing-key/status": () => ({ stored: true }),
      },
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(await screen.findByText(/visible to a reader/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Add" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Remove" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Store key" })).toBeNull();
    // But the read side of the same object survives: the reader still sees
    // whether a key is stored, and can still sign. A lockout would pass the
    // three assertions above and be the wrong fix.
    expect(await screen.findByText("Connected")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Sign" })).toBeTruthy();
  });

  // The screen against the WRONG body — the envelope the server used to send.
  //
  // Not a hypothetical: this is verbatim what `POST /v1/ext/notes/…`
  // returned before the unwrap, and it is why every read rendered `undefined`.
  // The assertion is that the screen FAILS VISIBLY rather than rendering
  // nothing, because a wrapper is exactly the shape a future transport change
  // could reintroduce and "renders undefined" is the state no gate noticed.
  it("shows an error, not a blank, when a read comes back wrapped", async () => {
    const envelope = (data: unknown) => ({
      schema_version: "1.0.0",
      trace_id: "019fe351-1f62-749f-ac9f-a89d5a81abfa",
      freshness: { authoritative: true },
      trust: "t0",
      evidence: [],
      warnings: [],
      data,
    });
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/notes/list": () => envelope({ notes: [] }),
      "/ext/notes/signing-key/status": () => envelope({ stored: true }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    // BOTH reads fail loudly, and the count is the assertion. There are two
    // wrapped bodies here — the notes list and the signing status — and each
    // card's query gate renders its own error card rather than an empty list
    // that would read as "you have no notes" or a badge that would read as a
    // claim about the installation.
    //
    // findByText(/…/) was what this asserted before, and it was two defects in
    // one line. It is `getBy` under a waitFor, so two matching cards are a
    // "found multiple elements" throw — it passed only because the two queries
    // settle on different ticks and the first poll sometimes caught one of
    // them. And it never said WHICH card it found: softening the notes hook's
    // guard to `return data?.notes ?? []` — precisely the regression this case
    // exists to catch, and precisely what the UAT found the first time — leaves
    // the notes card showing "No notes yet." and the SIGNING card's error card
    // on the page, which satisfied both of the old assertions. The length is
    // what distinguishes one broken read from two; that mutation fails here.
    await waitFor(() =>
      expect(screen.getAllByText(/Couldn't load this view/)).toHaveLength(2),
    );
    // And the connection state does NOT claim to be connected off a body it
    // could not read — nor "No key stored", which is the same claim in the
    // other direction and would invite someone to paste a key over one that is
    // already there.
    expect(screen.queryByText("Connected")).toBeNull();
    expect(screen.queryByText("No key stored")).toBeNull();
  });

  it("says so when the principal cannot read the unit's notes at all", async () => {
    const { fetchStub, calls } = stubTransport(
      { seat_type: "full", objects: {} },
      { "/ext/notes/signing-key/status": () => ({ stored: false }) },
    );
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    expect(
      await screen.findByText(
        "You do not hold read access to this extension's notes.",
      ),
    ).toBeTruthy();

    // And it asked for nothing. The list query is gated on the same grant the
    // card is, so an ungranted seat fires no request the server would answer
    // 403 — which matters here more than on a one-shot read, because this
    // query POLLS: an ungranted seat would otherwise repeat a refused request
    // every fifteen seconds for as long as the tab is open.
    expect(calls.some((c) => c.path === "/ext/notes/list")).toBe(false);
  });
});

// The grants filing needs on the unit's side. The CORE `activity:create` grant
// it also needs is not here and cannot be: nothing declares the pairing, so a
// seat can hold this one and still be refused by the server — which is the
// state the failure copy is written for.
const FILING_GRANT = {
  seat_type: "full",
  objects: {
    ext_notes_note: { read: true, create: true, delete: true },
    ext_notes_signing_key: { read: true, update: true },
    ext_notes_filing: { create: true },
  },
};

describe("filing a note to a record", () => {
  const listOnly = { "/ext/notes/list": () => ({ notes: [] }) };
  // One candidate, from the product's own cross-object search. The picker
  // debounces, so every case that picks one waits on findBy* rather than
  // asserting straight after typing.
  const searchAnswers = {
    "/search": () => ({
      data: [
        {
          type: "deal",
          id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
          title: "Acme renewal",
        },
      ],
      page: { has_more: false },
    }),
  };

  it("is absent for a seat that was not granted it", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, listOnly);
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();
    expect(
      await screen.findByText("You have not been granted filing."),
    ).toBeTruthy();
    expect(screen.queryByRole("button", { name: "File to record" })).toBeNull();
  });

  it("sends the note, the record kind and the record it was filed to", async () => {
    const { calls, fetchStub } = stubTransport(FILING_GRANT, {
      ...listOnly,
      ...searchAnswers,
      "/ext/notes/file": () => ({
        id: "11111111-1111-4111-8111-111111111111",
        kind: "note",
        body: "a filed note",
        filed_activity_id: "3f2504e0-4f89-41d3-9a0c-0305e82c3301",
        created_at: "2026-08-13T09:30:00Z",
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("Note to file"),
      "a filed note",
    );
    // Through the Select, not on its default: the narrowing that turns the
    // control's string back into the contract's enum is the code this case is
    // about, and a filing that never touches the control never runs it.
    await user.click(screen.getByLabelText("Record type"));
    await user.click(await screen.findByRole("option", { name: "Deal" }));
    // The record is CHOSEN, by name. Nobody types the id — which is the whole
    // point of the control — so the id in the request can only have come from
    // the candidate the search answered with.
    await user.type(
      screen.getByLabelText("Find the record by name"),
      "renewal",
    );
    await user.click(
      await screen.findByRole("button", { name: "Acme renewal" }),
    );
    await user.click(screen.getByRole("button", { name: "File to record" }));

    await waitFor(() => {
      expect(calls.some((call) => call.path === "/ext/notes/file")).toBe(true);
    });
    const filed = calls.find((call) => call.path === "/ext/notes/file");
    expect(filed?.body).toEqual({
      body: "a filed note",
      subject_type: "deal",
      subject_id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
    });
    // Both halves landed, so the form clears and says so.
    expect(
      await screen.findByText("Filed to the record's timeline."),
    ).toBeTruthy();
  });

  // The search is SCOPED to the record type the form is on. Unscoped, a query
  // for "acme" while the form says Person would offer the Acme organization,
  // and filing it would name a person id that is an organization's.
  it("searches only the record type the form is on", async () => {
    const { calls, fetchStub } = stubTransport(FILING_GRANT, {
      ...listOnly,
      ...searchAnswers,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    const user = userEvent.setup();
    await user.click(await screen.findByLabelText("Record type"));
    await user.click(await screen.findByRole("option", { name: "Deal" }));
    await user.type(screen.getByLabelText("Find the record by name"), "acme");

    await waitFor(() => {
      expect(calls.some((call) => call.path === "/search")).toBe(true);
    });
    const search = calls.find((call) => call.path === "/search");
    expect(search?.query.types).toBe("deal");
    expect(search?.query.q).toBe("acme");
  });

  // Changing the TYPE clears the pick. A contact chosen while the form said
  // Person is not a deal, and a stale pick would file the note against
  // whatever record happens to hold that id — or, far more often, against
  // nothing, with a refusal nobody can read.
  it("drops the picked record when the record type changes", async () => {
    const { fetchStub } = stubTransport(FILING_GRANT, {
      ...listOnly,
      ...searchAnswers,
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("Note to file"),
      "a filed note",
    );
    await user.type(screen.getByLabelText("Find the record by name"), "acme");
    await user.click(
      await screen.findByRole("button", { name: "Acme renewal" }),
    );
    // Filing is possible: a body and a record.
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: "File to record" })
        .disabled,
    ).toBe(false);

    await user.click(screen.getByLabelText("Record type"));
    await user.click(await screen.findByRole("option", { name: "Deal" }));

    // And is not any more, because the record it would have named is gone.
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: "File to record" })
        .disabled,
    ).toBe(true);
  });

  // A hit the contract types as nullable-titled is DROPPED rather than
  // rendered as an empty row: a candidate nobody can read is one nobody can
  // choose on purpose.
  it("offers no candidate for a hit with no title", async () => {
    const { calls, fetchStub } = stubTransport(FILING_GRANT, {
      ...listOnly,
      "/search": () => ({
        data: [
          {
            type: "deal",
            id: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
            title: null,
          },
        ],
        page: { has_more: false },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("Find the record by name"),
      "acme",
    );
    // The search ran and answered; nothing became pickable, so filing stays
    // impossible. Asserted on the control rather than on the absence of a row,
    // because "no row appeared" is also what a search that never ran looks
    // like — the /search call is what separates them.
    await waitFor(() => {
      expect(calls.some((call) => call.path === "/search")).toBe(true);
    });
    expect(
      screen.getByRole<HTMLButtonElement>("button", { name: "File to record" })
        .disabled,
    ).toBe(true);
  });

  // The two grants are separate and the second one is the server's to check, so
  // the copy says what is true either way: nothing was written.
  it("says nothing was written when the server refuses", async () => {
    const { fetchStub } = stubTransport(FILING_GRANT, {
      ...listOnly,
      ...searchAnswers,
      "/ext/notes/file": () => {
        throw new Error("refused");
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    const user = userEvent.setup();
    await user.type(
      await screen.findByLabelText("Note to file"),
      "a filed note",
    );
    await user.type(screen.getByLabelText("Find the record by name"), "acme");
    await user.click(
      await screen.findByRole("button", { name: "Acme renewal" }),
    );
    await user.click(screen.getByRole("button", { name: "File to record" }));

    expect(
      await screen.findByText(
        "The note may not have been filed. Check the notepad below before trying again.",
      ),
    ).toBeTruthy();
    // Nothing clears on a failure: retyping a note whose outcome is unknown is
    // how a person files it twice.
    expect(screen.getByLabelText<HTMLInputElement>("Note to file").value).toBe(
      "a filed note",
    );
  });
});

describe("the signing card's answer", () => {
  const stored = {
    "/ext/notes/signing-key/status": () => ({ stored: true }),
  };
  const listOnly = { "/ext/notes/list": () => ({ notes: [] }) };

  async function sign(user: ReturnType<typeof userEvent.setup>, text: string) {
    await user.type(await screen.findByLabelText("Payload to sign"), text);
    await user.click(screen.getByRole("button", { name: "Sign" }));
  }

  // The envelope the server actually used to send, nesting the payload under
  // `data`. The generated types mark both members required, so the compiler
  // does not help — and unguarded this rendered the string "undefined
  // undefined" as a signature, which a person cannot tell from a real one.
  it("shows no signature at all when the payload is not the declared shape", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      ...listOnly,
      ...stored,
      "/ext/notes/signature": () => ({
        data: { algorithm: "HMAC-SHA256", signature: "abc123" },
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    await sign(userEvent.setup(), "sign me");
    expect(
      await screen.findByText("Nothing was signed. Store a signing key first."),
    ).toBeTruthy();
    expect(screen.queryByText(/undefined/)).toBeNull();
  });

  // The other half of the same defect: a signature left standing through a
  // LATER attempt that failed still reads as the answer for the payload on
  // screen. Cleared at the start of the mutation, not on its success.
  it("clears the signature when a later attempt fails", async () => {
    let firstCall = true;
    const { fetchStub } = stubTransport(FULL_GRANT, {
      ...listOnly,
      ...stored,
      "/ext/notes/signature": () => {
        if (firstCall) {
          firstCall = false;
          return { algorithm: "HMAC-SHA256", signature: "abc123" };
        }
        throw new Error("refused");
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    const user = userEvent.setup();
    await sign(user, "sign me");
    expect(await screen.findByText("HMAC-SHA256 abc123")).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Sign" }));
    await waitFor(() => {
      expect(screen.queryByText("HMAC-SHA256 abc123")).toBeNull();
    });
  });

  // The payload stays editable while a signing is in flight, so an answer can
  // arrive for text nobody is looking at any more.
  it("ignores an answer for a payload that has since changed", async () => {
    let release: (() => void) | undefined;
    const { fetchStub } = stubTransport(FULL_GRANT, {
      ...listOnly,
      ...stored,
      "/ext/notes/signature": () => ({
        algorithm: "HMAC-SHA256",
        signature: "abc123",
      }),
    });
    const gated = async (input: Request | string | URL) => {
      const answer = fetchStub(input);
      if (
        String(input instanceof Request ? input.url : input).includes(
          "signature",
        )
      ) {
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      }
      return answer;
    };
    vi.stubGlobal("fetch", vi.fn(gated));
    renderScreen();

    const user = userEvent.setup();
    await sign(user, "sign me");
    // In flight: the person moves on to a different payload.
    await user.type(screen.getByLabelText("Payload to sign"), " and again");
    release?.();

    // The answer has to have ARRIVED before its absence means anything: the
    // Sign button is disabled for the whole flight, so waiting for it to come
    // back is waiting for the mutation to settle. Without this the assertion
    // below passes while the request is still in the air, which is a test that
    // cannot fail.
    await waitFor(() => {
      expect(
        screen.getByRole("button", { name: "Sign" }).hasAttribute("disabled"),
      ).toBe(false);
    });
    expect(screen.queryByText("HMAC-SHA256 abc123")).toBeNull();
  });

  // A signature belongs to the payload it was computed over. Left standing, it
  // sits beside text it does not sign — which reads as verification of the new
  // payload.
  it("clears the signature when the payload changes", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      ...listOnly,
      ...stored,
      "/ext/notes/signature": () => ({
        algorithm: "HMAC-SHA256",
        signature: "abc123",
      }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    const user = userEvent.setup();
    await sign(user, "sign me");
    expect(await screen.findByText("HMAC-SHA256 abc123")).toBeTruthy();

    await user.type(screen.getByLabelText("Payload to sign"), " and again");
    expect(screen.queryByText("HMAC-SHA256 abc123")).toBeNull();
  });
});

describe("a refused removal", () => {
  // One flag over a list names no row, and it survives the next row's success.
  it("is reported on the row it was pressed for", async () => {
    const notes = [
      {
        id: "11111111-1111-4111-8111-111111111111",
        body: "first",
        created_at: "2026-08-13T09:30:00Z",
      },
      {
        id: "22222222-2222-4222-8222-222222222222",
        body: "second",
        created_at: "2026-08-13T09:31:00Z",
      },
    ];
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/notes/list": () => ({ notes }),
      "/ext/notes/signing-key/status": () => ({ stored: false }),
      "/ext/notes/remove": () => {
        throw new Error("refused");
      },
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));
    renderScreen();

    const user = userEvent.setup();
    await screen.findByText(/second/);
    const removes = screen.getAllByRole("button", { name: "Remove" });
    await user.click(removes[1]);

    const refusal = "The note was not removed. Nothing was changed.";
    await waitFor(() => {
      expect(screen.getAllByText(refusal)).toHaveLength(1);
    });
    // ON the row it was pressed for, which is the whole difference from one
    // message under the list.
    const rows = screen.getAllByRole("listitem");
    expect(rows[1].textContent).toContain(refusal);
    expect(rows[0].textContent).not.toContain(refusal);

    // And it does not outlive the next attempt: a message from the row before
    // last is the other half of what one flag over a list got wrong.
    await user.click(removes[0]);
    await waitFor(() => {
      expect(screen.getAllByRole("listitem")[1].textContent).not.toContain(
        refusal,
      );
    });
  });
});
