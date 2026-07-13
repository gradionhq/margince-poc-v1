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
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";

// The shared list foundation (P-14): keyset pagination via useListQuery, and
// the search/sort/filter toolbar every list screen adopts next. The
// debounce is real (setTimeout) so we drive it with fake timers, never a
// real sleep (craft T11).

afterEach(() => {
  cleanup();
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

type Row = { id: string };

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
            data: [{ id: "a" }],
            page: { next_cursor: "c1", has_more: true },
          };
        }
        return {
          data: [{ id: "b" }],
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

const sortOptions = [
  { value: "-created_at", label: "list.search" as const },
  { value: "name", label: "list.sort" as const },
];

function baseQuery(): ListQuery {
  return { q: "", sort: "", includeArchived: false, filters: {} };
}

describe("ListToolbar", () => {
  it("debounces search updates and calls setQuery after the delay", () => {
    vi.useFakeTimers();
    try {
      const setQuery = vi.fn();
      render(
        <ListToolbar
          query={baseQuery()}
          setQuery={setQuery}
          sortOptions={sortOptions}
        />,
      );

      const search = screen.getByRole("searchbox");
      fireEvent.change(search, { target: { value: "acme" } });

      expect(setQuery).not.toHaveBeenCalled();

      vi.advanceTimersByTime(250);

      // setQuery is now called with a functional updater (see the
      // stale-query race regression test below), not a plain object.
      expect(setQuery).toHaveBeenCalledTimes(1);
      const updater = setQuery.mock.calls[0][0] as (
        prev: ListQuery,
      ) => ListQuery;
      expect(updater(baseQuery())).toEqual(
        expect.objectContaining({ q: "acme" }),
      );
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not revert a concurrent toggle when the debounced search commits", () => {
    // Regression: the debounce timer used to close over the `query` prop at
    // the time it was scheduled. Typing into search, then toggling
    // include-archived before the 250ms debounce fires, used to overwrite
    // the toggle with the stale query captured before it happened.
    function ControlledToolbar() {
      const [query, setQuery] = useState<ListQuery>(baseQuery());
      return (
        <>
          <ListToolbar
            query={query}
            setQuery={setQuery}
            sortOptions={sortOptions}
          />
          <div data-testid="query-json">{JSON.stringify(query)}</div>
        </>
      );
    }

    vi.useFakeTimers();
    try {
      render(<ControlledToolbar />);

      const search = screen.getByRole("searchbox");
      fireEvent.change(search, { target: { value: "acme" } });

      // Still inside the debounce window: toggle include-archived, which
      // commits to query immediately.
      act(() => {
        vi.advanceTimersByTime(100);
      });
      const archived = screen.getByLabelText(
        "Show archived",
      ) as HTMLInputElement;
      fireEvent.click(archived);

      // Let the pending debounce timer fire.
      act(() => {
        vi.advanceTimersByTime(250);
      });

      const finalQuery = JSON.parse(
        screen.getByTestId("query-json").textContent ?? "{}",
      ) as ListQuery;
      expect(finalQuery.q).toBe("acme");
      expect(finalQuery.includeArchived).toBe(true);
    } finally {
      vi.useRealTimers();
    }
  });

  it("updates sort and includeArchived immediately", async () => {
    const setQuery = vi.fn();
    render(
      <ListToolbar
        query={baseQuery()}
        setQuery={setQuery}
        sortOptions={sortOptions}
      />,
    );

    const sortSelect = screen.getByLabelText("Sort") as HTMLSelectElement;
    await userEvent.selectOptions(sortSelect, "name");
    expect(setQuery).toHaveBeenCalledWith(
      expect.objectContaining({ sort: "name" }),
    );

    const archived = screen.getByLabelText("Show archived") as HTMLInputElement;
    await userEvent.click(archived);
    expect(setQuery).toHaveBeenCalledWith(
      expect.objectContaining({ includeArchived: true }),
    );
  });

  it("updates a select filter", async () => {
    const setQuery = vi.fn();
    render(
      <ListToolbar
        query={baseQuery()}
        setQuery={setQuery}
        sortOptions={sortOptions}
        filters={[
          {
            kind: "select",
            key: "status",
            label: "people.name",
            options: [
              { value: "new", label: "list.sort" },
              { value: "won", label: "list.search" },
            ],
          },
        ]}
      />,
    );

    const statusSelect = screen.getByLabelText("Name") as HTMLSelectElement;
    await userEvent.selectOptions(statusSelect, "new");
    expect(setQuery).toHaveBeenCalledWith(
      expect.objectContaining({ filters: { status: "new" } }),
    );
  });
});
