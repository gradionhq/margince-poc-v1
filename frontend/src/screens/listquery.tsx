import { useInfiniteQuery } from "@tanstack/react-query";
import { type Dispatch, type SetStateAction, useEffect, useState } from "react";
import { SearchField } from "../design-system/atoms";
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
      last.page.has_more ? last.page.next_cursor : undefined,
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
            onChange={(event) =>
              setQuery({
                ...query,
                filters: { ...query.filters, [filter.key]: event.target.value },
              })
            }
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
            onChange={(event) =>
              setQuery({
                ...query,
                filters: { ...query.filters, [filter.key]: event.target.value },
              })
            }
          />
        ),
      )}
    </div>
  );
}
