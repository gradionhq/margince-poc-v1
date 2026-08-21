/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { ExtensionAccessCard } from "./extension-access";

// The extension-access card renders the composed unit inventory and one
// role × CRUD matrix per registered object, and drives the grant seam. The
// server stays the RBAC authority — this suite asserts the wire calls and the
// states an operator reads, not the gate itself.
//
// The transport is stubbed at `fetch`, but the SHAPES below are now the ones
// crm.yaml defines — /v1/roles and /v1/extensions have landed, and the fixtures
// were re-checked against the generated types. Two fields spelled `version`
// mean different things and are typed differently, which is the trap this
// fixture set exists to hold still: `ComposedExtension.version` is the unit's
// display string ("0.3.1"), while `Role.version` is an int64 RowVersion — the
// one that rides out as `If-Match`, and therefore the one a test asserts as the
// STRING the header carries.

// The SPA's generated registry, stubbed at the alias rather than at
// app/extensions: the lookup under test IS findExtension, and the vanilla
// registry this suite compiles against is empty by construction, so the "unit
// has a page" case can only be reached by handing the registry the shape the
// generator emits. `notes` resolves, `quiet` never does — which is exactly the
// disagreement the screen has to survive, since /extensions answers with both.
vi.mock("@composition/extensions", () => ({
  extensions: [
    {
      name: "notes",
      verbs: [
        {
          operationId: "notesList",
          route: "/ext/notes",
          method: "GET",
          title: "List demo notes",
          version: "1.0.0",
          rbacObject: "ext_notes_note",
        },
      ],
    },
  ],
}));

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const EXTENSIONS = {
  extensions: [
    {
      name: "notes",
      version: "0.3.1",
      rbac_objects: ["ext_notes_note", "ext_notes_signing_key"],
      // One entry per OPERATION, sorted by path then method, exactly as the
      // server composes it: /ext/notes/{id} carries both a GET and a DELETE,
      // and the destructive verb must survive onto the screen rather than
      // being deduplicated away behind the read on the same path.
      routes: [
        { method: "GET", path: "/ext/notes" },
        { method: "POST", path: "/ext/notes" },
        { method: "DELETE", path: "/ext/notes/{id}" },
        { method: "GET", path: "/ext/notes/{id}" },
      ],
      jobs: ["ext_notes_digest"],
    },
    {
      name: "quiet",
      version: "1.0.0",
      rbac_objects: [],
      routes: [],
      jobs: [],
    },
  ],
};

// Admin reads the note object; nobody reads the signing key — the exact state
// that produces the confusing empty screen the card exists to explain.
const ROLES = {
  roles: [
    {
      key: "admin",
      name: "Admin",
      is_system: true,
      version: 1,
      objects: {
        ext_notes_note: {
          read: true,
          create: true,
          update: false,
          delete: false,
        },
      },
    },
    {
      key: "rep",
      name: "Rep",
      is_system: true,
      version: 1,
      // No key at all for either object: an object a role was never granted is
      // absent from the map, and the matrix has to read that as a denial rather
      // than as an unrestricted grant.
      objects: {},
    },
  ],
};

type Call = {
  method: string;
  url: string;
  body?: unknown;
  // Null rather than undefined when the header is absent, so a test can tell
  // "sent nothing" from "the stub forgot to record it".
  ifMatch: string | null;
};

function backend(
  calls: Call[],
  opts: {
    roles?: string[];
    seat?: "full" | "read";
    extensions?: unknown;
    // A function so a test can change what the second read answers — the
    // concurrent-edit case needs the re-read to bring back someone else's
    // change, not the body the screen already holds.
    rolesBody?: unknown | (() => unknown);
    rolesStatus?: number;
    patch?: { status: number; body: unknown };
  } = {},
) {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req =
      input instanceof Request ? input : new Request(String(input), init);
    if (req.url.endsWith("/v1/me")) {
      return jsonResponse(
        meFixture({
          roles: opts.roles ?? ["admin"],
          seat: opts.seat ?? "full",
        }),
      );
    }
    if (req.url.endsWith("/v1/extensions")) {
      return jsonResponse(opts.extensions ?? EXTENSIONS);
    }
    if (req.url.endsWith("/v1/roles") && req.method === "GET") {
      const body =
        typeof opts.rolesBody === "function"
          ? (opts.rolesBody as () => unknown)()
          : (opts.rolesBody ?? ROLES);
      return jsonResponse(body, opts.rolesStatus ?? 200);
    }
    let body: unknown;
    try {
      body = await req.clone().json();
    } catch {
      body = undefined;
    }
    calls.push({
      method: req.method,
      url: req.url,
      body,
      ifMatch: req.headers.get("If-Match"),
    });
    if (opts.patch) {
      return jsonResponse(opts.patch.body, opts.patch.status);
    }
    // The PATCH answers with the WHOLE updated role, which is what the card
    // writes back into its cache — so the stub applies the write to the
    // fixture role rather than returning a canned body that would agree with
    // the assertion whatever was sent. The version moves on, exactly as a
    // RowVersion does: the next write from this screen must carry the new one.
    // /v1/roles/{key}/objects/{object}
    const segments = new URL(req.url).pathname.split("/");
    const roleKey = segments[3];
    const object = segments[5];
    const role = ROLES.roles.find((candidate) => candidate.key === roleKey);
    if (!role) {
      throw new Error(`patch against an unknown role: ${req.url}`);
    }
    return jsonResponse({
      ...role,
      version: role.version + 1,
      objects: { ...role.objects, [object]: body },
    });
  });
}

