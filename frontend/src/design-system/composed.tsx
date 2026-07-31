import {
  CalendarClock,
  CheckSquare,
  Mail,
  MessageCircle,
  Phone,
  Send,
  StickyNote,
} from "lucide-react";
import type { ReactNode } from "react";
import { useLayoutEffect, useRef, useState } from "react";
import { formatDate, formatDuration, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import { Avatar, Badge } from "./atoms";
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
      <span className="deal-org">{deal.org}</span>
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
          <div className="board-col-sub">
            <span>{t("board.count", { count: column.deals.length })}</span>
            <span>{formatMoney(column.rawMinor, column.currency, locale)}</span>
            <span>
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

export type TimelineEntry = {
  id: string;
  // The backend's activity kinds, not a reduced set: collapsing call, task
  // and the chat kinds into "note" told the reader an email was a note.
  kind:
    | "email"
    | "meeting"
    | "note"
    | "call"
    | "task"
    | "whatsapp"
    | "telegram";
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
};

const TIMELINE_ICON = {
  email: Mail,
  meeting: CalendarClock,
  note: StickyNote,
  call: Phone,
  task: CheckSquare,
  whatsapp: MessageCircle,
  telegram: Send,
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
              {timelineNotice ?? (
                <TimelineList entries={timeline} zone={zone} />
              )}
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

function TimelineList({
  entries,
  zone,
}: Readonly<{ entries: TimelineEntry[]; zone: string }>) {
  const { locale } = useLocale();
  return (
    <ul className="timeline">
      {entries.map((entry) => {
        const Icon = TIMELINE_ICON[entry.kind];
        return (
          <li key={entry.id}>
            <span className="tl-icon">
              <Icon aria-hidden />
            </span>
            <span className="tl-body">
              <span className="tl-title">{entry.title}</span>
              {entry.body && <TimelineText text={entry.body} />}
              <span className="tl-meta">
                <span>{formatDate(entry.atIso, locale, zone)}</span>
                <ProvenanceTag provenance={entry.provenance} />
                {entry.via}
              </span>
            </span>
            {entry.actions && (
              <span className="tl-actions">{entry.actions}</span>
            )}
          </li>
        );
      })}
    </ul>
  );
}
