/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../../i18n";
import { CrmDemoScreen } from "./crmdemo";

// The reference extension's screen, over a stubbed transport.
//
// It is a *.test.tsx, so it is outside BOTH TypeScript programs (each lane
// excludes tests) and vitest does not typecheck. That is what lets this suite
// run in the vanilla lane at all, where the extension's routes are not in
// `paths` — the same reason App.composed.test.tsx mocks the registry rather
// than reading build/composition/. The screen's own types are held by
// `make fe-typecheck-composed`, which compiles it against the merged contract.

/** The grants a full seat holds on the unit's object. */
const FULL_GRANT = {
  seat_type: "full",
  objects: {
    ext_crm_demo_note: { read: true, create: true, delete: true },
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
    const { fetchStub } = stubTransport(
      {
        seat_type: "read",
        objects: { ext_crm_demo_note: { read: true } },
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
  });

  it("says so when the principal cannot read the unit's notes at all", async () => {
    const { fetchStub } = stubTransport(
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
  });
});
