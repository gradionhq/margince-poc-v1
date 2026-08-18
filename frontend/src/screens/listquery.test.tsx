/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ListChip, ListView } from "../design-system/listsurface";
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import { ProblemError } from "./common";
import {
  type FilterSpec,
  LIST_PAGE_SIZES,
  type ListPage,
  type ListQuery,
  ListTable,
  listFetchLimit,
  useListQuery,
  type ViewSpec,
} from "./listquery";

// The shared list foundation (P-14): keyset pagination via useListQuery, and
// ListTable binding that query to the design-system list surface. The
// debounce is real (setTimeout) so we drive it with fake timers, never a
// real sleep (craft T11).

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function render(ui: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return rtlRender(
    <QueryClientProvider client={client}>
      <LocaleProvider initial="en">{ui}</LocaleProvider>
    </QueryClientProvider>,
  );
}

type Row = { id: string; name: string };

function Harness({
  fetchPage,
}: Readonly<{
  fetchPage: (
    query: ListQuery,
    cursor: string | null,
  ) => Promise<ListPage<Row>>;
}>) {
  const { rows, hasMore, loadMore } = useListQuery<Row>({
    key: "harness",
    fetchPage,
  });
  return (
    <div>
      <ul>
        {rows.map((row) => (
          <li key={row.id}>{row.id}</li>
        ))}
      </ul>
      <span data-testid="has-more">{String(hasMore)}</span>
      <button type="button" onClick={loadMore}>
        more
      </button>
    </div>
  );
}

describe("useListQuery", () => {
  it("accumulates rows across pages and tracks has_more", async () => {
    const fetchPage = vi.fn(
      async (_query: ListQuery, cursor: string | null) => {
        if (cursor === null) {
          return {
            data: [{ id: "a", name: "Anna" }],
            page: { next_cursor: "c1", has_more: true },
          };
        }
        return {
          data: [{ id: "b", name: "Bob" }],
          page: { next_cursor: null, has_more: false },
        };
      },
    );
    render(<Harness fetchPage={fetchPage} />);

    await screen.findByText("a");
    expect(screen.getByTestId("has-more").textContent).toBe("true");
    expect(screen.queryByText("b")).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "more" }));

    await screen.findByText("b");
    expect(screen.getByText("a")).toBeTruthy();
    expect(screen.getByTestId("has-more").textContent).toBe("false");
  });
});

function emptyPage(): ListPage<Row> {
  return { data: [], page: { next_cursor: null, has_more: false } };
}

function ListTableHarness({
  fetchPage,
  chips,
  action,
  views,
  dataViews,
  dataChips,
}: Readonly<{
  fetchPage: (
    query: ListQuery,
    cursor: string | null,
  ) => Promise<ListPage<Row>>;
  chips?: readonly FilterSpec[];
  action?: ReactNode;
  views?: readonly ViewSpec[];
  dataViews?: readonly ListView[];
  dataChips?: readonly ListChip[];
}>) {
  const state = useListQuery<Row>({
    key: "list-table-harness",
    initialSort: "-created_at",
    fetchPage,
  });
  return (
    <ListTable
      state={state}
      unit="nav.contacts"
      columns={[
        {
          key: "name",
          header: "people.name",
          cell: (row: Row) => row.name,
          sort: "full_name",
        },
      ]}
      rowKey={(row) => row.id}
      chips={chips}
      dataChips={dataChips}
      views={views}
      dataViews={dataViews}
      action={action}
    />
  );
}

