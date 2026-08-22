import type { ReactNode } from "react";
import type { components } from "../api/schema";
import { Badge, Button, EmptyState, Skeleton } from "../design-system/atoms";
import { Panel, PanelBody, PanelPlate, PanelRow } from "../design-system/panel";
import { formatDateTime } from "../format/format";
import { RECORD_ZONE } from "../format/timezone";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  ENGAGEMENT_LABELS,
  ENGAGEMENT_TONE,
  nextCommitmentLine,
  type SuggestionAction,
  signalKindLabel,
  useSuggestionsBody,
} from "./company360";

// "Today on this account" — the record's daily brief, and the only part of
// the page that answers *what do I do now*. It replaces two earlier cards
// ("Today on this account" and "Worth doing next") that used to say the same
// things twice between them: this one merged reading, not two that agree.
//
// TWO PARTS. A CONTEXT block of dated readings — whose move it is, the way
// in, what was last said, what is running, what is wrong — and, under it,
// the MOVES: the advice rows and the two verbs (draft a follow-up, prepare a
// meeting) that act on that context. The context states what IS; the moves
// say what to DO about it, and the split between them is this component's
// whole reason to exist.
//
// THE SPLIT IS DRAWN, not merely ordered. The context sits on its own tinted
// plate, inset from the panel's edges; the moves run full-bleed on the panel's
// own ground with their verbs at the row's end. A reader can tell which half
// is which before reading a word of either, and nothing on the plate looks
// pressable.
//
// WITHIN THE BLOCK, ONE READING LEADS. Whose move it is, with how long it has
// stood that way, sits above a rule in its own weight; the readings that
// support it are a label-and-answer list beneath. That is a claim about
// importance and it is the only ranking on the panel — everything under the
// rule is drawn alike, because a reader who cannot predict an order stops
// trusting it.
//
// A reading that is BAD NEWS still says so, in colour: an account gone quiet,
// a deal stalled, a move that has been ours for weeks. Nothing else in the
// block is coloured, so the colour means one thing.
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

// One reading under the lead: its name on the left, its answer on the right.
// Every one carries a label, because in a column of answers an unlabelled one
// is a sentence with nothing to hold it — "Demo Admin → Hill Pruksananont" is
// two names and an arrow until "Best route" stands in front of it.
type TodayItem = {
  key: string;
  label: string;
  headline: string;
  // What the reading could not see before making its claim, drawn quieter
  // than the claim itself. Only a reading whose honesty depends on it carries
  // one; it is not a slot for a second sentence.
  qualifier?: string;
};

