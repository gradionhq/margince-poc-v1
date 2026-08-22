// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components, operations } from "../api/schema";
import type { EntityKind } from "../app/entity";
import { RECORD_ZONE, startOfDayInZone } from "../format/timezone";
import { entityTimelineKeys } from "../screens/activitykeys";
import { throwProblem, useSorMode } from "../screens/common";
import type { ISODate } from "./dateinput";

type Activity = components["schemas"]["Activity"];
type ActivityPage = components["schemas"]["ActivityListResponse"];
type ListActivitiesQuery = NonNullable<
  operations["listActivities"]["parameters"]["query"]
>;
export type ActivityKind = NonNullable<ListActivitiesQuery["kind"]>;

/** Every kind the list accepts, in the order the filter offers them. */
export const ACTIVITY_KINDS: readonly ActivityKind[] = [
  "email",
  "message",
  "call",
  "meeting",
  "note",
  "task",
];

/**
 * TimelineFilters is what a reader narrows a record's timeline BY. Every
 * field is sent to the server: a filter applied over the page the client
 * holds answers "which of these 20 rows match", which is not the question —
 * the contact page did that for a release, and a rep filtering for tasks saw
 * none because the newest 20 rows happened to be mail.
 *
 * `after` / `before` are the calendar days the reader picked; the wire wants
 * instants, and `timelineQueryParams` is where a day becomes one.
 */
export type TimelineFilters = Readonly<{
  kind?: ActivityKind;
  q?: string;
  after?: ISODate | "";
  before?: ISODate | "";
}>;

export const NO_TIMELINE_FILTERS: TimelineFilters = {};

export function hasTimelineFilters(filters: TimelineFilters): boolean {
  return Boolean(filters.kind || filters.q || filters.after || filters.before);
}

/**
 * The page size every request asks for. A constant because a mobile budget
 * is measured against it (e2e/perf-mobile.spec.ts): a record's open on
 * Fast-3G pays for exactly one page of this size, and a larger first page is
 * a slower first paint on every record.
 */
export const TIMELINE_PAGE_SIZE = 20;

/**
 * dayStartIso turns a picked calendar day into the instant it begins — in
 * RECORD_ZONE, the clock every timeline row is rendered in, so the day the
 * reader picks is the day the rows show. `daysLater` shifts by whole days,
 * which is how an EXCLUSIVE `before` is spelled from an inclusive "to" date:
 * the next day's start.
 */
export function dayStartIso(day: ISODate, daysLater = 0): string {
  return startOfDayInZone(day, RECORD_ZONE, daysLater);
}

/**
 * timelineQueryParams is the ONE spelling of a record timeline read for the
 * wire. An absent filter is an absent parameter, never `""`: the server reads
 * an empty `q` as "no filter" today, but an empty `kind` is a 400, and not
 * sending what was not asked keeps both honest.
 */
export function timelineQueryParams(
  entityType: EntityKind,
  id: string,
  filters: TimelineFilters,
  cursor?: string,
): ListActivitiesQuery {
  const query: ListActivitiesQuery = {
    entity_type: entityType,
    entity_id: id,
    limit: TIMELINE_PAGE_SIZE,
  };
  if (cursor) query.cursor = cursor;
  if (filters.kind) query.kind = filters.kind;
  if (filters.q?.trim()) query.q = filters.q.trim();
  if (filters.after) query.occurred_after = dayStartIso(filters.after);
  if (filters.before) query.occurred_before = dayStartIso(filters.before, 1);
  return query;
}

/**
 * useTimelineFilters keeps the filters per RECORD rather than per session:
 * the record route swaps one cached record for another without unmounting
 * the page, so a filter set on one contact would silently narrow the next.
 */
export function useTimelineFilters(
  recordId: string,
): [TimelineFilters, (next: TimelineFilters) => void] {
  const [filters, setFilters] = useState<TimelineFilters>(NO_TIMELINE_FILTERS);
  const [filtersFor, setFiltersFor] = useState(recordId);
  if (filtersFor !== recordId) {
    setFiltersFor(recordId);
    setFilters(NO_TIMELINE_FILTERS);
  }
  return [filters, setFilters];
}

