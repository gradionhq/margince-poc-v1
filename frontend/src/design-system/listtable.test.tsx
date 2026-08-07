/** @vitest-environment jsdom */
import {
  act,
  cleanup,
  fireEvent,
  render as rtlRender,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LocaleProvider } from "../i18n";
import {
  type ListChip,
  type ListColumn,
  ListTable,
  type ListView,
} from "./listtable";
import { pickOption } from "./select-testing";

// The list surface (design-system/listtable.tsx) on its own props: the query
// dials it exposes (sort, chips, views, paging) and the presentation state it
// owns itself (column visibility, density). listquery.test.tsx covers the
// server-query binding on top of this — this file proves the surface alone.

afterEach(cleanup);

function render(ui: ReactNode) {
  return rtlRender(<LocaleProvider initial="en">{ui}</LocaleProvider>);
}

type Row = { id: string; name: string; value: number; region: string };

function testRows(count: number): Row[] {
  return Array.from({ length: count }, (_, index) => ({
    id: `r${index + 1}`,
    name: `Row ${String(index + 1).padStart(2, "0")}`,
    value: index + 1,
    region: "EU",
  }));
}

const columns: readonly ListColumn<Row>[] = [
  {
    key: "name",
    header: "Name",
    cell: (row) => row.name,
    sort: "name",
    fixed: true,
  },
  {
    key: "value",
    header: "Value",
    cell: (row) => String(row.value),
    sort: "value",
    numeric: true,
  },
  { key: "note", header: "Note", cell: () => "-" },
  { key: "region", header: "Region", cell: (row) => row.region },
];

describe("sorting", () => {
  it("clicking a sortable header requests that field, ascending first", async () => {
    const onChange = vi.fn();
    render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        sort={{ value: "", onChange }}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Sort by Name" }));
    expect(onChange).toHaveBeenCalledWith("name");
  });

  it("clicking the already-sorted header again sends the descending field", async () => {
    function Harness() {
      const [value, setValue] = useState("name");
      return (
        <ListTable
          rows={testRows(1)}
          columns={columns}
          rowKey={(row) => row.id}
          unit="rows"
          sort={{ value, onChange: setValue }}
        />
      );
    }
    render(<Harness />);
    await userEvent.click(screen.getByRole("button", { name: "Sort by Name" }));
    expect(
      screen
        .getByRole("columnheader", { name: /Name/ })
        .getAttribute("aria-sort"),
    ).toBe("descending");
  });

  it("a numeric column's first click sorts descending", async () => {
    const onChange = vi.fn();
    render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        sort={{ value: "", onChange }}
      />,
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Sort by Value" }),
    );
    expect(onChange).toHaveBeenCalledWith("-value");
  });

  it("a column with no sort field renders an inert header", () => {
    render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        sort={{ value: "", onChange: () => {} }}
      />,
    );
    const header = screen.getByRole("columnheader", { name: "Note" });
    expect(within(header).queryByRole("button")).toBeNull();
  });

  it("omitting the sort control makes every header inert", () => {
    render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );
    expect(screen.queryByRole("button", { name: /Sort by/ })).toBeNull();
  });
});

describe("filter chips", () => {
  const chips: readonly ListChip[] = [
    {
      key: "status",
      label: "Status",
      allLabel: "All statuses",
      options: [
        { value: "new", label: "New" },
        { value: "won", label: "Won" },
      ],
    },
  ];

  it("picking a chip option calls onChipChange with the value, and Delete filter clears it", async () => {
    const onChipChange = vi.fn();
    const { rerender } = render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chips}
        onChipChange={onChipChange}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    await userEvent.click(screen.getByRole("button", { name: "Status" }));
    await userEvent.click(screen.getByRole("button", { name: "New" }));
    expect(onChipChange).toHaveBeenCalledWith("status", "new");

    rerender(
      <LocaleProvider initial="en">
        <ListTable
          rows={[]}
          columns={columns}
          rowKey={(row) => row.id}
          unit="rows"
          chips={chips}
          chosen={{ status: "new" }}
          onChipChange={onChipChange}
        />
      </LocaleProvider>,
    );
    await userEvent.click(
      screen.getByRole("button", {
        name: "More actions for the Status filter",
      }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Delete filter" }),
    );
    expect(onChipChange).toHaveBeenCalledWith("status", "");
  });
});