const renderWith = (client: QueryClient, ui: ReactNode) =>
  rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );

const render = (ui: ReactNode) =>
  renderWith(
    new QueryClient({ defaultOptions: { queries: { retry: false } } }),
    ui,
  );

// The matrix for one object, found by its ACCESSIBLE NAME — the only thing that
// distinguishes two tables of identically-labelled columns. The name comes from
// the stacked `SettingRow`'s own label through `aria-labelledby`, so asking for
// it by role and name is also the assertion that the wiring holds: a grid whose
// row label came adrift would be a table announced as "read create update
// delete" with no subject, and this lookup would stop finding it.
function matrixFor(object: string): HTMLElement {
  return screen.getByRole("table", {
    name: `Who may do what with ${object}`,
  });
}

// One cell of the matrix. `role: "switch"` is load-bearing rather than
// incidental: flipping a cell WRITES the grant, and a control that announced
// itself as a checkbox would be telling a reader their next click is only an
// intent something later submits. There is no later here.
function cell(object: string, role: string, action: string) {
  const control = within(matrixFor(object)).getByRole("switch", {
    name: `Allow ${role} to ${action} ${object}`,
  });
  if (!(control instanceof HTMLButtonElement)) {
    throw new Error(`the ${action} cell for ${role} is not a switch`);
  }
  return {
    control,
    on: control.getAttribute("aria-checked") === "true",
    disabled: control.disabled,
    describedBy: control.getAttribute("aria-describedby"),
  };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("ExtensionAccessCard", () => {
  it("renders each composed unit, what it brings, and the matrix from the fetched grants", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<ExtensionAccessCard />);

    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());
    expect(screen.getByText("Version 0.3.1")).toBeTruthy();
    // What the unit brings, each family named rather than lumped together.
    expect(screen.getByText("ext_notes_note")).toBeTruthy();
    expect(screen.getByText("ext_notes_digest")).toBeTruthy();

    // The grants, read straight off the fixture: admin reads and creates the
    // note object and does neither of the other two verbs.
    expect(cell("ext_notes_note", "Admin", "Read").on).toBe(true);
    expect(cell("ext_notes_note", "Admin", "Create").on).toBe(true);
    expect(cell("ext_notes_note", "Admin", "Update").on).toBe(false);
    // An object absent from a role's map denies — never an unticked box that
    // silently means "unknown".
    expect(cell("ext_notes_note", "Rep", "Read").on).toBe(false);

    // A unit that registers nothing says so instead of rendering an empty grid.
    expect(screen.getByText(/registers no permission objects/i)).toBeTruthy();
  });

  it("gives every unit a card of its own, headed by the unit name", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    // The page's own heading stays on the lead card, which carries the seat and
    // inventory states and never a unit's grants.
    expect(
      screen.getByRole("heading", { name: /Extensions & access/i }),
    ).toBeTruthy();

    const unit = screen
      .getByRole("heading", { name: "notes" })
      .closest("section.panel");
    if (!(unit instanceof HTMLElement)) {
      throw new Error("the notes unit is not a card of its own");
    }
    // Version and page link ride the card's heading row rather than its body.
    expect(within(unit).getByText("Version 0.3.1")).toBeTruthy();
    expect(
      within(unit).getByRole("link", { name: "Open the notes page" }),
    ).toBeTruthy();
    // The grants are the card's rows; the inventory of what the unit brought is
    // its reference half, behind a disclosure that reads last and closed. So
    // the two are still told apart — by a row label and a summary rather than
    // by two section headings.
    expect(within(unit).getByText("What this unit brings")).toBeTruthy();
    // … and each registered object keeps a matrix of its own within the card,
    // NAMED by the row that holds it: the object is what a reader landing on a
    // tick in the middle of one has to be able to trace back to.
    expect(within(unit).getAllByRole("table").length).toBe(2);
    expect(
      within(unit).getByRole("table", {
        name: "Who may do what with ext_notes_note",
      }),
    ).toBeTruthy();
    expect(
      within(unit).getByRole("table", {
        name: "Who may do what with ext_notes_signing_key",
      }),
    ).toBeTruthy();
  });

  it("links to the page of a unit the SPA registry resolves, naming the unit in the link", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    // The accessible name carries the unit, not a bare "Open": several unit
    // blocks sit on this one page, so a link found by name alone has to be
    // unambiguous.
    const link = screen.getByRole("link", { name: "Open the notes page" });
    expect(link.getAttribute("href")).toBe("#/ext/notes");
  });

  it("says a unit is composed but unlinkable when this build's registry has never heard of it", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    // `quiet` comes back from /extensions — the binary composed it — and is
    // absent from the mocked SPA registry, the exact shape of a bundle older
    // than the server. No link is rendered, because #/ext/quiet would land on
    // the router's not-found card …
    expect(screen.queryByRole("link", { name: /quiet/ })).toBeNull();
    // … and the reason is SAID, so "this unit has no page" cannot be confused
    // with "the page is missing because the bundle is stale".
    const unit = screen.getByText("quiet").closest("section.panel");
    if (!(unit instanceof HTMLElement)) {
      throw new Error("no unit block rendered for quiet");
    }
    expect(
      within(unit).getByText(/quiet is composed into the API, but this build/),
    ).toBeTruthy();
    // And the resolvable unit says nothing of the kind.
    expect(screen.queryByText(/notes is composed into the API/)).toBeNull();
  });

  it("shows every route operation with its method, so a DELETE cannot hide behind a GET on the same path", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    // Scoped to the unit: every unit block carries its own Routes row.
    const unit = screen.getByText("notes").closest("section.panel");
    if (!(unit instanceof HTMLElement)) {
      throw new Error("no unit block rendered for notes");
    }
    const routes = within(unit)
      .getByText("Routes")
      .closest(".ext-brings-row")
      ?.querySelectorAll("li");
    expect([...(routes ?? [])].map((item) => item.textContent)).toEqual([
      "GET/ext/notes",
      "POST/ext/notes",
      "DELETE/ext/notes/{id}",
      "GET/ext/notes/{id}",
    ]);
    // The pair that shares a path is TWO chips, and the destructive one is
    // named — the whole reason the inventory stopped deduplicating by path.
    expect(screen.getAllByText("/ext/notes/{id}").length).toBe(2);
    expect(screen.getByText("DELETE")).toBeTruthy();
  });

  it("PATCHes the whole grant for the toggled role and object", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    await userEvent.click(cell("ext_notes_note", "Rep", "Read").control);

    await waitFor(() => {
      const patch = calls.find((call) => call.method === "PATCH");
      expect(patch).toBeTruthy();
      expect(patch?.url).toContain("/v1/roles/rep/objects/ext_notes_note");
      // The whole grant rides the body, not a delta: the request states the
      // grant the operator is looking at.
      expect(patch?.body).toEqual({
        read: true,
        create: false,
        update: false,
        delete: false,
      });
      // And the version of the role the tick was READ from, so a write
      // computed against a matrix someone else has since changed is refused
      // rather than applied over them. Optional in the contract, always sent
      // here.
      expect(patch?.ifMatch).toBe("1");
    });

    // The server's answer repaints the row — no refetch, no locally invented
    // grant.
    await waitFor(() =>
      expect(cell("ext_notes_note", "Rep", "Read").on).toBe(true),
    );
  });

  // One `useSetGrant` serves the whole matrix, so an in-flight flag read
  // straight off it belongs to no particular cell. Drawn that way, every switch
  // for every role turns at once and announces `aria-busy` — each one claiming
  // a write its reader never made, and refusing a press over it.
  it("marks only the role whose grant is being written, not the whole matrix", async () => {
    const user = userEvent.setup();
    const calls: Call[] = [];
    // The PATCH never settles, so the in-flight state is a state to look at
    // rather than a window this test has to race.
    const reads = backend(calls);
    vi.stubGlobal("fetch", (req: Request) =>
      req.method === "PATCH" ? new Promise<Response>(() => {}) : reads(req),
    );
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    await user.click(cell("ext_notes_note", "Rep", "Read").control);

    const busy = (role: string, action: string) =>
      cell("ext_notes_note", role, action).control.getAttribute("aria-busy");
    await waitFor(() => expect(busy("Rep", "Read")).toBe("true"));
    // The whole ROLE row is honest — the write carries that role's entire
    // grant record, so every action in it really is in flight.
    expect(busy("Rep", "Create")).toBe("true");
    // Another role is not, and this is the assertion the defect fails: nothing
    // about Admin's grant ever left the browser.
    expect(busy("Admin", "Read")).toBeNull();
  });

  it("re-reads and says who changed it when a concurrent edit refuses the write", async () => {
    // Someone else granted Rep read on the note object between this screen's
    // read and this write, so the PATCH's If-Match no longer matches: the
    // server refuses with version_skew and the tick did NOT apply.
    const calls: Call[] = [];
    let skewed = false;
    const ROLES_AFTER = {
      roles: ROLES.roles.map((role) =>
        role.key === "rep"
          ? {
              ...role,
              version: 9,
              objects: {
                ext_notes_note: {
                  read: true,
                  create: false,
                  update: false,
                  delete: false,
                },
              },
            }
          : role,
      ),
    };
    vi.stubGlobal(
      "fetch",
      backend(calls, {
        rolesBody: () => (skewed ? ROLES_AFTER : ROLES),
        patch: {
          status: 409,
          body: {
            code: "version_skew",
            title: "Conflict",
            detail: "the role changed since it was read",
          },
        },
      }),
    );
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    skewed = true;
    await userEvent.click(cell("ext_notes_note", "Rep", "Delete").control);

    // The message names the concurrent change rather than reading as a generic
    // save failure — the point is that the operator's change did not happen.
    await waitFor(() =>
      expect(screen.getByText(/Someone else changed this role/)).toBeTruthy(),
    );
    expect(screen.queryByText(/Couldn't load this view/)).toBeNull();

    // The matrix was re-read, so it now shows the OTHER admin's grant …
    await waitFor(() =>
      expect(cell("ext_notes_note", "Rep", "Read").on).toBe(true),
    );
    // … and never the tick this operator made, which the server refused.
    expect(cell("ext_notes_note", "Rep", "Delete").on).toBe(false);

    // Exactly one write: a silent replay against the fresh version would apply
    // an intent formed against grants the operator had not seen.
    expect(calls.filter((call) => call.method === "PATCH").length).toBe(1);
  });

  it("says plainly when no role holds read on an object, and stops saying it once one does", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    // The signing key: granted to nobody, which is exactly the state that
    // renders the extension's own screens empty.
    expect(
      screen.getByText(/No role holds read on ext_notes_signing_key/),
    ).toBeTruthy();
    // The note object has a reader, so it carries no such warning.
    expect(
      screen.queryByText(/No role holds read on ext_notes_note/),
    ).toBeNull();

    // Granting read to a role clears the warning for that object.
    await userEvent.click(cell("ext_notes_signing_key", "Rep", "Read").control);
    await waitFor(() =>
      expect(
        screen.queryByText(/No role holds read on ext_notes_signing_key/),
      ).toBeNull(),
    );
  });

  it("shows an admin-only notice and fetches nothing for a non-admin", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ roles: ["rep"] }));
        }
        // A non-admin must never reach the roster of roles — any other request
        // is a regression, so fail loudly rather than serve fixture data.
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      }),
    );
    render(<ExtensionAccessCard />);

    await waitFor(() => expect(screen.getByText(/admins only/i)).toBeTruthy());
    expect(screen.queryByText("notes")).toBeNull();
  });

  // `enabled: false` stops the next request; it does not forget the last one. A
  // seat that held admin earlier in the session has the whole inventory and every
  // role's grants sitting in the cache, and rendering a unit card from that cache
  // after the role is gone discloses exactly what the notice above it refuses.
  it("renders nothing from a cache the reader may no longer read", async () => {
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    // What an admin visit leaves behind, seeded through the keys the screen reads.
    client.setQueryData(
      ["extension-access", "extensions"],
      EXTENSIONS.extensions,
    );
    client.setQueryData(["extension-access", "roles"], ROLES.roles);
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ roles: ["rep"] }));
        }
        throw new Error(`unexpected request: ${req.method} ${req.url}`);
      }),
    );
    renderWith(client, <ExtensionAccessCard />);

    await waitFor(() => expect(screen.getByText(/admins only/i)).toBeTruthy());
    expect(screen.queryByRole("heading", { name: "notes" })).toBeNull();
    expect(screen.queryByText("ext_notes_note")).toBeNull();
    // And the cache itself is emptied, so a later render cannot resurrect it.
    expect(
      client.getQueryData(["extension-access", "extensions"]),
    ).toBeUndefined();
    expect(client.getQueryData(["extension-access", "roles"])).toBeUndefined();
  });

  it("disables every toggle for a read seat while still showing the grants", async () => {
    vi.stubGlobal("fetch", backend([], { seat: "read" }));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    // Once for the eye, in the block that holds the matrices — not on the card
    // above them, where a reader looking at dead toggles had to leave the card
    // to find out why.
    expect(
      screen.getAllByText(
        "Your seat reads this page. Changing a grant needs a full seat.",
      )[0],
    ).toBeTruthy();
    const denied = cell("ext_notes_note", "Admin", "Read");
    expect(denied.disabled).toBe(true);
    // The state is still legible — a read seat sees what is granted.
    expect(denied.on).toBe(true);
    // And the refusal reaches a reader who lands on ONE cell: the sentence in
    // the block above is for the eye, this is what a screen reader is handed
    // with the control itself.
    const reasonId = denied.describedBy;
    expect(reasonId).toBeTruthy();
    expect(document.getElementById(reasonId ?? "")?.textContent).toMatch(
      /needs a full seat/i,
    );
  });

  it("renders the loading state before either read answers", async () => {
    // Both reads hang: the card must show the shared skeleton, not an empty
    // inventory that reads as "no extensions installed".
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse(meFixture({ roles: ["admin"] }));
        }
        return new Promise<Response>(() => {});
      }),
    );
    const { container } = render(<ExtensionAccessCard />);

    await waitFor(() =>
      expect(container.querySelectorAll(".skeleton").length).toBeGreaterThan(0),
    );
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByText(/No extension units/i)).toBeNull();
  });

  it("reports a failed read with the server's own cause and a retry", async () => {
    vi.stubGlobal(
      "fetch",
      backend([], {
        rolesBody: {
          title: "Forbidden",
          detail: "role administration is admin-only",
        },
        rolesStatus: 403,
      }),
    );
    render(<ExtensionAccessCard />);

    await waitFor(() =>
      expect(
        screen.getByText("role administration is admin-only"),
      ).toBeTruthy(),
    );
    expect(screen.getByRole("button", { name: /retry/i })).toBeTruthy();
  });

  it("shows the empty state when nothing is composed in", async () => {
    vi.stubGlobal("fetch", backend([], { extensions: { extensions: [] } }));
    render(<ExtensionAccessCard />);

    await waitFor(() =>
      expect(screen.getByText(/No extension units are composed/i)).toBeTruthy(),
    );
  });

  it("labels every cell by role, verb and object, and associates it with both headers", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    const table = matrixFor("ext_notes_note");
    // The column headers are real scoped headers, so a cell can be traced to a
    // verb by assistive tech rather than by position.
    const columns = within(table)
      .getAllByRole("columnheader")
      .map((header) => header.textContent);
    expect(columns).toEqual(["Role", "Read", "Create", "Update", "Delete"]);
    expect(
      within(table)
        .getAllByRole("rowheader")
        .map((header) => header.textContent?.replace("Built-in role", "")),
    ).toEqual(["Admin", "Rep"]);
    for (const header of within(table).getAllByRole("columnheader")) {
      expect(header.getAttribute("scope")).toBe("col");
    }
    for (const header of within(table).getAllByRole("rowheader")) {
      expect(header.getAttribute("scope")).toBe("row");
    }

    // And each tick still names itself in full — the name a user hears when
    // they tab straight onto it, with no surrounding context read out.
    expect(cell("ext_notes_note", "Rep", "Delete")).toBeTruthy();
  });

  it("is operable from the keyboard alone", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    const { control } = cell("ext_notes_note", "Rep", "Read");
    control.focus();
    expect(document.activeElement).toBe(control);
    await userEvent.keyboard(" ");

    await waitFor(() =>
      expect(calls.some((call) => call.method === "PATCH")).toBe(true),
    );
  });
});
