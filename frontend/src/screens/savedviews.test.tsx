// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { meFixture } from "../app/mefixture";
import { LocaleProvider } from "../i18n";
import type { ListQuery } from "./listquery";
import { listStateOf, SaveViewAction, useSavedViewTabs } from "./savedviews";

afterEach(cleanup);

function wrap(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

const narrowed: ListQuery = {
  q: "",
  sort: "display_name",
  includeArchived: false,
  filters: { lifecycle: "customer" },
  perPage: 25,
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function Tabs() {
  const tabs = useSavedViewTabs("organizations");
  return (
    <ul>
      {tabs.map((tab) => (
        <li key={tab.id}>
          {[
            tab.label,
            tab.q,
            tab.sort,
            JSON.stringify(tab.filters),
            String(tab.includeArchived),
            String(tab.perPage),
          ].join("|")}
        </li>
      ))}
    </ul>
  );
}

/**
 * A /views read that fails ONCE, with /me answered normally throughout.
 *
 * Routed by URL rather than counted: the action reads /me to learn whether a
 * saved-view rail is drawn at all, so a stub that failed "the first request"
 * would fail whichever of the two happened to go out first.
 */
function viewsFailingOnce(me = meFixture({})) {
  let reads = 0;
  return vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input instanceof Request ? input.url : input);
    if (url.includes("/v1/me")) {
      return jsonResponse(me);
    }
    reads += 1;
    return reads === 1
      ? jsonResponse({ title: "Server error" }, 500)
      : jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
  });
}