describe("the frozen column's edge", () => {
  it("only casts a shadow once columns have scrolled under it", () => {
    const { container } = render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );
    const scroller = container.querySelector(".lt-scroll");
    if (!(scroller instanceof HTMLElement)) {
      throw new Error("the table has no scrolling body");
    }
    expect(scroller.className).not.toContain("shifted");

    scroller.scrollLeft = 120;
    fireEvent.scroll(scroller);
    expect(scroller.className).toContain("shifted");

    scroller.scrollLeft = 0;
    fireEvent.scroll(scroller);
    expect(scroller.className).not.toContain("shifted");
  });
});

describe("filter menu", () => {
  const chips: readonly ListChip[] = [
    {
      key: "status",
      label: "Status",
      allLabel: "All statuses",
      options: [
        { value: "new", label: "New" },
        { value: "won", label: "Won" },
      ],
    },
    {
      key: "priority",
      label: "Priority",
      allLabel: "All priorities",
      options: [{ value: "high", label: "High" }],
    },
  ];

  it("narrows the attribute list as you type", async () => {
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chips}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    expect(screen.getByRole("button", { name: "Status" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Priority" })).toBeTruthy();

    await userEvent.type(screen.getByLabelText("Search attributes"), "sta");
    expect(screen.getByRole("button", { name: "Status" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Priority" })).toBeNull();
  });

  // The menu closes on a click outside it, and picking an attribute REPLACES the
  // menu's contents — so the clicked button is gone from the document by the time
  // a document-level listener sees the event. A listener that asks a detached
  // node for its ancestors is told there are none, reads that as "outside", and
  // shuts the menu on the one step that should have advanced it.
  it("stays open when a click's own target has left the document", async () => {
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chips}
      />,
    );
    const trigger = screen.getByRole("button", { name: "Filter" });
    await userEvent.click(trigger);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");

    // Reproduce the real sequence: a node inside the document is clicked, and
    // the click's own handling removes it before the document-level listener
    // runs. A capture-phase listener stands in for the re-render.
    const probe = document.createElement("button");
    document.body.append(probe);
    const detach = () => probe.remove();
    document.addEventListener("click", detach, true);
    await act(async () => {
      probe.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    document.removeEventListener("click", detach, true);

    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByRole("button", { name: "Status" })).toBeTruthy();
  });

  it("picking an attribute then a value applies the filter", async () => {
    const onChipChange = vi.fn();
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chips}
        onChipChange={onChipChange}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    await userEvent.click(screen.getByRole("button", { name: "Status" }));
    await userEvent.click(screen.getByRole("button", { name: "New" }));
    expect(onChipChange).toHaveBeenCalledWith("status", "new");
  });

  it("an applied filter reads as attribute/condition/value row, and Delete filter clears it", async () => {
    const onChipChange = vi.fn();
    const { container } = render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chips}
        chosen={{ status: "new" }}
        onChipChange={onChipChange}
      />,
    );
    const row = container.querySelector(".lt-frow");
    expect(row?.getAttribute("aria-label")).toBe("Status: New");
    // The condition trigger and the value trigger share the row; the row's
    // own (closed) menus also carry an "is" and a "New" — scope to the
    // triggers themselves rather than a plain name match.
    expect(row?.querySelector(".lt-frow-seg:not(.lt-frow-value)")).toBeTruthy();
    expect(row?.querySelector(".lt-frow-value")?.textContent).toBe("New");

    await userEvent.click(
      screen.getByRole("button", {
        name: "More actions for the Status filter",
      }),
    );
    await userEvent.click(
      screen.getByRole("button", { name: "Delete filter" }),
    );
    expect(onChipChange).toHaveBeenCalledWith("status", "");
  });

  it("the condition menu offers exactly one condition", async () => {
    const { container } = render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chips}
        chosen={{ status: "new" }}
      />,
    );
    // The condition trigger reads "is", the same text its own single menu
    // entry carries — scoped to the trigger segment rather than a plain
    // name match, which would also catch that (closed) entry.
    const trigger = container.querySelector<HTMLElement>(
      ".lt-frow-seg:not(.lt-frow-value)",
    );
    if (!trigger) {
      throw new Error("the condition trigger did not render");
    }
    await userEvent.click(trigger);
    const items = container.querySelectorAll(".lt-menu.open .lt-mi");
    expect(items.length).toBe(1);
    expect(items[0]?.textContent).toBe("is");
  });

  it("'+' opens the attribute picker for the remaining attributes", async () => {
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chips}
        chosen={{ status: "new" }}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "Add a filter" }));
    const menu = document.querySelector(".lt-menu.open");
    expect(menu).toBeTruthy();
    // Status already carries its own row — the attribute picker offers only
    // what isn't applied yet.
    expect(within(menu as HTMLElement).queryByText("Status")).toBeNull();
    expect(
      within(menu as HTMLElement).getByRole("button", { name: "Priority" }),
    ).toBeTruthy();
  });
});

