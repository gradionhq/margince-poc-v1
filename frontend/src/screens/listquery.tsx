import { useInfiniteQuery } from "@tanstack/react-query";
import {
  type Dispatch,
  type ReactNode,
  type SetStateAction,
  useEffect,
  useMemo,
  useState,
} from "react";
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
import { useRoster } from "./entityref";

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

/** A filter chip, declared in the screen's own message keys. */
export type FilterSpec = {
  key: string;
  label: MessageKey;
  /** The "no filter" entry, which is also how a chosen value is cleared. */
  allLabel: MessageKey;
  options: { value: string; label: MessageKey }[];
};

/** A saved view: a named sort + filter preset, shown as a tab. */
export type ViewSpec = {
  label: MessageKey;
  sort?: string;
  filters?: Record<string, string>;
};

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
    initialPageParam: null as string | null,
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
  body,
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
  dataViews?: readonly ListView[];
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
  const allViews: readonly ListView[] = [
    ...views.map((spec) => ({
      label: spec.label,
      sort: spec.sort,
      filters: spec.filters,
    })),
    ...dataViews,
  ];
  const [view, setPicked] = useActiveView(allViews, query);
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
  // Carries the chip KEYS and the grouping, not just a flat list of values:
  // chosenFor reads both, so two different chip sets that happen to share the
  // same values must not produce the same key. JSON, rather than a join on a
  // separator — any separator can appear inside a value, and then ["a","b"]
  // and ["a<sep>b"] are indistinguishable.
  const chipOptionKey = JSON.stringify(
    allChips.map((chip) => [chip.key, chip.options.map((o) => o.value)]),
  );
  const filterKey = JSON.stringify(query.filters);
  // biome-ignore lint/correctness/useExhaustiveDependencies: keyed on the values the arrays carry, which is what makes the identity stable
  const chosen = useMemo(
    () => chosenFor(allChips, query.filters),
    [chipOptionKey, filterKey],
  );

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
      emptyNote={overlay ? t("overlay.emptyOwnerHint") : undefined}
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
      // A view tab whose preset the mirror would refuse is a tab that lights up
      // and does nothing, so overlay mode shows none — the same reason its
      // chips and its sort are withheld.
      views={
        overlay
          ? []
          : [...views.map((spec) => translateView(spec, t)), ...dataViews]
      }
      activeView={view}
      onViewChange={setPicked}
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
      label: t(option.label),
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
 */
function useActiveView(
  views: readonly ListView[],
  query: ListQuery,
): [number, (index: number) => void] {
  const [picked, setPicked] = useState<number | null>(null);
  const matched = views.findIndex((spec) => matchesView(spec, query));
  const pinned = picked !== null && views[picked];
  const view = pinned && matchesView(views[picked], query) ? picked : matched;
  return [view, setPicked];
}

/**
 * Is the list showing exactly what this view asks for, nothing added or left?
 *
 * Takes the sort and filters alone, so it reads a screen's built-in preset and
 * a reader's saved view the same way — the two differ only in where the label
 * came from, and the highlight is about the query, not the name.
 */
function matchesView(
  spec: Readonly<{ sort?: string; filters?: Readonly<Record<string, string>> }>,
  query: ListQuery,
): boolean {
  if (query.sort !== (spec.sort ?? "")) {
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
 * "My team" is the union of the viewer's teams when they belong to exactly one,
 * which is the ordinary case. With several, the dial is withheld rather than
 * guessing which one the reader meant — the wire takes one team id, and picking
 * for them would answer a question they did not ask.
 */
export function useOwnerChips(): readonly ListChip[] {
  const t = useT();
  const me = useMe();
  const viewerId = me.data?.user.id;
  const teams = useRoster("team", Boolean(viewerId));
  if (!viewerId) {
    return [];
  }
  // Named, not counted. The viewer may sit in several teams, and a dial that
  // withheld itself past the first one left everyone on two teams unable to
  // ask a question the API answers. Each team is its own option, so picking one
  // is picking a team rather than accepting a guess about which was meant.
  const mine = new Set(me.data?.teams ?? []);
  const named = (teams.data ?? []).filter((entry) => mine.has(entry.id));
  return [
    {
      key: "owner",
      label: t("list.owner"),
      allLabel: t("list.filterOwnerAll"),
      options: [
        { value: `owner_id:${viewerId}`, label: t("list.filterOwnerMe") },
        ...named.map((team) => ({
          value: `owner_team_id:${team.id}`,
          label: "display_name" in team ? team.display_name : team.name,
        })),
        { value: "unassigned:true", label: t("list.filterOwnerUnassigned") },
      ],
    },
  ];
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
