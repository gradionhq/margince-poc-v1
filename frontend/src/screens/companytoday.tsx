import {
  ArrowLeftRight,
  CalendarClock,
  Info,
  type LucideIcon,
  MessageSquare,
  TriangleAlert,
  Waypoints,
} from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState, Skeleton } from "../design-system/atoms";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { formatDateTime } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  ENGAGEMENT_LABELS,
  ENGAGEMENT_TONE,
  nextCommitmentLine,
  RECORD_ZONE,
  type SuggestionAction,
  signalKindLabel,
  signalTone,
  useSuggestionsBody,
} from "./company360";

// "Today on this account" — the record's daily brief, and the only part of
// the page that answers *what do I do now*. It replaces two earlier cards
// ("Today on this account" and "Worth doing next") that used to say the same
// things twice between them: this one merged reading, not two that agree.
//
// TWO PARTS. A CONTEXT band of dated readings — whose move it is, the way
// in, what was last said, what is running, what is wrong — and, under it,
// the MOVES: the advice rows and the two verbs (draft a follow-up, prepare a
// meeting) that act on that context. The context states what IS; the moves
// say what to DO about it, and the split between them is this component's
// whole reason to exist.
//
// THE ONE RULE THAT SHAPES THE CONTEXT BAND: a fact, an assessment and a
// recommendation are different kinds of claim and are labelled differently. "A
// meeting is booked for Thursday" is checkable. "This account has gone quiet" is
// a judgement made from a threshold. Rendering both in one voice is how a
// product gets trusted for the wrong things and then distrusted for all of
// them.
//
// The second rule is quieter and does more work: an item appears only when it
// has something to say. Missing data is not a recommendation — "no meeting
// scheduled" earns a line only when the system can name whom to contact and
// why, which is the suggestion engine's job, not this component's.
//
// WHAT THIS DELIBERATELY DOES NOT CARRY, and the rule behind it: a second,
// weaker rendering of a claim that already has a good one is the duplication
// the page's own rules forbid.
//
//   - the open tasks THEMSELVES, which the Tasks screen lists in full with
//     their quick actions. The footer's commitment reading answers how many
//     are open and how soon, and never repeats a subject you would act on
//     somewhere better;
//   - a converted pipeline total or a KPI reading the strip already carries
//     for the account's STANDING state — this band carries what is DATED.
//
// So this section earns its place by carrying what nothing else says: whose
// move it is, who can reach them, what was last said, what is running, what
// is wrong, and what to do about any of it.

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
  // What kind of reading this is, drawn beside the label. Six tiles of
  // undifferentiated text is a paragraph in a grid.
  icon: LucideIcon;
  // Names the tile — "Whose move", "Best route". The headline below it is
  // the answer, and an answer with no question is a fact floating on a card.
  label: string;
  nature: Nature;
  headline: string;
  // The reason this is here now, in the reader's words. A line that would read
  // the same next month is not today's business.
  detail?: string;
  // Colours the headline where the reading itself is bad news — an open
  // risk, a stalled deal, an account gone quiet.
  tone?: "warn" | "danger";
};

