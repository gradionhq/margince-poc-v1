import { useInfiniteQuery } from "@tanstack/react-query";
import {
  type Dispatch,
  type ReactNode,
  type SetStateAction,
  useEffect,
  useState,
} from "react";
import { FIRST_PAGE } from "../api/client";
import { navigate, type Route, routeHash } from "../app/router";
import { Button } from "../design-system/atoms";
import {
  type ListChip,
  type ListColumn,
  type ListSelection,
  ListTable as ListSurface,
  type ListView,
} from "../design-system/listtable";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, useMe, useSorMode } from "./common";
import { rosterReading, useRoster, useRosterPartial } from "./entityref";

// The shared list foundation (P-14): every list screen sends the rich
// q/sort/cursor/include_archived/filter vocabulary instead of a flat
// limit:50, and paginates by keyset (never offset — the workspace's rows
// mutate under a live feed). useListQuery owns the react-query wiring;
// ListTable binds that query to the list surface, which owns the controls.

/**
 * How long a typed filter settles before it becomes a request.
 *
 * Exported because it is a product decision about what a keystroke costs, not
 * a detail of this file: any surface that turns typing into a server query
 * reads it here rather than picking its own number, so the app has one answer
 * to "how responsive is a filter" instead of one per screen.
 */
export const SEARCH_DEBOUNCE_MS = 250;

export type ListQuery = {
  q: string;
  sort: string;
  includeArchived: boolean;
  filters: Record<string, string>;
  /**
   * Rows per page, chosen by the reader in the table footer. It is the size of
   * a RENDERED page, not of a read: one read fetches several of these at once
   * (see `listFetchLimit`) so the pager can offer them as numbers the reader
   * reaches without waiting.
   */
  perPage: number;
};

/** The page sizes the footer offers; the first is the default. */
export const LIST_PAGE_SIZES = [25, 50, 100] as const;

/** The most rows one list read may ask for (the contract's CAP-PAGE ceiling). */
const MAX_ROWS_PER_READ = 200;

/**
 * How many rows to fetch for a footer page size of `perPage`: as many WHOLE
 * rendered pages as one read is allowed to carry.
 *
 * A keyset cursor can only walk forward one read at a time, so a pager can
 * only number the pages already in hand. Reading a page at a time therefore
 * numbers exactly one, and every further number has to be earned by a round
 * trip — which is what made a second page appear only after the reader had
 * been told none existed. Reading in whole multiples instead puts several
 * numbered pages in hand at once, each of them a slice of ONE response, so
 * they are also one consistent snapshot: no row shows up twice and none is
 * skipped between them.
 *
 * Whole multiples matter. A remainder would leave a last page shorter than the
 * size the footer names, and asking for more than the ceiling gets silently
 * clamped, leaving the pager offering numbers the response has no rows for.
 */
export function listFetchLimit(perPage: number): number {
  return Math.max(1, Math.floor(MAX_ROWS_PER_READ / perPage)) * perPage;
}

export type ListPage<Row> = {
  data: Row[];
  page: { next_cursor: string | null; has_more: boolean };
};

/**
 * One value a filter chip offers: named by a message key when the vocabulary
 * is the screen's own, or by the server's text when it is an administered list
 * (lead sources, for one) the screen only relays.
 */
export type FilterOption =
  | { value: string; label: MessageKey }
  | { value: string; text: string };

/** A filter chip, declared in the screen's own message keys. */
export type FilterSpec = {
  key: string;
  label: MessageKey;
  /** The "no filter" entry, which is also how a chosen value is cleared. */
  allLabel: MessageKey;
  options: FilterOption[];
};

/** A screen's own preset: a named sort + filter preset, shown as a tab. */
export type ViewSpec = {
  label: MessageKey;
  sort?: string;
  filters?: Record<string, string>;
};

