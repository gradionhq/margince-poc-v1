import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { activityTimeline } from "../design-system/activitytimeline";
import { Avatar, Button } from "../design-system/atoms";
import { GroupedTimelineList } from "../design-system/composed";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import {
  hasTimelineFilters,
  type RecordTimeline,
  useRecordTimeline,
  useTimelineFilters,
} from "../design-system/recordtimeline";
import {
  type SectionState,
  SurfaceState,
  sectionState,
} from "../design-system/surfacestate";
import { TimelineFilterBar } from "../design-system/timelinefilterbar";
import { formatDateTime } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import { useViewerId } from "./common";
import { TimelineActions } from "./compose";
import { PersonCommercialCard, readableRole } from "./personcards";
import {
  CHRONOLOGY_EMPTY_KEYS,
  ChronologyFilter,
  ChronologyFooter,
  hasChronologyFooter,
  type RecordChronology,
  type TimelineFilter,
  useChronologyFilter,
  useRecordChronology,
} from "./recordchronology";
import { groupChronology } from "./timelinegroups";
import "./person360.css";

// The tabs beside Overview that read what the 360 already assembled — the same
// rule the overview cards hold to (personcards.tsx): a tab can never show a
// record the tab beside it is withholding. The Timeline's CHANGES half and the
// Files tab are the exceptions, and they fetch because the 360 carries
// neither.

type Person360 = components["schemas"]["Person360"];
type Activity = components["schemas"]["Activity"];

// --- Timeline ---------------------------------------------------------------

/**
 * PersonTimelineTab is the contact's ONE chronology: what was said to them and
 * what was changed about them, in one order, through the same
 * `useRecordChronology` the account page reads. They were two tabs for a
 * release, and a reader who wanted them in order had to interleave two lists
 * by hand.
 *
 * The activities half is the 360's own section — already fetched, and a PAGE:
 * when the server says there is more, the tab says so rather than letting a
 * cut list read as the whole ledger. The changes half is fetched here, and
 * only once the reader asks for it.
 */
export function PersonTimelineTab({
  personId,
  view,
  loading = false,
  onBriefMeeting,
}: Readonly<{
  personId: string;
  view?: Person360;
  loading?: boolean;
  // Opens the pre-meeting brief for one meeting row. The drawer lives on the
  // page, so the tab asks rather than renders it.
  onBriefMeeting?: (activityId: string) => void;
}>) {
  const t = useT();
  const [filter, setFilter] = useChronologyFilter(personId);
  const [filters, setFilters] = useTimelineFilters(personId);
  // The 360's own page seeds the list; older pages and every narrowed read
  // come from the activity list itself.
  const timeline = useRecordTimeline("person", personId, {
    filters,
    firstPage: view?.activities,
  });
  const chronology = useRecordChronology({
    kind: "person",
    recordId: personId,
    filter,
    activities: timeline.activities,
    activitiesHaveMore: timeline.hasNextPage,
    loadMore: timeline,
    renderActions: (activity) => (
      <TimelineActions
        activity={activity}
        entityType="person"
        entityId={personId}
        personId={personId}
        extra={(row) => (
          <MeetingBriefAction activity={row} onBriefMeeting={onBriefMeeting} />
        )}
      />
    ),
  });
  return (
    <Panel
      title={t("tab.timeline")}
      titleAction={<ChronologyFilter filter={filter} onFilter={setFilter} />}
      actions={
        hasChronologyFooter(filter, chronology) ? (
          <ChronologyFooter filter={filter} chronology={chronology} />
        ) : undefined
      }
    >
      <PanelBody>
        {filter !== "changes" && (
          <TimelineFilterBar value={filters} onChange={setFilters} />
        )}
        <SurfaceState
          state={timelineState(
            view,
            filter,
            chronology,
            timeline,
            hasTimelineFilters(filters),
            loading,
          )}
          emptyLabel={
            filter === "activities"
              ? t("person.timeline.empty")
              : t(CHRONOLOGY_EMPTY_KEYS[filter])
          }
          detail={
            filter === "activities"
              ? undefined
              : { onRetry: chronology.changes.refetch }
          }
        >
          <GroupedTimelineList
            groups={groupChronology(chronology.entries, timeline.hasNextPage)}
            zone={RECORD_ZONE}
          />
        </SurfaceState>
      </PanelBody>
    </Panel>
  );
}