export function TodayOnThisAccount({
  orgId,
  view,
  loading,
  failed,
  onPrepareMeeting,
  onDraftTo,
  onOpenRecord,
  onPerform,
  onOpenTasks,
}: Readonly<{
  orgId: string;
  view?: Organization360;
  loading: boolean;
  // The composite read failed. Distinct from "still loading" and from "nothing
  // is happening on this account" — all three draw a short section, and only
  // one of them is a fact about the account.
  failed: boolean;
  onPrepareMeeting?: (activityId: string) => void;
  // Starting a message from the account. Named separately from
  // onPrepareMeeting because the two open the composer on different grounds:
  // one anchors on a meeting, this one on the account and its recipient.
  onDraftTo?: (personId: string) => void;
  onOpenRecord?: (entityType: string, entityId: string) => void;
  // Performing a suggestion's own action. The composer, the deal and the
  // task form all live above this brief.
  onPerform?: (action: SuggestionAction) => void;
  // Where the footer's commitment reading leads. Absent for a caller with no
  // Tasks tab of its own.
  onOpenTasks?: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  // Called unconditionally regardless of the loading/failed branches below,
  // same as every other hook here — React requires it, and the hook itself
  // already answers "nothing to show" by returning `ready: false`.
  const suggestions = useSuggestionsBody({
    orgId,
    view,
    onOpenRecord,
    onPerform,
  });

  if (loading) {
    return (
      <Panel title={t("today.title")} className="co-lead">
        <PanelBody>
          <Skeleton width="100%" height={64} />
        </PanelBody>
      </Panel>
    );
  }
  if (failed || !view) {
    return (
      <Panel title={t("today.title")} className="co-lead">
        <PanelBody>
          <EmptyState>{t("today.failed")}</EmptyState>
        </PanelBody>
      </Panel>
    );
  }

  const items = todayContextItems({
    view,
    t,
    when: (at: string) => formatDateTime(at, locale, RECORD_ZONE),
    locale,
  });
  const manualMoves = manualMoveRows({ view, t, onPrepareMeeting, onDraftTo });
  const commitment = nextCommitmentLine(view, t);
  const hasContext = items.length > 0;
  const hasMoves = suggestions.ready || manualMoves.length > 0;
  // The way OUT of the brief belongs beside its name, not under it: a footer
  // band holding one link is a whole row of the record spent on a link. The
  // band below is kept for what genuinely belongs to the brief as a whole —
  // the commitment it is counting down to, and what the advice could not show.
  const footer =
    commitment || suggestions.footer ? (
      <>
        {commitment && (
          <Badge tone={commitment.overdue ? "warn" : undefined}>
            {commitment.headline}
          </Badge>
        )}
        {suggestions.footer}
      </>
    ) : undefined;

  return (
    <Panel
      title={t("today.title")}
      className="co-lead"
      titleAction={
        onOpenTasks && (
          <Button small variant="ghost" onClick={onOpenTasks}>
            {t("co.suggest.viewTasks")}
          </Button>
        )
      }
      footer={footer}
    >
      {!hasContext && !hasMoves ? (
        // Not "nothing to do": the brief read everything it can read and
        // found nothing that needs a person today. That is a real answer
        // and it is different from the account being empty.
        <PanelBody>
          <EmptyState>{t("today.quiet")}</EmptyState>
        </PanelBody>
      ) : (
        <>
          {hasContext && (
            <PanelBody>
              <ul className="today-tiles">
                {items.map((item) => (
                  // The key's prefix names WHICH reading this is ("move",
                  // "interaction", …). Exposed so a layout test can anchor on
                  // the tile it means without matching drawn copy, which
                  // would make a translation edit fail a layout suite.
                  <li
                    key={item.key}
                    className="today-tile"
                    data-tile={item.key.split(":")[0]}
                  >
                    <span className="today-tile-label">
                      <item.icon size={14} aria-hidden="true" />
                      {item.label}
                    </span>
                    <span
                      className={
                        item.tone
                          ? `today-headline today-headline-${item.tone}`
                          : "today-headline"
                      }
                    >
                      {item.headline}
                    </span>
                    {item.detail && (
                      <span className="today-detail">{item.detail}</span>
                    )}
                    {/* The kind of claim, last: it qualifies the reading
                        above it rather than announcing it. Every tile
                        carries one, fact included — a label that appears
                        only on the judgements teaches the reader that
                        unlabelled means unexamined. */}
                    <Badge tone={NATURE_TONE[item.nature]}>
                      {t(NATURE_LABEL[item.nature])}
                    </Badge>
                  </li>
                ))}
              </ul>
            </PanelBody>
          )}
          {suggestions.rows}
          {manualMoves}
        </>
      )}
      {(view.sections_omitted?.length ?? 0) > 0 && (
        <TodayWithheld view={view} />
      )}
    </Panel>
  );
}

// manualMoveRows are the two verbs the context band used to lead to as a
// sidebar: drafting a follow-up to the strongest reachable contact, and
// preparing for a booked meeting. Rendered as full-bleed "move" rows now,
// the same anatomy the advice rows use, so a reader meets one shape for
// every action on this panel rather than a sidebar of buttons beside a grid
// of tiles.
function manualMoveRows({
  view,
  t,
  onPrepareMeeting,
  onDraftTo,
}: Readonly<{
  view: Organization360;
  t: TodayContext["t"];
  onPrepareMeeting?: (activityId: string) => void;
  onDraftTo?: (personId: string) => void;
}>): ReactNode[] {
  const recipient = [...(view.people?.data ?? [])].sort(byStrengthThenId)[0];
  const meeting = view.next_meeting;
  const rows: ReactNode[] = [];
  // "Draft follow-up to <name>" names the strongest reachable contact,
  // because a button that says who it will write to is a decision the
  // reader can check before pressing it. It opens the composer grounded on
  // the ACCOUNT rather than on a message, which is why it passes the
  // recipient rather than an activity.
  if (recipient && onDraftTo) {
    rows.push(
      <PanelRow key="move:draft" className="co-move">
        <span className="co-move-body">
          <span className="co-move-ask">
            {t("today.draft.to", { name: firstName(recipient.full_name) })}
          </span>
          <span className="co-move-do">
            <span className="co-move-actions">
              <Button
                variant="primary"
                small
                onClick={() => onDraftTo(recipient.person_id)}
              >
                {/* The verb alone. The ask beside it already names who is
                    being written to, and a button that repeats the whole
                    sentence makes the row read as two moves. */}
                {t("today.draft.act")}
              </Button>
            </span>
          </span>
        </span>
      </PanelRow>,
    );
  }
  if (meeting && onPrepareMeeting) {
    const who = meeting.participants
      .map((participant) => participant.display_name)
      .join(", ");
    rows.push(
      <PanelRow key="move:meeting" className="co-move">
        <span className="co-move-body">
          <span className="co-move-ask">{meeting.subject}</span>
          {who && <span className="co-move-why">{who}</span>}
          <span className="co-move-do">
            <span className="co-move-actions">
              <Button
                small
                onClick={() => onPrepareMeeting(meeting.activity_id)}
              >
                {t("today.meeting.prepare")}
              </Button>
            </span>
          </span>
        </span>
      </PanelRow>,
    );
  }
  return rows;
}

// The button names a person, and "Draft follow-up to Sarah Cole-Hagemeyer"
// wraps to two lines in the column it sits in. A first name is how a rep
// refers to a contact they are about to write to anyway.
function firstName(fullName: string): string {
  return fullName.split(" ")[0] || fullName;
}

// omitted answers "was this withheld from me", which the page must never
// render as "there is none".
function omitted(view: Organization360, section: string): boolean {
  return (view.sections_omitted ?? []).some((name) => name === section);
}

// The sections this brief is assembled from. Naming them lets the footer say
// which ones the reader is missing, rather than silently composing a shorter
// brief and letting them believe it is complete. Each of the context band's
// several sections is withheld INDEPENDENTLY — one missing grant must not
// blank the rest of the band, and this list is what lets the footer say
// exactly which one it was.
//
// One entry per section a tile actually reads, and no others: a footer that
// reports a withheld section nothing here uses teaches the reader to ignore
// it, and one that omits a section a tile DOES use lets that tile vanish in
// silence.
const TODAY_SOURCES: ReadonlyArray<{ section: string; label: MessageKey }> = [
  { section: "next_steps", label: "today.source.nextSteps" },
  { section: "next_meeting", label: "today.source.nextMeeting" },
  { section: "people", label: "today.source.people" },
  { section: "deals", label: "today.source.deals" },
  // Whose move it is and the risk tile both read `state_strip` (whoseMove
  // and openRisk, below) — that is the section name the server actually
  // omits when a caller has no grant on it.
  { section: "state_strip", label: "today.source.signals" },
  { section: "activities", label: "today.source.activities" },
  // The moves themselves: a withheld advice section reads as "nothing to
  // add" from `useSuggestionsBody`, which is the right call for the rows —
  // but the footer still has to say the reader is missing them.
  { section: "suggestions", label: "today.source.suggestions" },
];

function TodayWithheld({ view }: Readonly<{ view: Organization360 }>) {
  const t = useT();
  const hidden = TODAY_SOURCES.filter((source) =>
    omitted(view, source.section),
  );
  if (hidden.length === 0) {
    return null;
  }
  // "Hidden from you", never "None". A brief assembled from some of its
  // sources is not the same brief, and the reader is the only one who can
  // judge whether the missing one mattered.
  return (
    <p className="today-withheld">
      {t("today.withheld", {
        sections: hidden.map((source) => t(source.label)).join(", "),
      })}
    </p>
  );
}

// todayContextItems is the ordering decision, in one place and in priority
// order — the context band's own reading order, before the moves under it.
//
// Whose move it is outranks the way in, which outranks what was last said,
// which outranks what is wrong. Nothing here is scored — the order is fixed,
// because a ranking a reader cannot predict is one they stop trusting. What
// is OWED moved to the footer as the commitment reading (how much, how
// soon), and the active-opportunity reading moved to the Commercial panel
// (organizations.tsx) — both answer questions this band no longer needs to.
//
// One builder per rule, each free to answer "nothing to say" by returning
// null. The alternative — one function branching over every source — was
// the shape that made the ordering invisible inside the conditions.
function todayContextItems(ctx: TodayContext): TodayItem[] {
  return [
    whoseMove(ctx),
    bookedMeeting(ctx),
    bestRoute(ctx),
    lastInteraction(ctx),
    openRisk(ctx),
  ].filter((item): item is TodayItem => item !== null);
}

// Whose move it is, and since when. Lifted from the state strip's own
// engagement tile rather than re-derived: the strip no longer draws it (it
// is a DATED reading, not the account's standing state), and the brief reads
// the same `state_strip.engagement` field the strip used to.
function whoseMove({ view, t, when }: TodayContext): TodayItem | null {
  const engagement = view.state_strip?.engagement;
  if (!engagement) {
    return null;
  }
  const detail =
    engagement.last_inbound_at || engagement.last_outbound_at
      ? t("co.strip.lastBoth", {
          inbound: engagement.last_inbound_at
            ? when(engagement.last_inbound_at)
            : t("co.strip.never"),
          outbound: engagement.last_outbound_at
            ? when(engagement.last_outbound_at)
            : t("co.strip.never"),
        })
      : undefined;
  return {
    key: "move",
    icon: ArrowLeftRight,
    label: t("co.strip.engagement"),
    nature: "fact",
    headline: t(ENGAGEMENT_LABELS[engagement.state]),
    detail,
    tone: ENGAGEMENT_TONE[engagement.state],
  };
}

// Who on our side can actually reach the account, and through whom.
//
// The rule, written down because it is a choice and not a derivation: of the
// contacts who HAVE a route, the strongest by their own relationship score,
// then that contact's strongest route — which the server already sorts, so
// `top[0]` is it. Filtering first is deliberate: the strongest contact overall
// may be someone nobody has ever written to, and naming them with no way in
// answers a different question than the one the tile asks.
//
// The people section is a page of 25, so on a large account this is the best
// route among the contacts the page carries rather than provably the best on
// the account. The tile says so when the section is truncated — a "best" that
// is really "best of the first 25" is the kind of quiet qualifier that costs
// a reader trust in every other figure.
function bestRoute({ view, t }: TodayContext): TodayItem | null {
  const contacts = view.people?.data ?? [];
  const best = contacts
    .filter((contact) => (contact.routes?.top?.length ?? 0) > 0)
    .sort(byStrengthThenId)[0];
  const route = best?.routes?.top?.[0];
  if (!best || !route) {
    return null;
  }
  return {
    key: `route:${best.person_id}`,
    icon: Waypoints,
    label: t("today.tile.route"),
    nature: "fact",
    headline: t("today.route.headline", {
      colleague: route.display_name,
      contact: best.full_name,
    }),
    detail: routeDetail(view, best, t),
  };
}

// The remainder of THIS contact's routes, and — when the contact list itself
// was capped — that the page did not see every contact before choosing.
function routeDetail(
  view: Organization360,
  best: Organization360Contact,
  t: TodayContext["t"],
): string | undefined {
  const parts: string[] = [];
  if (best.routes && best.routes.remainder > 0) {
    parts.push(t("today.route.remainder", { count: best.routes.remainder }));
  }
  if (view.people?.page?.has_more) {
    parts.push(t("today.route.ofThoseShown"));
  }
  return parts.length > 0 ? parts.join(" · ") : undefined;
}

// Strongest first, with the id as the tiebreak so two contacts on the same
// score do not swap places between renders of the same data.
function byStrengthThenId(
  a: Organization360Contact,
  b: Organization360Contact,
): number {
  const delta = (b.strength?.score ?? 0) - (a.strength?.score ?? 0);
  return delta !== 0 ? delta : a.person_id.localeCompare(b.person_id);
}

// The deal actually in play used to be a context tile here. It moved to the
// Commercial panel (organizations.tsx, CommercialPanel) with the open deals
// tile — that panel already lists every open deal with its stage and amount,
// so a "largest deal" reading beside it was the weaker of the two renderings
// this file's own rule forbids.

// What is wrong, if anything is. The state strip's signal is the worst thing
// standing open on the account — already chosen by the server, so this tile
// repeats its verdict rather than forming a second one that could disagree.
function openRisk({ view, t }: TodayContext): TodayItem | null {
  const signal = view.state_strip?.signal;
  if (!signal) {
    return null;
  }
  // An `info` signal is not a risk. "New opportunity" under a Risk heading in
  // a warning colour tells a rep something is wrong when the page meant the
  // opposite, so the label and the tone both follow the severity.
  const worrying = signal.severity !== "info";
  return {
    key: `signal:${signal.kind}`,
    icon: worrying ? TriangleAlert : Info,
    label: t(worrying ? "today.tile.risk" : "today.tile.signal"),
    // An assessment, not a fact: a signal is a threshold someone chose,
    // fired on records rather than observed directly.
    nature: "assessment",
    headline: signalKindLabel(signal.kind, t),
    detail: signal.summary ?? undefined,
    tone: signalTone(signal.severity),
  };
}

// The kinds that are an EXCHANGE with the account. The 360's timeline section
// is unfiltered — it carries tasks and meetings from the same table — and a
// task is something we wrote to ourselves rather than something that was said.
const EXCHANGE_KINDS: ReadonlySet<string> = new Set([
  "email",
  "call",
  "meeting",
  "note",
  "whatsapp",
  "telegram",
]);

/**
 * What was last said, and when.
 *
 * The subject of the most recent exchange, which is the one reading a rep
 * opens the page for that no other tile carries: the footer's commitment
 * reading says what we OWE, this says what was SAID.
 *
 * The pulse line under the title still names both directions with their dates,
 * and this does not replace it. The two answer different questions — the pulse
 * is who wrote last, which is the direction a rep acts on, and one tile could
 * only ever show the later of the two. This is what the exchange was ABOUT.
 *
 * TWO FILTERS, and neither is cosmetic. The timeline carries every activity
 * kind, so without them the head of the list can be a TASK — whose subject
 * this file refuses to render twice — or a meeting scheduled for next week,
 * which `occurred_at DESC` sorts to the top and which has not been said yet.
 *
 * A FACT: the subject is what the activity says, quoted rather than judged.
 * The builder returns null both when the section was withheld and when nothing
 * has been logged; it cannot tell those apart, and the withheld footer below
 * is what tells a reader they are missing what was said.
 */
function lastInteraction({ view, t, when }: TodayContext): TodayItem | null {
  const latest = (view.activities?.data ?? []).find(
    (activity) =>
      EXCHANGE_KINDS.has(activity.kind) &&
      Boolean(activity.subject) &&
      // Already happened, as of the read the rest of this page describes.
      Boolean(activity.occurred_at) &&
      (activity.occurred_at as string) <= view.as_of,
  );
  if (!latest?.subject) {
    return null;
  }
  return {
    key: `interaction:${latest.id}`,
    icon: MessageSquare,
    label: t("today.tile.lastInteraction"),
    nature: "fact",
    headline: latest.subject,
    detail: latest.occurred_at ? when(latest.occurred_at) : undefined,
  };
}

type TodayContext = {
  view: Organization360;
  t: (key: MessageKey, vars?: Record<string, string | number>) => string;
  when: (at: string) => string;
  // Money is formatted at the presentation edge, so the deal tile needs the
  // reader's locale rather than a pre-formatted string.
  locale: Locale;
};

type Organization360Contact = NonNullable<
  Organization360["people"]
>["data"][number];

// The meeting.
//
// ABSENT MEANS TWO THINGS and only `sections_omitted` separates them: named
// there, the reader has no calendar access; not named, the grant is held and
// nothing is scheduled. This builder writes a line for neither — a booked
// meeting is the only thing it has to say — and the withheld footer below is
// what tells a reader they are missing the calendar. Advising "book one"
// belongs to the suggestion engine, the only thing that can name whom.
function bookedMeeting({ view, t, when }: TodayContext): TodayItem | null {
  const meeting = view.next_meeting;
  if (!meeting) {
    return null;
  }
  const who = meeting.participants
    .map((participant) => participant.display_name)
    .join(", ");
  return {
    key: `meeting:${meeting.activity_id}`,
    icon: CalendarClock,
    label: t("today.tile.meeting"),
    nature: "fact",
    // Preparing for it is the moves section's verb now, not a link inside
    // the tile: one button, in the place the reader looks for verbs.
    headline: meeting.subject,
    detail: who
      ? t("today.meeting.withWhen", { who, when: when(meeting.starts_at) })
      : when(meeting.starts_at),
  };
}
