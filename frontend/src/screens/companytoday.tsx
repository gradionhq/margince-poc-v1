import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Badge, EmptyState } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { RECORD_ZONE, SectionCard } from "./company360";

// "Today on this account" — the first thing a rep reads, and the only section
// that answers *what do I do now*.
//
// Everything here already existed on the 360; what did not exist was one place
// that put it in the order a person works. A page that shows next steps, health,
// suggestions and last-touch as four equal cards makes the reader do the
// prioritizing, before a call, every time.
//
// THE ONE RULE THAT SHAPES THE WHOLE COMPONENT: a fact, an assessment and a
// recommendation are different kinds of claim and are labelled differently. "A
// meeting is booked for Thursday" is checkable. "This account has gone quiet" is
// a judgement made from a threshold. "Write to Dana" is advice. Rendering all
// three in one voice is how a product gets trusted for the wrong things and
// then distrusted for all of them.
//
// The second rule is quieter and does more work: an item appears only when it
// has something to say. Missing data is not a recommendation — "no meeting
// scheduled" earns a line only when the system can name whom to contact and
// why, which is the suggestion engine's job, not this component's.
//
// WHAT THIS DELIBERATELY DOES NOT CARRY: the suggestions, which the account
// brief already renders with their evidence chips, their action states and
// their dismissal; and the open commitments, which the next-steps card already
// lists in full with its quick actions. Repeating either here is the
// duplication the page's own rules forbid, and it would be a second, weaker
// rendering of claims that already have a good one. This section earns its
// place by carrying what nothing else says.

type Organization360 = components["schemas"]["Organization360"];

// The three kinds of claim, in the order a reader should trust them.
type Nature = "fact" | "assessment" | "recommendation";

const NATURE_LABEL: Record<Nature, MessageKey> = {
  fact: "today.nature.fact",
  assessment: "today.nature.assessment",
  recommendation: "today.nature.recommendation",
};

const NATURE_TONE: Record<Nature, "accent" | "warn" | undefined> = {
  fact: undefined,
  assessment: "warn",
  recommendation: "accent",
};

type TodayItem = {
  key: string;
  nature: Nature;
  headline: string;
  // The reason this is here now, in the reader's words. A line that would read
  // the same next month is not today's business.
  detail?: string;
  action?: ReactNode;
};

