import {
  CalendarClock,
  CheckSquare,
  Clock,
  Mail,
  MessageCircle,
  PencilLine,
  Phone,
  Send,
  StickyNote,
} from "lucide-react";
import type { ReactNode } from "react";
import { useLayoutEffect, useRef, useState } from "react";
import {
  formatDate,
  formatDuration,
  formatMoney,
  formatTimelineTimestamp,
} from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { Avatar, Badge, Button } from "./atoms";
import {
  AutonomyDot,
  type ConfidenceLevel,
  ConfidenceMeter,
  type Evidence,
  EvidenceChip,
  type Proposal,
  type Provenance,
  ProvenanceTag,
  type Resolution,
  StagedProposal,
} from "./trust";
import "./composed.css";

// Composed surfaces (B-EP09.3b): the brief item, the pipeline board, and the
// record view — each consumes the 3a trust primitives so staged / real /
// human-typed stay three distinguishable styles through composition.

// ----- MorningBrief item -----

export type BriefItem = {
  id: string;
  rank: number;
  title: string;
  evidence?: Evidence;
  confidence: ConfidenceLevel;
  proposal?: Proposal;
};

export function MorningBriefItem({
  item,
  onResolve,
}: Readonly<{
  item: BriefItem;
  onResolve?: (resolution: Resolution) => void;
}>) {
  const t = useT();
  return (
    <article className="brief-item card">
      <div className="brief-head">
        <span className="brief-rank">#{item.rank}</span>
        <span className="brief-title">{item.title}</span>
        <ConfidenceMeter level={item.confidence} />
      </div>
      {item.evidence && <EvidenceChip evidence={item.evidence} />}
      {item.proposal && (
        <>
          <span className="brief-nothing-sent">
            <AutonomyDot tier="confirm" />
            {t("brief.nothingSent")}
          </span>
          <StagedProposal proposal={item.proposal} onResolve={onResolve} />
        </>
      )}
    </article>
  );
}

// ----- Pipeline board -----

export type BoardDeal = {
  id: string;
  name: string;
  org: string;
  /** The company's resolved mark. Absent leaves the monogram, which is the
   *  floor rather than a fallback. */
  orgLogoUrl?: string | null;
  valueMinor: number;
  currency: string;
  ageMs: number;
  stalled?: boolean;
  singleThreaded?: boolean;
  staged?: boolean;
  archived?: boolean;
};

export type BoardColumn = {
  stage: string;
  label: string;
  probabilityPct: number;
  rawMinor: number;
  weightedMinor: number;
  currency: string;
  deals: BoardDeal[];
  /**
   * The stage holds deals in more than one currency, so it has no total to
   * state — native minor units are never summed across currencies. The column
   * then reports how many deals it holds and no figure at all, rather than a
   * zero that reads as an empty stage.
   */
  sumHidden?: boolean;
};

export function DealCard({
  deal,
  onOpen,
  dragHandlers,
}: Readonly<{
  deal: BoardDeal;
  onOpen?: (deal: BoardDeal) => void;
  dragHandlers?: {
    draggable: true;
    onDragStart: (event: React.DragEvent) => void;
  };
}>) {
  const t = useT();
  const { locale } = useLocale();
  const classes = [
    "deal-card",
    deal.stalled ? "stalled" : "",
    deal.staged ? "staged" : "",
    deal.archived ? "archived" : "",
  ]
    .filter(Boolean)
    .join(" ");
  return (
    <button
      type="button"
      className={classes}
      data-deal={deal.id}
      onClick={() => onOpen?.(deal)}
      {...dragHandlers}
    >
      <span className="deal-name">{deal.name}</span>
      {deal.org && (
        <span className="deal-org">
          <Avatar name={deal.org} src={deal.orgLogoUrl} tinted />
          {/* The name needs a box of its own to be truncated in: a bare text
              node has nothing for the ellipsis to apply to, and wraps under
              its own mark instead. */}
          <span className="deal-org-name">{deal.org}</span>
        </span>
      )}
      <span className="deal-meta">
        <span className="deal-value">
          {formatMoney(deal.valueMinor, deal.currency, locale)}
        </span>
        <span>{formatDuration(deal.ageMs, locale)}</span>
        {deal.archived && <Badge>{t("deal.archived")}</Badge>}
        {deal.stalled && <Badge tone="warn">{t("deal.stalled")}</Badge>}
        {deal.singleThreaded && (
          <Badge tone="danger">{t("deal.singleThreaded")}</Badge>
        )}
        {deal.staged && <Badge tone="ai">{t("deal.staged")}</Badge>}
      </span>
    </button>
  );
}

