/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import { ProblemError } from "./common";
import {
  type FilterSpec,
  type ListPage,
  type ListQuery,
  ListTable,
  useListQuery,
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
}: Readonly<{
  fetchPage: (
    query: ListQuery,
    cursor: string | null,
  ) => Promise<ListPage<Row>>;
  chips?: readonly FilterSpec[];
  action?: ReactNode;
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

    const statusChip = await screen.findByRole("button", { name: "Status" });
    await userEvent.click(statusChip);
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
