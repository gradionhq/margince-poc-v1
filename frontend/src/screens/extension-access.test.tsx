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
// Both endpoints are MOCKED rather than typed: /v1/roles and /v1/extensions are
// landing in parallel, so the fixtures below are this suite's copy of the agreed
// contract. When the endpoints reach crm.yaml these become generated types and
// the shapes here should be re-checked against them.

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
      routes: ["GET /ext/notes", "POST /ext/notes"],
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
      // No key at all for either object: an object a role was never granted is
      // absent from the map, and the matrix has to read that as a denial rather
      // than as an unrestricted grant.
      objects: {},
    },
  ],
};

type Call = { method: string; url: string; body?: unknown };

function backend(
  calls: Call[],
  opts: {
    roles?: string[];
    seat?: "full" | "read";
    extensions?: unknown;
    rolesBody?: unknown;
    rolesStatus?: number;
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
      return jsonResponse(opts.rolesBody ?? ROLES, opts.rolesStatus ?? 200);
    }
    let body: unknown;
    try {
      body = await req.clone().json();
    } catch {
      body = undefined;
    }
    calls.push({ method: req.method, url: req.url, body });
    // The PATCH answers with the WHOLE updated role, which is what the card
    // writes back into its cache — so the stub applies the write to the
    // fixture role rather than returning a canned body that would agree with
    // the assertion whatever was sent.
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
      objects: { ...role.objects, [object]: body },
    });
  });
}

const render = (ui: ReactNode) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
};

// The matrix for one object, found by the caption naming it — the only thing
// that distinguishes two tables of identically-labelled columns.
function matrixFor(object: string): HTMLElement {
  const table = screen
    .getByText(new RegExp(`Who may do what with ${object}$`))
    .closest("table");
  if (!(table instanceof HTMLElement)) {
    throw new Error(`no matrix rendered for ${object}`);
  }
  return table;
}

function cell(object: string, role: string, action: string) {
  const box = within(matrixFor(object)).getByRole("checkbox", {
    name: `Allow ${role} to ${action} ${object}`,
  });
  if (!(box instanceof HTMLInputElement)) {
    throw new Error(`the ${action} cell for ${role} is not a checkbox`);
  }
  return box;
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
    expect(screen.getByText("GET /ext/notes")).toBeTruthy();
    expect(screen.getByText("ext_notes_digest")).toBeTruthy();

    // The grants, read straight off the fixture: admin reads and creates the
    // note object and does neither of the other two verbs.
    expect(cell("ext_notes_note", "Admin", "Read").checked).toBe(true);
    expect(cell("ext_notes_note", "Admin", "Create").checked).toBe(true);
    expect(cell("ext_notes_note", "Admin", "Update").checked).toBe(false);
    // An object absent from a role's map denies — never an unticked box that
    // silently means "unknown".
    expect(cell("ext_notes_note", "Rep", "Read").checked).toBe(false);

    // A unit that registers nothing says so instead of rendering an empty grid.
    expect(screen.getByText(/registers no permission objects/i)).toBeTruthy();
  });

  it("PATCHes the whole grant for the toggled role and object", async () => {
    const calls: Call[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    await userEvent.click(cell("ext_notes_note", "Rep", "Read"));

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
    });

    // The server's answer repaints the row — no refetch, no locally invented
    // grant.
    await waitFor(() =>
      expect(cell("ext_notes_note", "Rep", "Read").checked).toBe(true),
    );
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
    await userEvent.click(cell("ext_notes_signing_key", "Rep", "Read"));
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

  it("disables every toggle for a read seat while still showing the grants", async () => {
    vi.stubGlobal("fetch", backend([], { seat: "read" }));
    render(<ExtensionAccessCard />);
    await waitFor(() => expect(screen.getByText("notes")).toBeTruthy());

    expect(screen.getByText(/needs a full seat/i)).toBeTruthy();
    expect(cell("ext_notes_note", "Admin", "Read").disabled).toBe(true);
    // The state is still legible — a read seat sees what is granted.
    expect(cell("ext_notes_note", "Admin", "Read").checked).toBe(true);
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

    const box = cell("ext_notes_note", "Rep", "Read");
    box.focus();
    expect(document.activeElement).toBe(box);
    await userEvent.keyboard(" ");

    await waitFor(() =>
      expect(calls.some((call) => call.method === "PATCH")).toBe(true),
    );
  });
});