export function PipelineBoard({
  columns,
  onOpen,
  columnExtras,
  cardDragHandlers,
  columnDropHandlers,
}: Readonly<{
  columns: BoardColumn[];
  onOpen?: (deal: BoardDeal) => void;
  columnExtras?: (column: BoardColumn) => ReactNode;
  cardDragHandlers?: (
    deal: BoardDeal,
    column: BoardColumn,
  ) => {
    draggable: true;
    onDragStart: (event: React.DragEvent) => void;
  };
  columnDropHandlers?: (column: BoardColumn) => {
    onDragOver: (event: React.DragEvent) => void;
    onDrop: (event: React.DragEvent) => void;
    onDragLeave: (event: React.DragEvent) => void;
  };
}>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <div className="board">
      {columns.map((column) => (
        <section
          key={column.stage}
          className="board-col"
          data-stage={column.stage}
          aria-label={column.label}
          {...columnDropHandlers?.(column)}
        >
          <div className="board-col-head">
            <span className="stage">{column.label}</span>
            <span className="prob">{column.probabilityPct}%</span>
          </div>
          {/* The stage's total is the figure being scanned down the board, so it
              leads with the deal count beside it; the weighted figure is derived
              from it and reads underneath rather than competing on the line. */}
          <div className="board-col-sub">
            <span className="board-col-total">
              {!column.sumHidden && (
                <span className="board-col-money">
                  {formatMoney(column.rawMinor, column.currency, locale)}
                </span>
              )}
              <span>{t("board.count", { count: column.deals.length })}</span>
            </span>
            {!column.sumHidden && (
              <span className="board-col-weighted">
                {t("board.weighted", {
                  value: formatMoney(
                    column.weightedMinor,
                    column.currency,
                    locale,
                  ),
                })}
              </span>
            )}
          </div>
          {column.deals.map((deal) => (
            <DealCard
              key={deal.id}
              deal={deal}
              onOpen={onOpen}
              dragHandlers={cardDragHandlers?.(deal, column)}
            />
          ))}
          {columnExtras?.(column)}
        </section>
      ))}
    </div>
  );
}

// ----- Record view + timeline -----

/**
 * TimelineGroup is a run of entries the reader sees as ONE event: a
 * conversation, or one message sent to several people. It lives here with the
 * component that renders it — the rules that BUILD one are a screen concern,
 * but the shape is the list's own vocabulary.
 */
export type TimelineGroup = {
  /** The newest member's id; the list keys on it. */
  id: string;
  kind: "thread" | "bulk" | "single";
  /** Newest first, like the list itself. */
  entries: TimelineEntry[];
  /** This group may continue past what the page holds. */
  partial: boolean;
};