/**
 * The reader's own saved view, as a tab: the WHOLE list state it claims, plus
 * the id that says which view it is.
 *
 * Both halves are load-bearing. A tab carrying only the sort and the filters
 * restores a view saved with "Show archived" on as a view with it off — and
 * then the tab does not even match itself, because the archived toggle is part
 * of what a view claims about the list. The search is here for the same reason:
 * it narrows the list exactly as a filter does, so a tab that omitted it would
 * light while a search nobody saved was narrowing the rows underneath it. And
 * the id is what survives a rename or a newly created view: `/views` answers
 * ordered by name, so a position is a different view as soon as one is inserted
 * ahead of it.
 *
 * `perPage` is optional because a stored blob need not claim one: a view that
 * saved a page size asks for it, and a view that saved none leaves the reader's
 * own choice alone.
 */
export type SavedViewTab = Readonly<{
  id: string;
  label: string;
  q: string;
  sort: string;
  filters: Readonly<Record<string, string>>;
  includeArchived: boolean;
  perPage?: number;
}>;

/**
 * One tab on the rail, whichever kind it came from: a screen's preset or a
 * reader's saved view. `perPage` is optional because neither kind need claim a
 * page size — a preset never does, and a saved view only when its stored blob
 * carried one.
 */
type RailView = ListView &
  Readonly<{
    id: string;
    q: string;
    includeArchived: boolean;
    perPage?: number;
  }>;

export function useListQuery<Row>({
  key,
  fetchPage,
  initialSort,
  initialFilters,
}: Readonly<{
  key: string;
  fetchPage: (
    query: ListQuery,
    cursor: string | null,
  ) => Promise<ListPage<Row>>;
  initialSort?: string;
  /**
   * Filters the list opens on. Read once, when the state is seeded: a filter
   * that only becomes known later (the viewer's own id, say) must arrive
   * through setQuery instead, because this initialiser never runs again.
   */
  initialFilters?: Readonly<Record<string, string>>;
}>) {
  // In overlay mode the incumbent mirror refuses sort/filter dials (422), so
  // list reads must carry neither: seed an empty sort (ListTable hides the
  // controls to match). Native mode keeps the screen's default sort.
  const overlay = useSorMode() === "overlay";
  const [query, setQuery] = useState<ListQuery>({
    q: "",
    sort: overlay ? "" : (initialSort ?? ""),
    includeArchived: false,
    // Overlay withholds filters for the same reason it withholds sort: the
    // incumbent mirror answers 422 to both. A screen that opens on a narrowed
    // list opens unnarrowed there rather than sending a dial the mirror
    // refuses.
    filters: overlay ? {} : (initialFilters ?? {}),
    perPage: LIST_PAGE_SIZES[0],
  });
  const infinite = useInfiniteQuery({
    queryKey: [key, query],
    queryFn: ({ pageParam }) => fetchPage(query, pageParam),
    initialPageParam: FIRST_PAGE,
    getNextPageParam: (last) =>
      last.page.has_more && last.page.next_cursor
        ? last.page.next_cursor
        : undefined,
  });
  const rows = (infinite.data?.pages ?? []).flatMap((page) => page.data);
  return {
    rows,
    query,
    setQuery,
    hasMore: infinite.hasNextPage,
    loadMore: () => infinite.fetchNextPage(),
    isPending: infinite.isPending,
    isError: infinite.isError,
    error: infinite.error,
    refetch: () => infinite.refetch(),
  };
}

export type ListState<Row> = Readonly<{
  rows: Row[];
  query: ListQuery;
  setQuery: Dispatch<SetStateAction<ListQuery>>;
  isPending: boolean;
  isError: boolean;
  error: unknown;
  refetch: () => void;
  hasMore: boolean;
  loadMore: () => void;
}>;

/**
 * The list surface bound to the server query: the state ladder every screen
 * renders identically (skeletons while pending, an error with a retry,
 * otherwise the table), with search, sort and filters reported straight back
 * into the ListQuery so the server answers them.
 *
 * The empty case belongs to the table rather than to this ladder: the table
 * knows whether the list is empty because nothing exists yet or because the
 * filters excluded everything, and only the second one should offer to clear
 * them.
 */