// The lead reading: whose move it is, at the top of the context block and in
// its own weight. It is the one thing on the panel a reader must know before
// the moves under it mean anything, which is why it is not one row of the
// list beneath it.
type TodayLead = {
  headline: string;
  // How long it has stood that way, beside the state and quieter than it. A
  // state with no duration is a status; with one it is a fact a rep can act
  // on.
  note?: string;
  // Colours the state where it is bad news — an account gone quiet, a move
  // that has been ours for weeks.
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
  // Opens the pre-meeting brief for the day's meeting.
  onPrepareMeeting?: (activityId: string) => void;
  // Starting a message from the account: opens the composer anchored on the
  // account and its recipient.
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
      <Panel title={t("today.title")} tone="accent" className="co-lead">
        <PanelBody>
          <Skeleton width="100%" height={64} />
        </PanelBody>
      </Panel>
    );
  }
  if (failed || !view) {
    return (
      <Panel title={t("today.title")} tone="accent" className="co-lead">
        <PanelBody>
          <EmptyState>{t("today.failed")}</EmptyState>
        </PanelBody>
      </Panel>
    );
  }

  const ctx: TodayContext = {
    view,
    t,
    when: (at: string) => formatDateTime(at, locale, RECORD_ZONE),
  };
  const lead = whoseMove(ctx);
  const items = todayContextItems(ctx);
  const manualMoves = manualMoveRows({ view, t, onPrepareMeeting, onDraftTo });
  const commitment = nextCommitmentLine(view, t);
  const hasContext = lead !== null || items.length > 0;
  const hasMoves = suggestions.ready || manualMoves.length > 0;
  const footer = briefFooter(commitment, suggestions.footer);

  return (
    <Panel
      title={<TodayTitle moves={suggestions.count + manualMoves.length} />}
      tone="accent"
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
          {hasContext && <TodayContextBlock lead={lead} items={items} />}
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

// The band under the rows: what the brief as a whole is counting down to, and
// what the advice could not show. Undefined rather than an empty fragment when
// there is neither — Panel draws the band on truthiness, and an empty band is
// a row of the record spent on nothing.
//
// The way OUT of the brief is deliberately not here: a footer holding one link
// is that same wasted row, so "View tasks" sits beside the panel's name.
function briefFooter(
  commitment: ReturnType<typeof nextCommitmentLine>,
  fromAdvice: ReactNode,
): ReactNode | undefined {
  if (!commitment && !fromAdvice) {
    return undefined;
  }
  return (
    <>
      {commitment && (
        <Badge tone={commitment.overdue ? "warn" : undefined}>
          {commitment.headline}
        </Badge>
      )}
      {fromAdvice}
    </>
  );
}

// The panel's name, and how many moves are waiting behind it. The count
// answers "how much is on me here" before the reader has read a single row,
// and it counts what is DRAWN — a count that included advice the panel
// withheld would send a reader looking for rows that are not there.
function TodayTitle({ moves }: Readonly<{ moves: number }>) {
  const t = useT();
  return (
    <>
      {t("today.title")}
      {moves > 0 && <Badge tone="accent">{moves}</Badge>}
    </>
  );
}

// The context block: the state of the account at the top of the panel, and
// the readings behind it under a rule. It is drawn on its own plate so a
// reader can tell at a glance which part of the panel is what IS and which
// part is what to DO — the moves below run full-bleed on the panel's own
// ground, and nothing here looks pressable.
function TodayContextBlock({
  lead,
  items,
}: Readonly<{ lead: TodayLead | null; items: TodayItem[] }>) {
  return (
    <PanelPlate>
      {lead && (
        <p
          className={
            lead.tone ? `today-lead today-lead-${lead.tone}` : "today-lead"
          }
          data-tile="move"
        >
          {/* A mark, not a word: it colours with the state and says nothing a
              screen reader would have to hear twice. */}
          <span className="today-lead-mark" aria-hidden="true" />
          <span className="today-lead-state">{lead.headline}</span>
          {lead.note && <span className="today-lead-note">{lead.note}</span>}
        </p>
      )}
      {items.length > 0 && (
        <dl className="today-readings">
          {items.map((item) => (
            // The key's prefix names WHICH reading this is ("route",
            // "interaction", …). Exposed so a layout test can anchor on the
            // reading it means without matching drawn copy, which would make
            // a translation edit fail a layout suite.
            <div
              key={item.key}
              className="today-reading"
              data-tile={item.key.split(":")[0]}
            >
              <dt>{item.label}</dt>
              <dd>
                {item.headline}
                {item.qualifier && (
                  <span className="today-reading-note">{item.qualifier}</span>
                )}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </PanelPlate>
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
  { section: "state_strip", label: "today.source.standing" },
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
    bookedMeeting(ctx),
    bestRoute(ctx),
    lastInteraction(ctx),
    openRisk(ctx),
  ].filter((item): item is TodayItem => item !== null);
}

// Whose move it is, and how long it has been. Lifted from the state strip's
// own engagement tile rather than re-derived: the strip no longer draws it
// (it is a DATED reading, not the account's standing state), and the brief
// reads the same `state_strip.engagement` field the strip used to.
function whoseMove({ view, t }: TodayContext): TodayLead | null {
  const engagement = view.state_strip?.engagement;
  if (!engagement) {
    return null;
  }
  return {
    headline: t(ENGAGEMENT_LABELS[engagement.state]),
    note: silenceNote(view, t),
    tone: ENGAGEMENT_TONE[engagement.state],
  };
}

// How long the account has been silent, counted from the last thing WE sent
// and measured against the same `as_of` the rest of the page is read at — so
// the number does not drift while a tab is left open, and does not disagree
// with the dates beside it.
//
// Only when they owe us. Counting days since our own last message says
// nothing when the reply already came, and "no answer in 19 days" against a
// thread that ended with their answer is the kind of line that costs a reader
// trust in the whole panel.
function silenceNote(
  view: Organization360,
  t: TodayContext["t"],
): string | undefined {
  const engagement = view.state_strip?.engagement;
  const sent = engagement?.last_outbound_at;
  if (!sent || engagement.state !== "waiting_on_them") {
    return undefined;
  }
  const days = Math.floor(
    (Date.parse(view.as_of) - Date.parse(sent)) / MS_PER_DAY,
  );
  // Same day, or a clock that disagrees with itself: "no answer in 0 days"
  // reads as a fault, and there is nothing yet to report.
  return days > 0 ? t("today.silence.days", { count: days }) : undefined;
}

const MS_PER_DAY = 24 * 60 * 60 * 1000;

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
    label: t("today.tile.route"),
    headline: t("today.route.headline", {
      colleague: route.display_name,
      contact: best.full_name,
    }),
    qualifier: routeQualifier(view, t),
  };
}

// What this reading did NOT see before calling something best. The contact
// section is one page, so on a large account this is the best route among the
// contacts the page carries — a "best" that is really "best of the first 25"
// is the kind of quiet omission that costs a reader trust in every other
// reading beside it.
function routeQualifier(
  view: Organization360,
  t: TodayContext["t"],
): string | undefined {
  return view.people?.page?.has_more
    ? t("today.route.ofThoseShown")
    : undefined;
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
  // An `info` signal is not a risk. "New opportunity" filed under Risk tells a
  // rep something is wrong when the page meant the opposite, so the name of
  // the reading follows the severity.
  const worrying = signal.severity !== "info";
  return {
    key: `signal:${signal.kind}`,
    // The label is what says this is a judgement: a signal is a threshold
    // someone chose, fired on records rather than seen, and a summary standing
    // on its own in a column of facts would read as one more of them.
    label: t(worrying ? "today.tile.risk" : "today.tile.signal"),
    // The server's own summary when it wrote one: "We wrote 19 days ago and
    // nobody has answered" says what fired AND on what evidence, where the
    // kind alone leaves a reader to guess. The kind is the fallback for a rule
    // that summarised nothing.
    headline: signal.summary ?? signalKindLabel(signal.kind, t),
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
  "message",
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
    label: t("today.tile.lastInteraction"),
    headline: latest.occurred_at
      ? t("today.exchange.subjectWhen", {
          subject: latest.subject,
          when: when(latest.occurred_at),
        })
      : latest.subject,
  };
}

type TodayContext = {
  view: Organization360;
  t: (key: MessageKey, vars?: Record<string, string | number>) => string;
  // Dates are formatted at the presentation edge, so a builder is handed the
  // formatter rather than the reader's locale: none of them chooses a format,
  // and one that could would be the second place this page decides how a date
  // looks.
  when: (at: string) => string;
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
  return {
    key: `meeting:${meeting.activity_id}`,
    label: t("today.tile.meeting"),
    // What it is and when, in that order — the same shape the last exchange
    // reads in, because both answer "what happened, and when". Who is in it
    // belongs to the move row that prepares for it, beside the button: a
    // reading nobody can act on from here does not need the guest list.
    headline: t("today.exchange.subjectWhen", {
      subject: meeting.subject,
      when: when(meeting.starts_at),
    }),
  };
}