export type TimelineEntry = {
  id: string;
  // The backend's activity kinds, not a reduced set: collapsing call, task
  // and the chat kinds into "note" told the reader an email was a note.
  //
  // `change` is not an activity: it is a field edit projected from the audit
  // spine. It rides the same list because what was said to an account and what
  // was changed about it are one chronology to the person reading them — kept
  // apart, a rep comparing "we told them X" against "someone set stage to Y"
  // had to hold two orderings in their head.
  kind:
    | "email"
    | "meeting"
    | "note"
    | "call"
    | "task"
    | "whatsapp"
    | "telegram"
    | "change";
  title: string;
  atIso: string;
  provenance: Provenance;
  // A right-aligned per-row action slot (Reply / Relink). Absent on rows with
  // no affordance, which render exactly as before.
  actions?: ReactNode;
  // The records this entry is about, as the backend's links[] reports them —
  // already pruned to what the reader may see.
  via?: ReactNode;
  /**
   * Which way it went, when the record knows: `outbound` is us reaching out,
   * `inbound` is them coming back.
   *
   * A single undifferentiated stream reads as "things happened here" and hides
   * the one shape a rep is looking for before they reach out — whether the last
   * few moves were all ours. Absent on kinds that have no direction (a note, a
   * task), which render exactly as before.
   */
  direction?: "inbound" | "outbound" | null;
  /**
   * The provider's own conversation id, when capture stamped one. It is what
   * makes a thread a thread — a subject match would merge two unrelated
   * "Re: Update" exchanges and split one renamed mid-conversation.
   */
  threadKey?: string | null;
  /**
   * The SENDER declared this message bulk (RFC 2369 List-Unsubscribe). Per
   * message, never per sender: the same address sends a newsletter and a reply.
   */
  bulkAttested?: boolean;
  /**
   * The message's own subject, when it had one — NOT the rendered `title`,
   * which falls back to the body (or to the kind) so a subjectless row still
   * has something to show. Bulk grouping keys on this: keyed on the title, two
   * subjectless messages that happen to render the same text would fold into
   * one summary and hide each other.
   */
  subject?: string | null;
  /**
   * What the message actually said.
   *
   * A timeline of subject lines is a list of things you cannot read: the rep
   * knows an email happened and still has to leave for their mail client to
   * find out what was in it. The body rides along in the same composite read
   * the row came from, so showing it costs nothing.
   *
   * Legitimately absent on a row whose body was erased under retention or an
   * Art. 17 request, which is why this is optional rather than empty string.
   */
  body?: string | null;
  /**
   * Rendered content for a row whose substance is not prose — the old→new
   * diff on a `change` row. Sits where the body would, so a change reads at
   * the same place in the row as a message does.
   */
  detail?: ReactNode;
};

const TIMELINE_ICON = {
  email: Mail,
  meeting: CalendarClock,
  note: StickyNote,
  call: Phone,
  task: CheckSquare,
  whatsapp: MessageCircle,
  telegram: Send,
  change: PencilLine,
} as const;

// The record's identity band: who it is, what it is, the values a reader
// changes in place and the verbs they act with. Its own component so
// RecordView reads as the page's zones rather than as one long header.
function RecordMasthead({
  name,
  avatarSrc,
  subtitle,
  pulse,
  badges,
  actions,
  controls,
}: Readonly<{
  name: string;
  avatarSrc?: string | null;
  subtitle?: ReactNode;
  pulse?: ReactNode;
  badges?: ReactNode;
  actions?: ReactNode;
  controls?: ReactNode;
}>) {
  const verbs = actions ? (
    <div className="record-actions">{actions}</div>
  ) : null;
  return (
    <header
      className={controls ? "record-head record-head-wide" : "record-head"}
    >
      <Avatar name={name} src={avatarSrc} size="lg" />
      <div className="record-id">
        <h1>{name}</h1>
        {/* A div, not a p: a caller passing structure — the company page's
            description line plus its chip row — would otherwise nest block
            elements inside a paragraph, which the browser silently un-nests,
            leaving the chips outside the header they belong to. */}
        {subtitle && <div className="record-sub">{subtitle}</div>}
        {pulse && <div className="record-pulse">{pulse}</div>}
      </div>
      {badges && <div className="record-badges">{badges}</div>}
      {/* The values a reader changes in place, stacked over the verbs they are
          read beside. Only a caller that passes `controls` gets the column;
          every other record keeps its verbs alone. */}
      {controls ? (
        <div className="record-controls">
          {controls}
          {verbs}
        </div>
      ) : (
        verbs
      )}
    </header>
  );
}