/**
 * timelineState reads the state of whichever feed the FILTER is actually
 * showing. The two halves fail independently: a 360 that withheld its
 * activities says nothing about the change feed, and reporting the Changes
 * view as withheld on that basis would hide rows that loaded perfectly well.
 */
function timelineState(
  view: Person360 | undefined,
  filter: TimelineFilter,
  chronology: RecordChronology,
  timeline: RecordTimeline,
  narrowed: boolean,
  loading: boolean,
): SectionState {
  if (filter === "activities") {
    // A narrowed read is the list's own, not the 360's section: it has its
    // own wait and its own failure, and a grant that withheld the section
    // withholds the list the same way through the server's 403.
    const base = narrowed
      ? narrowedState(timeline)
      : sectionState(
          view,
          "activities",
          Boolean(view?.activities),
          timeline.activities.length,
          loading,
        );
    return base === "ready" && chronology.truncated ? "partial" : base;
  }
  if (chronology.loading) {
    return "loading";
  }
  if (chronology.failed) {
    return "failed";
  }
  return chronology.entries.length === 0 ? "empty" : "ready";
}

function narrowedState(timeline: RecordTimeline): SectionState {
  if (timeline.isPending) {
    return "loading";
  }
  if (timeline.isError) {
    return "failed";
  }
  return timeline.activities.length === 0 ? "empty" : "ready";
}

// --- Deals ------------------------------------------------------------------

/**
 * PersonDealsTab answers two different questions and keeps them apart: which
 * deals this person is recorded on at all, and what the one that matters looks
 * like right now.
 *
 * The second half is the overview's own commercial card, rendered again rather
 * than re-spelled — a second wording of the same figures is how two surfaces
 * start disagreeing about one deal.
 */
export function PersonDealsTab({
  view,
  loading = false,
}: Readonly<{ view?: Person360; loading?: boolean }>) {
  const t = useT();
  const roles = view?.deal_roles?.data ?? [];
  const state = sectionState(
    view,
    "deal_roles",
    Boolean(view?.deal_roles),
    roles.length,
    loading,
  );
  return (
    <div className="pe-overview-stack">
      <Panel title={t("tab.deals")}>
        <SurfaceState state={state} emptyLabel={t("person.deals.empty")}>
          {roles.map((role) => (
            <PanelRow className="pe-row" key={role.relationship_id}>
              {/* The seat first, in the row grid's own label column: which
                  deal it is answers "where", and the seat answers the question
                  a reader opened this tab with — what am I to them there. */}
              <span className="pe-row-label">{readableRole(role.role)}</span>
              <span className="pe-row-value">
                <button
                  type="button"
                  className="link-button"
                  onClick={() =>
                    navigate({ screen: "deals", id: role.deal_id })
                  }
                >
                  {role.deal_title ?? t("person.deals.untitled")}
                </button>
              </span>
              <span className="pe-row-label">
                {role.deal_stage ?? t("person.deals.noStage")}
              </span>
            </PanelRow>
          ))}
        </SurfaceState>
      </Panel>
      {view && <PersonCommercialCard view={view} />}
    </div>
  );
}

// --- Meetings ---------------------------------------------------------------

