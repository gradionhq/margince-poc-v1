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
import { pickOption } from "../design-system/select-testing";
import { LocaleProvider } from "../i18n";
import {
  type FilterSpec,
  ListGate,
  type ListGateState,
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
    const user = userEvent.setup();
    const setQuery = vi.fn();
    render(
      <ListToolbar
        query={baseQuery()}
        setQuery={setQuery}
        sortOptions={sortOptions}
      />,
    );

    // The sort control's own name and the "name" option's label are both
    // "Sort" here (the fixture reuses catalog keys as labels) — the control is
    // the combobox, the choice is the option inside its popup.
    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Sort" }),
      "Sort",
    );
    expect(setQuery).toHaveBeenCalledWith(
      expect.objectContaining({ sort: "name" }),
    );

    await user.click(screen.getByLabelText("Show archived"));
    expect(setQuery).toHaveBeenCalledWith(
      expect.objectContaining({ includeArchived: true }),
    );
  });

  it("updates a select filter", async () => {
    const user = userEvent.setup();
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

    // "Sort" is the label of the option whose value is "new" (the fixture
    // reuses catalog keys as labels).
    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Name" }),
      "Sort",
    );
    expect(setQuery).toHaveBeenCalledWith(
      expect.objectContaining({ filters: { status: "new" } }),
    );
  });

  it("removes a cleared select filter's key instead of storing an empty string", async () => {
    const user = userEvent.setup();
    const setQuery = vi.fn();
    render(
      <ListToolbar
        query={{ ...baseQuery(), filters: { status: "new" } }}
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

    // The unset entry is an option like any other, named by the filter itself
    // when the spec passes no placeholder — picking it is how a reader comes
    // back to the unfiltered list.
    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Name" }),
      "Name",
    );
    expect(setQuery).toHaveBeenCalledWith(
      expect.objectContaining({ filters: {} }),
    );
    const lastCall = setQuery.mock.calls.at(-1)?.[0] as ListQuery;
    expect(lastCall.filters).not.toHaveProperty("status");
  });
});

// A text filter is a design-system TextInput named by its own spec, and it
// shares withFilter with the select above: a typed value stores the key, an
// emptied field deletes it rather than sending `key=""`.
describe("ListToolbar text filter", () => {
  const domainFilter: FilterSpec[] = [
    { kind: "text", key: "domain", label: "people.name" },
  ];

  it("stores what was typed under the filter's key", () => {
    const setQuery = vi.fn();
    render(
      <ListToolbar
        query={baseQuery()}
        setQuery={setQuery}
        sortOptions={sortOptions}
        filters={domainFilter}
      />,
    );

    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "acme.test" },
    });
    expect(setQuery).toHaveBeenCalledWith(
      expect.objectContaining({ filters: { domain: "acme.test" } }),
    );
  });

  it("drops the key when the field is emptied", () => {
    const setQuery = vi.fn();
    render(
      <ListToolbar
        query={{ ...baseQuery(), filters: { domain: "acme.test" } }}
        setQuery={setQuery}
        sortOptions={sortOptions}
        filters={domainFilter}
      />,
    );

    fireEvent.change(screen.getByRole("textbox", { name: "Name" }), {
      target: { value: "" },
    });
    const lastCall = setQuery.mock.calls.at(-1)?.[0] as ListQuery;
    expect(lastCall.filters).not.toHaveProperty("domain");
  });
});

describe("ListGate", () => {
  function emptyState(): ListGateState<{ id: string }> {
    return {
      rows: [],
      isPending: false,
      isError: false,
      error: null,
      refetch: () => {},
      hasMore: false,
      loadMore: () => {},
    };
  }

  function stubMe(mode: "native" | "overlay") {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        async () =>
          new Response(
            JSON.stringify({
              user: { id: "u1", email: "a@example.test", display_name: "A" },
              roles: ["admin"],
              teams: [],
              system_of_record: { mode },
            }),
            { headers: { "Content-Type": "application/json" } },
          ),
      ),
    );
  }

  it("explains owner mapping in the overlay empty state", async () => {
    stubMe("overlay");
    render(
      <ListGate state={emptyState()} empty="No leads yet.">
        {() => null}
      </ListGate>,
    );
    await screen.findByText("No leads yet.");
    expect(await screen.findByText(/owner's HubSpot email/i)).toBeTruthy();
  });

  it("shows only the caller's empty copy in native mode", async () => {
    stubMe("native");
    render(
      <ListGate state={emptyState()} empty="No leads yet.">
        {() => null}
      </ListGate>,
    );
    await screen.findByText("No leads yet.");
    expect(screen.queryByText(/owner's HubSpot email/i)).toBeNull();
  });
});
