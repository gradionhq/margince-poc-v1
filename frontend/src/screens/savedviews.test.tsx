// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
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
        <li
          key={tab.id}
        >{`${tab.label}|${tab.sort}|${JSON.stringify(tab.filters)}`}</li>
      ))}
    </ul>
  );
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
          'German customers|display_name|{"lifecycle":"customer"}',
        ),
      ).toBeTruthy(),
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
