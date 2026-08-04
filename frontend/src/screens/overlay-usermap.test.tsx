/** @vitest-environment jsdom */
import "@testing-library/jest-dom/vitest";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "../api/schema";
import { type GrantSpec, meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import { MirrorUserMapCard } from "./overlay-usermap";

// The mapping card's job is to make an UNMAPPED user actionable: name them,
// say WHY they are unmapped, say what it costs them (no mirrored records at
// all), and offer the one fix. Everything it prints is a server fact — it
// never guesses a reason the server declined to derive, never names an
// incumbent brand the server didn't report, and never silently trims the
// owner directory it picks from.

type Entry = components["schemas"]["OverlayUserMapEntry"];
type Owner = components["schemas"]["OverlayOwner"];

const ada: Owner = {
  incumbent_user_id: "o1",
  name: "Ada Lovelace",
  email: "ada@acme.test",
};
const grace: Owner = {
  incumbent_user_id: "o2",
  name: "Grace Hopper",
  email: "grace@acme.test",
};

const mappedEntry: Entry = {
  user_id: "u1",
  email: "mapped@acme.test",
  name: "Mapped Person",
  incumbent_user_id: "o1",
  incumbent_user_name: "Ada Lovelace",
  incumbent_user_email: "ada@acme.test",
  match_source: "email",
  unmapped_reason: "none",
};

// Only ever served past a cursor, so a page-two row proves the walk happened.
const secondPageEntry: Entry = {
  user_id: "u-page-2",
  email: "second-page@acme.test",
  unmapped_reason: "not_yet_synced",
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(body === undefined ? null : JSON.stringify(body), {
    status,
    headers:
      body === undefined ? undefined : { "Content-Type": "application/json" },
  });
}

type RouteHandler = (request: Request) => Response | Promise<Response>;

// A refusal the server genuinely produces for a mapping write: a disconnect
// that committed while the tab was open answers mode_not_overlay carrying the
// sentinel's own detail. Pinning a code no backend path emits would prove
// nothing about the running system.
const refusedMapping: RouteHandler = () =>
  jsonResponse(
    { code: "mode_not_overlay", detail: "workspace is not in overlay mode" },
    404,
  );

// A minimal method+path router over the real fetch surface, mirroring
// overlay.test.tsx's local stubApi (it also records every call, for the
// "which request actually fired" assertions). The per-user mapping ops carry
// the user id in the path, so a route may end in `*` to match any last
// segment without the test naming every id.
function stubApi(routes: Record<string, RouteHandler>): Request[] {
  const calls: Request[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (request: Request) => {
      calls.push(request);
      const path = new URL(request.url).pathname.replace(/^\/v1/, "");
      const wildcard = path.replace(/[^/]+$/, "*");
      const handler =
        routes[`${request.method} ${path}`] ??
        routes[`${request.method} ${wildcard}`];
      if (!handler) {
        throw new Error(`unstubbed: ${request.method} ${path}`);
      }
      return handler(request);
    }),
  );
  return calls;
}

// The LISTING itself needs overlay_connection:update, not read: these rows
// carry the incumbent's directory, so the server demands the write grant
// merely to look.
const USER_MAP_VIEWER: GrantSpec = { overlay_connection: ["update"] };

type Fixture = {
  me?: string;
  allow?: GrantSpec;
  incumbent?: string;
  entries?: Entry[];
  nextCursor?: string;
  owners?: Owner[];
  truncated?: boolean;
  ownersFail?: boolean;
  userMapProblem?: { status: number; body: unknown };
  extra?: Record<string, RouteHandler>;
};

