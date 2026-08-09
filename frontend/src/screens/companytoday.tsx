import {
  CalendarCheck,
  CalendarClock,
  History,
  type LucideIcon,
  Target,
  TriangleAlert,
  Waypoints,
} from "lucide-react";
import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState } from "../design-system/atoms";
import { formatDateTime, formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { RECORD_ZONE, SectionCard, signalKindLabel } from "./company360";

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
// WHAT THIS DELIBERATELY DOES NOT CARRY, and the rule behind all three: a
// second, weaker rendering of a claim that already has a good one is the
// duplication the page's own rules forbid.
//
//   - the suggestions, which the account brief renders with their evidence
//     chips, their action states and their dismissal;
//   - the open tasks THEMSELVES, which the next-steps card lists in full with
//     its quick actions. The commitment tile answers how many are open and how
//     soon — the question the card does not put at the top of the page — and
//     never repeats a subject the card is about to render with a due-date edit
//     and a complete button beside it;
//   - the last interaction, which the account pulse line under the title
//     already names in BOTH directions with their dates. One tile could only
//     show the later of the two, and which side wrote last is the whole
//     distinction a reader acts on.
//
// So this section earns its place by carrying what nothing else says: what is
// owed, when we next speak, who can reach them, what is running, and what is
// wrong.

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
  // Names the tile — "Next commitment", "Best route". The headline below it is
  // the answer, and an answer with no question is a fact floating on a card.
  label: string;
  nature: Nature;
  headline: string;
  // The reason this is here now, in the reader's words. A line that would read
  // the same next month is not today's business.
  detail?: string;
  // Colours the headline where the reading itself is bad news — an overdue
  // commitment, a stalled deal, an open risk.
  tone?: "warn" | "danger";
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
    locale,
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
        // Tiles on the left, the verbs they lead to on the right (mockup State
        // D). The tile grid flows: it holds six on a wide screen and folds to
        // fewer columns rather than shrinking them past reading width.
        <div className="today-body">
          <ul className="today-tiles">
            {items.map((item) => (
              <li key={item.key} className="today-tile">
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
                {/* The kind of claim, last: it qualifies the reading above it
                    rather than announcing it. Every tile carries one, fact
                    included — a label that appears only on the judgements
                    teaches the reader that unlabelled means unexamined, and
                    the whole point of the vocabulary is that "checkable" is
                    itself a claim worth making. */}
                <Badge tone={NATURE_TONE[item.nature]}>
                  {t(NATURE_LABEL[item.nature])}
                </Badge>
                {item.action}
              </li>
            ))}
          </ul>
          <TodayActions view={view} t={t} onPrepareMeeting={onPrepareMeeting} />
        </div>
      )}
      <TodayWithheld view={view} />
    </SectionCard>
  );
}