export function RecordView({
  name,
  avatarSrc,
  subtitle,
  badges,
  pulse,
  actions,
  strip,
  tabs,
  lead,
  controls,
  rail,
  aside,
  asideFirst,
  timeline,
  timelineTitle,
  timelineGroups,
  onOpenThread,
  timelineHeader,
  timelineFooter,
  timelineNotice,
  zone,
  now = new Date(),
  children,
}: Readonly<{
  name: string;
  // The record's own image for the header chip — a company's resolved logo.
  // Null or absent renders the deterministic monogram, which is the floor for
  // every record type that has no image at all.
  avatarSrc?: string | null;
  // A string for the records whose subtitle IS one line of joined facts, or a
  // node for a record that needs structure under its name — the company page's
  // editable description plus its row of attribute chips.
  subtitle?: ReactNode;
  badges?: ReactNode;
  // A one-line "state of this record" strip under the name — warmth, last
  // touch, owner. Absent on records that have no such summary.
  pulse?: ReactNode;
  // The record's verbs, kept beside the identity rather than scattered
  // through the body.
  actions?: ReactNode;
  // The record's standing — where it is, whose move it is — and the tabs
  // that switch what is read about it. Given either, the identity block
  // becomes one bordered sheet holding all of them, so a reader sees one
  // masthead rather than a name, some floating tiles and a tab strip that
  // happen to sit near each other. Absent both, the header renders as the
  // plain heading every other record still uses.
  strip?: ReactNode;
  tabs?: ReactNode;
  // Full-width content between the masthead and the columns — the reader's
  // plate. It spans the page because it is what they act on before they
  // choose a column to read.
  lead?: ReactNode;
  // The values a reader changes in place rather than acts on: lifecycle,
  // owner. Passing it seats them beside the record's verbs; a record that
  // passes none keeps the verbs alone.
  controls?: ReactNode;
  // The three-zone record page: rail is the left column (what this record
  // IS), children the middle (what is happening), aside the right (the
  // business around it). With neither rail nor aside the layout collapses
  // to the single column every existing caller already renders.
  rail?: ReactNode;
  aside?: ReactNode;
  // The aside keeps its identity (the business around the record) but sits
  // as the LEFT column, for a page whose story is the wide right-hand side.
  asideFirst?: boolean;
  // The entries, or undefined when this view has NO timeline at all. The
  // distinction is the same one every card on a record page keeps: absent is
  // not empty. `[]` renders the section with its honest "nothing logged yet";
  // undefined omits the section, for a view whose body is not a history.
  timeline?: TimelineEntry[];
  // The heading over the timeline section. A record whose chronology is the
  // page's story names it in its own words ("What happened"); the default
  // stays the neutral "Timeline" every other record uses.
  timelineTitle?: string;
  /**
   * When set, the timeline renders CONVERSATIONS rather than messages. The
   * flat list stays the default: a person's timeline is a handful of rows and
   * grouping it would collapse events that were never one.
   */
  timelineGroups?: readonly TimelineGroup[];
  onOpenThread?: (threadKey: string) => void;
  // Controls above the timeline list (filters), and below it (load more).
  timelineHeader?: ReactNode;
  timelineFooter?: ReactNode;
  // When set, replaces the timeline list — e.g. an overlay-mode "not available"
  // note, since the mirror cannot serve entity-scoped activity reads. Keeps the
  // section honest instead of rendering an empty list that reads as "no activity".
  timelineNotice?: ReactNode;
  zone: string;
  // The instant the timeline's relative rows (this week, "Tue 14:05") are
  // read against. Defaults to the real clock — a caller only supplies this
  // to fix the reading instant for a test, the same reason `timelinePeriod`
  // takes one rather than calling `new Date()` itself.
  now?: Date;
  children?: ReactNode;
}>) {
  const t = useT();
  // The grid follows which slots are actually filled. One class per shape,
  // because a three-column template with an empty column does not collapse:
  // it reserves the space and leaves the story narrower than the rail
  // beside it.
  const zones = zoneClass(Boolean(rail), Boolean(aside), Boolean(asideFirst));
  const sheet = Boolean(strip || tabs);
  return (
    <div>
      <div className={sheet ? "record-sheet" : undefined}>
        <RecordMasthead
          name={name}
          avatarSrc={avatarSrc}
          subtitle={subtitle}
          pulse={pulse}
          badges={badges}
          actions={actions}
          controls={controls}
        />
        {strip}
        {tabs && <div className="record-tabs">{tabs}</div>}
      </div>
      {lead && <div className="record-lead">{lead}</div>}
      <div className={zones}>
        {rail && (
          <aside className="record-rail" aria-label={t("record.profile")}>
            {rail}
          </aside>
        )}
        <div className="record-main">
          {children}
          {timeline && (
            // A card, like every other reading in the columns: the chronology
            // was the one block with no edge of its own, so it read as loose
            // rows spilled under the cards rather than as the record's story.
            <section
              className="card record-timeline"
              aria-label={timelineTitle ?? t("record.timeline")}
            >
              <h2 className="t-sub">{timelineTitle ?? t("record.timeline")}</h2>
              {timelineHeader}
              {timelineNotice ??
                (timelineGroups ? (
                  <GroupedTimelineList
                    groups={timelineGroups}
                    zone={zone}
                    now={now}
                    onOpenThread={onOpenThread}
                  />
                ) : (
                  <TimelineList entries={timeline} zone={zone} now={now} />
                ))}
              {timelineFooter}
            </section>
          )}
        </div>
        {aside && (
          <aside className="record-aside" aria-label={t("record.business")}>
            {aside}
          </aside>
        )}
      </div>
    </div>
  );
}