describe("saved views", () => {
  it("restores the sort and filters a view was saved with", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            {
              id: "v-1",
              owner_id: "u-1",
              resource: "organizations",
              name: "German customers",
              version: 1,
              query: {
                list: {
                  q: "",
                  sort: "display_name",
                  includeArchived: false,
                  filters: { lifecycle: "customer" },
                  perPage: 25,
                },
              },
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    wrap(<Tabs />);
    await waitFor(() =>
      expect(
        screen.getByText(
          'German customers||display_name|{"lifecycle":"customer"}|false|25',
        ),
      ).toBeTruthy(),
    );
  });

  it("carries the archived toggle and the page size the view was saved with", async () => {
    // The rail used to convert a view into a tab carrying only the sort and the
    // filters, so a view saved with "Show archived" on restored with it off —
    // and then matched as equal to the default list.
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            {
              id: "v-4",
              owner_id: "u-1",
              resource: "organizations",
              name: "Closed too",
              version: 1,
              query: {
                list: {
                  q: "",
                  sort: "",
                  includeArchived: true,
                  filters: {},
                  perPage: 100,
                },
              },
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    wrap(<Tabs />);
    await waitFor(() =>
      expect(screen.getByText("Closed too|||{}|true|100")).toBeTruthy(),
    );
  });

  it("drops a view whose stored state cannot be read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            // A row from an older build, or one written by hand: required by
            // the contract, absent here. A tab that lights up and restores
            // nothing is worse than no tab, and reading it must not take the
            // whole list screen down with it.
            {
              id: "v-2",
              owner_id: "u-1",
              resource: "organizations",
              name: "No query at all",
              version: 1,
            },
            {
              id: "v-3",
              owner_id: "u-1",
              resource: "organizations",
              name: "A query with no list state",
              version: 1,
              query: {},
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    wrap(<Tabs />);
    await waitFor(() => expect(screen.getByRole("list")).toBeTruthy());
    expect(screen.queryByRole("listitem")).toBeNull();
  });

  it("reads an unreadable row as absent rather than throwing", () => {
    // React retries a failed render, so a component test cannot tell a skipped
    // row from a crashed-and-recovered one. The reader is checked directly:
    // `query` is required by the contract and still arrives missing from a
    // stub, an older build, or a hand-written row, and a list screen that
    // throws while drawing its own tab rail takes the whole screen with it.
    const shapes = [
      { id: "v-a", name: "No query at all", version: 1 },
      { id: "v-b", name: "A query with no list state", version: 1, query: {} },
      {
        id: "v-c",
        name: "List state that is not an object",
        version: 1,
        query: { list: 7 },
      },
    ];
    for (const shape of shapes) {
      expect(() =>
        listStateOf(shape as Parameters<typeof listStateOf>[0]),
      ).not.toThrow();
      expect(
        listStateOf(shape as Parameters<typeof listStateOf>[0]),
      ).toBeNull();
    }
  });

  it("offers to save a narrowed list, and sends the state it is showing", async () => {
    const user = userEvent.setup();
    let posted: Record<string, unknown> | null = null;
    vi.stubGlobal(
      "fetch",
      vi.fn(async (request: Request) => {
        if (request.method === "POST") {
          posted = JSON.parse(await request.text());
          return jsonResponse({ id: "v-3" }, 201);
        }
        return jsonResponse({
          data: [],
          page: { next_cursor: null, has_more: false },
        });
      }),
    );
    wrap(<SaveViewAction resource="organizations" query={narrowed} />);

    await user.click(screen.getByRole("button", { name: "Save view" }));
    await user.type(screen.getByRole("textbox", { name: "Name" }), "Customers");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(posted).not.toBeNull());
    expect(posted).toMatchObject({
      resource: "organizations",
      name: "Customers",
      query: {
        list: { sort: "display_name", filters: { lifecycle: "customer" } },
      },
    });
  });

  it("says the saved views failed to load rather than showing an empty rail", async () => {
    const user = userEvent.setup();
    vi.stubGlobal("fetch", viewsFailingOnce());
    wrap(<SaveViewAction resource="organizations" query={narrowed} />);

    // An empty rail claims the reader has saved nothing. A failed read knows
    // no such thing, and the retry is what makes it a failure rather than an
    // absence.
    const failed = await screen.findByText("This section did not load.");
    expect(failed).toBeTruthy();
    // And it says WHICH section. The notice lands in the list's tools slot
    // beside Columns and Compact, where an unnamed "this section" could be any
    // of the three.
    expect(
      screen.getByRole("region", { name: "Saved views" }).contains(failed),
    ).toBe(true);

    await user.click(screen.getByRole("button", { name: "Try again" }));

    await waitFor(() =>
      expect(screen.queryByText("This section did not load.")).toBeNull(),
    );
  });

  it("reports no saved-view failure in overlay mode, where no rail is drawn", async () => {
    vi.stubGlobal(
      "fetch",
      viewsFailingOnce({
        ...meFixture({}),
        system_of_record: { mode: "overlay" },
      }),
    );
    wrap(<SaveViewAction resource="organizations" query={narrowed} />);

    // The rail is withheld in overlay mode, so there is nothing on screen for
    // the failure to be about. Waiting on the save button proves the read has
    // settled rather than that the assertion below ran too early.
    await screen.findByRole("button", { name: "Save view" });
    expect(screen.queryByText("This section did not load.")).toBeNull();
  });

  it("carries the search a view was saved with", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            {
              id: "v-5",
              owner_id: "u-1",
              resource: "organizations",
              name: "Acme",
              version: 1,
              query: {
                list: {
                  q: "acme",
                  sort: "",
                  includeArchived: false,
                  filters: {},
                  perPage: 25,
                },
              },
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    wrap(<Tabs />);
    // The search was stored and read back all along, and then dropped on the
    // way to the tab — so a tab named after a search claimed a list that was
    // not searched.
    await waitFor(() =>
      expect(screen.getByText("Acme|acme||{}|false|25")).toBeTruthy(),
    );
  });

  it("claims no page size for a stored blob that carries none", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse({
          data: [
            {
              id: "v-6",
              owner_id: "u-1",
              resource: "organizations",
              name: "No page size",
              version: 1,
              // `query` is an open JSON object on the wire, so a view written
              // through the API or MCP can carry list state with no page size
              // in it at all.
              query: { list: { q: "", sort: "display_name", filters: {} } },
            },
          ],
          page: { next_cursor: null, has_more: false },
        }),
      ),
    );
    wrap(<Tabs />);
    // Absent, not 25: substituting the default made "asked for the default"
    // and "asked for nothing" the same tab, and the rail acts on the
    // difference by leaving the reader's own choice alone.
    await waitFor(() =>
      expect(
        screen.getByText("No page size||display_name|{}|false|undefined"),
      ).toBeTruthy(),
    );
  });

  it("does not offer to save a list nobody has narrowed", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse({ data: [] })),
    );
    // Saving the default would add a tab that does what All already does.
    wrap(
      <SaveViewAction
        resource="organizations"
        query={{
          q: "",
          sort: "",
          includeArchived: false,
          filters: {},
          perPage: 25,
        }}
      />,
    );
    expect(screen.queryByRole("button", { name: "Save view" })).toBeNull();
  });
});
