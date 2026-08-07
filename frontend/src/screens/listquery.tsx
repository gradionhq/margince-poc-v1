import { useInfiniteQuery } from "@tanstack/react-query";
import {
  type Dispatch,
  type ReactNode,
  type SetStateAction,
  useEffect,
  useState,
} from "react";
import {
  Button,
  Checkbox,
  EmptyState,
  SearchField,
  Skeleton,
} from "../design-system/atoms";
import { Select } from "../design-system/select";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, useSorMode } from "./common";

// The shared list foundation (P-14): every list screen sends the rich
// q/sort/cursor/include_archived/filter vocabulary instead of a flat
// limit:50, and paginates by keyset (never offset — the workspace's rows
// mutate under a live feed). useListQuery owns the react-query wiring;
// ListToolbar owns the controls. Screens compose both in Tasks 1.6–1.8.

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

export type SortOption = { value: string; label: MessageKey };

export type FilterSpec =
  | {
      kind: "select";
      key: string;
      label: MessageKey;
      // Names the empty (unset) option so the control names the filter at
      // rest; re-selecting it clears the filter. Absent, the filter's own
      // label names that option — an option a reader cannot read is an option
      // they cannot come back to.
      placeholder?: MessageKey;
      options: { value: string; label: MessageKey }[];
    }
  | { kind: "text"; key: string; label: MessageKey };

// A blank choice CLEARS the filter rather than storing an empty string: the
// query object is what each screen builds its request from, and `status=""`
// would send an empty filter where none was meant. Shared by both filter
// controls below so the select and the text input cannot drift on it.
function withFilter(query: ListQuery, key: string, value: string): ListQuery {
  const filters = { ...query.filters };
  if (value) {
    filters[key] = value;
  } else {
    delete filters[key];
  }
  return { ...query, filters };
}

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
  // list reads must carry neither: seed an empty sort (ListToolbar hides the
  // control to match). Native mode keeps the screen's default sort.
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

export function ListToolbar({
  query,
  setQuery,
  sortOptions,
  filters,
  searchable = true,
  showArchivedToggle = true,
}: Readonly<{
  query: ListQuery;
  setQuery: Dispatch<SetStateAction<ListQuery>>;
  sortOptions: SortOption[];
  filters?: FilterSpec[];
  searchable?: boolean;
  showArchivedToggle?: boolean;
}>) {
  const t = useT();
  const [localSearch, setLocalSearch] = useState(query.q);
  // Overlay mode reads a mirror that cannot sort or filter (the server 422s
  // those dials), so we render neither control — only what the mirror can
  // honestly answer: search, and the archived toggle (the mirror holds no
  // archived rows, so it is a harmless no-op there). This is the honest half
  // of "render only what works"; the sort/filter dials return with a flip.
  const overlay = useSorMode() === "overlay";

  // A functional updater reads the query at commit time, not at the time the
  // timer was scheduled: a concurrent sort/filter/includeArchived toggle
  // (which sets query immediately, before this timer fires) is preserved
  // instead of being silently reverted by a stale closure over `query`.
  // Skipped entirely when the screen isn't searchable (e.g. /partners, whose
  // GET has no `q` param) — there is no debounce to race in that case.
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

  return (
    <div className="list-toolbar">
      {searchable && (
        <SearchField
          placeholder={t("list.search")}
          aria-label={t("list.search")}
          value={localSearch}
          onChange={(event) => setLocalSearch(event.target.value)}
        />
      )}
      {overlay ? (
        <span className="list-toolbar-note">{t("list.overlayReadOnly")}</span>
      ) : (
        <Select
          aria-label={t("list.sort")}
          // A screen that opens on no explicit sort has nothing to show as the
          // chosen one, and an unlabelled control reads as a control that failed
          // to load. The face says what the control is FOR until it has an answer;
          // the server's own default ordering is what the list is showing.
          placeholder={t("list.sort")}
          value={query.sort}
          onChange={(sort) => setQuery({ ...query, sort })}
          options={sortOptions.map((option) => ({
            value: option.value,
            label: t(option.label),
          }))}
        />
      )}
      {showArchivedToggle && (
        <Checkbox
          label={t("list.showArchived")}
          checked={query.includeArchived}
          onChange={(event) =>
            setQuery({ ...query, includeArchived: event.target.checked })
          }
        />
      )}
      {!overlay &&
        filters?.map((filter) =>
          filter.kind === "select" ? (
            <Select
              key={filter.key}
              aria-label={t(filter.label)}
              value={query.filters[filter.key] ?? ""}
              onChange={(value) =>
                setQuery(withFilter(query, filter.key, value))
              }
              // The unset entry is a real OPTION, not the select's placeholder:
              // a placeholder is only a face for an unset value, and a reader
              // who narrowed the list has to be able to come back to all of it.
              options={[
                { value: "", label: t(filter.placeholder ?? filter.label) },
                ...filter.options.map((option) => ({
                  value: option.value,
                  label: t(option.label),
                })),
              ]}
            />
          ) : (
            <input
              key={filter.key}
              type="text"
              className="input"
              aria-label={t(filter.label)}
              value={query.filters[filter.key] ?? ""}
              onChange={(event) =>
                setQuery(withFilter(query, filter.key, event.target.value))
              }
            />
          ),
        )}
    </div>
  );
}

export type ListGateState<Row> = Readonly<{
  rows: Row[];
  isPending: boolean;
  isError: boolean;
  error: unknown;
  refetch: () => void;
  hasMore: boolean;
  loadMore: () => void;
}>;

// The shared list-state ladder every list screen renders identically:
// skeletons while pending, an EmptyState+retry on error, an EmptyState when
// the page is empty, otherwise the caller's rows plus a keyset "Load more".
// Extracted so contacts/companies/leads (Tasks 1.6-1.8) stay in lockstep
// instead of hand-rolling the same four branches three times.
export function ListGate<Row>({
  state,
  empty,
  children,
}: Readonly<{
  state: ListGateState<Row>;
  empty: string;
  children: (rows: Row[]) => ReactNode;
}>): ReactNode {
  const t = useT();
  const overlay = useSorMode() === "overlay";
  const { rows, isPending, isError, error, refetch, hasMore, loadMore } = state;

  if (isPending) {
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <Skeleton width="60%" />
        <Skeleton width="90%" />
        <Skeleton width="75%" />
      </div>
    );
  }

  if (isError) {
    return (
      <EmptyState>
        <p>{t("common.error")}</p>
        <p className="t-mono" style={{ marginTop: 6 }}>
          {problemMessageOf(error, t)}
        </p>
        <Button small onClick={() => refetch()} style={{ marginTop: 10 }}>
          {t("common.retry")}
        </Button>
      </EmptyState>
    );
  }

  if (rows.length === 0) {
    return (
      <EmptyState>
        <p>{empty}</p>
        {/* An empty overlay list is far more often a mirror row whose
            HubSpot owner email has no matching workspace user (so
            mirror_visibility never grants it to anyone) than a genuinely
            empty HubSpot portal — name that cause rather than leaving the
            caller's generic empty copy to imply "there is nothing here". */}
        {overlay && (
          <p className="t-caption" style={{ marginTop: "var(--space-2)" }}>
            {t("overlay.emptyOwnerHint")}
          </p>
        )}
      </EmptyState>
    );
  }

  return (
    <>
      {children(rows)}
      {hasMore && (
        <Button small onClick={() => loadMore()} style={{ marginTop: 10 }}>
          {t("list.loadMore")}
        </Button>
      )}
    </>
  );
}