// zoneClass names the layout for the slots this record actually has.
function zoneClass(
  hasRail: boolean,
  hasAside: boolean,
  asideFirst: boolean,
): string | undefined {
  if (hasRail && hasAside) {
    return "record-zones record-zones-both";
  }
  if (hasRail) {
    return "record-zones record-zones-rail";
  }
  if (hasAside) {
    return asideFirst
      ? "record-zones record-zones-aside record-zones-aside-first"
      : "record-zones record-zones-aside";
  }
  return undefined;
}

/**
 * TimelineText is the message itself, two lines by default and the whole of it
 * on request.
 *
 * Two lines is enough to recognise a thread; the full text is one click away
 * rather than one application away. Collapsed by default because a timeline
 * where every row is a full email is a mailbox, and the point of the row is
 * still the sequence.
 */
function TimelineText({ text }: Readonly<{ text: string }>) {
  const t = useT();
  const [open, setOpen] = useState(false);
  // Whether the clamp is actually cutting the text off, measured rather than
  // guessed. Counting characters was wrong in the one direction that matters:
  // the clamp is two VISUAL lines at whatever width the column happens to be,
  // so a message short enough to look safe still wrapped past it in a narrow
  // column, got clipped by CSS, and — having failed the character test — was
  // given no way to expand. Text the reader could not reach.
  const [clipped, setClipped] = useState(false);
  const bodyRef = useRef<HTMLSpanElement>(null);
  const trimmed = text.trim();

  useLayoutEffect(() => {
    const el = bodyRef.current;
    // Nothing to measure while expanded: scrollHeight equals clientHeight, and
    // re-measuring there would drop the control that collapses it again. Empty
    // text renders nothing, so there is nothing that could be clipped.
    if (!el || open || !trimmed) {
      return;
    }
    const measure = () => setClipped(el.scrollHeight > el.clientHeight + 1);
    measure();
    // The column is resizable, so a width change can start or stop the
    // clipping. Guarded because jsdom has no ResizeObserver.
    if (typeof ResizeObserver === "undefined") {
      return;
    }
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, [open, trimmed]);

  if (!trimmed) {
    return null;
  }
  return (
    <span className="tl-text">
      <span ref={bodyRef} className={open ? "tl-text-full" : "tl-text-clamp"}>
        {trimmed}
      </span>
      {(clipped || open) && (
        <button
          type="button"
          className="tl-text-toggle"
          aria-expanded={open}
          onClick={() => setOpen(!open)}
        >
          {open ? t("timeline.textLess") : t("timeline.textMore")}
        </button>
      )}
    </span>
  );
}

// directionClass tracks the row to one side of the spine: ours or theirs.
function directionClass(direction: TimelineEntry["direction"]): string {
  if (direction === "outbound") {
    return "tl-out";
  }
  return direction === "inbound" ? "tl-in" : "";
}

// rowClass joins a row's direction accent with the one modifier that ends the
// spine at its node — computed here rather than in CSS's :last-child, because
// the DOM's actual last row is not always the last element a :last-child
// selector would see (a period heading wraps each row in its own list item).
function rowClass(direction: TimelineEntry["direction"], isLast: boolean) {
  return [directionClass(direction), isLast ? "tl-row-last" : ""]
    .filter(Boolean)
    .join(" ");
}

const SILENCE_THRESHOLD_MS = 21 * 86_400_000;

// silentSpan names the gap between two chronologically adjacent moments, when
// it is wide enough that the account looks quiet rather than merely between
// events. Takes the two boundary instants directly rather than the entries
// that carry them, so the flat list and the grouped list (whose real
// boundary is a group's oldest/newest MEMBER, not its representative row)
// share the one rule.
function silentSpan(
  olderIso: string,
  newerIso: string,
): { olderIso: string; newerIso: string } | null {
  const span = new Date(newerIso).getTime() - new Date(olderIso).getTime();
  return span > SILENCE_THRESHOLD_MS ? { olderIso, newerIso } : null;
}

/**
 * timelinePeriod buckets an entry by how a reader thinks about when it
 * happened: this week, last week, or the month it fell in. A run of thirty
 * rows each stamped with its own date is a list nobody can see the shape of —
 * whether the account went quiet, and for how long.
 *
 * `now` is passed rather than read so the buckets are a function of their
 * inputs: a test fixes both ends and the boundaries stay assertable.
 */
export function timelinePeriod(
  atIso: string,
  now: Date,
): { key: string; monthOf?: Date } {
  const at = new Date(atIso);
  const days = Math.floor((now.getTime() - at.getTime()) / 86_400_000);
  // Calendar weeks, not rolling sevens: a reader who says "last week" means
  // the week that ended, not the seven days before today.
  const startOfWeek = (date: Date) => {
    const start = new Date(date);
    // Monday-first, and Sunday counts as the end of the week it closes.
    start.setDate(start.getDate() - ((start.getDay() + 6) % 7));
    start.setHours(0, 0, 0, 0);
    return start;
  };
  if (days < 0) {
    return { key: "timeline.period.upcoming" };
  }
  const thisWeek = startOfWeek(now);
  if (at >= thisWeek) {
    return { key: "timeline.period.thisWeek" };
  }
  const lastWeek = new Date(thisWeek);
  lastWeek.setDate(lastWeek.getDate() - 7);
  if (at >= lastWeek) {
    return { key: "timeline.period.lastWeek" };
  }
  return { key: "timeline.period.month", monthOf: at };
}

function TimelineList({
  entries,
  zone,
  now,
}: Readonly<{ entries: TimelineEntry[]; zone: string; now: Date }>) {
  const t = useT();
  const { locale } = useLocale();
  let lastHeading: string | null = null;
  return (
    <ul className="timeline">
      {entries.flatMap((entry, index) => {
        const heading = periodHeading(entry.atIso, now, t, locale);
        // The heading is drawn when the period CHANGES, so a run of rows in
        // one week carries one label rather than repeating it per row.
        const show = heading !== lastHeading;
        lastHeading = heading;
        const isLast = index === entries.length - 1;
        const row = (
          <li key={`p-${entry.id}`} className="tl-period-wrap">
            {show && <p className="tl-period">{heading}</p>}
            <ul className="tl-period-rows">
              <TimelineRow
                entry={entry}
                zone={zone}
                now={now}
                isLast={isLast}
              />
            </ul>
          </li>
        );
        // Entries are newest-first, so the quiet stretch this row closes off
        // sits between it and the OLDER neighbour still to come.
        const older = entries[index + 1];
        const gap = older && silentSpan(older.atIso, entry.atIso);
        return gap
          ? [
              row,
              <TimelineGapRow
                key={`gap-${entry.id}`}
                olderIso={gap.olderIso}
                newerIso={gap.newerIso}
                zone={zone}
              />,
            ]
          : [row];
      })}
    </ul>
  );
}

/**
 * GroupedTimelineList renders conversations rather than messages.
 *
 * A collapsed group states what it IS before what it says — "5 messages" or
 * "sent to 3 people" — because the reader is scanning for an event, not for a
 * sentence. Expanding shows the same rows the flat list would have shown, from
 * the same component, so the two can never drift.
 *
 * A group that may continue past the page says so. A summary that implied it
 * was whole would be a worse answer than the repetition this replaced.
 */
export function GroupedTimelineList({
  groups,
  zone,
  now,
  onOpenThread,
}: Readonly<{
  groups: readonly TimelineGroup[];
  zone: string;
  now: Date;
  // Fetches the rest of a conversation the page holds only part of. Absent for
  // a caller that cannot complete it — a bulk group has no thread to ask for.
  onOpenThread?: (threadKey: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  let lastHeading: string | null = null;
  return (
    <ul className="timeline">
      {groups.flatMap((group, index) => {
        // The group is dated by its newest member, which is where it sits in
        // the chronology and therefore which period it belongs to.
        const heading = periodHeading(group.entries[0].atIso, now, t, locale);
        const show = heading !== lastHeading;
        lastHeading = heading;
        const isLast = index === groups.length - 1;
        const row = (
          <li key={`p-${group.id}`} className="tl-period-wrap">
            {show && <p className="tl-period">{heading}</p>}
            <ul className="tl-period-rows">
              {group.kind === "single" ? (
                <TimelineRow
                  entry={group.entries[0]}
                  zone={zone}
                  now={now}
                  isLast={isLast}
                />
              ) : (
                <TimelineGroupRow
                  group={group}
                  zone={zone}
                  now={now}
                  isLast={isLast}
                  onOpenThread={onOpenThread}
                />
              )}
            </ul>
          </li>
        );
        // The silent stretch a group closes off runs from its own OLDEST
        // member to the next (older) group's NEWEST — the span its members
        // do not already fill, not the gap between the two representative
        // rows the collapsed view happens to show.
        const olderGroup = groups[index + 1];
        const gap =
          olderGroup &&
          silentSpan(
            olderGroup.entries[0].atIso,
            group.entries[group.entries.length - 1].atIso,
          );
        return gap
          ? [
              row,
              <TimelineGapRow
                key={`gap-${group.id}`}
                olderIso={gap.olderIso}
                newerIso={gap.newerIso}
                zone={zone}
              />,
            ]
          : [row];
      })}
    </ul>
  );
}

// periodHeading is the words a period bucket renders as: a named week, or the
// month it fell in written out.
function periodHeading(
  atIso: string,
  now: Date,
  t: (key: MessageKey) => string,
  locale: string,
): string {
  const period = timelinePeriod(atIso, now);
  return period.monthOf
    ? period.monthOf.toLocaleDateString(locale, {
        month: "long",
        year: "numeric",
      })
    : t(period.key as MessageKey);
}

// groupCountLabel counts the group's members in words that read. A group of
// one is reachable both ways — a thread whose other messages are on another
// page, and a single message the sender attested as a bulk send — and the
// plural forms rendered "1 messages" and "sent to 1 people" for it.
function groupCountLabel(
  group: TimelineGroup,
  t: (key: MessageKey, vars?: Record<string, string | number>) => string,
): string {
  const count = group.entries.length;
  if (group.kind === "bulk") {
    return count === 1
      ? t("timeline.group.bulkOne")
      : t("timeline.group.bulk", { count });
  }
  return count === 1
    ? t("timeline.group.threadOne")
    : t("timeline.group.thread", { count });
}

// aiNodeClass tints a row's node indigo when — and only when — an agent
// authored it (ADR-0040): indigo means AI authorship here and nothing else,
// so this reads the entry's provenance, never its activity kind.
function aiNodeClass(provenance: Provenance): string {
  return provenance.kind === "agent" ? "tl-icon tl-icon-ai" : "tl-icon";
}

function TimelineGroupRow({
  group,
  zone,
  now,
  isLast,
  onOpenThread,
}: Readonly<{
  group: TimelineGroup;
  zone: string;
  now: Date;
  isLast: boolean;
  onOpenThread?: (threadKey: string) => void;
}>) {
  const { locale } = useLocale();
  const t = useT();
  const [open, setOpen] = useState(false);
  const newest = group.entries[0];
  const Icon = TIMELINE_ICON[newest.kind];
  const threadKey = newest.threadKey;
  return (
    <li className={rowClass(newest.direction, isLast)}>
      <span className={aiNodeClass(newest.provenance)}>
        <Icon aria-hidden />
      </span>
      <div className="tl-body">
        <div className="tl-head">
          <span className="tl-title">{newest.title}</span>
          <span className="tl-meta">
            <span className="tl-group-count">{groupCountLabel(group, t)}</span>
            <ProvenanceTag provenance={newest.provenance} />
          </span>
          <span className="tl-when">
            {formatTimelineTimestamp(newest.atIso, locale, zone, now)}
          </span>
        </div>
        <div className="tl-foot">
          <Button small aria-expanded={open} onClick={() => setOpen(!open)}>
            {open ? t("timeline.group.collapse") : t("timeline.group.expand")}
          </Button>
          {/* Only a real conversation can be completed: a bulk group is one
              send with no thread to ask the server for. Where it cannot be
              completed — a bulk group, or a page that passed no handler — the
              notice still stands. Rendering neither would present a group cut
              off by the page edge as the whole of it. */}
          {group.partial &&
            (threadKey && onOpenThread ? (
              <Button small onClick={() => onOpenThread(threadKey)}>
                {t("timeline.group.openThread")}
              </Button>
            ) : (
              <span className="t-caption">
                {t("timeline.group.mayContinue")}
              </span>
            ))}
        </div>
        {open && (
          <ul className="timeline tl-group-members">
            {group.entries.map((entry, index) => (
              <TimelineRow
                key={entry.id}
                entry={entry}
                zone={zone}
                now={now}
                isLast={index === group.entries.length - 1}
              />
            ))}
          </ul>
        )}
      </div>
    </li>
  );
}

/**
 * TimelineRow is one entry. Split out of the list so a grouped view can render
 * the SAME row inside an expanded conversation — a second rendering of a
 * message would drift from this one the first time either changed.
 */
export function TimelineRow({
  entry,
  zone,
  now,
  isLast,
}: Readonly<{
  entry: TimelineEntry;
  zone: string;
  now: Date;
  isLast: boolean;
}>) {
  const { locale } = useLocale();
  const t = useT();
  const Icon = TIMELINE_ICON[entry.kind];
  return (
    <li className={rowClass(entry.direction, isLast)}>
      <span className={aiNodeClass(entry.provenance)}>
        <Icon aria-hidden />
      </span>
      {/* A div, not a span: a change row's detail is a field diff whose
                long-value side is a focusable region — flow content, invalid
                inside phrasing content. The row lays out identically, because
                .tl-body is a flex column either way. */}
      <div className="tl-body">
        <div className="tl-head">
          <span className="tl-title">{entry.title}</span>
          <span className="tl-meta">
            {/* The direction is said in words as well as drawn, so it does
                    not depend on telling two accent colours apart. */}
            {entry.direction && (
              <span className="tl-direction">
                {entry.direction === "outbound"
                  ? t("timeline.sent")
                  : t("timeline.received")}
              </span>
            )}
            {entry.via}
            <ProvenanceTag provenance={entry.provenance} />
          </span>
          <span className="tl-when">
            {formatTimelineTimestamp(entry.atIso, locale, zone, now)}
          </span>
        </div>
        {entry.body && <TimelineText text={entry.body} />}
        {entry.detail}
        {entry.actions && (
          <div className="tl-foot">
            <span className="tl-actions">{entry.actions}</span>
          </div>
        )}
      </div>
    </li>
  );
}

/**
 * TimelineGapRow is the silence a run of dated rows would otherwise hide: two
 * entries far enough apart that the account went quiet between them, not
 * merely between events. It carries no body and no footer — there is nothing
 * that happened to show — so it renders the same node-and-spine shape with
 * one italic line naming the span it covers.
 */
function TimelineGapRow({
  olderIso,
  newerIso,
  zone,
}: Readonly<{ olderIso: string; newerIso: string; zone: string }>) {
  const { locale } = useLocale();
  const t = useT();
  return (
    <li className="tl-gap">
      <span className="tl-icon">
        <Clock aria-hidden />
      </span>
      <div className="tl-body">
        <span className="tl-quiet">
          {t("timeline.gap", {
            from: formatDate(olderIso, locale, zone),
            to: formatDate(newerIso, locale, zone),
          })}
        </span>
      </div>
    </li>
  );
}
