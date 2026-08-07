import {
  CalendarClock,
  CheckSquare,
  Mail,
  MessageCircle,
  PencilLine,
  Phone,
  Send,
  StickyNote,
} from "lucide-react";
import type { ReactNode } from "react";
import { useLayoutEffect, useRef, useState } from "react";
import { formatDate, formatDuration, formatMoney } from "../format/format";
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
          {deal.org}
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
              <span className="board-col-money">
                {formatMoney(column.rawMinor, column.currency, locale)}
              </span>
              <span>{t("board.count", { count: column.deals.length })}</span>
            </span>
            <span className="board-col-weighted">
              {t("board.weighted", {
                value: formatMoney(
                  column.weightedMinor,
                  column.currency,
                  locale,
                ),
              })}
            </span>
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

export function RecordView({
  name,
  avatarSrc,
  subtitle,
  badges,
  pulse,
  actions,
  rail,
  aside,
  timeline,
  timelineGroups,
  onOpenThread,
  timelineHeader,
  timelineFooter,
  timelineNotice,
  zone,
  children,
}: Readonly<{
  name: string;
  // The record's own image for the header chip — a company's resolved logo.
  // Null or absent renders the deterministic monogram, which is the floor for
  // every record type that has no image at all.
  avatarSrc?: string | null;
  subtitle?: string;
  badges?: ReactNode;
  // A one-line "state of this record" strip under the name — warmth, last
  // touch, owner. Absent on records that have no such summary.
  pulse?: ReactNode;
  // The record's verbs, kept beside the identity rather than scattered
  // through the body.
  actions?: ReactNode;
  // The three-zone record page: rail is the left column (what this record
  // IS), children the middle (what is happening), aside the right (the
  // business around it). With neither rail nor aside the layout collapses
  // to the single column every existing caller already renders.
  rail?: ReactNode;
  aside?: ReactNode;
  // The entries, or undefined when this view has NO timeline at all. The
  // distinction is the same one every card on a record page keeps: absent is
  // not empty. `[]` renders the section with its honest "nothing logged yet";
  // undefined omits the section, for a view whose body is not a history.
  timeline?: TimelineEntry[];
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
  children?: ReactNode;
}>) {
  const t = useT();
  // The grid follows which slots are actually filled. One class per shape,
  // because a three-column template with an empty column does not collapse:
  // it reserves the space and leaves the story narrower than the rail
  // beside it.
  const zones = zoneClass(Boolean(rail), Boolean(aside));
  return (
    <div>
      <header className="record-head">
        <Avatar name={name} src={avatarSrc} size="lg" />
        <div className="record-id">
          <h1>{name}</h1>
          {subtitle && <p className="record-sub">{subtitle}</p>}
          {pulse && <div className="record-pulse">{pulse}</div>}
        </div>
        {badges && <div className="record-badges">{badges}</div>}
      </header>
      {actions && <div className="record-actions">{actions}</div>}
      <div className={zones}>
        {rail && (
          <aside className="record-rail" aria-label={t("record.profile")}>
            {rail}
          </aside>
        )}
        <div className="record-main">
          {children}
          {timeline && (
            <section aria-label={t("record.timeline")}>
              <h2 className="t-sub">{t("record.timeline")}</h2>
              {timelineHeader}
              {timelineNotice ??
                (timelineGroups ? (
                  <GroupedTimelineList
                    groups={timelineGroups}
                    zone={zone}
                    onOpenThread={onOpenThread}
                  />
                ) : (
                  <TimelineList entries={timeline} zone={zone} />
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
function zoneClass(hasRail: boolean, hasAside: boolean): string | undefined {
  if (hasRail && hasAside) {
    return "record-zones record-zones-both";
  }
  if (hasRail) {
    return "record-zones record-zones-rail";
  }
  if (hasAside) {
    return "record-zones record-zones-aside";
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

function TimelineList({
  entries,
  zone,
}: Readonly<{ entries: TimelineEntry[]; zone: string }>) {
  return (
    <ul className="timeline">
      {entries.map((entry) => (
        <TimelineRow key={entry.id} entry={entry} zone={zone} />
      ))}
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
  onOpenThread,
}: Readonly<{
  groups: readonly TimelineGroup[];
  zone: string;
  // Fetches the rest of a conversation the page holds only part of. Absent for
  // a caller that cannot complete it — a bulk group has no thread to ask for.
  onOpenThread?: (threadKey: string) => void;
}>) {
  return (
    <ul className="timeline">
      {groups.map((group) =>
        group.kind === "single" ? (
          <TimelineRow key={group.id} entry={group.entries[0]} zone={zone} />
        ) : (
          <TimelineGroupRow
            key={group.id}
            group={group}
            zone={zone}
            onOpenThread={onOpenThread}
          />
        ),
      )}
    </ul>
  );
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

function TimelineGroupRow({
  group,
  zone,
  onOpenThread,
}: Readonly<{
  group: TimelineGroup;
  zone: string;
  onOpenThread?: (threadKey: string) => void;
}>) {
  const { locale } = useLocale();
  const t = useT();
  const [open, setOpen] = useState(false);
  const newest = group.entries[0];
  const Icon = TIMELINE_ICON[newest.kind];
  const threadKey = newest.threadKey;
  return (
    <li className={directionClass(newest.direction)}>
      <span className="tl-icon">
        <Icon aria-hidden />
      </span>
      <div className="tl-body">
        <span className="tl-title">{newest.title}</span>
        <span className="tl-meta">
          <span className="tl-group-count">{groupCountLabel(group, t)}</span>
          <span>{formatDate(newest.atIso, locale, zone)}</span>
          <ProvenanceTag provenance={newest.provenance} />
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
        </span>
        {open && (
          <ul className="timeline tl-group-members">
            {group.entries.map((entry) => (
              <TimelineRow key={entry.id} entry={entry} zone={zone} />
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
}: Readonly<{ entry: TimelineEntry; zone: string }>) {
  const { locale } = useLocale();
  const t = useT();
  const Icon = TIMELINE_ICON[entry.kind];
  return (
    <li className={directionClass(entry.direction)}>
      <span className="tl-icon">
        <Icon aria-hidden />
      </span>
      {/* A div, not a span: a change row's detail is a field diff whose
                long-value side is a focusable region — flow content, invalid
                inside phrasing content. The row lays out identically, because
                .tl-body is a flex column either way. */}
      <div className="tl-body">
        <span className="tl-title">{entry.title}</span>
        {entry.body && <TimelineText text={entry.body} />}
        {entry.detail}
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
          <span>{formatDate(entry.atIso, locale, zone)}</span>
          <ProvenanceTag provenance={entry.provenance} />
          {entry.via}
        </span>
      </div>
      {entry.actions && <span className="tl-actions">{entry.actions}</span>}
    </li>
  );
}
