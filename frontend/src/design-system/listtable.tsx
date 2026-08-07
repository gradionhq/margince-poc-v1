// The phone layout lays the table's own elements out as cards (listtable.css),
// and a table element laid out as blocks loses its implicit ARIA roles in
// Chrome and Safari. Naming every role explicitly is the fix, so the roles that
// read as redundant in the markup are exactly what keeps the grid announceable
// once the layout changes underneath it.
// biome-ignore-all lint/a11y/noRedundantRoles: display:block drops implicit table roles
// biome-ignore-all lint/a11y/useSemanticElements: the semantic element is already in use

import { Check, ChevronDown, Columns3, Rows3 } from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useT } from "../i18n";
import {
  CountLine,
  type ListChip,
  ListSurface,
  type ListView,
  Menu,
  type SortControl,
  useCloseOnOutsideClick,
} from "./listsurface";
import { Select } from "./select";
import "./listtable.css";

export type {
  ListChip,
  ListView,
  SortControl,
} from "./listsurface";

// The list surface: one component owning the header, the controls, the rows and
// the footer of a record list, so the dials read as belonging to the data they
// act on rather than floating above it.
//
// The query dials are CONTROLLED and server-backed — search, sort and filters
// are reported upward and the caller re-reads the list. Only presentation is
// local state: which columns are shown, how tight the rows are, and which saved
// view is selected. That split is the whole design. A table that quietly sorted
// its own page would be lying about the other pages, and this list is a keyset
// cursor over a set larger than what is loaded.
//
// Generic on purpose: nothing here knows what a CRM record is, which is why
// contacts, companies, leads, deals, products and partners share it.

export type ListColumn<Row> = {
  key: string;
  header: string;
  cell: (row: Row) => ReactNode;
  /**
   * The server sort field behind this column. Its presence is what makes the
   * header clickable — a column the API cannot order by stays inert rather
   * than offering a control that would silently do nothing.
   */
  sort?: string;
  /** Right-aligns, and makes the first sort click descending. */
  numeric?: boolean;
  /**
   * Exempt from the column picker, and the card heading on a phone. The
   * identity column has to stay: it is what makes a row recognisable.
   */
  fixed?: boolean;
};

/**
 * Never larger than the page the list endpoints return (50). A bigger choice
 * cannot be filled from one response, so the table would hold the reader on a
 * page it has no rows for until enough cursor pages had been walked — a size
 * the server cannot serve is not a size worth offering.
 */
const PAGE_SIZES = [25, 50] as const;

/** Narrow enough to tuck a column away, wide enough to still read a header. */
const MIN_COLUMN_WIDTH = 72;

/**
 * How the table divides its width when the reader has not resized anything.
 *
 * Left to itself a table sizes each column to its content, which reads badly
 * both ways: a name column carrying an avatar, a name and a badge takes half
 * the page, and once that is stopped the whole grid huddles at the left edge
 * with empty space beside it. So the columns take shares of the full width
 * instead, weighted by what they actually hold — a date or an amount is a
 * known short string and never needs more, a name is the one column worth
 * reading in full, and everything else sits between them.
 *
 * The minimums are what the column stops shrinking at: below the sum of them
 * the table scrolls sideways rather than crushing every column at once.
 */
const COLUMN_SIZES = {
  identity: { share: 2.4, min: 200 },
  numeric: { share: 0.9, min: 110 },
  standard: { share: 1.3, min: 130 },
} as const;

function sizeOf(column: { fixed?: boolean; numeric?: boolean }) {
  if (column.fixed) {
    return COLUMN_SIZES.identity;
  }
  return column.numeric ? COLUMN_SIZES.numeric : COLUMN_SIZES.standard;
}

/**
 * Column widths outlive the visit: a reader who widened a column to fit their
 * data expects it that way tomorrow, not reset by a reload. Stored per table so
 * two lists never inherit each other's layout, and read defensively — a browser
 * with storage denied still gets a working table, just a forgetful one.
 *
 * The key carries the layout's version: widths written when the columns sized
 * themselves to their content mean something else under shares, and reading
 * them back pins every column at a width nobody chose.
 */
const WIDTHS_PREFIX = "margince.table.widths.v2.";