/**
 * RecordTimeline is what a page renders a timeline from: every row loaded so
 * far, whether the server holds older ones, and the one verb that fetches
 * them. `LoadMoreButton` in screens/common takes it as-is.
 */
export type RecordTimeline = Readonly<{
  activities: Activity[];
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  fetchNextPage: () => unknown;
  /** No page is on hand yet — neither a seed nor a fetched one. */
  isPending: boolean;
  isSuccess: boolean;
  isError: boolean;
}>;

/**
 * useRecordTimeline reads the activities linked to ONE record, for the
 * timeline zone of that record's page, as many pages as the reader asks for.
 *
 * It lives here rather than on a screen because six screens render a
 * timeline and each one that reached into a neighbour's file for it made that
 * neighbour a de-facto library — the way `SurfaceState` spent months
 * importable only from `company360.tsx`.
 *
 * `firstPage` is for a page whose composite read (the 360) already carries
 * the newest page: that page is the seed, this hook fetches nothing until
 * the reader presses Load more, and then continues from the seed's cursor.
 * Without it — and whenever a filter is set, because the seed was cut
 * without one — the hook fetches the first page itself. Either way a
 * record's open costs the one request it costs today.
 *
 * The kind is `EntityKind`, not a hand-written union of the three that
 * happened to render a timeline first. `activity_link` has carried a lead arm
 * since migration 0038 and the contract's `entity_type` admits every kind the
 * link table does (ADR-0118/A169), so narrowing here would refuse a read the
 * server answers.
 */
export function useRecordTimeline(
  entityType: EntityKind,
  id: string,
  options: Readonly<{
    filters?: TimelineFilters;
    firstPage?: ActivityPage;
  }> = {},
): RecordTimeline {
  const filters = options.filters ?? NO_TIMELINE_FILTERS;
  // The seed stands only for the unfiltered read it was cut from.
  const seed = hasTimelineFilters(filters) ? undefined : options.firstPage;
  // The timeline is an entity-scoped activity read, a dial the overlay mirror
  // refuses (422) — skip the fetch in overlay; the record page renders the
  // honest unavailable state in the timeline slot instead.
  const overlay = useSorMode() === "overlay";
  const seedCursor = seed?.page.next_cursor ?? undefined;
  const query = useInfiniteQuery({
    // The key the write path invalidates, extended by the filters and the
    // seed's edge. Spelled without the filters, a kind-narrowed page would be
    // handed to the unfiltered view; spelled without the edge, pages continued
    // from a seed that has since changed would start from a cursor that no
    // longer sits at its end.
    queryKey: [
      ...entityTimelineKeys(entityType, id)[0],
      filters,
      { continuesFrom: seedCursor ?? null },
    ],
    // With a seed the first page is already on screen: nothing is fetched
    // until the reader asks, and `fetchNextPage` fetches regardless of this.
    enabled: !overlay && !seed,
    initialPageParam: seedCursor,
    getNextPageParam: (last: ActivityPage) =>
      last.page.has_more ? (last.page.next_cursor ?? undefined) : undefined,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/activities", {
        params: {
          query: timelineQueryParams(entityType, id, filters, pageParam),
        },
      });
      if (error) {
        throwProblem(error);
      }
      // A 200 with no body is a page with no rows and no edge. `isSuccess`
      // says nothing about whether the body arrived, and a page read off an
      // absent one crashed the record before any row was drawn.
      const page: ActivityPage = {
        data: data?.data ?? [],
        page: data?.page ?? { has_more: false },
      };
      return page;
    },
  });
  const fetched = query.data?.pages ?? [];
  const activities = [
    ...(seed?.data ?? []),
    ...fetched.flatMap((page) => page.data),
  ];
  // Before the first Load more there is no fetched page to ask, so the seed
  // answers for the edge; the query's own flag is false while it holds no data.
  const hasNextPage =
    fetched.length > 0 ? query.hasNextPage : Boolean(seed?.page.has_more);
  return {
    activities,
    hasNextPage,
    isFetchingNextPage: query.isFetching,
    fetchNextPage: () => query.fetchNextPage(),
    isPending: seed ? false : query.isPending,
    isSuccess: seed ? true : query.isSuccess,
    isError: query.isError,
  };
}
