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
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { UsersAdminCard } from "./users-admin";

// The admin member-management card renders the include-inactive roster and drives
// the invite / role / deactivate / reactivate seams; the server stays the RBAC
// authority (this suite asserts the wire calls, not the gate).

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

// `roles` rides the roster only for an admin caller, which this card always is.
// Nora holds none — an unassigned seat is reachable and has no current role to
// show.
const ROSTER = {
  data: [
    {
      id: "u-active",
      email: "ada@acme.test",
      display_name: "Ada Active",
      status: "active",
      is_agent: false,
      roles: ["admin"],
    },
    {
      id: "u-off",
      email: "otto@acme.test",
      display_name: "Otto Off",
      status: "deactivated",
      is_agent: false,
      roles: ["read_only"],
    },
    {
      id: "u-none",
      email: "nora@acme.test",
      display_name: "Nora None",
      status: "active",
      is_agent: false,
      roles: [],
    },
    // The workspace's agent identity, which bootstrap writes into every
    // installation — so every case in this suite renders the roster a real
    // admin sees, rather than a people-only one that no longer exists.
    {
      id: "u-agent",
      email: "agent@acme.gradion.local",
      display_name: "Gradion Agent",
      status: "active",
      is_agent: true,
      roles: [],
    },
  ],
  page: { next_cursor: null, has_more: false },
};

// Both helpers narrow by instance rather than asserting: a cast would let the
// suite read `.disabled` off whatever the query happened to return, so a control
// that stopped being the Select trigger would surface as a confusing undefined
// instead of a named failure.
function roleSelect(row: HTMLElement, name: string) {
  const control = within(row).getByRole("combobox", {
    name: new RegExp(`set role for ${name}`, "i"),
  });
  if (!(control instanceof HTMLButtonElement)) {
    throw new Error(`the role control for ${name} is not a select trigger`);
  }
  return control;
}

// What the closed control reads. The trigger's only text is its face — the
// chevron is aria-hidden and carries none — so this is the role an operator
// sees on the row without opening anything.
function roleShown(row: HTMLElement, name: string): string {
  return roleSelect(row, name).textContent ?? "";
}

// A member is one SettingRow inside a wrapper carrying the row's refusal and
// the focus target its deactivate confirm hands back to. The wrapper's testid is
// what identifies it: SettingRow is a <div>, so there is no <li> to climb to.
function rowFor(name: string) {
  const row = screen.getByText(name).closest('[data-testid^="member-"]');
  if (!(row instanceof HTMLElement)) {
    throw new Error(`no member row rendered for ${name}`);
  }
  return row;
}

// The row's verbs — the set-password link and the status change — live behind
// its OverflowMenu, and the menu's panel is portalled to the body: a query
// scoped to the row cannot see them, and the children are not even rendered
// until the menu is first opened. So open it and scope to the panel the trigger
// names.
async function rowMenu(user: ReturnType<typeof userEvent.setup>, name: string) {
  const trigger = within(rowFor(name)).getByRole("button", {
    name: new RegExp(`actions for ${name}`, "i"),
  });
  // The trigger TOGGLES, so a helper that always clicks would shut a menu a
  // previous step left open — and the assertion after it would then be about a
  // panel with `hidden` on it rather than about what the row offers.
  if (trigger.getAttribute("aria-expanded") !== "true") {
    await user.click(trigger);
  }
  const panelId = trigger.getAttribute("aria-controls");
  const panel = panelId === null ? null : document.getElementById(panelId);
  if (!(panel instanceof HTMLElement)) {
    throw new Error(`no actions menu rendered for ${name}`);
  }
  return within(panel);
}

