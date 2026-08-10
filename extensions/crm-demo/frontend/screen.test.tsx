/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "@margince/frontend/app";
import CrmDemoScreen from "./screen";

// The reference extension's screen, over a stubbed transport.
//
// It is a *.test.tsx, so it is outside the app and node TypeScript programs
// (both exclude tests) and vitest does not typecheck. It IS compiled, by
// tsconfig.composed-tests.json — the fourth project, added for exactly this
// file and run by `make fe-typecheck-composed` — so the fixtures below are
// held against the merged contract even though vitest never looks.
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
 * Two, not one: the secrets operations gate on `ext_crm_demo_signing_key`
 * separately from the notes, because a role that may add a note has no business
 * rotating the installation's credential. `update` is what stores or rotates the
 * key — there is one per workspace, so setting it is never a create.
 */
const FULL_GRANT = {
  seat_type: "full",
  objects: {
    ext_crm_demo_note: { read: true, create: true, delete: true },
    ext_crm_demo_signing_key: { read: true, update: true },
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
  const calls: { path: string; body: unknown }[] = [];
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
    const path = url.slice(url.indexOf("/v1/") + "/v1".length);
    const raw = input instanceof Request ? await input.text() : "";
    const body = raw === "" ? null : JSON.parse(raw);
    calls.push({ path, body });
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
        <CrmDemoScreen />
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
  it("lists the workspace's notes, heartbeat rows included", async () => {
    const { fetchStub } = stubTransport(FULL_GRANT, {
      "/ext/crm-demo/notes/list": () => ({
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
      "/ext/crm-demo/signing-key/status": () => ({ stored: false }),
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

  it("reports the signing key as absent, then present, without ever showing it", async () => {
    let stored = false;
    const { fetchStub, calls } = stubTransport(FULL_GRANT, {
      "/ext/crm-demo/notes/list": () => ({ notes: [] }),
      "/ext/crm-demo/signing-key/status": () => ({ stored }),
      "/ext/crm-demo/signing-key": () => {
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
    const sent = calls.find((c) => c.path === "/ext/crm-demo/signing-key");
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
      "/ext/crm-demo/notes/list": () => ({ notes: [] }),
      "/ext/crm-demo/signing-key/status": () => ({ stored: true }),
      "/ext/crm-demo/signature": () => ({
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
      "/ext/crm-demo/notes/list": () => ({ notes }),
      "/ext/crm-demo/signing-key/status": () => ({ stored: true }),
      "/ext/crm-demo/notes/add": () => {
        const added = {
          id,
          body: "a note",
          created_at: "2026-08-09T09:14:00Z",
        };
        notes = [added];
        return added;
      },
      "/ext/crm-demo/notes/remove": () => {
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
    expect(
      calls.find((c) => c.path === "/ext/crm-demo/notes/add")?.body,
    ).toEqual({ body: "a note" });

    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => {
      expect(
        calls.find((c) => c.path === "/ext/crm-demo/notes/remove")?.body,
      ).toEqual({ id });
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
          ext_crm_demo_note: { read: true },
          ext_crm_demo_signing_key: { read: true },
        },
      },
      {
        "/ext/crm-demo/notes/list": () => ({
          notes: [
            {
              id: "11111111-1111-4111-8111-111111111111",
              body: "visible to a reader",
              created_at: "2026-08-09T09:14:00Z",
            },
          ],
        }),
        "/ext/crm-demo/signing-key/status": () => ({ stored: true }),
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
  // Not a hypothetical: this is verbatim what `POST /v1/ext/crm-demo/…`
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
      "/ext/crm-demo/notes/list": () => envelope({ notes: [] }),
      "/ext/crm-demo/signing-key/status": () => envelope({ stored: true }),
    });
    vi.stubGlobal("fetch", vi.fn(fetchStub));

    renderScreen();
    // The notes read fails loudly. `notes` is undefined on a wrapped body, and
    // the query gate renders its error card rather than an empty list that
    // would read as "you have no notes".
    expect(await screen.findByText(/Couldn't load this view/)).toBeTruthy();
    // And the connection state does NOT claim to be connected off a body it
    // could not read — "No key stored" is wrong here too, but it is the
    // failing-closed direction.
    expect(screen.queryByText("Connected")).toBeNull();
  });

  it("says so when the principal cannot read the unit's notes at all", async () => {
    const { fetchStub, calls } = stubTransport(
      { seat_type: "full", objects: {} },
      { "/ext/crm-demo/signing-key/status": () => ({ stored: false }) },
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
    expect(calls.some((c) => c.path === "/ext/crm-demo/notes/list")).toBe(
      false,
    );
  });
});
