/** @vitest-environment jsdom */
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
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

      expect(setQuery).not.toHaveBeenCalledWith(
        expect.objectContaining({ q: "acme" }),
      );

      vi.advanceTimersByTime(250);

      expect(setQuery).toHaveBeenCalledWith(
        expect.objectContaining({ q: "acme" }),
      );
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