// The brief verb for one meeting, booked or already held — the backend
// assembles a brief for any meeting activity, and reading one afterwards is
// how a reader recovers what a room agreed.
//
// Every reason NOT to offer it is decided here rather than at each call site,
// because a third caller that forgets one of them ships a button that fails:
//
//   - no id, or a surface with no drawer to open — nothing to ask for.
//   - a row that is not a meeting — the endpoint answers 404 for any other
//     kind, by design.
//   - a meeting the reader may DISCOVER but not READ. The timeline carries
//     those deliberately, as `content_state: "withheld"`, so the reader knows
//     a conversation happened without seeing it. The brief endpoint applies
//     the stricter content gate, so offering the verb here would promise a
//     reader something their own grant refuses.
function MeetingBriefAction({
  activity,
  onBriefMeeting,
}: Readonly<{
  activity: Pick<Activity, "id" | "kind" | "content_state"> | undefined;
  onBriefMeeting?: (activityId: string) => void;
}>) {
  const t = useT();
  if (!activity?.id || !onBriefMeeting) {
    return null;
  }
  if (activity.kind !== "meeting" || activity.content_state === "withheld") {
    return null;
  }
  const activityId = activity.id;
  return (
    <Button small onClick={() => onBriefMeeting(activityId)}>
      {t("person.meeting.brief")}
    </Button>
  );
}

/**
 * PersonMeetingsTab puts the meeting that has not happened yet above the ones
 * that have. The booked meeting is the server's own next-meeting read, taken
 * through this person's activity link rather than their account's — the org's
 * answer names a meeting this person may not be in.
 */
export function PersonMeetingsTab({
  view,
  loading = false,
  onBriefMeeting,
}: Readonly<{
  view?: Person360;
  loading?: boolean;
  onBriefMeeting?: (activityId: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const viewerId = useViewerId();
  // The booked meeting is drawn above, from the server's own next-meeting
  // read. It is also an activity, so an unfiltered list draws it a second time
  // under "already held" — which was merely untidy while the rows were inert
  // and becomes two identical brief buttons for one room now that they carry a
  // verb.
  const booked = view?.next_meeting?.activity_id;
  const met = (view?.activities?.data ?? []).filter(
    (activity: Activity) =>
      activity.kind === "meeting" && activity.id !== booked,
  );
  const hasMore = view?.activities?.page.has_more ?? false;
  const past = sectionState(
    view,
    "activities",
    Boolean(view?.activities),
    met.length,
    loading,
  );
  const next = view?.next_meeting;
  return (
    <div className="pe-overview-stack">
      <Panel title={t("person.meetings.next")}>
        <PanelBody>
          <SurfaceState
            state={sectionState(
              view,
              "next_meeting",
              Boolean(view),
              next ? 1 : 0,
              loading,
            )}
            emptyLabel={t("person.meetings.noneBooked")}
          >
            {next && (
              <>
                <p className="pe-prose">
                  {next.subject ?? t("person.meetings.untitled")}
                </p>
                <p className="pe-brief-line">
                  {formatDateTime(next.starts_at, locale, RECORD_ZONE)}
                </p>
                {/* next_meeting carries no content_state because the 360
                    withholds the whole section rather than a redacted row, so
                    a booked meeting the reader can see here is one they can
                    read. The kind is stated for the same reason: this section
                    IS the meeting. */}
                <MeetingBriefAction
                  activity={{ id: next.activity_id, kind: "meeting" }}
                  onBriefMeeting={onBriefMeeting}
                />
                {next.participants && next.participants.length > 0 && (
                  <>
                    <Eyebrow as="h3">
                      {t("person.meetings.participants")}
                    </Eyebrow>
                    <div className="pe-chiprow">
                      {next.participants.map((who) => (
                        <span className="pe-memory-channel" key={who.person_id}>
                          <Avatar
                            name={who.full_name}
                            identity={who.person_id}
                            size="xs"
                          />
                          {who.full_name}
                        </span>
                      ))}
                    </div>
                  </>
                )}
              </>
            )}
          </SurfaceState>
        </PanelBody>
      </Panel>
      <Panel title={t("person.meetings.past")}>
        <PanelBody>
          <SurfaceState
            state={past === "ready" && hasMore ? "partial" : past}
            emptyLabel={t("person.meetings.noneLogged")}
          >
            <GroupedTimelineList
              groups={groupChronology(
                activityTimeline(met, viewerId, (activity) => (
                  <MeetingBriefAction
                    activity={activity}
                    onBriefMeeting={onBriefMeeting}
                  />
                )),
                hasMore,
              )}
              zone={RECORD_ZONE}
            />
          </SurfaceState>
        </PanelBody>
      </Panel>
    </div>
  );
}