describe("a chip with an async search source", () => {
  function chipWithSearch(
    search: NonNullable<ListChip["search"]>,
  ): readonly ListChip[] {
    return [
      {
        key: "org",
        label: "Company",
        allLabel: "All companies",
        options: [],
        search,
      },
    ];
  }

  async function openCompanyValueStep() {
    await userEvent.click(screen.getByRole("button", { name: "Filter" }));
    await userEvent.click(screen.getByRole("button", { name: "Company" }));
  }

  it("queries on debounce and renders the results", async () => {
    const search = vi.fn().mockResolvedValue([{ value: "o1", label: "Acme" }]);
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chipWithSearch(search)}
      />,
    );
    await openCompanyValueStep();
    expect(screen.getByText("Type to search")).toBeTruthy();

    vi.useFakeTimers();
    try {
      fireEvent.change(screen.getByLabelText("Search Company values"), {
        target: { value: "ac" },
      });
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }

    await waitFor(() => expect(search).toHaveBeenCalledWith("ac"));
    expect(await screen.findByRole("button", { name: "Acme" })).toBeTruthy();
  });

  it("keeps the previous results on screen while the next query is in flight", async () => {
    let resolveSecond:
      | ((value: readonly { value: string; label: string }[]) => void)
      | undefined;
    const search = vi
      .fn()
      .mockResolvedValueOnce([{ value: "o1", label: "Acme" }])
      .mockImplementationOnce(
        () =>
          new Promise<readonly { value: string; label: string }[]>(
            (resolve) => {
              resolveSecond = resolve;
            },
          ),
      );
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chipWithSearch(search)}
      />,
    );
    await openCompanyValueStep();
    const input = screen.getByLabelText("Search Company values");

    vi.useFakeTimers();
    try {
      fireEvent.change(input, { target: { value: "ac" } });
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }
    expect(await screen.findByRole("button", { name: "Acme" })).toBeTruthy();

    vi.useFakeTimers();
    try {
      fireEvent.change(input, { target: { value: "acm" } });
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }
    await waitFor(() => expect(search).toHaveBeenCalledTimes(2));
    // The prior result stays up while the next query is still in flight.
    expect(screen.getByRole("button", { name: "Acme" })).toBeTruthy();
    expect(screen.getByText("Searching…")).toBeTruthy();

    resolveSecond?.([{ value: "o2", label: "Acme Renewals" }]);
    expect(
      await screen.findByRole("button", { name: "Acme Renewals" }),
    ).toBeTruthy();
  });

  it("shows a failure when the search rejects", async () => {
    const search = vi.fn().mockRejectedValue(new Error("search down"));
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chipWithSearch(search)}
      />,
    );
    await openCompanyValueStep();

    vi.useFakeTimers();
    try {
      fireEvent.change(screen.getByLabelText("Search Company values"), {
        target: { value: "ac" },
      });
      act(() => {
        vi.advanceTimersByTime(250);
      });
    } finally {
      vi.useRealTimers();
    }

    expect(
      await screen.findByText("The search failed. Try again."),
    ).toBeTruthy();
  });
});