describe("ListTable: query vocabulary", () => {
  it("debounces search input before sending it to fetchPage", async () => {
    const fetchPage = vi.fn(async (_query: ListQuery, _cursor: string | null) =>
      emptyPage(),
    );
    render(<ListTableHarness fetchPage={fetchPage} />);
    const search = await screen.findByPlaceholderText("Search");

    vi.useFakeTimers();
    try {
      fireEvent.change(search, { target: { value: "acme" } });

      expect(fetchPage.mock.calls.some(([query]) => query.q === "acme")).toBe(
        false,
      );

      await act(async () => {
        vi.advanceTimersByTime(250);
        await Promise.resolve();
      });

      expect(fetchPage.mock.calls.some(([query]) => query.q === "acme")).toBe(
        true,
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not revert a concurrent archived toggle when the debounced search commits", async () => {
    // Regression: the debounce timer used to close over the `query` prop at
    // the time it was scheduled. Typing into search, then toggling
    // include-archived before the 250ms debounce fires, used to overwrite
    // the toggle with the stale query captured before it happened.
    const fetchPage = vi.fn(async (_query: ListQuery, _cursor: string | null) =>
      emptyPage(),
    );
    render(<ListTableHarness fetchPage={fetchPage} />);
    const search = await screen.findByPlaceholderText("Search");

    vi.useFakeTimers();
    try {
      fireEvent.change(search, { target: { value: "acme" } });

      await act(async () => {
        vi.advanceTimersByTime(100);
      });
      const archived = screen.getByLabelText("Show archived");
      fireEvent.click(archived);

      await act(async () => {
        vi.advanceTimersByTime(250);
        await Promise.resolve();
      });

      const lastCall = fetchPage.mock.calls.at(-1);
      expect(lastCall?.[0].q).toBe("acme");
      expect(lastCall?.[0].includeArchived).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("clicking a sortable column header requests that field from the server", async () => {
    const fetchPage = vi.fn(async (_query: ListQuery, _cursor: string | null) =>
      emptyPage(),
    );
    render(<ListTableHarness fetchPage={fetchPage} />);

    const sortButton = await screen.findByRole("button", {
      name: "Sort by people.name",
    });
    await userEvent.click(sortButton);

    expect(
      fetchPage.mock.calls.some(([query]) => query.sort === "full_name"),
    ).toBe(true);
  });

  it("toggling Show archived requests archived rows", async () => {
    const fetchPage = vi.fn(async (_query: ListQuery, _cursor: string | null) =>
      emptyPage(),
    );
    render(<ListTableHarness fetchPage={fetchPage} />);

    const archived = await screen.findByLabelText("Show archived");
    await userEvent.click(archived);

    expect(
      fetchPage.mock.calls.some(([query]) => query.includeArchived === true),
    ).toBe(true);
  });

  it("picking a filter chip narrows the query, and clearing it drops the key", async () => {
    const fetchPage = vi.fn(async (_query: ListQuery, _cursor: string | null) =>
      emptyPage(),
    );
    const { container } = render(
      <ListTableHarness
        fetchPage={fetchPage}
        chips={[
          {
            key: "status",
            label: "lead.filterStatus",
            allLabel: "lead.filterStatusAll",
            options: [
              { value: "new", label: "lead.statusNew" },
              { value: "working", label: "lead.statusWorking" },
            ],
          },
        ]}
      />,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Filter" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Status" }));
    await userEvent.click(screen.getByRole("button", { name: "New" }));

    expect(
      fetchPage.mock.calls.some(([query]) => query.filters.status === "new"),
    ).toBe(true);

    // The applied filter now reads as a row (attribute/condition/value); its
    // value segment reopens the same value list, showing the chosen label —
    // scoped to the trigger itself, since its own (closed) menu carries a
    // same-labelled option.
    const valueTrigger = container.querySelector<HTMLElement>(".lt-frow-value");
    if (!valueTrigger) {
      throw new Error("the applied filter's value trigger did not render");
    }
    await userEvent.click(valueTrigger);
    await userEvent.click(screen.getByRole("button", { name: "All statuses" }));

    const lastCall = fetchPage.mock.calls.at(-1);
    expect(lastCall?.[0].filters).not.toHaveProperty("status");
  });
});

describe("ListTable: pending, error and empty states", () => {
  it("keeps the header, toolbar and primary action on screen while the first page loads, and shows placeholder rows in the body", () => {
    const fetchPage = vi.fn(() => new Promise<ListPage<Row>>(() => {}));
    const { container } = render(
      <ListTableHarness
        fetchPage={fetchPage}
        action={<button type="button">New contact</button>}
      />,
    );
    expect(screen.getByRole("button", { name: "New contact" })).toBeTruthy();
    expect(container.querySelectorAll(".lt-loading").length).toBeGreaterThan(0);
    expect(container.querySelector(".lt-bone")).toBeTruthy();
  });

  it("shows the server's error detail and retries on demand", async () => {
    const fetchPage = vi
      .fn<(query: ListQuery, cursor: string | null) => Promise<ListPage<Row>>>()
      // A server problem, not a bare Error: the body reports the detail the
      // API sent, and a failure with no problem behind it falls back to the
      // generic copy rather than putting an internal message on the screen.
      .mockRejectedValueOnce(
        new ProblemError({ detail: "missing scope people:read" }),
      )
      .mockResolvedValue(emptyPage());
    render(<ListTableHarness fetchPage={fetchPage} />);

    await screen.findByText("Couldn't load this view.");
    expect(screen.getByText("missing scope people:read")).toBeTruthy();

    await userEvent.click(screen.getByRole("button", { name: "Retry" }));

    await screen.findByRole("cell", { name: "No Contacts yet." });
  });

  it("renders the table's own empty state once the list loads with no rows", async () => {
    const fetchPage = vi.fn(async (_query: ListQuery, _cursor: string | null) =>
      emptyPage(),
    );
    render(<ListTableHarness fetchPage={fetchPage} />);

    await screen.findByRole("cell", { name: "No Contacts yet." });
  });
});

describe("removing an applied filter", () => {
  it("drops the key from the query when the row's Delete filter is used", async () => {
    const fetchPage = vi.fn(async (_query: ListQuery, _cursor: string | null) =>
      emptyPage(),
    );
    render(
      <ListTableHarness
        fetchPage={fetchPage}
        chips={[
          {
            key: "status",
            label: "lead.filterStatus",
            allLabel: "lead.filterStatusAll",
            options: [{ value: "working", label: "lead.statusWorking" }],
          },
        ]}
      />,
    );

    await userEvent.click(
      await screen.findByRole("button", { name: "Filter" }),
    );
    await userEvent.click(screen.getByRole("button", { name: "Status" }));
    await userEvent.click(screen.getByRole("button", { name: "Working" }));
    await waitFor(() =>
      expect(fetchPage.mock.calls.some(([query]) => query.filters.status)).toBe(
        true,
      ),
    );

    await userEvent.click(
      screen.getByRole("button", {
        name: "More actions for the Status filter",
      }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Delete filter" }),
    );

    await waitFor(() =>
      expect(fetchPage.mock.calls.at(-1)?.[0].filters).not.toHaveProperty(
        "status",
      ),
    );
  });
});

describe("listFetchLimit — one read carries several rendered pages", () => {
  it("fetches whole rendered pages, never a remainder, for every offered size", () => {
    for (const perPage of LIST_PAGE_SIZES) {
      const limit = listFetchLimit(perPage);
      // A remainder would leave a last page shorter than the size the footer
      // names; over the ceiling the server clamps and the pager offers numbers
      // with no rows behind them.
      expect(limit % perPage).toBe(0);
      expect(limit).toBeLessThanOrEqual(200);
      expect(limit).toBeGreaterThan(200 - perPage);
    }
  });

  it("fills the default page size's strip in one read", () => {
    expect(listFetchLimit(25) / 25).toBe(8);
  });
});

describe("useListQuery — the page size is part of the server query", () => {
  it("hands every fetcher the page size the reader picked", async () => {
    const user = userEvent.setup();
    const fetchPage = vi.fn(
      async (_query: ListQuery, _cursor: string | null) => ({
        data: [] as Row[],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(<ListTableHarness fetchPage={fetchPage} />);
    await waitFor(() => expect(fetchPage).toHaveBeenCalled());
    expect(fetchPage.mock.calls[0]?.[0].perPage).toBe(25);

    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Rows per page" }),
      "50 per page",
    );

    // Every screen reads its `limit` off this one value. A fetcher that kept a
    // literal instead would render a page size the server never returned.
    await waitFor(() =>
      expect(fetchPage.mock.calls.at(-1)?.[0].perPage).toBe(50),
    );
  });
});

describe("the owner dial — one question the server answers three ways", () => {
  it("swaps the owner parameter instead of stacking two of them", async () => {
    const user = userEvent.setup();
    const fetchPage = vi.fn(
      async (_query: ListQuery, _cursor: string | null) => ({
        data: [] as Row[],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(
      <ListTableHarness
        fetchPage={fetchPage}
        chips={[
          {
            key: "owner",
            label: "list.owner",
            allLabel: "list.filterOwnerAll",
            options: [
              { value: "owner_id:u-1", label: "list.filterOwnerMe" },
              { value: "unassigned:true", label: "list.filterOwnerUnassigned" },
            ],
          },
        ]}
      />,
    );
    await waitFor(() => expect(fetchPage).toHaveBeenCalled());

    await user.click(await screen.findByRole("button", { name: "Filter" }));
    await user.click(screen.getByRole("button", { name: "Owner" }));
    await user.click(screen.getByRole("button", { name: "My records" }));

    // The option carries the parameter it sets, so the chip writes `owner_id`
    // rather than a filter named after the chip itself.
    await waitFor(() =>
      expect(fetchPage.mock.calls.at(-1)?.[0].filters.owner_id).toBe("u-1"),
    );
    // And the chip reads back as chosen: a dial that narrows the list and then
    // renders as "Any owner" looks like a filter that did not take.
    expect(
      screen.getByRole("group", { name: "Owner: My records" }),
    ).toBeTruthy();
  });
});

describe("view tabs — two views can ask for the same thing", () => {
  it("highlights the tab the reader pressed, not the first one that matches", async () => {
    const user = userEvent.setup();
    const fetchPage = vi.fn(
      async (_query: ListQuery, _cursor: string | null) => ({
        data: [] as Row[],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(
      <ListTableHarness
        fetchPage={fetchPage}
        views={[
          { label: "list.viewAll" },
          { label: "list.viewAZ", sort: "full_name" },
        ]}
        dataViews={[{ label: "My A-Z", sort: "full_name" }]}
      />,
    );
    await waitFor(() => expect(fetchPage).toHaveBeenCalled());

    // The saved view narrows exactly as the built-in preset does. Derived from
    // the query alone, the highlight lands on the first match and the reader's
    // own view never lights up when they pick it.
    await user.click(screen.getByRole("button", { name: "My A-Z" }));
    await waitFor(() =>
      expect(
        screen
          .getByRole("button", { name: "My A-Z" })
          .getAttribute("aria-pressed"),
      ).toBe("true"),
    );
    expect(
      screen.getByRole("button", { name: "A–Z" }).getAttribute("aria-pressed"),
    ).toBe("false");
  });
});

describe("paging a filtered list", () => {
  it("keeps going forward instead of snapping back to page 1", async () => {
    const user = userEvent.setup();
    const page = (from: number) => ({
      data: Array.from({ length: 25 }, (_, i) => ({
        id: `r-${from + i}`,
        name: `Row ${from + i}`,
      })),
      page: { next_cursor: `c-${from + 25}`, has_more: true },
    });
    let served = 0;
    const fetchPage = vi.fn(async (_query: ListQuery, _cursor: string | null) =>
      page(25 * served++),
    );
    render(
      <ListTableHarness
        fetchPage={fetchPage}
        chips={[
          {
            key: "owner",
            label: "list.owner",
            allLabel: "list.filterOwnerAll",
            options: [{ value: "owner_id:u-1", label: "list.filterOwnerMe" }],
          },
        ]}
      />,
    );
    await waitFor(() => expect(screen.getByText("Row 0")).toBeTruthy());

    // Next twice, each press waiting for its page to actually render rather
    // than for a timer to fire: a zero-duration sleep races React Query's
    // commit, and a test that sometimes clicks Next on a page that has not
    // arrived is a test that sometimes passes for the wrong reason.
    //
    // The table resets to page 1 whenever `chosen` changes IDENTITY — so a
    // chosen object rebuilt on every render reset on every render, and the
    // list flipped between the first two pages forever. ONE press hides that:
    // the reset lands on a page whose content happens to match. It takes two.
    await user.click(screen.getByRole("button", { name: /Next/ }));
    await waitFor(() => expect(screen.getByText("Row 25")).toBeTruthy());

    await user.click(screen.getByRole("button", { name: /Next/ }));
    await waitFor(() => expect(screen.getByText("Row 50")).toBeTruthy());
    expect(screen.queryByText("Row 0")).toBeNull();
  });
});

describe("a data-driven chip narrows the list", () => {
  it("sends the parameter its option names, not the chip's own key", async () => {
    const user = userEvent.setup();
    const fetchPage = vi.fn(
      async (_query: ListQuery, _cursor: string | null) => ({
        data: [] as Row[],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(
      <ListTableHarness
        fetchPage={fetchPage}
        // The owner dial is a dataChip: it names the viewer's teams, which are
        // server strings rather than message keys.
        dataChips={[
          {
            key: "owner",
            label: "Owner",
            allLabel: "Any owner",
            options: [
              { value: "owner_id:u-1", label: "My records" },
              { value: "unassigned:true", label: "Unassigned" },
            ],
          },
        ]}
      />,
    );
    await waitFor(() => expect(fetchPage).toHaveBeenCalled());

    await user.click(await screen.findByRole("button", { name: "Filter" }));
    await user.click(screen.getByRole("button", { name: "Owner" }));
    await user.click(screen.getByRole("button", { name: "Unassigned" }));

    // `unassigned=true`, not `owner=unassigned:true`. The server ignores a
    // parameter it does not know, so the wrong spelling answers the WHOLE list
    // with 200 OK — a filter that reads as working and is not.
    await waitFor(() =>
      expect(fetchPage.mock.calls.at(-1)?.[0].filters.unassigned).toBe("true"),
    );
    expect(fetchPage.mock.calls.at(-1)?.[0].filters).not.toHaveProperty(
      "owner",
    );
  });
});

describe("two chips on one list", () => {
  it("does not let one dial clear the other's answer", async () => {
    const user = userEvent.setup();
    const fetchPage = vi.fn(
      async (_query: ListQuery, _cursor: string | null) => ({
        data: [] as Row[],
        page: { next_cursor: null, has_more: false },
      }),
    );
    render(
      <ListTableHarness
        fetchPage={fetchPage}
        chips={[
          {
            key: "lifecycle",
            label: "org.lifecycle",
            allLabel: "org.filterLifecycleAll",
            options: [{ value: "customer", label: "org.lifecycle.customer" }],
          },
        ]}
        dataChips={[
          {
            key: "owner",
            label: "Owner",
            allLabel: "Any owner",
            options: [{ value: "unassigned:true", label: "Unassigned" }],
          },
        ]}
      />,
    );
    await waitFor(() => expect(fetchPage).toHaveBeenCalled());

    await user.click(await screen.findByRole("button", { name: "Filter" }));
    await user.click(screen.getByRole("button", { name: "Owner" }));
    await user.click(screen.getByRole("button", { name: "Unassigned" }));
    await waitFor(() =>
      expect(fetchPage.mock.calls.at(-1)?.[0].filters.unassigned).toBe("true"),
    );

    // Now narrow by something else. Clearing every composite parameter on the
    // surface — rather than only the chip being changed — would drop the owner
    // answer here, so picking a lifecycle would silently widen the list back to
    // every owner while the owner chip still showed "Unassigned".
    await user.click(screen.getByRole("button", { name: "Account lifecycle" }));
    await user.click(screen.getByRole("button", { name: "Customer" }));

    await waitFor(() =>
      expect(fetchPage.mock.calls.at(-1)?.[0].filters.lifecycle).toBe(
        "customer",
      ),
    );
    expect(fetchPage.mock.calls.at(-1)?.[0].filters.unassigned).toBe("true");
  });
});
