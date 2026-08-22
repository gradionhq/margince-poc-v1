import type { ReactNode } from "react";
import { useState } from "react";
import type { components } from "../api/schema";
import type { EntityKind } from "../app/entity";
import { activityTimeline } from "../design-system/activitytimeline";
import { EmptyState, SegmentedControl, Skeleton } from "../design-system/atoms";
import type { TimelineEntry } from "../design-system/composed";
import type { RecordTimeline } from "../design-system/recordtimeline";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { coldFieldLabel, LoadMoreButton, useViewerId } from "./common";
import { changeTimeline, useFieldHistory } from "./history";
import { mergeChronology } from "./history.logic";

// A record has ONE chronology, and this is where it is assembled — for any
// record, not for the account page alone. What was said to a record and what
// was changed about it are one story to the person reading them: kept apart,
// a reader comparing "we told them X" against "someone set stage to Y" had to
// hold two orderings in their head.
//
// It lives here rather than in the account page because the contact page asks
// the same question of the same two feeds. A second copy would answer it
// slightly differently the first time either half moved.

type Activity = components["schemas"]["Activity"];
type ChangesQuery = ReturnType<typeof useFieldHistory>;

export const TIMELINE_FILTERS = ["activities", "changes", "all"] as const;
export type TimelineFilter = (typeof TIMELINE_FILTERS)[number];

/**
 * useChronologyFilter owns the filter for ONE record rather than for the
 * session. When both records are already cached the route swaps one for
 * another without ever unmounting the section, so a reader who checked
 * Changes once met Changes on every record afterwards.
 */
export function useChronologyFilter(
  recordId: string,
): [TimelineFilter, (next: TimelineFilter) => void] {
  const [filter, setFilter] = useState<TimelineFilter>("activities");
  const [filterFor, setFilterFor] = useState(recordId);
  if (filterFor !== recordId) {
    setFilterFor(recordId);
    setFilter("activities");
  }
  return [filter, setFilter];
}

/**
 * ChronologyFilter narrows the record's own history. It sits ABOVE the list
 * rather than in the page's tab strip: it scopes this section, and a control
 * that looks like a tab reads as a different page.
 */
export function ChronologyFilter({
  filter,
  onFilter,
}: Readonly<{
  filter: TimelineFilter;
  onFilter: (next: TimelineFilter) => void;
}>) {
  const t = useT();
  return (
    <SegmentedControl
      options={TIMELINE_FILTERS}
      value={filter}
      onChange={onFilter}
      label={t("chronology.label")}
      labels={{
        activities: t("chronology.activities"),
        changes: t("chronology.changes"),
        all: t("chronology.all"),
      }}
    />
  );
}

export type RecordChronology = {
  entries: TimelineEntry[];
  truncated: boolean;
  changes: ChangesQuery;
  // What the CURRENT filter is waiting on or failed at. A query that is
  // switched off never resolves — it reports pending forever — so the caller
  // must not read the query's own flags. Reading them turned the default
  // Activities view into a skeleton that never became a timeline.
  loading: boolean;
  failed: boolean;
  // Whether fetching more CHANGES would lengthen the merged view. When the
  // activity feed is the shorter of the two, it is not: the merge cuts at its
  // oldest row and every extra change page falls below that line.
  changesAreTheLimit: boolean;
  // The activity feed's own next page, when the caller can fetch one. Under
  // "all" it is offered exactly when the changes are NOT the limit — the
  // activity feed is the cut, so another page of activities lengthens the
  // merged view and another page of changes would not.
  activities?: RecordTimeline;
};

/**
 * useRecordChronology reads the change feed for the record and folds it in
 * with the activities the caller already holds. The activities arrive as a
 * prop because the composite record read (the 360) has already fetched them:
 * a second fetch here would show the reader a different moment in the two
 * halves of one list.
 */
export function useRecordChronology({
  kind,
  recordId,
  filter,
  activities,
  activitiesHaveMore,
  loadMore,
  renderActions,
}: Readonly<{
  kind: EntityKind;
  recordId: string;
  filter: TimelineFilter;
  activities: Activity[];
  activitiesHaveMore: boolean;
  // The paged read behind `activities`, for the footer's Load more. Absent on
  // a surface that shows one page and says so.
  loadMore?: RecordTimeline;
  // The per-row verbs (Reply, Relink). Absent on a surface that offers none.
  renderActions?: (activity: Activity) => ReactNode;
}>): RecordChronology {
  const t = useT();
  const viewerId = useViewerId();
  const wantsChanges = filter !== "activities";
  const changes = useFieldHistory(kind, recordId, { enabled: wantsChanges });
  const changeRows = changes.data?.pages.flatMap((page) => page.data) ?? [];
  const activityEntries = activityTimeline(activities, viewerId, renderActions);
  const changeEntries = changeTimeline(
    changeRows,
    (field) => coldFieldLabel(field, t),
    viewerId,
  );
  const loading = wantsChanges && changes.isPending;
  const failed = wantsChanges && changes.isError;

  if (filter === "activities") {
    return {
      entries: activityEntries,
      // The composite read caps this section, and a capped list that says
      // nothing reads as the whole history: a reader looking at the oldest of
      // 25 rows would take it for the day the relationship began.
      truncated: activitiesHaveMore,
      changes,
      loading: false,
      failed: false,
      changesAreTheLimit: false,
      activities: loadMore,
    };
  }
  if (filter === "changes") {
    return {
      entries: changeEntries,
      truncated: false,
      changes,
      loading,
      failed,
      changesAreTheLimit: changes.hasNextPage,
      activities: undefined,
    };
  }
  const merged = mergeChronology<TimelineEntry>(
    [
      { rows: activityEntries, hasMore: activitiesHaveMore },
      { rows: changeEntries, hasMore: changes.hasNextPage },
    ],
    (entry) => entry.atIso,
  );
  return {
    entries: merged.rows,
    truncated: merged.truncated,
    changes,
    loading,
    failed,
    changesAreTheLimit: changesOwnTheCut(
      changes.hasNextPage,
      activitiesHaveMore,
      changeEntries,
      activityEntries,
    ),
    activities: loadMore,
  };
}