function renderCard(fixture: Fixture = {}) {
  const incumbent = fixture.incumbent ?? "hubspot";
  const routes: Record<string, RouteHandler> = {
    "GET /me": () => {
      const me = meFixture({ allow: fixture.allow ?? USER_MAP_VIEWER });
      return jsonResponse({
        ...me,
        user: { ...me.user, id: fixture.me ?? "admin-1" },
      });
    },
    "GET /overlay/user-map": (request) => {
      if (fixture.userMapProblem) {
        return jsonResponse(
          fixture.userMapProblem.body,
          fixture.userMapProblem.status,
        );
      }
      // A cursor means the caller walked past page one; answering the same
      // rows again would let a broken "Load more" look like a working one.
      const walked = new URL(request.url).searchParams.has("cursor");
      return jsonResponse(
        walked
          ? { incumbent, entries: [secondPageEntry] }
          : {
              incumbent,
              entries: fixture.entries ?? [],
              next_cursor: fixture.nextCursor,
            },
      );
    },
    "GET /overlay/owners": () =>
      fixture.ownersFail
        ? jsonResponse(
            {
              code: "upstream_unavailable",
              detail: "the incumbent directory could not be read",
            },
            502,
          )
        : jsonResponse({
            incumbent,
            owners: fixture.owners ?? [ada, grace],
            truncated: fixture.truncated ?? false,
          }),
    ...fixture.extra,
  };
  const calls = stubApi(routes);
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const result = rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">
        <MirrorUserMapCard />
      </LocaleProvider>
    </QueryClientProvider>,
  );
  return { ...result, calls, client };
}

