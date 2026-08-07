import { Check, Filter, MoreVertical, Plus, Search } from "lucide-react";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { useT } from "../i18n";
import "./listtable.css";

// The value list's own search box debounces on the same rhythm as the list
// search (listquery.tsx's SEARCH_DEBOUNCE_MS) — kept as a sibling constant
// rather than imported, since that one is a screen-binding concern and this
// is a design-system one; the number is the contract, not the module.
const FILTER_SEARCH_DEBOUNCE_MS = 250;

// The list surface's shell: the header (view tabs, count, primary action),
// the caption and the toolbar (search, filter chips, an archived toggle and
// whatever presentation dials the caller owns). It knows nothing about
// columns, density, paging or rows — those stay in listtable.tsx, and the
// pipeline board reuses this same shell for the same reason a contact list
// and a lead list do: the dials belong to the surface, not to what fills it.
//
// A view tab reports only its index. What a view MEANS — a sort, a set of
// filters, a stage — is the caller's to decide, which is what lets the board
// define views that are not a sort/filter preset at all.

/**
 * One filter chip. Single-select by design: the list endpoints take one value
 * per filter param, so a multi-select chip would compose a query the API
 * cannot answer.
 */
export type ListChip = {
  key: string;
  label: string;
  /** The "no filter" entry, which is also how a chosen value is cleared. */
  allLabel: string;
  options: readonly { value: string; label: string }[];
  /**
   * A relation filter too large to list whole (a workspace's companies): when
   * present, the value step searches this instead of walking `options`. The
   * "all" entry still clears the filter, and `options` stays required so a
   * chip declared without `search` needs no separate shape.
   */
  search?: (
    query: string,
  ) => Promise<readonly { value: string; label: string }[]>;
};

/** A saved view: a named tab whose meaning is entirely the caller's. */
export type ListView = {
  label: string;
  sort?: string;
  filters?: Readonly<Record<string, string>>;
};

/**
 * One attribute the sort pill can order by. ListSurface never learns what a
 * column is — the table hands it this plain list, keyed by the same server
 * sort field a column header already uses.
 */
export type SortControl = {
  /** The server sort string, e.g. `-created_at`. */
  value: string;
  onChange: (next: string) => void;
};

const EMPTY_FILTERS: Readonly<Record<string, string>> = {};

export function ListSurface({
  views = [],
  activeView = 0,
  onViewChange,
  count,
  action,
  caption,
  note,
  search,
  chips = [],
  chosen = EMPTY_FILTERS,
  onChipChange,
  archived,
  tools,
  children,
  footer,
}: Readonly<{
  views?: readonly ListView[];
  activeView?: number;
  onViewChange?: (index: number) => void;
  /** The reader's-eye summary line — the table's range, the board's deal count. */
  count?: ReactNode;
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
  /** Omit for a list whose GET has no `q` param; the box is then not rendered. */
  search?: { value: string; onChange: (next: string) => void };
  chips?: readonly ListChip[];
  chosen?: Readonly<Record<string, string>>;
  /** Called with "" to clear. */
  onChipChange?: (key: string, value: string) => void;
  /** Omit for a body with no server sort at all (the board, an overlay read). */
  /** The attributes the "Sorted by" pill offers. A column header stays the
   * other route to the same state — this is not its replacement. */
  archived?: { checked: boolean; onChange: (next: boolean) => void };
  /**
   * Controls that change HOW the body is shown rather than what is in it, kept
   * to the right: the table's Columns and Compact buttons, the board/table
   * switch, the pipeline being looked at.
   */
  tools?: ReactNode;
  /** The body: a scrolling table, a Kanban board, whatever this surface holds. */
  children: ReactNode;
  /** Under the body — the table's pager, or nothing at all. */
  footer?: ReactNode;
}>) {
  const [openMenu, setOpenMenu] = useState<string | null>(null);
  useCloseOnOutsideClick(() => setOpenMenu(null));

  return (
    <div className="lt">
      <div className="lt-head">
        {views.length > 0 && (
          <div className="lt-views">
            {views.map((view, index) => (
              <button
                type="button"
                key={view.label}
                className={`lt-vtab${index === activeView ? " on" : ""}`}
                aria-pressed={index === activeView}
                onClick={() => onViewChange?.(index)}
              >
                {view.label}
              </button>
            ))}
          </div>
        )}
        {/* Wrapped here rather than by the caller, so every body's count sits
            in the same place: pushed right of the view tabs, left of the
            action. */}
        {/* Always rendered, even with nothing to say yet: this element carries
            the margin that pushes itself and the action to the right, so a
            count that only arrives with the rows would otherwise let the action
            start at the left and jump across once they land. */}
        <span className="lt-count">{count}</span>
        {action}
      </div>

      {caption && <p className="lt-caption">{caption}</p>}

      <Toolbar
        search={search}
        chips={chips}
        chosen={chosen}
        onChipChange={onChipChange}
        note={note}
        archived={archived}
        tools={tools}
        openMenu={openMenu}
        setOpenMenu={setOpenMenu}
      />

      {children}

      {footer}
    </div>
  );
}