function readWidths(key?: string): Record<string, number> {
  if (!key) {
    return {};
  }
  try {
    const raw = localStorage.getItem(WIDTHS_PREFIX + key);
    if (!raw) {
      return {};
    }
    const parsed: unknown = JSON.parse(raw);
    if (typeof parsed !== "object" || parsed === null) {
      return {};
    }
    return Object.fromEntries(
      Object.entries(parsed).filter(
        (entry): entry is [string, number] =>
          typeof entry[1] === "number" && Number.isFinite(entry[1]),
      ),
    );
  } catch {
    // A malformed or unreadable entry is not worth failing a table render for;
    // the columns fall back to their content widths.
    return {};
  }
}

function writeWidths(key: string | undefined, widths: Record<string, number>) {
  if (!key) {
    return;
  }
  try {
    localStorage.setItem(WIDTHS_PREFIX + key, JSON.stringify(widths));
  } catch {
    // Storage full or denied: the widths still apply for this visit.
  }
}

/** Placeholder rows while the first page loads: enough to read as a list. */
const PLACEHOLDER_ROWS = [0, 1, 2, 3, 4];

const EMPTY_FILTERS: Readonly<Record<string, string>> = {};

/** Is this column the one currently sorted, and which way? */
function sortState(
  column: { sort?: string },
  value: string,
): "asc" | "desc" | null {
  if (!column.sort) {
    return null;
  }
  if (value === column.sort) {
    return "asc";
  }
  return value === `-${column.sort}` ? "desc" : null;
}