function requests(calls: Request[], method: string, suffix: string): Request[] {
  return calls.filter(
    (r) => r.method === method && new URL(r.url).pathname.endsWith(suffix),
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("the mirror user-map card", () => {
  it("lists unmapped users with the derived reason", async () => {
    renderCard({
      entries: [
        mappedEntry,
        {
          user_id: "u2",
          email: "amb@acme.test",
          incumbent_user_id: "",
          unmapped_reason: "ambiguous_email",
        },
      ],
    });
    expect(await screen.findByText(/ada@acme.test/)).toBeInTheDocument();
    expect(
      screen.getByText(/two .* users share this email/i),
    ).toBeInTheDocument();
  });

  it("says plainly what being unmapped costs", async () => {
    renderCard({
      entries: [
        {
          user_id: "u2",
          email: "amb@acme.test",
          unmapped_reason: "no_email_match",
        },
      ],
    });
    expect(
      await screen.findByText(/sees no mirrored records at all/i),
    ).toBeInTheDocument();
  });

  it("flags a manual mapping whose incumbent user is gone", async () => {
    renderCard({
      entries: [
        {
          user_id: "u1",
          email: "a@acme.test",
          incumbent_user_id: "gone",
          match_source: "manual",
          unmapped_reason: "none",
          stale_owner_ref: true,
        },
      ],
    });
    expect(
      await screen.findByText(/no longer in the .* directory/i),
    ).toBeInTheDocument();
    // Reported, never auto-revoked: the row must still read as mapped, and
    // nothing on it may claim the override was withdrawn.
    expect(screen.getByRole("button", { name: /unmap/i })).toBeInTheDocument();
  });

  it("does not invent a reason when the directory is unavailable", async () => {
    renderCard({
      entries: [
        {
          user_id: "u1",
          email: "a@acme.test",
          incumbent_user_id: "",
          unmapped_reason: "directory_unavailable",
        },
      ],
    });
    expect(
      await screen.findByText(/couldn't read the .* directory/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/no matching/i)).not.toBeInTheDocument();
  });

  it("renders every unmapped reason with its own copy", async () => {
    renderCard({
      entries: [
        {
          user_id: "u1",
          email: "a@acme.test",
          unmapped_reason: "no_email_match",
        },
        {
          user_id: "u2",
          email: "b@acme.test",
          unmapped_reason: "ambiguous_email",
        },
        {
          user_id: "u3",
          email: "c@acme.test",
          unmapped_reason: "blocked_by_admin",
        },
        {
          user_id: "u4",
          email: "d@acme.test",
          unmapped_reason: "not_yet_synced",
        },
        {
          user_id: "u5",
          email: "e@acme.test",
          unmapped_reason: "directory_unavailable",
        },
      ],
    });
    await screen.findByText(/no .* user has this email address/i);
    expect(
      screen.getByText(/two or more .* users share this email/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/an admin unmapped this user/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/hasn't listed this user yet/i),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/couldn't read the .* directory/i),
    ).toBeInTheDocument();
  });

  it("prints a reason this build doesn't know as the server's own token, never blank", async () => {
    renderCard({
      entries: [
        {
          user_id: "u1",
          email: "a@acme.test",
          // A reason the running server added after this build's schema was
          // generated — the honest fallback is the server's raw value.
          unmapped_reason: "seat_suspended" as Entry["unmapped_reason"],
        },
      ],
    });
    expect(await screen.findByText("seat_suspended")).toBeInTheDocument();
    expect(screen.queryByText("undefined")).not.toBeInTheDocument();
  });

  it("confirms before you unmap yourself", async () => {
    renderCard({
      me: "u1",
      entries: [
        {
          user_id: "u1",
          email: "me@acme.test",
          incumbent_user_id: "o1",
          incumbent_user_name: "Ada Lovelace",
          incumbent_user_email: "ada@acme.test",
          match_source: "manual",
          unmapped_reason: "none",
        },
      ],
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /unmap/i }),
    );
    expect(
      screen.getByText(/you will stop seeing every mirrored record/i),
    ).toBeInTheDocument();
  });

  it("does not unmap until the confirmation is accepted", async () => {
    const { calls } = renderCard({
      me: "u1",
      entries: [{ ...mappedEntry, user_id: "u1" }],
      extra: {
        "DELETE /overlay/user-map/*": () => jsonResponse(undefined, 204),
      },
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /unmap/i }),
    );
    expect(requests(calls, "DELETE", "/user-map/u1")).toHaveLength(0);
    const confirms = screen.getAllByRole("button", { name: /unmap/i });
    await userEvent.click(confirms[confirms.length - 1]);
    await waitFor(() =>
      expect(requests(calls, "DELETE", "/user-map/u1")).toHaveLength(1),
    );
  });

  it("names the other person when unmapping someone else", async () => {
    renderCard({
      me: "admin-1",
      entries: [mappedEntry],
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /unmap/i }),
    );
    expect(
      screen.getByText(/Mapped Person will stop seeing every mirrored record/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/you will stop seeing/i)).not.toBeInTheDocument();
  });

  it("surfaces a refused unmap in the confirmation instead of closing it", async () => {
    renderCard({
      entries: [mappedEntry],
      extra: {
        "DELETE /overlay/user-map/*": () =>
          jsonResponse(
            { code: "mode_not_overlay", detail: "the workspace went native" },
            404,
          ),
      },
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /unmap/i }),
    );
    const confirms = screen.getAllByRole("button", { name: /unmap/i });
    await userEvent.click(confirms[confirms.length - 1]);
    // The dialog stays open carrying the server's own reason — a silent close
    // would read exactly like a mapping that was actually removed.
    expect(
      await screen.findByText(/the workspace went native/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Mapped Person will stop seeing every mirrored record/i),
    ).toBeInTheDocument();
  });

  // Nothing orders two in-flight mapping writes against each other. A PUT and
  // a DELETE for the same user are opposite decisions about their whole CRM,
  // and the second one sent can be the first one to land — leaving the user
  // mapped after the admin confirmed Unmap, or applying a picker choice the
  // admin has since replaced. One outstanding write per row, enforced by the
  // controls going inert rather than by hoping the admin waits.
  //
  // A write that never settles is how the pending window is held open; a
  // resolved one would close the very state under test.
  const stalled = (): Promise<Response> => new Promise<Response>(() => {});

  it("offers no second write while a mapping write is in flight", async () => {
    renderCard({
      entries: [mappedEntry],
      extra: { "PUT /overlay/user-map/*": stalled },
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /^Change/ }),
    );
    await userEvent.type(screen.getByLabelText(/search .* users/i), "grace");
    await userEvent.click(
      await screen.findByRole("button", { name: /Grace Hopper/ }),
    );

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /^Change/ })).toBeDisabled(),
    );
    expect(screen.getByRole("button", { name: /^Unmap$/ })).toBeDisabled();
    expect(screen.getByLabelText(/search .* users/i)).toBeDisabled();
  });

  it("offers no second write while an unmap is in flight", async () => {
    renderCard({
      entries: [mappedEntry],
      extra: { "DELETE /overlay/user-map/*": stalled },
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /^Unmap$/ }),
    );
    const confirms = screen.getAllByRole("button", { name: /^Unmap$/ });
    await userEvent.click(confirms[confirms.length - 1]);

    await waitFor(() =>
      expect(screen.getByRole("button", { name: /^Change/ })).toBeDisabled(),
    );
    // Including the confirm the admin already accepted: a re-armed button
    // reads as "that didn't take" and invites the retry that races the first.
    for (const confirm of screen.getAllByRole("button", { name: /^Unmap$/ })) {
      expect(confirm).toBeDisabled();
    }
  });

  // The refusal is one the server genuinely produces: a disconnect that
  // committed while this tab was open answers mode_not_overlay with the
  // sentinel's own detail.
  it("keeps the picker open, with the reason, when the mapping write is refused", async () => {
    renderCard({
      entries: [
        {
          user_id: "u2",
          email: "amb@acme.test",
          unmapped_reason: "no_email_match",
        },
      ],
      extra: { "PUT /overlay/user-map/*": refusedMapping },
    });
    await userEvent.click(await screen.findByRole("button", { name: /^Map/ }));
    await userEvent.type(screen.getByLabelText(/search .* users/i), "grace");
    await userEvent.click(
      await screen.findByRole("button", { name: /Grace Hopper/ }),
    );
    expect(
      await screen.findByText(/workspace is not in overlay mode/),
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/search .* users/i)).toBeInTheDocument();
  });

  // An error outlives the dialog it happened in. Carried into the next row's
  // picker it blames a write that was never attempted for that user.
  it("does not carry a failed mapping's error into another row's picker", async () => {
    renderCard({
      entries: [
        {
          user_id: "u2",
          email: "amb@acme.test",
          unmapped_reason: "no_email_match",
        },
        {
          user_id: "u3",
          email: "other@acme.test",
          unmapped_reason: "no_email_match",
        },
      ],
      extra: { "PUT /overlay/user-map/*": refusedMapping },
    });
    const openPickers = await screen.findAllByRole("button", { name: /^Map/ });
    await userEvent.click(openPickers[0]);
    await userEvent.type(screen.getByLabelText(/search .* users/i), "grace");
    await userEvent.click(
      await screen.findByRole("button", { name: /Grace Hopper/ }),
    );
    await screen.findByText(/workspace is not in overlay mode/);

    await userEvent.click(screen.getAllByRole("button", { name: /^Map/ })[1]);
    expect(
      screen.queryByText(/workspace is not in overlay mode/),
    ).not.toBeInTheDocument();
  });

  it("does not carry a failed unmap's error into the next confirmation", async () => {
    renderCard({
      entries: [
        { ...mappedEntry, user_id: "u1", name: "Mapped One" },
        { ...mappedEntry, user_id: "u2", name: "Mapped Two" },
      ],
      extra: {
        "DELETE /overlay/user-map/*": () =>
          jsonResponse(
            { code: "mode_not_overlay", detail: "the workspace went native" },
            404,
          ),
      },
    });
    const unmapButtons = await screen.findAllByRole("button", {
      name: /unmap/i,
    });
    await userEvent.click(unmapButtons[0]);
    const confirms = screen.getAllByRole("button", { name: /unmap/i });
    await userEvent.click(confirms[confirms.length - 1]);
    await screen.findByText(/the workspace went native/);

    await userEvent.click(screen.getByRole("button", { name: /cancel/i }));
    await userEvent.click(screen.getAllByRole("button", { name: /unmap/i })[1]);
    expect(
      screen.getByText(/Mapped Two will stop seeing every mirrored record/i),
    ).toBeInTheDocument();
    expect(
      screen.queryByText(/the workspace went native/),
    ).not.toBeInTheDocument();
  });

  it("drops every cached read when you remap yourself, not just this card's", async () => {
    const { client } = renderCard({
      me: "u2",
      entries: [
        {
          user_id: "u2",
          email: "me@acme.test",
          unmapped_reason: "no_email_match",
        },
      ],
      extra: {
        "PUT /overlay/user-map/*": () => jsonResponse(undefined, 204),
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: /^Map/ }));
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    await userEvent.type(screen.getByLabelText(/search .* users/i), "grace");
    await userEvent.click(
      await screen.findByRole("button", { name: /Grace Hopper/ }),
    );
    // Your own mapping decides which mirrored records this session can see at
    // all, so the whole cache is suspect — called with no key, not ["overlay"].
    await waitFor(() => expect(invalidateSpy).toHaveBeenCalledWith());
  });

  it("drops only the overlay reads when you remap someone else", async () => {
    const { client } = renderCard({
      me: "admin-1",
      entries: [
        {
          user_id: "u2",
          email: "other@acme.test",
          unmapped_reason: "no_email_match",
        },
      ],
      extra: {
        "PUT /overlay/user-map/*": () => jsonResponse(undefined, 204),
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: /^Map/ }));
    const invalidateSpy = vi.spyOn(client, "invalidateQueries");
    await userEvent.type(screen.getByLabelText(/search .* users/i), "grace");
    await userEvent.click(
      await screen.findByRole("button", { name: /Grace Hopper/ }),
    );
    await waitFor(() =>
      expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: ["overlay"] }),
    );
    expect(invalidateSpy).not.toHaveBeenCalledWith();
  });

  it("maps a user to the owner picked from the directory", async () => {
    const { calls } = renderCard({
      entries: [
        {
          user_id: "u2",
          email: "amb@acme.test",
          unmapped_reason: "no_email_match",
        },
      ],
      extra: {
        "PUT /overlay/user-map/*": () => jsonResponse(undefined, 204),
      },
    });
    await userEvent.click(await screen.findByRole("button", { name: /^Map/ }));
    await userEvent.type(screen.getByLabelText(/search .* users/i), "grace");
    await userEvent.click(
      await screen.findByRole("button", { name: /Grace Hopper/ }),
    );
    await waitFor(() =>
      expect(requests(calls, "PUT", "/user-map/u2")).toHaveLength(1),
    );
    const body = await requests(calls, "PUT", "/user-map/u2")[0].json();
    expect(body).toEqual({ incumbent_user_id: "o2" });
  });

  it("says the owner directory is truncated so a short list doesn't read as absence", async () => {
    renderCard({
      entries: [
        {
          user_id: "u2",
          email: "amb@acme.test",
          unmapped_reason: "no_email_match",
        },
      ],
      truncated: true,
    });
    await userEvent.click(await screen.findByRole("button", { name: /^Map/ }));
    expect(screen.getByText(/longer than this list/i)).toBeInTheDocument();
  });

  it("offers no picker, and says why, when the directory read failed", async () => {
    renderCard({
      entries: [
        {
          user_id: "u2",
          email: "amb@acme.test",
          unmapped_reason: "directory_unavailable",
        },
      ],
      ownersFail: true,
    });
    await userEvent.click(await screen.findByRole("button", { name: /^Map/ }));
    expect(
      screen.getByText(/the incumbent directory could not be read/i),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText(/search .* users/i)).not.toBeInTheDocument();
  });

  it("shows a shared seat only the by-owner view can reveal", async () => {
    renderCard({
      entries: [
        { ...mappedEntry, user_id: "u1", name: "Mapped One" },
        { ...mappedEntry, user_id: "u2", name: "Mapped Two" },
      ],
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /^By HubSpot user$/ }),
    );
    expect(await screen.findByText(/shared seat/i)).toBeInTheDocument();
    expect(screen.getByText(/Mapped One/)).toBeInTheDocument();
    expect(screen.getByText(/Mapped Two/)).toBeInTheDocument();
  });

  it("counts the users the by-owner view cannot show", async () => {
    renderCard({
      entries: [
        mappedEntry,
        {
          user_id: "u9",
          email: "x@acme.test",
          unmapped_reason: "not_yet_synced",
        },
      ],
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /^By HubSpot user$/ }),
    );
    expect(
      await screen.findByText(/1 user is not mapped/i),
    ).toBeInTheDocument();
  });

  // The grouping and the count only ever see the pages that are loaded, so
  // with a page still unread a shared seat can be split across the boundary
  // and the count is of part of the workspace. Reading as a full census would
  // send an admin away believing everyone else is fine.
  it("says the by-owner view covers only the loaded pages", async () => {
    renderCard({
      entries: [
        mappedEntry,
        {
          user_id: "u9",
          email: "x@acme.test",
          unmapped_reason: "not_yet_synced",
        },
      ],
      nextCursor: "cur-2",
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /^By HubSpot user$/ }),
    );
    expect(
      await screen.findByText(/cover the users loaded so far/i),
    ).toBeInTheDocument();
  });

  // "Nobody is mapped yet" over a partially-loaded list is the same false
  // census as an under-count, and a worse one to act on.
  it("qualifies an empty by-owner view while pages are still unloaded", async () => {
    renderCard({
      entries: [
        {
          user_id: "u9",
          email: "x@acme.test",
          unmapped_reason: "not_yet_synced",
        },
      ],
      nextCursor: "cur-2",
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /^By HubSpot user$/ }),
    );
    expect(await screen.findByText(/Nobody is mapped/i)).toBeInTheDocument();
    expect(
      screen.getByText(/cover the users loaded so far/i),
    ).toBeInTheDocument();
  });

  it("claims no scope caveat once every page is loaded", async () => {
    renderCard({
      entries: [
        mappedEntry,
        {
          user_id: "u9",
          email: "x@acme.test",
          unmapped_reason: "not_yet_synced",
        },
      ],
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /^By HubSpot user$/ }),
    );
    await screen.findByText(/1 user is not mapped/i);
    expect(
      screen.queryByText(/cover the users loaded so far/i),
    ).not.toBeInTheDocument();
  });

  it("names the incumbent from the server, never a hardcoded brand", async () => {
    renderCard({
      incumbent: "salesforce",
      entries: [
        {
          user_id: "u1",
          email: "a@acme.test",
          unmapped_reason: "no_email_match",
        },
      ],
    });
    // An incumbent this build has no noun for reads as the generic one — a
    // wrong brand name would be worse than a generic one.
    expect(
      await screen.findByText(/no connected CRM user has this email address/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/HubSpot/)).not.toBeInTheDocument();
  });

  it("withholds the surface, and the reads behind it, without the grant", async () => {
    const { calls } = renderCard({ allow: {} });
    expect(
      await screen.findByText(/You do not have permission/i),
    ).toBeInTheDocument();
    expect(requests(calls, "GET", "/overlay/user-map")).toHaveLength(0);
    expect(requests(calls, "GET", "/overlay/owners")).toHaveLength(0);
  });

  it("reads a native workspace as nothing to map, not as a failure", async () => {
    renderCard({
      userMapProblem: {
        status: 404,
        body: { code: "mode_not_overlay", detail: "workspace is native" },
      },
    });
    expect(
      await screen.findByText(/reads from native tables/i),
    ).toBeInTheDocument();
    expect(screen.queryByText(/workspace is native/)).not.toBeInTheDocument();
  });

  it("reads a deployment without overlay wiring as unconfigured", async () => {
    renderCard({
      userMapProblem: {
        status: 501,
        body: { code: "not_implemented", detail: "overlay not wired" },
      },
    });
    expect(
      await screen.findByText(/isn't configured in this deployment/i),
    ).toBeInTheDocument();
  });

  it("surfaces an unexpected load failure with the server's own detail", async () => {
    renderCard({
      userMapProblem: {
        status: 500,
        body: { code: "internal", detail: "the mapping store is unreachable" },
      },
    });
    expect(
      await screen.findByText(/the mapping store is unreachable/),
    ).toBeInTheDocument();
  });

  it("walks the next page rather than truncating the workspace's users", async () => {
    const { calls } = renderCard({
      entries: [mappedEntry],
      nextCursor: "cur-2",
    });
    await userEvent.click(
      await screen.findByRole("button", { name: /load more/i }),
    );
    expect(
      await screen.findByText(/second-page@acme.test/),
    ).toBeInTheDocument();
    expect(
      calls.filter(
        (r) => new URL(r.url).searchParams.get("cursor") === "cur-2",
      ),
    ).toHaveLength(1);
    // The first page's rows stay on screen — a next page appends, never
    // replaces.
    expect(screen.getByText(/Mapped Person/)).toBeInTheDocument();
  });

  it("has nothing to show for a workspace with no users", async () => {
    renderCard({ entries: [] });
    expect(await screen.findByText(/no users to map/i)).toBeInTheDocument();
  });
});