// The two verbs the tiles lead to, in the column State D puts them in.
//
// "Draft follow-up to <name>" names the strongest contact, because a button
// that says who it will write to is a decision the reader can check before
// pressing it. It is DISABLED until account drafting exists (DRAFT-WIRE-N-1):
// a composer that opens with nothing drafted is worse than a button that says
// why it cannot yet, and the reason is on the button rather than in a tooltip
// nobody hovers.
function TodayActions({
  view,
  t,
  onPrepareMeeting,
}: Readonly<{
  view: Organization360;
  t: TodayContext["t"];
  onPrepareMeeting?: (activityId: string) => void;
}>) {
  const recipient = [...(view.people?.data ?? [])].sort(byStrengthThenId)[0];
  const meeting = view.next_meeting;
  if (!recipient && !meeting) {
    return null;
  }
  return (
    <div className="today-actions">
      {recipient && (
        <Button
          variant="primary"
          disabled
          title={t("today.draft.notYet")}
          onClick={undefined}
        >
          {t("today.draft.to", { name: firstName(recipient.full_name) })}
        </Button>
      )}
      {meeting && onPrepareMeeting && (
        <Button onClick={() => onPrepareMeeting(meeting.activity_id)}>
          {t("today.meeting.prepare")}
        </Button>
      )}
      {recipient && (
        <p className="today-actions-note">{t("today.draft.notYet")}</p>
      )}
    </div>
  );
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
//
// The six the mockup's State D draws, in its reading order: what we owe, when
// we next speak, who can reach them, what was last said, what is running, and
// what is wrong. Every one is derived from a section the 360 already serves;
// none of them is a model output.
function todayItems(ctx: TodayContext): TodayItem[] {
  return [
    nextCommitment(ctx),
    bookedMeeting(ctx),
    bestRoute(ctx),
    activeOpportunity(ctx),
    openRisk(ctx),
    changedSinceLastVisit(ctx),
  ].filter((item): item is TodayItem => item !== null);
}

// What we owe them, as a COUNT and a deadline rather than as the task itself.
//
// The mockup draws the task's subject here, and the next-steps card below
// lists every open task in full with its quick actions. Rendering the subject
// in both is the duplication this file's own rule forbids, and it would be the
// weaker of the two renderings — no due-date edit, no complete, no snooze. So
// the tile answers the question the card does not put at the top of the page —
// how much is open and how soon — and the card stays where you act on it.
//
// `next_steps.data` is already ordered overdue → due → undated by the server,
// so the head of the list is the soonest and this makes no ordering decision.
function nextCommitment({ view, t, when }: TodayContext): TodayItem | null {
  const steps = view.next_steps?.data ?? [];
  const step = steps[0];
  if (!step) {
    return null;
  }
  const overdue = steps.filter((each) => each.overdue).length;
  return {
    key: `commitment:${step.activity_id}`,
    icon: CalendarCheck,
    label: t("today.tile.commitment"),
    nature: "fact",
    headline:
      overdue > 0
        ? t("today.commitment.overdueCount", { count: overdue })
        : t("today.commitment.openCount", { count: steps.length }),
    detail: dueLine(step, t, when),
    tone: step.overdue ? "warn" : undefined,
  };
}

function dueLine(
  step: NonNullable<Organization360["next_steps"]>["data"][number],
  t: TodayContext["t"],
  when: TodayContext["when"],
): string | undefined {
  if (!step.due_at) {
    // An undated task is a real state, and saying nothing about the date is
    // more honest than implying one.
    return t("today.commitment.undated");
  }
  return step.overdue
    ? t("today.commitment.overdue", { when: when(step.due_at) })
    : t("today.commitment.due", { when: when(step.due_at) });
}

// Who on our side can actually reach the account, and through whom.
//
// The rule, written down because it is a choice and not a derivation: the
// strongest CONTACT by their own relationship score, then that contact's
// strongest ROUTE — which the server already sorts, so `top[0]` is it. A
// contact nobody has ever written to carries no route and is skipped rather
// than named with an empty way in.
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
    detail:
      best.routes && best.routes.remainder > 0
        ? t("today.route.remainder", { count: best.routes.remainder })
        : undefined,
  };
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

// The mockup's sixth tile — "Last meaningful interaction" — is deliberately
// NOT here. The account pulse line under the page title already names both
// directions with their dates ("They wrote 3 days ago · We wrote 12 May"),
// and it says more than a tile would: the pulse keeps the two apart, which is
// the distinction a reader acts on, where one tile can only show the later of
// them. A second, shorter rendering of the same two timestamps is the
// duplication this file's own rule forbids.