export function TodayOnThisAccount({
  view,
  loading,
  failed,
  onPrepareMeeting,
}: Readonly<{
  view?: Organization360;
  loading: boolean;
  // The composite read failed. Distinct from "still loading" and from "nothing
  // is happening on this account" — all three draw a short section, and only
  // one of them is a fact about the account.
  failed: boolean;
  onPrepareMeeting?: (activityId: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();

  if (loading) {
    return (
      <SectionCard
        title={t("today.title")}
        state="loading"
        emptyLabel={t("today.quiet")}
      >
        {null}
      </SectionCard>
    );
  }
  if (failed || !view) {
    return (
      <SectionCard
        title={t("today.title")}
        state="ready"
        emptyLabel={t("today.quiet")}
      >
        <EmptyState>{t("today.failed")}</EmptyState>
      </SectionCard>
    );
  }

  const items = todayItems({
    view,
    t,
    when: (at: string) => formatDateTime(at, locale, RECORD_ZONE),
    onPrepareMeeting,
  });

  return (
    <SectionCard
      title={t("today.title")}
      state="ready"
      emptyLabel={t("today.quiet")}
    >
      {items.length === 0 ? (
        // Not "nothing to do": the section read everything it can read and found
        // nothing that needs a person today. That is a real answer and it is
        // different from the account being empty.
        <EmptyState>{t("today.quiet")}</EmptyState>
      ) : (
        <ul className="today-list">
          {items.map((item) => (
            <li key={item.key} className="today-item">
              <Badge tone={NATURE_TONE[item.nature]}>
                {t(NATURE_LABEL[item.nature])}
              </Badge>
              <span className="today-headline">{item.headline}</span>
              {item.detail && (
                <span className="today-detail">{item.detail}</span>
              )}
              {item.action}
            </li>
          ))}
        </ul>
      )}
      <TodayWithheld view={view} />
    </SectionCard>
  );
}

// omitted answers "was this withheld from me", which the page must never
// render as "there is none".
function omitted(view: Organization360, section: string): boolean {
  return (view.sections_omitted ?? []).some((name) => name === section);
}

// The sections this list is assembled from. Naming them lets the footer say
// which ones the reader is missing, rather than silently composing a shorter
// list and letting them believe it is complete.
const TODAY_SOURCES: ReadonlyArray<{ section: string; label: MessageKey }> = [
  { section: "next_steps", label: "today.source.nextSteps" },
  { section: "next_meeting", label: "today.source.nextMeeting" },
  { section: "deals", label: "today.source.deals" },
  { section: "suggestions", label: "today.source.suggestions" },
  { section: "last_touch", label: "today.source.lastTouch" },
];

function TodayWithheld({ view }: Readonly<{ view: Organization360 }>) {
  const t = useT();
  const hidden = TODAY_SOURCES.filter((source) =>
    omitted(view, source.section),
  );
  if (hidden.length === 0) {
    return null;
  }
  // "Hidden from you", never "None". A list assembled from four of five sources
  // is not the same list, and the reader is the only one who can judge whether
  // the missing one mattered.
  return (
    <p className="today-withheld">
      {t("today.withheld", {
        sections: hidden.map((source) => t(source.label)).join(", "),
      })}
    </p>
  );
}

// todayItems is the ordering decision, in one place and in priority order.
//
// A commitment we made outranks a meeting we will have, which outranks what
// changed while we were away, which outranks what the machine noticed. Nothing
// here is scored — the order is fixed, because a ranking a reader cannot
// predict is one they stop trusting.
//
// One builder per rule, each free to answer "nothing to say" by returning null.
// The alternative — one function branching over every source — was the shape
// that made the ordering invisible inside the conditions.
function todayItems(ctx: TodayContext): TodayItem[] {
  return [bookedMeeting(ctx), changedSinceLastVisit(ctx)].filter(
    (item): item is TodayItem => item !== null,
  );
}

type TodayContext = {
  view: Organization360;
  t: (key: MessageKey, vars?: Record<string, string | number>) => string;
  when: (at: string) => string;
  onPrepareMeeting?: (activityId: string) => void;
};

// The meeting.
//
// ABSENT MEANS TWO THINGS and only `sections_omitted` separates them: named
// there, the reader has no calendar access; not named, the grant is held and
// nothing is scheduled. This builder writes a line for neither — a booked
// meeting is the only thing it has to say — and the withheld footer below is
// what tells a reader they are missing the calendar. Advising "book one"
// belongs to the suggestion engine, the only thing that can name whom.
function bookedMeeting({
  view,
  t,
  when,
  onPrepareMeeting,
}: TodayContext): TodayItem | null {
  const meeting = view.next_meeting;
  if (!meeting) {
    return null;
  }
  const who = meeting.participants
    .map((participant) => participant.display_name)
    .join(", ");
  return {
    key: `meeting:${meeting.activity_id}`,
    nature: "fact",
    headline: t("today.meeting.headline", {
      subject: meeting.subject,
      when: when(meeting.starts_at),
    }),
    detail: who ? t("today.meeting.with", { who }) : undefined,
    action: onPrepareMeeting ? (
      <button
        type="button"
        className="link-button"
        onClick={() => onPrepareMeeting(meeting.activity_id)}
      >
        {t("today.meeting.prepare")}
      </button>
    ) : undefined,
  };
}

// What changed while the reader was away. Always a fact, and only worth a line
// when something did change.
function changedSinceLastVisit({
  view,
  t,
  when,
}: TodayContext): TodayItem | null {
  const since = view.since_last_visit;
  if (!since || since.new_activities === 0) {
    return null;
  }
  return {
    key: "since",
    nature: "fact",
    headline: t("today.since.headline", { count: since.new_activities }),
    detail: since.baseline_at
      ? t("today.since.baseline", { when: when(since.baseline_at) })
      : t("today.since.firstVisit"),
  };
}