/**
 * The "all" entry plus every option, shared by the Filter button's value
 * step and an applied chip's own reopened menu — the same list either way,
 * since both end at picking one value for one attribute.
 */
function FilterValueList({
  chip,
  value,
  onPick,
}: Readonly<{
  chip: ListChip;
  value: string;
  onPick: (value: string) => void;
}>) {
  return (
    <>
      <button
        type="button"
        className={`lt-mi${value ? "" : " on"}`}
        onClick={() => onPick("")}
      >
        <span className="lt-cb">
          <Check size={10} strokeWidth={3} aria-hidden="true" />
        </span>
        {chip.allLabel}
      </button>
      {chip.options.map((option) => (
        <button
          type="button"
          key={option.value}
          className={`lt-mi${option.value === value ? " on" : ""}`}
          onClick={() => onPick(option.value)}
        >
          <span className="lt-cb">
            <Check size={10} strokeWidth={3} aria-hidden="true" />
          </span>
          {option.label}
        </button>
      ))}
    </>
  );
}

/**
 * A relation filter's value step when the attribute is too large to list
 * whole: a text box in place of the fixed options, searching on debounce.
 * The four honest states a search can be in — nothing typed, in flight, no
 * matches, failed — are each their own line, and a query in flight keeps the
 * previous results on screen rather than clearing the menu out from under
 * the reader while the next answer is still coming back.
 */
