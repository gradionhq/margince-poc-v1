import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { activityTimeline } from "../design-system/activitytimeline";
import { Avatar } from "../design-system/atoms";
import { GroupedTimelineList } from "../design-system/composed";
import { Eyebrow } from "../design-system/eyebrow";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { SurfaceState, sectionState } from "../design-system/surfacestate";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { useViewerId } from "./common";
import { RECORD_ZONE } from "./company360";
import { PersonCommercialCard, readableRole } from "./personcards";
import { groupChronology } from "./timelinegroups";
import "./person360.css";

// The three tabs that are a READ of what the 360 already assembled — the same
// rule the overview cards hold to (personcards.tsx): none of them fetches, so
// a tab can never show a record the tab beside it is withholding. Files and
// History fetch, because the 360 does not carry them.

type Person360 = components["schemas"]["Person360"];
type Activity = components["schemas"]["Activity"];

// --- Activity ---------------------------------------------------------------

/**
 * PersonActivityTab is the chronology, grouped into the exchanges a reader
 * recognises rather than one row per transport event.
 *
 * It reads the 360's own activities section, which is a PAGE: when the server
 * says there is more, the tab says so through `partial` rather than letting a
 * cut list read as the whole ledger.
 */
export function PersonActivityTab({
  view,
  loading = false,
}: Readonly<{ view?: Person360; loading?: boolean }>) {
  const t = useT();
  const viewerId = useViewerId();
  const logged = view?.activities?.data ?? [];
  const hasMore = view?.activities?.page.has_more ?? false;
  const base = sectionState(
    view,
    "activities",
    Boolean(view?.activities),
    logged.length,
    loading,
  );
  const groups = groupChronology(activityTimeline(logged, viewerId), hasMore);
  return (
    <Panel title={t("person.tab.activity")}>
      <PanelBody>
        <SurfaceState
          state={base === "ready" && hasMore ? "partial" : base}
          emptyLabel={t("person.activity.empty")}
        >
          <GroupedTimelineList groups={groups} zone={RECORD_ZONE} />
        </SurfaceState>
      </PanelBody>
    </Panel>
  );
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
      <Panel title={t("person.tab.deals")}>
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

/**
 * PersonMeetingsTab puts the meeting that has not happened yet above the ones
 * that have. The booked meeting is the server's own next-meeting read, taken
 * through this person's activity link rather than their account's — the org's
 * answer names a meeting this person may not be in.
 */
export function PersonMeetingsTab({
  view,
  loading = false,
}: Readonly<{ view?: Person360; loading?: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  const viewerId = useViewerId();
  const met = (view?.activities?.data ?? []).filter(
    (activity: Activity) => activity.kind === "meeting",
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
              groups={groupChronology(activityTimeline(met, viewerId), hasMore)}
              zone={RECORD_ZONE}
            />
          </SurfaceState>
        </PanelBody>
      </Panel>
    </div>
  );
}