describe("column picker", () => {
  it("hides and re-shows a column, and does not offer a fixed column", async () => {
    render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "Columns" }));
    expect(screen.queryByRole("button", { name: "Name" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Value" }));
    expect(screen.queryByRole("columnheader", { name: "Value" })).toBeNull();

    await userEvent.click(screen.getByRole("button", { name: "Value" }));
    expect(screen.getByRole("columnheader", { name: "Value" })).toBeTruthy();
  });
});

describe("density", () => {
  it("flips aria-pressed on the compact toggle", async () => {
    render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );
    const compact = screen.getByRole("button", { name: "Compact" });
    expect(compact.getAttribute("aria-pressed")).toBe("false");
    await userEvent.click(compact);
    expect(compact.getAttribute("aria-pressed")).toBe("true");
  });
});

describe("views", () => {
  it("switching a view reports its index and applies its sort and filters", async () => {
    const chips: readonly ListChip[] = [
      {
        key: "status",
        label: "Status",
        allLabel: "All statuses",
        options: [{ value: "new", label: "New" }],
      },
    ];
    const views: readonly ListView[] = [
      { label: "All" },
      { label: "New leads", sort: "name", filters: { status: "new" } },
    ];
    const onViewChange = vi.fn();
    const onChipChange = vi.fn();
    const sortOnChange = vi.fn();

    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        chips={chips}
        onChipChange={onChipChange}
        sort={{ value: "", onChange: sortOnChange }}
        views={views}
        activeView={0}
        onViewChange={onViewChange}
      />,
    );

    await userEvent.click(screen.getByRole("button", { name: "New leads" }));
    expect(onViewChange).toHaveBeenCalledWith(1);
    expect(sortOnChange).toHaveBeenCalledWith("name");
    expect(onChipChange).toHaveBeenCalledWith("status", "new");
  });
});

describe("pagination", () => {
  it("shows only the first 25 of 60 rows, with a 3-button pager", () => {
    render(
      <ListTable
        rows={testRows(60)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );
    const dataRows = screen.getAllByRole("row").slice(1);
    expect(dataRows).toHaveLength(25);
    expect(
      screen.getByRole("button", { name: "1" }).getAttribute("aria-current"),
    ).toBe("page");
    expect(screen.getByRole("button", { name: "3" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "4" })).toBeNull();
  });

  it("clicking page 2 renders rows 26 through 50", async () => {
    const data = testRows(60);
    render(
      <ListTable
        rows={data}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "2" }));
    expect(screen.getByText(data[25].name)).toBeTruthy();
    expect(screen.queryByText(data[0].name)).toBeNull();
  });

  it("changing rows-per-page resets to page 1", async () => {
    const data = testRows(60);
    render(
      <ListTable
        rows={data}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "2" }));
    await pickOption(
      user,
      screen.getByRole("combobox", { name: "Rows per page" }),
      "50 per page",
    );
    expect(screen.getByText(data[0].name)).toBeTruthy();
    const dataRows = screen.getAllByRole("row").slice(1);
    expect(dataRows).toHaveLength(50);
  });

  it("disables Next on the last loaded page when hasMore is false, and enables it (calling onLoadMore) when hasMore is true", async () => {
    const data = testRows(60);
    const onLoadMore = vi.fn();
    const { rerender } = render(
      <ListTable
        rows={data}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        hasMore={false}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: "3" }));
    expect(
      screen.getByRole("button", { name: "Next ›" }).hasAttribute("disabled"),
    ).toBe(true);

    rerender(
      <LocaleProvider initial="en">
        <ListTable
          rows={data}
          columns={columns}
          rowKey={(row) => row.id}
          unit="rows"
          hasMore
          onLoadMore={onLoadMore}
        />
      </LocaleProvider>,
    );
    const next = screen.getByRole("button", { name: "Next ›" });
    expect(next.hasAttribute("disabled")).toBe(false);
    await userEvent.click(next);
    expect(onLoadMore).toHaveBeenCalled();
  });
});