function AsyncFilterValueList({
  chip,
  value,
  onPick,
}: Readonly<{
  chip: ListChip;
  value: string;
  onPick: (value: string) => void;
}>) {
  const t = useT();
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<
    readonly { value: string; label: string }[]
  >([]);
  const [pending, setPending] = useState(false);
  const [failed, setFailed] = useState(false);
  const search = chip.search;

  useEffect(() => {
    if (!search) {
      return;
    }
    if (!query) {
      // Nothing typed yet: no request, and no stale hits from an
      // abandoned query linger once the box is cleared back to empty.
      setResults([]);
      setPending(false);
      setFailed(false);
      return;
    }
    let cancelled = false;
    setPending(true);
    const timer = setTimeout(() => {
      search(query)
        .then((next) => {
          if (cancelled) {
            return;
          }
          setResults(next);
          setFailed(false);
          setPending(false);
        })
        .catch(() => {
          if (cancelled) {
            return;
          }
          setFailed(true);
          setPending(false);
        });
    }, FILTER_SEARCH_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [query, search]);

  if (!search) {
    return null;
  }

  return (
    <>
      <button
        type="button"
        className={`lt-mi${value ? "" : " on"}`}
        onClick={() => onPick("")}
      >
        <span className="lt-cb">
          <Check size={10} strokeWidth={3} aria-hidden="true" />
        </span>
        {chip.allLabel}
      </button>
      <label className="lt-fsearch">
        <span className="sr-only">
          {t("table.filterValueSearch", { filter: chip.label })}
        </span>
        <input
          className="lt-fsearch-input"
          value={query}
          placeholder={t("table.filterValueSearch", { filter: chip.label })}
          onChange={(event) => setQuery(event.target.value)}
        />
      </label>
      {!query && (
        <p className="lt-fvalue-status">{t("table.filterTypeToSearch")}</p>
      )}
      {query && pending && (
        <p className="lt-fvalue-status">{t("table.filterSearching")}</p>
      )}
      {query && failed && (
        <p className="lt-fvalue-status error">
          {t("table.filterSearchFailed")}
        </p>
      )}
      {query && !pending && !failed && results.length === 0 && (
        <p className="lt-fvalue-status">{t("table.filterNoMatches")}</p>
      )}
      {results.map((option) => (
        <button
          type="button"
          key={option.value}
          className={`lt-mi${option.value === value ? " on" : ""}`}
          onClick={() => onPick(option.value)}
        >
          <span className="lt-cb">
            <Check size={10} strokeWidth={3} aria-hidden="true" />
          </span>
          {option.label}
        </button>
      ))}
    </>
  );
}

/** Either value step a filter row's value segment can open, keyed by whether
 * the chip declares a search source. */
function ChipValueList({
  chip,
  value,
  onPick,
}: Readonly<{
  chip: ListChip;
  value: string;
  onPick: (value: string) => void;
}>) {
  return chip.search ? (
    <AsyncFilterValueList chip={chip} value={value} onPick={onPick} />
  ) : (
    <FilterValueList chip={chip} value={value} onPick={onPick} />
  );
}

/**
 * The one Filter trigger: a searchable list of the attributes not yet
 * applied, then — once one is picked — that attribute's value list. Reads as
 * the "Filter" button (its icon and label) until a filter exists, and as a
 * bare "+" once the first applied row is standing — the vocabulary the
 * button offers hasn't changed, only what it's called once there is
 * something beside it to add to. Its own component because the two steps,
 * plus the search text narrowing the first one, were most of the toolbar's
 * cognitive complexity.
 */
function FilterMenu({
  chips,
  chosen,
  onChipChange,
  open,
  onToggle,
  hasApplied,
}: Readonly<{
  chips: readonly ListChip[];
  chosen: Readonly<Record<string, string>>;
  onChipChange?: (key: string, value: string) => void;
  open: boolean;
  onToggle: () => void;
  hasApplied: boolean;
}>) {
  const t = useT();
  const [attributeKey, setAttributeKey] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  // A closed menu always reopens at the attribute search, never wherever the
  // reader last left it — the button says "Filter", not "Filter: Status".
  useEffect(() => {
    if (!open) {
      setAttributeKey(null);
      setQuery("");
    }
  }, [open]);

  // Only the attributes with no filter row of their own yet: once a filter is
  // applied it stands as its own row (FilterRow below), so offering it again
  // here would be a second way to reach the same value, through a menu that
  // no longer names the attribute it belongs to.
  const addable = chips.filter((chip) => !chosen[chip.key]);
  const attribute = addable.find((chip) => chip.key === attributeKey);
  const narrowed = addable.filter((chip) =>
    chip.label.toLowerCase().includes(query.toLowerCase()),
  );

  return (
    <span className="lt-menu-wrap">
      <button
        type="button"
        className="lt-btn"
        aria-expanded={open}
        aria-label={hasApplied ? t("table.addFilter") : undefined}
        onClick={onToggle}
      >
        {hasApplied ? (
          <Plus size={13} strokeWidth={1.8} aria-hidden="true" />
        ) : (
          <>
            <Filter size={13} strokeWidth={1.6} aria-hidden="true" />
            {t("table.filter")}
          </>
        )}
      </button>
      <Menu open={open} head={attribute ? attribute.label : t("table.filter")}>
        {attribute ? (
          <ChipValueList
            chip={attribute}
            value={chosen[attribute.key] ?? ""}
            onPick={(value) => {
              onChipChange?.(attribute.key, value);
              onToggle();
            }}
          />
        ) : (
          <>
            <label className="lt-fsearch">
              <span className="sr-only">{t("table.filterSearch")}</span>
              <input
                className="lt-fsearch-input"
                value={query}
                placeholder={t("table.filterSearch")}
                onChange={(event) => setQuery(event.target.value)}
              />
            </label>
            {narrowed.map((chip) => (
              <button
                type="button"
                key={chip.key}
                className="lt-mi"
                onClick={() => setAttributeKey(chip.key)}
              >
                {chip.label}
              </button>
            ))}
          </>
        )}
      </Menu>
    </span>
  );
}

/**
 * The condition segment of an applied filter row. Its menu offers exactly
 * one entry — the list endpoints compare for equality only — but it is a
 * real menu rather than a disabled label, so a second condition can join it
 * later without a shape change here.
 */
function FilterConditionMenu({
  open,
  onToggle,
}: Readonly<{ open: boolean; onToggle: () => void }>) {
  const t = useT();
  return (
    <span className="lt-menu-wrap">
      <button
        type="button"
        className="lt-frow-seg"
        aria-expanded={open}
        onClick={onToggle}
      >
        {t("table.filterIs")}
      </button>
      <Menu open={open} head={t("table.filterCondition")}>
        <button type="button" className="lt-mi on" onClick={onToggle}>
          <span className="lt-cb">
            <Check size={10} strokeWidth={3} aria-hidden="true" />
          </span>
          {t("table.filterIs")}
        </button>
      </Menu>
    </span>
  );
}

/** Which segment of a filter row currently owns the one open popover. */
type FilterRowSegment = "cond" | "value" | "menu";

function isFilterRowSegment(value: string): value is FilterRowSegment {
  return value === "cond" || value === "value" || value === "menu";
}

/**
 * Reads which segment (if any) of one row's own popover the toolbar's single
 * `openMenu` key names — a plain string key rather than per-row state, so
 * the existing one-menu-open-at-a-time behaviour covers filter rows too.
 */
function openRowSegment(
  openMenu: string | null,
  key: string,
): FilterRowSegment | null {
  const prefix = `row:${key}:`;
  if (!openMenu?.startsWith(prefix)) {
    return null;
  }
  const segment = openMenu.slice(prefix.length);
  return isFilterRowSegment(segment) ? segment : null;
}

/**
 * An applied filter, read as one row: the attribute, its condition, its
 * value, and a "⋮" menu to delete it — a compound pill rather than a bare
 * chip, so a second condition (already possible in FilterConditionMenu) has
 * somewhere to live without the row changing shape again.
 */
function FilterRow({
  chip,
  value,
  openSegment,
  onToggleSegment,
  onPick,
  onDelete,
}: Readonly<{
  chip: ListChip;
  value: string;
  openSegment: FilterRowSegment | null;
  onToggleSegment: (segment: FilterRowSegment) => void;
  onPick: (value: string) => void;
  onDelete: () => void;
}>) {
  const t = useT();
  const active = chip.options.find((option) => option.value === value);
  const valueLabel = active?.label ?? value;
  return (
    <fieldset className="lt-frow" aria-label={`${chip.label}: ${valueLabel}`}>
      <span className="lt-frow-attr">{chip.label}</span>
      <FilterConditionMenu
        open={openSegment === "cond"}
        onToggle={() => onToggleSegment("cond")}
      />
      <span className="lt-menu-wrap">
        <button
          type="button"
          className="lt-frow-seg lt-frow-value"
          aria-expanded={openSegment === "value"}
          onClick={() => onToggleSegment("value")}
        >
          {valueLabel}
        </button>
        <Menu open={openSegment === "value"} head={chip.label}>
          <ChipValueList chip={chip} value={value} onPick={onPick} />
        </Menu>
      </span>
      <span className="lt-menu-wrap">
        <button
          type="button"
          className="lt-frow-more"
          aria-label={t("table.filterMore", { filter: chip.label })}
          aria-expanded={openSegment === "menu"}
          onClick={() => onToggleSegment("menu")}
        >
          <MoreVertical size={14} strokeWidth={1.8} aria-hidden="true" />
        </button>
        <Menu open={openSegment === "menu"} head={chip.label} align="right">
          <button type="button" className="lt-mi" onClick={onDelete}>
            {t("table.deleteFilter")}
          </button>
        </Menu>
      </span>
    </fieldset>
  );
}

export function Menu({
  open,
  head,
  align = "left",
  children,
}: Readonly<{
  open: boolean;
  head: string;
  align?: "left" | "right";
  children: ReactNode;
}>) {
  return (
    <div
      className={`lt-menu${open ? " open" : ""}${align === "right" ? " right" : ""}`}
      // A closed menu leaves the tab order entirely; otherwise Tab walks every
      // option of every filter before reaching the body below it.
      inert={!open}
    >
      <div className="lt-mhead">{head}</div>
      {children}
    </div>
  );
}

/**
 * Click anywhere that is not a menu or its trigger, and the menus close.
 *
 * The handler is read through a ref so the listener is attached once for the
 * life of the surface rather than being torn down and re-added on every
 * render — a document-level listener that churns is a real cost on a page
 * this size.
 */
export function useCloseOnOutsideClick(close: () => void) {
  const latest = useRef(close);
  latest.current = close;
  useEffect(() => {
    const onClick = (event: MouseEvent) => {
      // An event target is not necessarily an element — a click can land on a
      // text node — and only an element can be asked what it sits inside.
      const target = event.target;
      if (!(target instanceof Element)) {
        return;
      }
      // A click that re-rendered the menu — picking an attribute swaps the
      // attribute list for that attribute's values — leaves its own target
      // detached by the time this listener runs, and a detached node has no
      // ancestors to find. That is a click INSIDE the menu, not outside it, so
      // treating it as outside closed the menu on the very step that should
      // have advanced it.
      if (!target.isConnected) {
        return;
      }
      if (!target.closest(".lt-menu-wrap, .lt-menu")) {
        latest.current();
      }
    };
    document.addEventListener("click", onClick);
    return () => document.removeEventListener("click", onClick);
  }, []);
}

/**
 * What the reader is looking at: the visible range, the size of the set behind
 * it, and the ordering. Stated in words rather than left to a lone arrow, since
 * a sort applied from a saved view has no arrow to notice.
 */
export function CountLine({
  unit,
  first,
  last,
  total,
  more = false,
  sortedBy,
}: Readonly<{
  unit: string;
  first: number;
  last: number;
  total: number;
  /**
   * The server holds pages past the ones counted. `total` is then how many rows
   * have been LOADED, not how many exist — a keyset cursor never learns the
   * second number — and the line has to say which of the two it is rather than
   * report a total the client cannot know.
   */
  more?: boolean;
  sortedBy?: string;
}>) {
  const t = useT();
  // The surface owns the count's placement; this only says what it reads.
  return (
    <>
      {total === 0 && t("table.none", { unit })}
      {total > 0 &&
        (more
          ? t("table.rangeLoaded", { first, last, count: total, unit })
          : t("table.range", { first, last, count: total, unit }))}
      {sortedBy && `, ${t("table.sortedBy", { column: sortedBy })}`}
    </>
  );
}

/**
 * The controls row, read as two halves. What narrows the list — search, chips,
 * the body's own filter pickers — is on the left; what changes how the list is
 * displayed is on the right. A reader looking to cut the set down never has to
 * scan the whole row to find out which controls do that.
 */
function Toolbar({
  search,
  chips,
  chosen,
  onChipChange,
  note,
  archived,
  tools,
  openMenu,
  setOpenMenu,
}: Readonly<{
  search?: { value: string; onChange: (next: string) => void };
  chips: readonly ListChip[];
  chosen: Readonly<Record<string, string>>;
  onChipChange?: (key: string, value: string) => void;
  note?: ReactNode;
  archived?: { checked: boolean; onChange: (next: boolean) => void };
  tools?: ReactNode;
  openMenu: string | null;
  setOpenMenu: (next: string | null) => void;
}>) {
  const t = useT();
  const applied = chips.filter((chip) => chosen[chip.key]);
  return (
    <div className="lt-tools">
      {search && (
        <label className="lt-search">
          <Search size={13} strokeWidth={1.6} aria-hidden="true" />
          <span className="sr-only">{t("list.search")}</span>
          <input
            className="lt-search-input"
            value={search.value}
            placeholder={t("list.search")}
            onChange={(event) => search.onChange(event.target.value)}
          />
        </label>
      )}

      {applied.map((chip) => (
        <FilterRow
          key={chip.key}
          chip={chip}
          value={chosen[chip.key] ?? ""}
          openSegment={openRowSegment(openMenu, chip.key)}
          onToggleSegment={(next) => {
            const key = `row:${chip.key}:${next}`;
            setOpenMenu(openMenu === key ? null : key);
          }}
          onPick={(value) => {
            onChipChange?.(chip.key, value);
            setOpenMenu(null);
          }}
          onDelete={() => {
            onChipChange?.(chip.key, "");
            setOpenMenu(null);
          }}
        />
      ))}

      {chips.length > 0 && (
        <FilterMenu
          chips={chips}
          chosen={chosen}
          onChipChange={onChipChange}
          open={openMenu === "filter"}
          onToggle={() => setOpenMenu(openMenu === "filter" ? null : "filter")}
          hasApplied={applied.length > 0}
        />
      )}

      {note && <span className="lt-note">{note}</span>}

      <span className="lt-spacer" />

      {archived && (
        <label className="lt-toggle">
          <input
            type="checkbox"
            checked={archived.checked}
            onChange={(event) => archived.onChange(event.target.checked)}
          />
          {t("list.showArchived")}
        </label>
      )}

      {tools}
    </div>
  );
}