// The deal actually in play.
//
// The selection rule, written down because it is a choice: the largest open
// deal by `amount_minor`, with the deal id as the tiebreak so two equal deals
// do not swap places between renders. Unpriced deals rank last rather than as
// zero — a deal nobody has costed is not a deal worth nothing.
//
// A deal's `amount` is in the deal's OWN currency and carries no base
// conversion, so ranking across currencies would be comparing 100 JPY against
// 100 EUR. When the open deals do not share one currency this tile names the
// count instead of picking a winner: the state strip is where a converted
// total belongs, and it is the surface that carries the FX as-of date.
function activeOpportunity({
  view,
  t,
  locale,
}: TodayContext): TodayItem | null {
  const open = view.deals?.data ?? [];
  if (open.length === 0) {
    return null;
  }
  if (!sharesOneCurrency(open)) {
    return {
      key: "deal:mixed",
      icon: Target,
      label: t("today.tile.opportunity"),
      nature: "fact",
      headline: t("today.deal.count", { count: open.length }),
      detail: t("today.deal.mixedCurrency"),
    };
  }
  const deal = [...open].sort(byAmountThenId)[0];
  if (!deal) {
    return null;
  }
  const amount =
    deal.amount?.amount_minor != null && deal.amount.currency
      ? formatMoney(deal.amount.amount_minor, deal.amount.currency, locale)
      : undefined;
  return {
    key: `deal:${deal.deal_id}`,
    icon: Target,
    label: t("today.tile.opportunity"),
    nature: "fact",
    headline: amount
      ? t("today.deal.headline", { name: deal.name, amount })
      : deal.name,
    detail: deal.stage_name ?? undefined,
    tone: deal.stalled ? "warn" : undefined,
  };
}

// Whether every priced deal here is in the same currency. Unpriced deals are
// ignored: they carry no currency to disagree with, and they cannot win the
// ranking anyway.
function sharesOneCurrency(deals: readonly Organization360Deal[]): boolean {
  const currencies = new Set(
    deals
      .map((deal) => deal.amount?.currency)
      .filter((currency): currency is string => Boolean(currency)),
  );
  return currencies.size <= 1;
}

function byAmountThenId(
  a: Organization360Deal,
  b: Organization360Deal,
): number {
  // An unpriced deal sorts last rather than as 0: the page cannot price it,
  // which is not the same as it being worth nothing.
  const left = a.amount?.amount_minor ?? -1;
  const right = b.amount?.amount_minor ?? -1;
  return right !== left ? right - left : a.deal_id.localeCompare(b.deal_id);
}

// What is wrong, if anything is. The state strip's signal is the worst thing
// standing open on the account — already chosen by the server, so this tile
// repeats its verdict rather than forming a second one that could disagree.
function openRisk({ view, t }: TodayContext): TodayItem | null {
  const signal = view.state_strip?.signal;
  if (!signal) {
    return null;
  }
  return {
    key: `risk:${signal.kind}`,
    icon: TriangleAlert,
    label: t("today.tile.risk"),
    // An assessment, not a fact: a signal is a threshold someone chose,
    // fired on records rather than observed directly.
    nature: "assessment",
    headline: signalKindLabel(signal.kind, t),
    detail: signal.summary ?? undefined,
    tone: signal.severity === "urgent" ? "danger" : "warn",
  };
}

type TodayContext = {
  view: Organization360;
  t: (key: MessageKey, vars?: Record<string, string | number>) => string;
  when: (at: string) => string;
  // Money is formatted at the presentation edge, so the deal tile needs the
  // reader's locale rather than a pre-formatted string.
  locale: Locale;
  onPrepareMeeting?: (activityId: string) => void;
};

type Organization360Contact = NonNullable<
  Organization360["people"]
>["data"][number];
type Organization360Deal = NonNullable<
  Organization360["deals"]
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
    // Preparing for it is the CTA column's verb now, not a link inside the
    // tile: one button, in the place the reader looks for verbs.
    headline: meeting.subject,
    detail: who
      ? t("today.meeting.withWhen", { who, when: when(meeting.starts_at) })
      : when(meeting.starts_at),
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
    icon: History,
    label: t("today.tile.since"),
    nature: "fact",
    headline: t("today.since.headline", { count: since.new_activities }),
    detail: since.baseline_at
      ? t("today.since.baseline", { when: when(since.baseline_at) })
      : t("today.since.firstVisit"),
  };
}