describe("count line", () => {
  it("reads as a range and names the sorted column", () => {
    render(
      <ListTable
        rows={testRows(60)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        sort={{ value: "name", onChange: () => {} }}
      />,
    );
    expect(screen.getByText(/1–25 of 60 rows, sorted by Name/)).toBeTruthy();
  });
});

describe("a row as a link", () => {
  it("makes the identity cell a real link, so the row can be opened in a new tab", () => {
    render(
      <ListTable
        rows={testRows(2)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        rowHref={(row) => `#/rows/${row.id}`}
      />,
    );
    const [first] = screen.getAllByRole("link");
    expect(first.getAttribute("href")).toBe(`#/rows/${testRows(2)[0].id}`);
  });

  it("leaves the cell as plain text when the caller names no address", () => {
    render(
      <ListTable
        rows={testRows(2)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("does not navigate the current page as well when the link is followed", () => {
    const onRowClick = vi.fn();
    render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        rowHref={(row) => `#/rows/${row.id}`}
        onRowClick={onRowClick}
      />,
    );
    // A modifier-click is what opens the new tab, and it must not also move
    // this one — the row's handler is what would move it, so the link keeps
    // the click to itself.
    fireEvent.click(screen.getByRole("link"), { metaKey: true });
    expect(onRowClick).not.toHaveBeenCalled();
  });
});

describe("empty state", () => {
  it("offers to clear filters only when the list is filtered", () => {
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        search={{ value: "acme", onChange: () => {} }}
      />,
    );
    expect(screen.getByText("No rows match these filters.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Clear filters" })).toBeTruthy();
  });

  it("shows the plain none-yet copy when nothing is filtered", () => {
    render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );
    const table = screen.getByRole("table");
    expect(within(table).getByText("No rows yet.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
  });

  it("names a likelier cause under the none-yet copy, and only when unfiltered", () => {
    const { rerender } = render(
      <ListTable
        rows={[]}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
        emptyNote="No owner here maps to a workspace user."
      />,
    );
    expect(
      screen.getByText("No owner here maps to a workspace user."),
    ).toBeTruthy();

    // A filtered-empty table already explains itself, so the note would only
    // blame the data source for what the reader's own filter did.
    rerender(
      <LocaleProvider initial="en">
        <ListTable
          rows={[]}
          columns={columns}
          rowKey={(row) => row.id}
          unit="rows"
          emptyNote="No owner here maps to a workspace user."
          search={{ value: "acme", onChange: () => {} }}
        />
      </LocaleProvider>,
    );
    expect(
      screen.queryByText("No owner here maps to a workspace user."),
    ).toBeNull();
  });
});

describe("phone card layout hooks", () => {
  it("labels every non-identity cell with its column header, and leaves the identity cell and table roles intact", () => {
    render(
      <ListTable
        rows={testRows(1)}
        columns={columns}
        rowKey={(row) => row.id}
        unit="rows"
      />,
    );

    expect(screen.getByRole("table")).toBeTruthy();
    const [headerRow, dataRow] = screen.getAllByRole("row");
    expect(within(headerRow).getAllByRole("columnheader")).toHaveLength(
      columns.length,
    );

    const cells = within(dataRow).getAllByRole("cell");
    expect(cells).toHaveLength(columns.length);
    const [identity, value, note, region] = cells;
    expect(identity.hasAttribute("data-label")).toBe(false);
    expect(value.getAttribute("data-label")).toBe("Value");
    expect(note.getAttribute("data-label")).toBe("Note");
    expect(region.getAttribute("data-label")).toBe("Region");
  });
});