export function ListTable<Row>({
  state,
  columns,
  rowKey,
  rowRoute,
  unit,
  chips = [],
  dataChips = [],
  views = [],
  dataViews = [],
  action,
  caption,
  footer,
  searchable = true,
  showArchivedToggle = true,
  tools,
  emptyNote,
  scopeKey,
  body,
  bodyOwnsPaging = false,
  selection,
}: Readonly<{
  state: ListState<Row>;
  columns: readonly ListColumn<Row>[];
  rowKey: (row: Row) => string;
  /** Row selection for a bulk action — see the design-system ListTable. */
  selection?: ListSelection<Row>;
  /**
   * A second view of the same query, rendered instead of the grid while the
   * surface keeps its search, chips, views and page-size dial — see the
   * design-system ListTable's own `body`.
   */
  body?: ReactNode;
  /** The alternate body renders its own complete-count/load-more contract. */
  bodyOwnsPaging?: boolean;
  /**
   * Where a row's record lives. One declaration drives both ways in: clicking
   * the row navigates, and the identity cell becomes a real link that opens in
   * a new tab. Declaring them separately is how the two drift apart.
   */
  rowRoute?: (row: Row) => Route;
  /** Message key for the plural noun in the count and the empty state. */
  unit: MessageKey;
  chips?: readonly FilterSpec[];
  /**
   * Chips whose options are runtime record names rather than message keys — the
   * stages of a pipeline, the companies on the workspace. Already translated by
   * definition, since the server is what named them.
   */
  dataChips?: readonly ListChip[];
  views?: readonly ViewSpec[];
  /**
   * View tabs whose labels are server strings rather than message keys — the
   * reader's own saved views. Rendered after the screen's built-in presets, so
   * All/Mine/A-Z keep their positions as a saved view is added or removed.
   */
  dataViews?: readonly SavedViewTab[];
  action?: ReactNode;
  /** What this list is, for the lists that need saying. Never the screen name. */
  caption?: MessageKey;
  footer?: ReactNode;
  /** False for a list whose GET has no `q` param, e.g. /partners. */
  searchable?: boolean;
  showArchivedToggle?: boolean;
  /** Passed straight through to the surface's own tools slot, alongside the
   * Columns and Compact buttons — for the one screen (deals) whose board and
   * table views share a pipeline picker that lives beside them. */
  tools?: ReactNode;
  /**
   * What the empty table says under its generic line when THIS screen knows
   * why it is empty — a "Mine" view for a reader who owns nothing, with the
   * way back to everything. Overlay's own note wins when both apply.
   */
  emptyNote?: ReactNode;
  /**
   * What this list is reading, when the screen narrows it by something that is
   * neither a chip nor a filter — passed straight to the surface, which resets
   * the reader to page 1 when it changes. Deals is the case: its pipeline
   * picker is screen state, so switching pipelines leaves `filters` untouched
   * while the result set changes entirely.
   */
  scopeKey?: string;
}>): ReactNode {
  const t = useT();
  // Overlay reads a mirror that cannot sort or filter (the server 422s those
  // dials), so the table is handed neither, and a note says why. Search and
  // the archived toggle survive: the mirror answers the first and holds no
  // archived rows, so the second is a harmless no-op.
  const overlay = useSorMode() === "overlay";
  const { rows, query, setQuery, isPending, isError, error, refetch } = state;
  const [localSearch, setLocalSearch] = useState(query.q);
  // Which view tab is lit is READ from the query rather than remembered: a tab
  // is a claim about what the list is showing, and a reader who then edits a
  // filter or a sort is no longer looking at that preset. Stored, the highlight
  // would keep claiming a view the query had already left; derived, it simply
  // stops matching, and comes back by itself if the reader undoes the edit.
  //
  // ONE rail, built once: the tabs the surface renders and the tabs the
  // highlight is matched against have to be the same list, or an index reported
  // by a press names a different view than the one that was pressed. A view tab
  // whose preset the mirror would refuse is a tab that lights up and does
  // nothing, so overlay mode has no rail at all — the same reason its chips and
  // its sort are withheld.
  const railViews: readonly RailView[] = overlay
    ? []
    : [
        ...views.map((spec) => ({
          ...translateView(spec, t),
          // A preset is identified by its message key: the screen wrote the set,
          // so the key is unique within it, and it stays the same string when
          // the label is translated or the rail is reordered. `preset:` keeps
          // it out of the uuid namespace a saved view's id lives in.
          id: `preset:${spec.label}`,
          // A preset is a sort and a set of filters over the whole live list:
          // it asks for no search and no archive, and the tab has to say so or
          // it would keep claiming the list while the reader has a search typed
          // or the archive switched on.
          q: "",
          includeArchived: false,
        })),
        ...dataViews,
      ];
  const [view, pickView] = useActiveView(railViews, query);
  // Keyed on the VALUES, not on the arrays that carry them. The table treats a
  // new `chosen` object as the reader narrowing the list and resets to page 1,
  // so an object rebuilt every render resets on every render: pressing Next
  // re-read page 2 and then immediately snapped back to page 1, and the list
  // flipped between the first two pages forever.
  //
  // `chips` cannot be the key — screens declare it as an inline array literal,
  // which is a fresh reference each render. The option values are what
  // chosenFor actually reads, and they are strings.
  // EVERY chip on the surface, declared and data-driven alike. The owner dial
  // is a dataChip because it names the viewer's teams at runtime, and code that
  // asks "which chips are there" must see it: reading `chips` alone is what
  // made picking Unassigned send `owner=unassigned:true` — the chip's key with
  // the raw option value — instead of `unassigned=true`, so the server ignored
  // an unknown parameter and answered the whole list.
  const allChips: readonly ListChip[] = [
    ...chips.map((chip) => translateChip(chip, t)),
    ...dataChips,
  ];
  // Keyed on the FILTERS, which are what the list actually reads. The table
  // treats a new `chosen` identity as the reader narrowing the list and resets
  // to page 1, so this identity must change only when the answer changes.
  //
  // Keying on the chip options made the roster do it: the owner dial names the
  // viewer's teams, which arrive on their own query, so a chip gained an option
  // seconds after the list rendered and threw the reader from page 2 back to
  // page 1 — for a reason they could not see, having touched nothing.
  //
  // Keying on chosenFor's RESULT is not enough either, and the saved views are
  // why. A restored view sets `owner_team_id=t-9` before the roster answers, so
  // the filter is already there when the matching option arrives; chosenFor
  // then adds the chip's own key and the result changes shape while the query,
  // and every row, stays exactly as it was.
  //
  // The filters are the honest signal: the fetch reads them and nothing else,
  // so an identity that follows them resets when the rows change and never
  // otherwise. What a dial OFFERS is presentation, and presentation must not
  // move the reader.
  // Sorted, so re-picking the same value cannot reorder the object and read as
  // a change: setFilter deletes a composite param and re-adds it, which moves
  // its insertion order without moving what it selects.
  const narrowKey = JSON.stringify(
    Object.entries(query.filters).sort(([a], [b]) => a.localeCompare(b)),
  );
  // Rebuilt every render, on purpose: a dial must show what is chosen the
  // moment its options arrive. Nothing resets on this — `narrowKey` is the
  // trigger — so a fresh object here costs nothing.
  const chosen = chosenFor(allChips, query.filters);

  // A functional updater reads the query at commit time, not at the time the
  // timer was scheduled: a concurrent sort/filter/includeArchived change
  // (which sets query immediately, before this timer fires) is preserved
  // instead of being reverted by a stale closure over `query`. Skipped when
  // the screen isn't searchable — there is no debounce to race in that case.
  useEffect(() => {
    if (!searchable) {
      return;
    }
    const timer = setTimeout(() => {
      setQuery((prev) =>
        prev.q === localSearch ? prev : { ...prev, q: localSearch },
      );
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [localSearch, setQuery, searchable]);

  // Neither state replaces the surface: the header, the dials and the primary
  // action belong to the screen rather than to the response, so they stay put
  // and only the body reports what happened.
  const problem = isError ? (
    <>
      <p>{t("common.error")}</p>
      <p className="t-mono" style={{ marginTop: "var(--space-1)" }}>
        {problemMessageOf(error, t)}
      </p>
      <Button
        small
        onClick={() => refetch()}
        style={{ marginTop: "var(--space-2)" }}
      >
        {t("common.retry")}
      </Button>
    </>
  ) : undefined;

  // Pressing a tab pins it AND restores the list state it holds. The surface
  // rewrites the sort and the filters itself; the archived toggle and the page
  // size are this layer's, because they are part of the ListQuery the fetchers
  // read and the surface knows nothing about a saved view's stored state.
  //
  // The toggle is rewritten unconditionally, exactly as the filters are: a view
  // describes the WHOLE list, so an archived toggle left on from the previous
  // tab both widens the view the reader just picked and stops that tab matching
  // itself. Page size is the one dial a tab may make no claim about — a preset
  // never claims one, and a saved view only where its stored blob carried one —
  // so the reader's own choice survives a tab that carries none.
  const applyViewState = (index: number) => {
    pickView(index);
    const picked = railViews[index];
    if (!picked) {
      return;
    }
    setQuery((prev) => ({
      ...prev,
      includeArchived: picked.includeArchived,
      perPage: picked.perPage ?? prev.perPage,
    }));
  };

  const setFilter = (key: string, value: string) =>
    setQuery((prev) => {
      const filters = { ...prev.filters };
      // A chip whose options each name a DIFFERENT query parameter carries the
      // parameter in the value, as `param:value` (the owner dial: mine, my
      // team's, unowned — one question the server answers three ways, and
      // refuses if asked two at once). Clearing such a chip has to drop
      // whichever parameter is currently set, not the chip's own key, which
      // names no parameter at all.
      // Only THIS chip's own parameters are cleared. A chip whose options each
      // name a different query parameter (the owner dial: mine, my team's,
      // unowned) has to drop whichever of its own it currently holds, because
      // its key names no parameter at all. Clearing every composite parameter
      // on the surface instead would make picking a lifecycle silently drop the
      // owner filter — one dial reaching into another's answer.
      const mine = allChips
        .filter((chip) => chip.key === key)
        .flatMap((chip) => chip.options.map((option) => option.value))
        .filter((candidate) => candidate.includes(":"))
        .map((candidate) => candidate.slice(0, candidate.indexOf(":")));
      for (const param of mine) {
        delete filters[param];
      }
      if (value) {
        const split = value.indexOf(":");
        if (split > 0 && mine.includes(value.slice(0, split))) {
          filters[value.slice(0, split)] = value.slice(split + 1);
        } else {
          filters[key] = value;
        }
      } else {
        delete filters[key];
      }
      return { ...prev, filters };
    });

  return (
    <ListSurface<Row>
      rows={rows}
      columns={columns}
      rowKey={rowKey}
      onRowClick={rowRoute ? (row) => navigate(rowRoute(row)) : undefined}
      rowHref={rowRoute ? (row) => routeHash(rowRoute(row)) : undefined}
      unit={t(unit)}
      // An empty overlay list is far more often a mirror row whose HubSpot
      // owner email has no matching workspace user (so mirror_visibility never
      // grants it to anyone) than a genuinely empty HubSpot portal — name that
      // cause rather than letting the generic empty copy imply "there is
      // nothing here".
      emptyNote={overlay ? t("overlay.emptyOwnerHint") : emptyNote}
      action={action}
      caption={caption ? t(caption) : undefined}
      footer={footer}
      tools={tools}
      note={overlay ? t("list.overlayReadOnly") : undefined}
      search={
        searchable
          ? { value: localSearch, onChange: setLocalSearch }
          : undefined
      }
      sort={
        overlay
          ? undefined
          : {
              value: query.sort,
              onChange: (next) => setQuery((prev) => ({ ...prev, sort: next })),
            }
      }
      chips={overlay ? [] : allChips}
      chosen={chosen}
      onChipChange={setFilter}
      views={railViews}
      activeView={view}
      narrowKey={narrowKey}
      scopeKey={scopeKey}
      onViewChange={applyViewState}
      archived={
        showArchivedToggle
          ? {
              checked: query.includeArchived,
              onChange: (next) =>
                setQuery((prev) => ({ ...prev, includeArchived: next })),
            }
          : undefined
      }
      // An overlay mirror pages by cursor like the native store, so paging is
      // the one dial that behaves identically in both modes.
      hasMore={state.hasMore}
      onLoadMore={state.loadMore}
      // The page size is part of the server query, not a second slice on top
      // of it: changing it re-asks the server, which is why it lives in the
      // ListQuery the fetchers read their `limit` from.
      perPage={query.perPage}
      onPerPage={(next) => setQuery((prev) => ({ ...prev, perPage: next }))}
      // The unit key names the table for the widths it remembers.
      widthsKey={unit}
      body={body}
      bodyOwnsPaging={bodyOwnsPaging}
      selection={selection}
      pending={isPending}
      problem={problem}
    />
  );
}

type Translate = ReturnType<typeof useT>;

function translateChip(chip: FilterSpec, t: Translate): ListChip {
  return {
    key: chip.key,
    label: t(chip.label),
    allLabel: t(chip.allLabel),
    options: chip.options.map((option) => ({
      value: option.value,
      label: "text" in option ? option.text : t(option.label),
    })),
  };
}

function translateView(spec: ViewSpec, t: Translate): ListView {
  return { label: t(spec.label), sort: spec.sort, filters: spec.filters };
}

/**
 * Which view tab is lit, and the setter the table reports a press to.
 *
 * The highlight is DERIVED from the query, so a reader who edits a filter is no
 * longer claimed to be looking at the preset they started from. But two views
 * can ask for the same sort and filters — a saved "German customers" that
 * narrows exactly as the built-in Customers preset does — and a purely derived
 * highlight lights the first match, so the reader's own view never lights when
 * they pick it.
 *
 * The pressed tab therefore wins, but only while it still describes the list.
 * The moment the query moves away it stops matching and the highlight falls
 * back to whatever the query now describes.
 *
 * What is pinned is the view's ID, never its position. `/views` answers ordered
 * by name, so creating or renaming a view re-orders the rail under the reader:
 * a stored index then points at whichever view moved into that slot, and the
 * highlight silently claims a view nobody picked.
 */
function useActiveView(
  views: readonly RailView[],
  query: ListQuery,
): [number, (index: number) => void] {
  const [pickedId, setPickedId] = useState<string | null>(null);
  const matched = views.findIndex((spec) => matchesView(spec, query));
  const pinned = views.findIndex((spec) => spec.id === pickedId);
  const view =
    pinned >= 0 && matchesView(views[pinned], query) ? pinned : matched;
  // The press reports an index into the rail just rendered, which is where the
  // id it names comes from. A press on nothing (the surface's own "clear
  // everything" reports index 0 against an empty rail) unpins rather than
  // pinning a view that is not there.
  return [view, (index: number) => setPickedId(views[index]?.id ?? null)];
}

/**
 * Is the list showing exactly what this view asks for, nothing added or left?
 *
 * Takes the state a view describes rather than its name, so it reads a screen's
 * built-in preset and a reader's saved view the same way — the two differ only
 * in where the label came from, and the highlight is about the query.
 *
 * Every dial that decides WHICH rows the list holds counts, and the two that
 * are easy to leave out are the reason this is spelled out. The archived toggle
 * widens the list exactly as a filter narrows it, so a view saved with "Show
 * archived" on is a DIFFERENT view from the default one. The search narrows it
 * the same way: a view saved over "acme" describes the acme rows, and a tab that
 * ignored the box would light over the whole list and name it acme.
 *
 * The invariant, then: a tab is lit only while the list on screen is the one it
 * names, down to the search box. Page size is the exception it is allowed to be,
 * because it changes how many of those rows are drawn at once and not which rows
 * they are.
 */
function matchesView(
  spec: Readonly<{
    q?: string;
    sort?: string;
    filters?: Readonly<Record<string, string>>;
    includeArchived?: boolean;
  }>,
  query: ListQuery,
): boolean {
  if (query.q !== (spec.q ?? "")) {
    return false;
  }
  if (query.sort !== (spec.sort ?? "")) {
    return false;
  }
  if (query.includeArchived !== (spec.includeArchived ?? false)) {
    return false;
  }
  const wanted = Object.entries(spec.filters ?? {});
  const applied = Object.entries(query.filters).filter(([, value]) => value);
  return (
    wanted.length === applied.length &&
    wanted.every(([key, value]) => query.filters[key] === value)
  );
}

/**
 * The owner dials every record list offers: mine, my team's, and the unowned
 * queue.
 *
 * One chip rather than three, because they answer ONE question — whose rows —
 * and the server refuses two of them at once (they name different sets, so a
 * pair can only ever match nothing). A single-select chip makes that refusal
 * unreachable from the UI instead of something the reader discovers as a 422.
 *
 * Built only once /me has answered. A chip option whose value is still "" reads
 * as "clear this filter" to the table, so a half-built dial would quietly
 * narrow nothing while looking armed.
 *
 * The wire takes ONE team id, so a viewer on several teams gets one dial per
 * team rather than a guess about which of them was meant; `viewerTeamOptions`
 * builds those.
 */
export function useOwnerChips(): readonly ListChip[] {
  const t = useT();
  const me = useMe();
  const viewerId = me.data?.user.id;
  const teams = useRoster("team", Boolean(viewerId));
  const teamsPartial = useRosterPartial("team", Boolean(viewerId));
  if (!viewerId) {
    return [];
  }
  return [
    {
      key: "owner",
      label: t("list.owner"),
      allLabel: t("list.filterOwnerAll"),
      options: [
        { value: `owner_id:${viewerId}`, label: t("list.filterOwnerMe") },
        ...viewerTeamOptions(me.data?.teams ?? [], teams, teamsPartial, t),
        { value: "unassigned:true", label: t("list.filterOwnerUnassigned") },
      ],
    },
  ];
}

/**
 * One dial per team the viewer belongs to, named off the roster.
 *
 * Named, not counted. The viewer may sit in several teams, and a dial that
 * withheld itself past the first one left everyone on two teams unable to ask a
 * question the API answers. Each team is its own option, so picking one is
 * picking a team rather than accepting a guess about which was meant.
 *
 * The IDS come from /me and only the LABELS come from the roster, so the two
 * reads can disagree about which teams there are. Filtering the roster BY the
 * membership made the label's absence decide the dial's: a team the walk never
 * reached yielded no option at all, and the viewer silently lost a filter the
 * API would still have answered. So every membership gets its option, and a name
 * the roster could not supply is reported as missing rather than taken as proof
 * the team is.
 */
function viewerTeamOptions(
  memberships: readonly string[],
  roster: ReturnType<typeof useRoster>,
  partial: boolean,
  t: ReturnType<typeof useT>,
): { value: string; label: string }[] {
  const named = new Map(
    (roster.data ?? []).map((entry) => [
      entry.id,
      // The team roster: every entry carries a name. The user roster is the
      // other kind this hook serves, and it is never asked for here.
      "display_name" in entry ? entry.display_name : entry.name,
    ]),
  );
  const reading = rosterReading(roster, partial);
  return memberships.flatMap((teamId) => {
    const value = `owner_team_id:${teamId}`;
    const name = named.get(teamId);
    if (name) {
      return [{ value, label: name }];
    }
    // A roster walked to the END has answered about this team: it is not one
    // this reader may list — `/teams` excludes archived teams — and an option
    // whose only honest label is a uuid is worse than one dial fewer. The other
    // two readings have answered nothing about it, so the dial stays and says
    // which of the two it is waiting on.
    if (reading === "unnamed") {
      return [];
    }
    return [
      {
        value,
        label:
          reading === "pending" ? t("common.loading") : t("ref.nameLoadFailed"),
      },
    ];
  });
}

/**
 * What each chip currently shows, given the filters actually in the query.
 *
 * A normal chip stores its value under its own key and needs no translation. A
 * chip whose options each name a different query parameter (`param:value` —
 * the owner dial) stores the value under the PARAMETER, so its selected option
 * has to be read back from whichever parameter is set. Without this the dial
 * narrows the list correctly and then renders as "Any owner", which reads as a
 * filter that did not take.
 */
function chosenFor(
  // Takes the option VALUES, so it reads a declared chip and a data-driven one
  // the same way — the two differ only in where their labels came from, and a
  // reader of this function never looks at a label.
  chips: readonly ListChip[],
  filters: Readonly<Record<string, string>>,
): Record<string, string> {
  const chosen = { ...filters };
  for (const chip of chips) {
    for (const option of chip.options) {
      const split = option.value.indexOf(":");
      if (split <= 0) {
        continue;
      }
      const param = option.value.slice(0, split);
      if (filters[param] === option.value.slice(split + 1)) {
        chosen[chip.key] = option.value;
      }
    }
  }
  return chosen;
}