// `roles` defaults to the admin this suite is mostly about; a caller naming
// another role gets the same routed backend, so a non-admin case differs from an
// admin one by the principal alone and not by a second hand-rolled stub.
function backend(
  calls: { method: string; url: string; body?: unknown }[],
  me: { roles: string[] } = { roles: ["admin"] },
) {
  // openapi-fetch calls fetch(request) with a Request object, so read the
  // method + body off it rather than a separate init.
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req =
      input instanceof Request ? input : new Request(String(input), init);
    if (req.url.endsWith("/v1/me")) {
      return jsonResponse({
        user: { email: "admin@acme.test" },
        roles: me.roles,
        teams: [],
        // The installation CAN mint set-password links. Without this the card
        // withholds that action from every row, and any assertion that one
        // particular row lacks it passes without the row having anything to do
        // with it.
        admin_password_link: true,
      });
    }
    if (req.url.includes("/teams") && req.method === "GET") {
      return jsonResponse({ data: [], page: { has_more: false } });
    }
    // The access preview is the server's own sentence about the role; this
    // suite is about the invite and the roster, so it answers a neutral rep.
    if (req.url.includes("/users/access-preview")) {
      return jsonResponse({
        role: "rep",
        row_scope: "team",
        objects: {},
        field_masks: [],
        teams: [],
      });
    }
    if (req.url.includes("/users") && req.method === "GET") {
      return jsonResponse(ROSTER);
    }
    let body: unknown;
    try {
      body = await req.clone().json();
    } catch {
      body = undefined;
    }
    calls.push({ method: req.method, url: req.url, body });
    // The link mint answers its own shape. With admin_password_link on, the
    // invite flow opens the link dialog itself, and a generic user row handed
    // to it renders an expiry from `undefined` — an unhandled error rather
    // than a failed assertion, which fails the run without naming a test.
    if (req.url.includes("/password-link")) {
      return jsonResponse(
        {
          set_password_url: "https://crm.example.test/set-password?t=fixture",
          expires_at: "2026-08-18T09:00:00Z",
        },
        201,
      );
    }
    return jsonResponse({ ...ROSTER.data[0], id: "u-new" }, 201);
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

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("UsersAdminCard", () => {
  it("gives a non-admin the roster and withholds only the controls that change it", async () => {
    vi.stubGlobal("fetch", backend([], { roles: ["rep"] }));
    render(<UsersAdminCard />);

    // The roster itself is NOT admin surface: `GET /users` answers 200 to any
    // authenticated principal, and who is on the team is not an admin's private
    // question. So a rep reads the list.
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());
    expect(screen.getByRole("heading", { name: /^Members$/ })).toBeTruthy();

    // A role they cannot change is a FACT, so it reads as text rather than as a
    // picker that could only be refused, and no row offers a verb at all — so
    // no row offers the menu those verbs live behind either.
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.getByText("Read-only")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /actions for/i })).toBeNull();

    // Inviting IS the admin's, and the card SAYS it is withheld rather than
    // simply dropping the verb: the page opens for every seat, so a roster with
    // no explanation reads as "this installation cannot add people".
    expect(
      screen.queryByRole("button", { name: /invite a member/i }),
    ).toBeNull();
    expect(screen.getByText(/admins only/i)).toBeTruthy();
    expect(screen.queryByLabelText(/new member's email/i)).toBeNull();
  });

  it("carries the roster count and the invite verb in one card's header", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    // ONE card, because there is one subject. Inviting used to be a second card
    // whose title, whose only row's label and whose button all read "Invite a
    // member" — three copies of the same three words above the list a reader
    // came for.
    expect(screen.getAllByRole("heading", { name: /^Members$/ })).toHaveLength(
      1,
    );
    const members = screen
      .getByRole("heading", { name: /^Members$/ })
      .closest("section");
    if (!(members instanceof HTMLElement)) {
      throw new Error("the roster is not a card of its own");
    }
    // The count states what the roster holds — the deactivated member and the
    // workspace's own agent seat included, because the read opts into both and a
    // count that skipped either would disagree with the rows beneath it. The
    // verb that adds to it stands beside the count, on the title's own line.
    const header = members.querySelector(".panel-head");
    if (!(header instanceof HTMLElement)) {
      throw new Error("the members card has no header band");
    }
    expect(within(header).getByText("4 members")).toBeTruthy();
    expect(
      within(header).getByRole("button", { name: /invite a member/i }),
    ).toBeTruthy();
    // And the invite fields are not on the page until the dialog carries them.
    expect(
      within(members).queryByPlaceholderText("name@company.com"),
    ).toBeNull();
  });

  it("renders the include-inactive roster with per-status actions", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);

    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());
    expect(screen.getByText("Otto Off")).toBeTruthy();
    // The roster request opts into the inactive members.
    // (asserted indirectly: the deactivated member is present at all.)
    expect(
      (await rowMenu(user, "Ada Active")).getByText("Deactivate"),
    ).toBeTruthy();
    expect(
      (await rowMenu(user, "Otto Off")).getByText("Reactivate"),
    ).toBeTruthy();
  });

  // A row is ONE line: the member's name over their address on the left, and on
  // the right the role, the status and the menu holding the verbs. Nine members
  // used to be nine 140px blocks — a full-width Select on its own line and two
  // ghost buttons stacked under it — which read as nine cards rather than one
  // list.
  it("keeps a member's role, status and verbs in the row's one control column", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const row = rowFor("Ada Active");
    expect(within(row).getByText("ada@acme.test")).toBeTruthy();
    const control = row.querySelector(".settingrow-control");
    if (!(control instanceof HTMLElement)) {
      throw new Error("the member row has no control column");
    }
    expect(
      within(control).getByRole("combobox", { name: /set role for ada/i }),
    ).toBeTruthy();
    expect(within(control).getByText("Active")).toBeTruthy();
    expect(
      within(control).getByRole("button", { name: /actions for ada active/i }),
    ).toBeTruthy();
    // The verbs are BEHIND the menu, so the closed row draws neither of them.
    expect(within(row).queryByText("Deactivate")).toBeNull();
    expect(within(row).queryByText(/set-password link/i)).toBeNull();
  });

  // The agent seat is listed — a client resolving the owner of a record it owns
  // has to find it — but it is not a colleague, and the row has to say so. Each
  // absence below is a control the server refuses anyway, so offering it could
  // only produce a 409 an admin cannot act on.
  it("marks the agent seat and offers it no control meant for a person", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Gradion Agent")).toBeTruthy());

    const agent = rowFor("Gradion Agent");
    expect(within(agent).getByText("Agent")).toBeTruthy();
    // No role control at all, not a disabled one: the seat's authority comes
    // from a passport and the person it names, never from a role of its own. The
    // line stands where the picker would be, as the row's ANSWER.
    expect(
      within(agent).queryByRole("combobox", { name: /set role for/i }),
    ).toBeNull();
    expect(within(agent).getByText(/acts under a passport/i)).toBeTruthy();
    // No set-password link: the seat holds no password by construction, which
    // is what makes it a thing that signs in nowhere. Its menu still opens —
    // deactivating the seat is a posture an operator is entitled to take.
    const agentVerbs = await rowMenu(user, "Gradion Agent");
    expect(agentVerbs.queryByText(/set-password link/i)).toBeNull();
    expect(agentVerbs.getByText("Deactivate")).toBeTruthy();

    // A person's row is untouched by any of that — and the link's absence above
    // has to be about the AGENT rather than about an installation that mints no
    // links at all, so the same verb is asserted PRESENT here.
    const person = rowFor("Nora None");
    expect(roleSelect(person, "Nora None")).toBeTruthy();
    expect(within(person).queryByText("Agent")).toBeNull();
    expect(
      (await rowMenu(user, "Nora None")).getByText(/set-password link/i),
    ).toBeTruthy();
  });

  // Deactivating the seat stays offered — an operator is entitled to that — but
  // what it stops is invisible from this screen, and the body written for a
  // person describes sessions and sign-ins that the seat has none of.
  it("warns what stops before deactivating the agent seat", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Gradion Agent")).toBeTruthy());

    await user.click(
      (await rowMenu(user, "Gradion Agent")).getByRole("button", {
        name: /deactivate/i,
      }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/every job that runs with nobody/i),
    ).toBeTruthy();
    expect(within(dialog).queryByText(/signed out everywhere/i)).toBeNull();
  });

  // The invite form is a dialog the row's verb opens — three inputs, a team
  // fieldset and an access preview are not an answer that fits in a row's right
  // column. So every invite case opens it first, and the row's verb names the
  // whole act ("Invite a member") while the dialog's submit carries the bare
  // one ("Invite").
  const openInvite = async () => {
    await userEvent.click(
      screen.getByRole("button", { name: /invite a member/i }),
    );
    return screen.findByRole("dialog");
  };

  it("invites nobody until the dialog behind the header verb is open", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    expect(screen.queryByLabelText("New member's email")).toBeNull();
    const dialog = await openInvite();
    // The dialog's own submit reads the plain form of the verb, so the two are
    // tellable apart — for a reader and for `getByRole`.
    expect(
      within(dialog).getByRole("button", { name: /^invite$/i }),
    ).toBeTruthy();
    expect(within(dialog).getByLabelText(/new member's email/i)).toBeTruthy();
    expect(
      within(dialog).getByLabelText(/new member's full name/i),
    ).toBeTruthy();
    expect(
      within(dialog).getByRole("combobox", {
        name: /role for the new member/i,
      }),
    ).toBeTruthy();
  });

  it("invites a member with the entered email, name, and role", async () => {
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const dialog = await openInvite();
    await userEvent.type(
      within(dialog).getByPlaceholderText("name@company.com"),
      "new@acme.test",
    );
    await userEvent.type(
      within(dialog).getByPlaceholderText("Full name"),
      "New Person",
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: /^invite$/i }),
    );

    await waitFor(() => {
      const post = calls.find(
        (c) => c.method === "POST" && c.url.endsWith("/users"),
      );
      expect(post).toBeTruthy();
      expect(post?.body).toEqual({
        email: "new@acme.test",
        display_name: "New Person",
        role: "rep",
        team_ids: [],
      });
    });
  });

  it("deactivates an active member through the deactivate seam", async () => {
    const user = userEvent.setup();
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    await user.click(
      (await rowMenu(user, "Ada Active")).getByText("Deactivate"),
    );
    // Deactivation is destructive (revokes sessions/passports): confirm first.
    const dialog = await screen.findByRole("dialog");
    await user.click(
      within(dialog).getByRole("button", { name: /deactivate/i }),
    );

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" && c.url.includes("/users/u-active/deactivate"),
        ),
      ).toBe(true),
    );
  });

  // The confirm's own trigger does not survive the action it confirms: a
  // deactivated row offers Reactivate in its place. Handing focus back to the
  // removed button is a silent no-op that leaves focus on <body>, from where the
  // operator's next Tab restarts at the top of the page.
  it("returns focus to the member's row after deactivating, never to the document", async () => {
    const user = userEvent.setup();
    let deactivated = false;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/teams") && req.method === "GET") {
          return jsonResponse({ data: [], page: { has_more: false } });
        }
        if (req.url.includes("/users/access-preview")) {
          return jsonResponse({
            role: "rep",
            row_scope: "team",
            objects: {},
            field_masks: [],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          // The roster the server would really answer with once the seat is off,
          // which is what removes the Deactivate button. A stub that kept
          // reporting the member as active would leave the opener in place and
          // prove nothing about the case.
          return jsonResponse({
            ...ROSTER,
            data: ROSTER.data.map((member) =>
              member.id === "u-active" && deactivated
                ? { ...member, status: "deactivated" }
                : member,
            ),
          });
        }
        deactivated = true;
        return jsonResponse({});
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const row = rowFor("Ada Active");
    const verbs = await rowMenu(user, "Ada Active");
    await user.click(verbs.getByText("Deactivate"));
    await user.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: /deactivate/i,
      }),
    );

    // The menu the verb was pressed in stays open — a dialog restores focus to
    // whatever opened it, so hiding that item first would send focus, on close,
    // to a node that is gone. What it now offers is the opposite verb.
    await waitFor(() => expect(verbs.getByText("Reactivate")).toBeTruthy());
    expect(document.activeElement).toBe(row);
    expect(document.activeElement).not.toBe(document.body);
  });

  it("reads back each member's current role", async () => {
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    expect(roleShown(rowFor("Ada Active"), "ada active")).toBe("Admin");
    expect(roleShown(rowFor("Otto Off"), "otto off")).toBe("Read-only");
  });

  it("offers the placeholder to a member holding no role", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", backend([]));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Nora None")).toBeTruthy());

    expect(roleShown(rowFor("Nora None"), "nora none")).toBe("Set role…");
    // It is a face and never an entry: picking "Set role…" back would set no
    // role, so the list this row opens is the five assignable roles and nothing
    // else. The options exist only while the popup is open, hence the click.
    await user.click(roleSelect(rowFor("Nora None"), "nora none"));
    const offered = within(screen.getByRole("listbox"))
      .getAllByRole("option")
      .map((option) => option.textContent);
    expect(offered).toEqual([
      "Admin",
      "Management",
      "Team Lead",
      "Member",
      "Read-only",
      "Ops",
    ]);
  });

  // Any choice replaces the whole set, so a member holding several roles must
  // not read as a blank "Set role…" — that would let an admin strip privileges
  // they were never shown.
  it("names the roles a multi-role member holds", async () => {
    const twoRoles = {
      ...ROSTER,
      data: ROSTER.data.map((u) =>
        u.id === "u-none" ? { ...u, roles: ["manager", "ops"] } : u,
      ),
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/teams") && req.method === "GET") {
          return jsonResponse({ data: [], page: { has_more: false } });
        }
        if (req.url.includes("/users/access-preview")) {
          return jsonResponse({
            role: "rep",
            row_scope: "team",
            objects: {},
            field_masks: [],
            teams: [],
          });
        }
        return jsonResponse(twoRoles);
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Nora None")).toBeTruthy());

    // Both held roles are named, under their display labels, and the copy says
    // what picking one does.
    const shown = roleShown(rowFor("Nora None"), "nora none");
    expect(shown).toMatch(/holds/i);
    expect(shown).toContain("Team Lead");
    expect(shown).toContain("Ops");
    expect(shown).toMatch(/replaces them all/i);
  });

  it("sets a member's role through the role seam", async () => {
    const user = userEvent.setup();
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = rowFor("Ada Active");
    await pickOption(user, roleSelect(active, "ada active"), "Team Lead");

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "PATCH" &&
            c.url.includes("/users/u-active/role") &&
            (c.body as { role?: string })?.role === "manager",
        ),
      ).toBe(true),
    );
  });

  it("reactivates a deactivated member", async () => {
    const user = userEvent.setup();
    const calls: { method: string; url: string; body?: unknown }[] = [];
    vi.stubGlobal("fetch", backend(calls));
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Otto Off")).toBeTruthy());

    await user.click((await rowMenu(user, "Otto Off")).getByText("Reactivate"));

    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" && c.url.includes("/users/u-off/reactivate"),
        ),
      ).toBe(true),
    );
  });

  it("surfaces a failed member action as an inline alert on the row", async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/teams") && req.method === "GET") {
          return jsonResponse({ data: [], page: { has_more: false } });
        }
        if (req.url.includes("/users/access-preview")) {
          return jsonResponse({
            role: "rep",
            row_scope: "team",
            objects: {},
            field_masks: [],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          return jsonResponse(ROSTER);
        }
        return jsonResponse(
          { title: "Conflict", detail: "That would leave no admin." },
          409,
        );
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = rowFor("Ada Active");
    await pickOption(user, roleSelect(active, "ada active"), "Member");

    await waitFor(() => expect(within(active).getByRole("alert")).toBeTruthy());
    expect(screen.getByText(/leave no admin/i)).toBeTruthy();
    // The refused change left the role untouched, so the select must read the
    // role the member still holds — anything else would claim a change the
    // server rejected.
    expect(roleShown(active, "ada active")).toBe("Admin");
  });

  // The whole reason the select returns to the held role after a refusal: a
  // select left showing the refused target would make re-picking it a no-op,
  // and the operator's retry would silently never reach the server.
  it("lets the same role be re-picked after a refusal", async () => {
    const user = userEvent.setup();
    const patches: string[] = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/teams") && req.method === "GET") {
          return jsonResponse({ data: [], page: { has_more: false } });
        }
        if (req.url.includes("/users/access-preview")) {
          return jsonResponse({
            role: "rep",
            row_scope: "team",
            objects: {},
            field_masks: [],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          return jsonResponse(ROSTER);
        }
        patches.push(req.url);
        return jsonResponse({ title: "Conflict", detail: "Try again." }, 409);
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = rowFor("Ada Active");
    await pickOption(user, roleSelect(active, "ada active"), "Member");
    await waitFor(() => expect(patches).toHaveLength(1));

    // The SAME target again — the retry the operator would make.
    await pickOption(user, roleSelect(active, "ada active"), "Member");
    await waitFor(() => expect(patches).toHaveLength(2));
  });

  // A settled mutation whose roster refetch is still outstanding would render
  // the member's replaced role from the stale cache — the operator would watch
  // their change appear and then undo itself.
  it("stays pending until the refreshed roster lands", async () => {
    const user = userEvent.setup();
    let rosterReads = 0;
    // Built up front rather than captured lazily: the refetch has to be held
    // open from the moment it starts, and a deferred that only exists once the
    // request arrives would race the assertions below.
    let releaseRoster = () => {};
    const rosterHeld = new Promise<void>((resolve) => {
      releaseRoster = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/teams") && req.method === "GET") {
          return jsonResponse({ data: [], page: { has_more: false } });
        }
        if (req.url.includes("/users/access-preview")) {
          return jsonResponse({
            role: "rep",
            row_scope: "team",
            objects: {},
            field_masks: [],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          rosterReads += 1;
          if (rosterReads === 1) {
            return jsonResponse(ROSTER);
          }
          // Hold the refetch open so the window between "mutation done" and
          // "new roster in hand" is observable rather than a race.
          await rosterHeld;
          return jsonResponse({
            ...ROSTER,
            data: ROSTER.data.map((u) =>
              u.id === "u-active" ? { ...u, roles: ["manager"] } : u,
            ),
          });
        }
        return jsonResponse({}, 200);
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const active = rowFor("Ada Active");
    await pickOption(user, roleSelect(active, "ada active"), "Team Lead");
    await waitFor(() => expect(rosterReads).toBe(2));

    // Mid-flight: the row reads the role being applied and stays locked. "Admin"
    // here would be the stale cache showing through.
    expect(roleShown(active, "ada active")).toBe("Team Lead");
    expect(roleSelect(active, "ada active").disabled).toBe(true);

    releaseRoster();
    await waitFor(() =>
      expect(roleSelect(rowFor("Ada Active"), "ada active").disabled).toBe(
        false,
      ),
    );
    expect(roleShown(rowFor("Ada Active"), "ada active")).toBe("Team Lead");
  });

  it("surfaces a failed invite as an inline error", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
        const req =
          input instanceof Request ? input : new Request(String(input), init);
        if (req.url.endsWith("/v1/me")) {
          return jsonResponse({
            user: { email: "admin@acme.test" },
            roles: ["admin"],
            teams: [],
          });
        }
        if (req.url.includes("/teams") && req.method === "GET") {
          return jsonResponse({ data: [], page: { has_more: false } });
        }
        if (req.url.includes("/users/access-preview")) {
          return jsonResponse({
            role: "rep",
            row_scope: "team",
            objects: {},
            field_masks: [],
            teams: [],
          });
        }
        if (req.url.includes("/users") && req.method === "GET") {
          return jsonResponse(ROSTER);
        }
        return jsonResponse(
          { title: "Conflict", detail: "That email already exists." },
          409,
        );
      }),
    );
    render(<UsersAdminCard />);
    await waitFor(() => expect(screen.getByText("Ada Active")).toBeTruthy());

    const dialog = await openInvite();
    await userEvent.type(
      within(dialog).getByPlaceholderText("name@company.com"),
      "dupe@acme.test",
    );
    await userEvent.type(
      within(dialog).getByPlaceholderText("Full name"),
      "Dupe",
    );
    await userEvent.click(
      within(dialog).getByRole("button", { name: /^invite$/i }),
    );

    await waitFor(() =>
      expect(screen.getByText(/already exists/i)).toBeTruthy(),
    );
  });
});
