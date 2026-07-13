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
  EmptyState,
  SearchField,
  Skeleton,
} from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";

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
      options: { value: string; label: MessageKey }[];
    }
  | { kind: "text"; key: string; label: MessageKey };

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
  const [query, setQuery] = useState<ListQuery>({
    q: "",
    sort: initialSort ?? "",
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
}: Readonly<{
  query: ListQuery;
  setQuery: Dispatch<SetStateAction<ListQuery>>;
  sortOptions: SortOption[];
  filters?: FilterSpec[];
}>) {
  const t = useT();
  const [localSearch, setLocalSearch] = useState(query.q);

  // A functional updater reads the query at commit time, not at the time the
  // timer was scheduled: a concurrent sort/filter/includeArchived toggle
  // (which sets query immediately, before this timer fires) is preserved
  // instead of being silently reverted by a stale closure over `query`.
  useEffect(() => {
    const timer = setTimeout(() => {
      setQuery((prev) =>
        prev.q === localSearch ? prev : { ...prev, q: localSearch },
      );
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  }, [localSearch, setQuery]);

  return (
    <div className="list-toolbar">
      <SearchField
        placeholder={t("list.search")}
        aria-label={t("list.search")}
        value={localSearch}
        onChange={(event) => setLocalSearch(event.target.value)}
      />
      <select
        className="input"
        aria-label={t("list.sort")}
        value={query.sort}
        onChange={(event) => setQuery({ ...query, sort: event.target.value })}
      >
        {sortOptions.map((option) => (
          <option key={option.value} value={option.value}>
            {t(option.label)}
          </option>
        ))}
      </select>
      <label>
        <input
          type="checkbox"
          checked={query.includeArchived}
          onChange={(event) =>
            setQuery({ ...query, includeArchived: event.target.checked })
          }
        />
        {t("list.showArchived")}
      </label>
      {filters?.map((filter) =>
        filter.kind === "select" ? (
          <select
            key={filter.key}
            className="input"
            aria-label={t(filter.label)}
            value={query.filters[filter.key] ?? ""}
            onChange={(event) => {
              const next = { ...query.filters };
              if (event.target.value) {
                next[filter.key] = event.target.value;
              } else {
                delete next[filter.key];
              }
              setQuery({ ...query, filters: next });
            }}
          >
            <option value="" />
            {filter.options.map((option) => (
              <option key={option.value} value={option.value}>
                {t(option.label)}
              </option>
            ))}
          </select>
        ) : (
          <input
            key={filter.key}
            type="text"
            className="input"
            aria-label={t(filter.label)}
            value={query.filters[filter.key] ?? ""}
            onChange={(event) => {
              const next = { ...query.filters };
              if (event.target.value) {
                next[filter.key] = event.target.value;
              } else {
                delete next[filter.key];
              }
              setQuery({ ...query, filters: next });
            }}
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
          {error instanceof Error ? error.message : null}
        </p>
        <Button small onClick={() => refetch()} style={{ marginTop: 10 }}>
          {t("common.retry")}
        </Button>
      </EmptyState>
    );
  }

  if (rows.length === 0) {
    return <EmptyState>{empty}</EmptyState>;
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
