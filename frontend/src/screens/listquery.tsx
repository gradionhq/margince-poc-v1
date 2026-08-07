import { useInfiniteQuery } from "@tanstack/react-query";
import {
  type Dispatch,
  type ReactNode,
  type SetStateAction,
  useEffect,
  useState,
} from "react";
import { navigate, type Route, routeHash } from "../app/router";
import { Button } from "../design-system/atoms";
import {
  type ListChip,
  type ListColumn,
  ListTable as ListSurface,
  type ListView,
} from "../design-system/listtable";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, useSorMode } from "./common";

// The shared list foundation (P-14): every list screen sends the rich
// q/sort/cursor/include_archived/filter vocabulary instead of a flat
// limit:50, and paginates by keyset (never offset — the workspace's rows
// mutate under a live feed). useListQuery owns the react-query wiring;
// ListTable binds that query to the list surface, which owns the controls.

const SEARCH_DEBOUNCE_MS = 250;

export type ListQuery = {
  q: string;
  sort: string;
  includeArchived: boolean;
  filters: Record<string, string>;
};

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
}: Readonly<{
  key: string;
  fetchPage: (
    query: ListQuery,
    cursor: string | null,
  ) => Promise<ListPage<Row>>;
  initialSort?: string;
}>) {
  // In overlay mode the incumbent mirror refuses sort/filter dials (422), so
  // list reads must carry neither: seed an empty sort (ListTable hides the
  // controls to match). Native mode keeps the screen's default sort.
  const overlay = useSorMode() === "overlay";
  const [query, setQuery] = useState<ListQuery>({
    q: "",
    sort: overlay ? "" : (initialSort ?? ""),
    includeArchived: false,
    filters: {},
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
  action,
  caption,
  footer,
  searchable = true,
  showArchivedToggle = true,
  tools,
}: Readonly<{
  state: ListState<Row>;
  columns: readonly ListColumn<Row>[];
  rowKey: (row: Row) => string;
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
  const view = views.findIndex((spec) => matchesView(spec, query));

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
      if (value) {
        filters[key] = value;
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
      chips={
        overlay
          ? []
          : [...chips.map((chip) => translateChip(chip, t)), ...dataChips]
      }
      chosen={query.filters}
      onChipChange={setFilter}
      // A view tab whose preset the mirror would refuse is a tab that lights up
      // and does nothing, so overlay mode shows none — the same reason its
      // chips and its sort are withheld.
      views={overlay ? [] : views.map((spec) => translateView(spec, t))}
      activeView={view}
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
      // The unit key names the table for the widths it remembers.
      widthsKey={unit}
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

/** Is the list showing exactly what this view asks for, nothing added or left? */
function matchesView(spec: ViewSpec, query: ListQuery): boolean {
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