export function ListTable<Row>({
  rows,
  columns,
  rowKey,
  onRowClick,
  rowHref,
  unit,
  emptyNote,
  search,
  sort,
  chips = [],
  chosen = EMPTY_FILTERS,
  onChipChange,
  archived,
  views = [],
  activeView = 0,
  onViewChange,
  action,
  caption,
  note,
  footer,
  hasMore = false,
  onLoadMore,
  pending = false,
  problem,
  widthsKey,
  tools,
}: Readonly<{
  rows: readonly Row[];
  columns: readonly ListColumn<Row>[];
  rowKey: (row: Row) => string;
  onRowClick?: (row: Row) => void;
  /**
   * Where this row lives, as a URL. Turns the identity cell into a link, so a
   * row can be opened in a new tab or reached by keyboard — a click handler
   * alone can do neither.
   */
  rowHref?: (row: Row) => string;
  /** Plural noun for the count and the empty state — "contacts", "leads". */
  unit: string;
  /**
   * A likelier cause than "there is nothing here", for a data source that has
   * one. Shown under the empty state only when no filter is narrowing the
   * list, since a filtered-empty table already explains itself.
   */
  emptyNote?: ReactNode;
  /** Omit for a list whose GET has no `q` param; the box is then not rendered. */
  search?: { value: string; onChange: (next: string) => void };
  /**
   * Omit when the data source refuses to sort. The overlay mirror 422s the
   * dial, so its screens pass nothing and the headers render inert — the
   * table never offers a control the server would reject.
   */
  sort?: SortControl;
  chips?: readonly ListChip[];
  chosen?: Readonly<Record<string, string>>;
  /** Called with "" to clear. */
  onChipChange?: (key: string, value: string) => void;
  archived?: { checked: boolean; onChange: (next: boolean) => void };
  views?: readonly ListView[];
  activeView?: number;
  onViewChange?: (index: number) => void;
  /** The one primary action for this surface, e.g. "New contact". */
  action?: ReactNode;
  /**
   * A standing note about what this list is, when the list needs one. The
   * screen's name is not it: the shell already says which screen you are on,
   * and repeating it here would title the surface twice.
   */
  caption?: ReactNode;
  /** Says why the dials are missing, when they are. */
  note?: ReactNode;
  /** An aggregate row under the table, e.g. a count and a total value. */
  footer?: ReactNode;
  /**
   * Whether the server holds rows beyond the ones passed in. Paging is a keyset
   * cursor, so there is no total and no way to jump to an arbitrary page: the
   * pager walks the pages it has, and stepping past the last one fetches the
   * next cursor page rather than pretending a page count it cannot know.
   */
  hasMore?: boolean;
  onLoadMore?: () => void;
  /**
   * The rows are still loading. The surface keeps its header and controls and
   * puts placeholders in the body: the primary action and the dials belong to
   * the screen, not to the response, and a create button that disappears while
   * a list loads is a button the reader has to wait for.
   */
  pending?: boolean;
  /** Why the rows could not be read, with whatever retry the caller offers. */
  problem?: ReactNode;
  /** Names this table for the column widths it remembers between visits. */
  widthsKey?: string;
  /** Appended to the surface's tools slot ahead of the Columns/Compact
   * buttons — a caller's own view-switch or picker, e.g. deals' board/table
   * toggle and pipeline picker. */
  tools?: ReactNode;
}>) {
  const t = useT();
  const [hidden, setHidden] = useState<ReadonlySet<string>>(new Set());
  const [dense, setDense] = useState(false);
  const [columnsOpen, setColumnsOpen] = useState(false);
  const [widths, setWidths] = useState<Readonly<Record<string, number>>>(() =>
    readWidths(widthsKey),
  );
  // A drag reports a width per pointer event, so the two costly steps sit at
  // its edges instead: the other columns are measured once when it starts, and
  // storage is written once when it ends. Doing either per event would read
  // layout back and write to disk a hundred times a second while the reader
  // holds the mouse down. The ref is what the edges read, since a handler
  // mid-drag closes over the state from the render it was created in.
  const live = useRef(widths);
  const applyWidths = (next: Readonly<Record<string, number>>) => {
    live.current = next;
    setWidths(next);
  };
  const [page, setPage] = useState(1);
  const [perPage, setPerPage] = useState<number>(PAGE_SIZES[0]);
  const scroller = useRef<HTMLDivElement>(null);
  const head = useRef<HTMLTableElement>(null);
  // The frozen column only casts a shadow once columns have actually slid under
  // it. At rest there is nothing behind the edge, and a shadow over open space
  // reads as a seam in the table.
  const [shifted, setShifted] = useState(false);
  useCloseOnOutsideClick(() => setColumnsOpen(false));

  const lastPage = Math.max(1, Math.ceil(rows.length / perPage));
  const current = Math.min(page, lastPage);
  const from = (current - 1) * perPage;
  const pageRows = rows.slice(from, from + perPage);

  const shown = columns.filter((column) => !hidden.has(column.key));
  // A column the reader has dragged keeps the width they gave it; the rest
  // divide what is left by their shares, so hiding a column widens the others
  // instead of leaving a gap where it was.
  const shares = shown.reduce(
    (total, column) => total + (widths[column.key] ? 0 : sizeOf(column).share),
    0,
  );
  // What the dragged columns have already claimed. The shares divide what is
  // left over, not the whole width: a percentage in a table resolves against
  // the table itself, so shares that added up to the full width beside a
  // column fixed in pixels would push the last column off the right edge.
  const claimed = shown.reduce(
    (total, column) => total + (widths[column.key] ?? 0),
    0,
  );
  const widthOf = (column: ListColumn<Row>) => {
    const resized = widths[column.key];
    if (resized) {
      return `${resized}px`;
    }
    if (shares <= 0) {
      return undefined;
    }
    const portion = sizeOf(column).share / shares;
    return `calc((100% - ${claimed}px) * ${portion})`;
  };
  // Below this the columns would be squeezed past reading, so the body scrolls
  // sideways from here rather than shrinking any further.
  const floor = shown.reduce(
    (total, column) => total + (widths[column.key] ?? sizeOf(column).min),
    0,
  );
  // What the columns are on screen right now, so a drag can hold the others
  // still at the width they already have. Reads the rendered header rather
  // than recomputing the shares, which is the only thing that knows what a
  // percentage actually came out as.
  const measured = (): Record<string, number> => {
    const cells = head.current?.tHead?.rows[0]?.cells;
    if (!cells) {
      return {};
    }
    return Object.fromEntries(
      shown.map((column, index) => [
        column.key,
        Math.round(cells[index]?.getBoundingClientRect().width ?? 0),
      ]),
    );
  };
  // Where the width goes when the columns do not want all of it — which only
  // happens once the reader has pinned every column narrower than the page.
  // Handing it back to the columns would undo the widths they just set, so a
  // trailing gap takes it. While any column is still on a share this is zero
  // wide, because the shares divide the whole remainder between them.
  const slack = shares === 0 ? undefined : "0px";
  // Which column the server is ordering by, so the header can say so in words
  // rather than leaving a single arrow to carry it.
  const sorted = sort
    ? columns.find((column) => sortState(column, sort.value) !== null)
    : undefined;
  const optional = columns.filter((column) => !column.fixed);
  // What is actually narrowing the set, and so whether an empty result should
  // offer to clear anything. A view is not itself a narrowing: applying one
  // writes its filters into `chosen`, so a view that narrows already reads as
  // narrowed here, and a sort-only view correctly does not. Show-archived is
  // not one either — it WIDENS the set, so an empty list with it on means the
  // records do not exist rather than that a filter is hiding them.
  const filtered =
    Boolean(search?.value) || Object.values(chosen).some(Boolean);

  // Narrowing the set changes what page 1 even means, so go back to it rather
  // than stranding the reader on a page that no longer exists. Clamping alone
  // is not enough: filtering 80 rows down to 5 while on page 2 should show the
  // 5, not the last page that still happens to be valid.
  //
  // Re-ordering counts too: page 2 of a list sorted by name holds different
  // records than page 2 of the same list sorted by date, so the reader is
  // looking at rows they never asked for. Same for widening the set with
  // Show archived.
  //
  // These deps are the TRIGGER, not values the body reads — the body only calls
  // setPage(1). The lint rule cannot see that distinction, and dropping them
  // would break the reset.
  // biome-ignore lint/correctness/useExhaustiveDependencies: trigger-only deps
  useEffect(
    () => setPage(1),
    [
      search?.value,
      chosen,
      activeView,
      perPage,
      sort?.value,
      archived?.checked,
    ],
  );

  // The overlay needs two numbers CSS cannot work out for itself: where the
  // frozen column ends, which a reader changes by dragging its grip, and how
  // tall the visible body is. The column set and the row height are the TRIGGER
  // to re-measure, not values the body reads — hence the suppression.
  // biome-ignore lint/correctness/useExhaustiveDependencies: trigger-only deps
  useEffect(() => {
    const body = scroller.current;
    if (!body) {
      return;
    }
    const measure = () => {
      const frozen = body.querySelector("thead .lt-identity");
      const width = frozen ? frozen.getBoundingClientRect().width : 0;
      body.style.setProperty("--lt-freeze", `${Math.round(width)}px`);
      body.style.setProperty("--lt-body", `${body.clientHeight}px`);
    };
    measure();
    // Measured once wherever the observer is unavailable: the numbers are still
    // right for the render that just happened, they simply stop following a
    // resize. A table is worth more than a shadow that tracks perfectly.
    if (typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(measure);
    observer.observe(body);
    const frozen = body.querySelector("thead .lt-identity");
    if (frozen) {
      observer.observe(frozen);
    }
    return () => observer.disconnect();
  }, [shown.length, dense]);

  const goto = (to: number) => {
    // Stepping past what is loaded asks the server for the next cursor page;
    // the row count grows and the pager grows with it.
    if (to > lastPage && hasMore) {
      onLoadMore?.();
    }
    setPage(Math.max(1, to));
    // The header is sticky and the body scrolls, so without this you land on
    // page 2 already scrolled to its middle.
    if (scroller.current) {
      scroller.current.scrollTop = 0;
    }
  };

  const clearAll = () => {
    search?.onChange("");
    // Whatever is narrowing the list, not only what a chip can name — a filter
    // a view applied without a chip of its own is still one the reader is
    // asking to be rid of.
    for (const key of new Set([
      ...chips.map((chip) => chip.key),
      ...Object.keys(chosen),
    ])) {
      onChipChange?.(key, "");
    }
    onViewChange?.(0);
  };

  const applyView = (index: number) => {
    onViewChange?.(index);
    const view = views[index];
    if (sort) {
      sort.onChange(view?.sort ?? "");
    }
    // Every filter is rewritten, not merged: a view describes the whole filter
    // state, so leaving one from the previous view set would silently narrow
    // the view the user just picked.
    //
    // Keyed on the union rather than on the chips, because a view may narrow by
    // something no chip offers — a lead's minimum score is a number, not a list
    // to pick from — and a loop over the chips alone would drop exactly those,
    // leaving a view that highlights itself and changes nothing.
    const keys = new Set([
      ...chips.map((chip) => chip.key),
      ...Object.keys(chosen),
      ...Object.keys(view?.filters ?? {}),
    ]);
    for (const key of keys) {
      onChipChange?.(key, view?.filters?.[key] ?? "");
    }
  };

  return (
    <ListSurface
      views={views}
      activeView={activeView}
      onViewChange={applyView}
      count={
        !pending && (
          <CountLine
            unit={unit}
            first={from + 1}
            last={from + pageRows.length}
            total={rows.length}
            more={hasMore}
            sortedBy={sorted?.header}
          />
        )
      }
      action={action}
      caption={caption}
      note={note}
      search={search}
      chips={chips}
      chosen={chosen}
      onChipChange={onChipChange}
      archived={archived}
      tools={
        <>
          {tools}
          <TableTools
            optional={optional}
            hidden={hidden}
            onToggleColumn={(key) =>
              setHidden((prev) => {
                const next = new Set(prev);
                if (next.has(key)) {
                  next.delete(key);
                } else {
                  next.add(key);
                }
                return next;
              })
            }
            dense={dense}
            onDense={() => setDense(!dense)}
            open={columnsOpen}
            setOpen={setColumnsOpen}
          />
        </>
      }
      footer={
        <>
          {footer && <div className="lt-agg">{footer}</div>}
          <Pager
            current={current}
            lastPage={lastPage}
            hasMore={hasMore}
            perPage={perPage}
            onGoto={goto}
            onPerPage={setPerPage}
          />
        </>
      }
    >
      <div
        className={`lt-scroll${shifted ? " shifted" : ""}`}
        ref={scroller}
        onScroll={(event) => {
          const next = event.currentTarget.scrollLeft > 0;
          if (next !== shifted) {
            setShifted(next);
          }
        }}
      >
        {/* One element for the frozen edge's shadow. It cannot hang off the
            cells: a shadow per cell starts and stops at each cell's own box, so
            the column ends up wearing a shadow per row with a seam at every
            divider. This sticks to the left of the scrolling body and spans its
            visible height, which is all that is ever seen of it. */}
        <div className="lt-freeze" aria-hidden="true" />
        <table
          ref={head}
          className={`lt-table${dense ? " dense" : ""}`}
          role="table"
          style={{ minWidth: `${floor}px` }}
        >
          {/* The widths live here rather than on the header cells: under fixed
              layout a col wins over the cell below it, so one place decides
              and a resized column cannot be quietly overruled. */}
          <colgroup>
            {shown.map((column) => (
              <col key={column.key} style={{ width: widthOf(column) }} />
            ))}
            <col style={{ width: slack }} />
          </colgroup>
          <thead role="rowgroup">
            <tr role="row">
              {shown.map((column) => (
                <HeaderCell
                  key={column.key}
                  column={column}
                  sort={sort}
                  state={sort ? sortState(column, sort.value) : null}
                  className={cellClass(column)}
                  // Dragging an edge moves that edge. The other columns are
                  // pinned at what they currently measure first, so they stay
                  // where they are and the table itself grows or shrinks —
                  // rather than every column re-dividing the row because one
                  // of them changed.
                  onResizeStart={() =>
                    applyWidths({ ...measured(), ...live.current })
                  }
                  onResize={(key, width) =>
                    applyWidths({ ...live.current, [key]: width })
                  }
                  onResizeEnd={() => writeWidths(widthsKey, live.current)}
                />
              ))}
              <td className="lt-slack" aria-hidden="true" />
            </tr>
          </thead>
          <tbody role="rowgroup">
            {pending &&
              PLACEHOLDER_ROWS.map((placeholder) => (
                <tr key={placeholder} className="lt-loading" role="row">
                  {shown.map((column) => (
                    <td key={column.key} role="cell">
                      <span className="lt-bone" />
                    </td>
                  ))}
                  <td className="lt-slack" aria-hidden="true" />
                </tr>
              ))}
            {!pending &&
              pageRows.map((row) => (
                <tr
                  key={rowKey(row)}
                  role="row"
                  className={onRowClick ? "lt-rowlink" : undefined}
                  onClick={onRowClick ? () => onRowClick(row) : undefined}
                >
                  {shown.map((column) => (
                    <td
                      key={column.key}
                      role="cell"
                      className={cellClass(column)}
                      // On a phone the rows become cards and the header row is
                      // gone, so each value carries its own label. The identity
                      // cell is the card's heading and needs none.
                      data-label={column.fixed ? undefined : column.header}
                    >
                      {column.fixed && rowHref ? (
                        // The identity cell is a real link, so the row can be
                        // opened the ways a link can: a new tab, a new window,
                        // a bookmark, or the keyboard. Only the default click
                        // is stopped from reaching the row's own handler —
                        // preventing the anchor instead would navigate the
                        // current page while the new tab opens too.
                        <a
                          className="lt-cellink"
                          href={rowHref(row)}
                          onClick={(event) => event.stopPropagation()}
                        >
                          {column.cell(row)}
                        </a>
                      ) : (
                        column.cell(row)
                      )}
                    </td>
                  ))}
                  <td className="lt-slack" aria-hidden="true" />
                </tr>
              ))}
            {!pending && problem && (
              <tr className="lt-empty" role="row">
                <td colSpan={shown.length + 1} role="cell">
                  {problem}
                </td>
              </tr>
            )}
            {!pending && !problem && rows.length === 0 && (
              <tr className="lt-empty" role="row">
                <td colSpan={shown.length + 1} role="cell">
                  {filtered ? (
                    <>
                      {t("table.noMatches", { unit })}{" "}
                      <button
                        type="button"
                        className="lt-linkish"
                        onClick={clearAll}
                      >
                        {t("table.clearFilters")}
                      </button>
                    </>
                  ) : (
                    <>
                      {t("table.none", { unit })}
                      {emptyNote && (
                        <p
                          className="t-caption"
                          style={{ marginTop: "var(--space-2)" }}
                        >
                          {emptyNote}
                        </p>
                      )}
                    </>
                  )}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </ListSurface>
  );
}

/**
 * Numeric alignment, and the marker the phone layout uses to promote the
 * identity column to the card's heading. Spelled once so th and td agree.
 */
function cellClass<Row>(column: ListColumn<Row>): string | undefined {
  const names = [
    column.numeric ? "lt-num" : "",
    column.fixed ? "lt-identity" : "",
  ].filter(Boolean);
  return names.length > 0 ? names.join(" ") : undefined;
}

/**
 * A column header. Sortable only when the column names a server sort field,
 * so the arrow never appears on a column the API cannot order by.
 */
function HeaderCell<Row>({
  column,
  sort,
  state,
  className,
  onResizeStart,
  onResize,
  onResizeEnd,
}: Readonly<{
  column: ListColumn<Row>;
  sort?: SortControl;
  state: "asc" | "desc" | null;
  className?: string;
  onResizeStart: () => void;
  onResize: (key: string, width: number) => void;
  onResizeEnd: () => void;
}>) {
  const t = useT();
  const grip = (
    <ResizeGrip
      onStart={onResizeStart}
      onResize={(next) => onResize(column.key, next)}
      onEnd={onResizeEnd}
    />
  );
  if (!column.sort || !sort) {
    return (
      <th className={className} role="columnheader">
        {column.header}
        {grip}
      </th>
    );
  }
  const field = column.sort;
  // Unsorted, a number column almost always wants its biggest value first.
  const next =
    state === "asc"
      ? `-${field}`
      : state === "desc"
        ? field
        : column.numeric
          ? `-${field}`
          : field;
  return (
    <th
      className={className}
      role="columnheader"
      aria-sort={
        state === "asc"
          ? "ascending"
          : state === "desc"
            ? "descending"
            : undefined
      }
    >
      <button
        type="button"
        className={`lt-sort${state ? " on" : ""}`}
        aria-label={t("table.sortBy", { column: column.header })}
        onClick={() => sort.onChange(next)}
      >
        {column.header}
        <ChevronDown
          size={12}
          strokeWidth={2}
          aria-hidden="true"
          className={`lt-arrow${state === "asc" ? " up" : ""}`}
        />
      </button>
      {grip}
    </th>
  );
}

/**
 * The handle on a column's trailing edge, dragged with a pointer.
 *
 * Deliberately hidden from assistive technology. A labelled control inside a
 * `th` joins that header's accessible name, so every column would announce as
 * "Value, resize the Value column" — the price of a keyboard affordance here is
 * making every header read worse for the people who rely on the name most. The
 * column picker already gives keyboard users control over what a table shows,
 * and a width is presentation rather than content.
 */
function ResizeGrip({
  onStart,
  onResize,
  onEnd,
}: Readonly<{
  onStart: () => void;
  onResize: (width: number) => void;
  onEnd: () => void;
}>) {
  const drag = useRef<{ startX: number; startWidth: number } | null>(null);
  const self = useRef<HTMLSpanElement>(null);
  const cellWidth = (target: HTMLElement) =>
    target.closest("th")?.getBoundingClientRect().width ?? MIN_COLUMN_WIDTH;

  return (
    <span
      className="lt-grip"
      ref={self}
      aria-hidden="true"
      // The grip lives inside the header button's cell; without this a drag or
      // a click on it would also sort the column it is resizing.
      onClick={(event) => event.stopPropagation()}
      onPointerDown={(event) => {
        event.stopPropagation();
        event.preventDefault();
        const target: HTMLElement = event.currentTarget;
        target.setPointerCapture(event.pointerId);
        drag.current = {
          startX: event.clientX,
          startWidth: cellWidth(target),
        };
        onStart();
      }}
      onPointerMove={(event) => {
        const from = drag.current;
        if (!from) {
          return;
        }
        onResize(
          Math.max(
            MIN_COLUMN_WIDTH,
            from.startWidth + event.clientX - from.startX,
          ),
        );
      }}
      onPointerUp={(event) => {
        const dragging = drag.current !== null;
        drag.current = null;
        event.currentTarget.releasePointerCapture(event.pointerId);
        if (dragging) {
          onEnd();
        }
      }}
    />
  );
}

/**
 * The table-specific half of the toolbar: the column picker and the density
 * toggle. Passed into ListSurface's `tools` slot — the surface itself has no
 * notion of a column or a row density, only that callers may want a slot
 * there.
 */
function TableTools<Row>({
  optional,
  hidden,
  onToggleColumn,
  dense,
  onDense,
  open,
  setOpen,
}: Readonly<{
  optional: readonly ListColumn<Row>[];
  hidden: ReadonlySet<string>;
  onToggleColumn: (key: string) => void;
  dense: boolean;
  onDense: () => void;
  open: boolean;
  setOpen: (next: boolean) => void;
}>) {
  const t = useT();
  return (
    <>
      {optional.length > 0 && (
        <span className="lt-menu-wrap">
          <button
            type="button"
            className="lt-btn"
            aria-expanded={open}
            onClick={() => setOpen(!open)}
          >
            <Columns3 size={13} strokeWidth={1.5} aria-hidden="true" />
            {t("table.columns")}
          </button>
          <Menu open={open} head={t("table.shownColumns")} align="right">
            {optional.map((column) => (
              <button
                type="button"
                key={column.key}
                className={`lt-mi${hidden.has(column.key) ? "" : " on"}`}
                aria-pressed={!hidden.has(column.key)}
                onClick={() => onToggleColumn(column.key)}
              >
                <span className="lt-cb">
                  <Check size={10} strokeWidth={3} aria-hidden="true" />
                </span>
                {column.header}
              </button>
            ))}
          </Menu>
        </span>
      )}

      <button
        type="button"
        className={`lt-btn${dense ? " on" : ""}`}
        aria-pressed={dense}
        onClick={onDense}
      >
        <Rows3 size={13} strokeWidth={1.5} aria-hidden="true" />
        {t("table.compact")}
      </button>
    </>
  );
}

/**
 * The pager: every loaded page as its own button, prev/next either side, and the
 * page size on the right. Next stays enabled on the last loaded page while the
 * cursor still has rows to give, which is how the set grows without a total.
 */
function Pager({
  current,
  lastPage,
  hasMore,
  perPage,
  onGoto,
  onPerPage,
}: Readonly<{
  current: number;
  lastPage: number;
  hasMore: boolean;
  perPage: number;
  onGoto: (to: number) => void;
  onPerPage: (next: number) => void;
}>) {
  const t = useT();
  return (
    <div className={`lt-foot${lastPage === 1 && !hasMore ? " single" : ""}`}>
      <div className="lt-pager">
        <button
          type="button"
          disabled={current === 1}
          onClick={() => onGoto(current - 1)}
        >
          {t("table.prev")}
        </button>
        {Array.from({ length: lastPage }, (_, index) => index + 1).map(
          (number) => (
            <button
              type="button"
              key={number}
              className={number === current ? "on" : undefined}
              aria-current={number === current ? "page" : undefined}
              onClick={() => onGoto(number)}
            >
              {number}
            </button>
          ),
        )}
        <button
          type="button"
          disabled={current === lastPage && !hasMore}
          onClick={() => onGoto(current + 1)}
        >
          {t("table.next")}
        </button>
      </div>
      <span className="lt-perpage">
        <Select
          aria-label={t("table.rowsPerPage")}
          value={String(perPage)}
          onChange={(next) => onPerPage(Number(next))}
          options={PAGE_SIZES.map((size) => ({
            value: String(size),
            label: t("table.perPage", { count: size }),
          }))}
        />
      </span>
    </div>
  );
}