/**
 * hasChronologyFooter says whether the footer would draw anything at all. A
 * caller that hangs it in a band of its own asks first: an empty band is a
 * strip of chrome the reader sees and cannot use.
 */
export function hasChronologyFooter(
  filter: TimelineFilter,
  chronology: RecordChronology,
): boolean {
  return (
    chronology.truncated ||
    filter === "changes" ||
    (filter === "all" && chronology.changesAreTheLimit) ||
    activitiesCanGrow(filter, chronology)
  );
}

/**
 * ChronologyFooter is what the list owes the reader underneath it: where the
 * merged view stops being complete, and the one button that can lengthen it.
 * Silence here would read as the end of the record's history.
 */
export function ChronologyFooter({
  filter,
  chronology,
}: Readonly<{ filter: TimelineFilter; chronology: RecordChronology }>) {
  const t = useT();
  return (
    <>
      {chronology.truncated && (
        <p className="t-small">
          {t(
            filter === "activities"
              ? "chronology.truncatedActivities"
              : "chronology.truncated",
          )}
        </p>
      )}
      {/* Only where fetching more changes actually lengthens the list. Under
          "all" the merge is cut at whichever feed is shorter, so if the
          ACTIVITY feed is the constraint, another page of changes is filtered
          straight back out and the button does nothing. */}
      {(filter === "changes" ||
        (filter === "all" && chronology.changesAreTheLimit)) && (
        <LoadMoreButton query={chronology.changes} />
      )}
      {activitiesCanGrow(filter, chronology) && chronology.activities && (
        <LoadMoreButton query={chronology.activities} />
      )}
    </>
  );
}

// Another page of ACTIVITIES lengthens the list under Activities whenever the
// server holds one, and under All only while the activity feed owns the cut.
function activitiesCanGrow(
  filter: TimelineFilter,
  chronology: RecordChronology,
): boolean {
  if (!chronology.activities?.hasNextPage) {
    return false;
  }
  return (
    filter === "activities" ||
    (filter === "all" && !chronology.changesAreTheLimit)
  );
}

/**
 * chronologyEmptyKey is what "there is none" says under each filter. Only the
 * Activities view takes the caller's own word, because only that view is
 * about a relationship somebody has to recognise; the other two are about the
 * record and read the same everywhere.
 */
export const CHRONOLOGY_EMPTY_KEYS: Readonly<
  Record<Exclude<TimelineFilter, "activities">, MessageKey>
> = {
  changes: "chronology.changesEmpty",
  all: "chronology.allEmpty",
};

// The merged view is cut at the newest "oldest loaded" among the feeds that
// still have more. Another page of changes only reaches the reader when the
// change feed owns that cut — i.e. its oldest loaded row is not older than
// the activity feed's.
function changesOwnTheCut(
  changesHaveMore: boolean,
  activitiesHaveMore: boolean,
  changeEntries: TimelineEntry[],
  activityEntries: TimelineEntry[],
): boolean {
  // Instants, not the strings that spell them — the same reason
  // mergeChronology compares numbers: two feeds written by two stores spell
  // one moment two ways. Seeded with the first row rather than left to throw
  // on an empty one.
  const oldest = (rows: TimelineEntry[]) =>
    rows.length > 0
      ? Date.parse(
          rows.reduce(
            (a, b) => (Date.parse(a.atIso) < Date.parse(b.atIso) ? a : b),
            rows[0],
          ).atIso,
        )
      : undefined;
  const oldestChange = oldest(changeEntries);
  const oldestActivity = oldest(activityEntries);
  return (
    changesHaveMore &&
    (!activitiesHaveMore ||
      oldestChange === undefined ||
      oldestActivity === undefined ||
      oldestChange >= oldestActivity)
  );
}

/**
 * chronologyNotice keeps four things apart that all render as an empty list
 * if you let them: still loading, the read failed, the section was never in
 * the payload, and the record genuinely has nothing to show. Only the last
 * one may say so — the other three would have a rep conclude nobody has ever
 * touched this record.
 *
 * The empty sentence names what the filter was looking for. "Nothing logged
 * on this account" under the Changes filter would be a claim about the
 * activity feed the reader is not looking at. `activitiesEmptyKey` is the
 * caller's own word for the Activities view, for the same reason
 * CHRONOLOGY_EMPTY_KEYS leaves that one out.
 */
export function chronologyNotice(
  activitiesEmptyKey: MessageKey,
  timeline: {
    loading: boolean;
    failed: boolean;
    assembled: boolean;
    filter: TimelineFilter;
  },
  count: number,
  t: ReturnType<typeof useT>,
): ReactNode {
  if (timeline.loading) {
    return <Skeleton width="100%" height={48} />;
  }
  if (timeline.failed || !timeline.assembled) {
    return <EmptyState>{t("co.section.unavailable")}</EmptyState>;
  }
  if (count > 0) {
    return undefined;
  }
  return (
    <EmptyState>
      {t(
        timeline.filter === "activities"
          ? activitiesEmptyKey
          : CHRONOLOGY_EMPTY_KEYS[timeline.filter],
      )}
    </EmptyState>
  );
}
