import { CalendarClock, Mail, StickyNote } from "lucide-react";
import type { ReactNode } from "react";
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
}: {
  item: BriefItem;
  onResolve?: (resolution: Resolution) => void;
}) {
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
}: {
  deal: BoardDeal;
  onOpen?: (deal: BoardDeal) => void;
  dragHandlers?: {
    draggable: true;
    onDragStart: (event: React.DragEvent) => void;
  };
}) {
  const t = useT();
  const { locale } = useLocale();
  const classes = [
    "deal-card",
    deal.stalled ? "stalled" : "",
    deal.staged ? "staged" : "",
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
}: {
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
}) {
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
  kind: "email" | "meeting" | "note";
  title: string;
  atIso: string;
  provenance: Provenance;
};

const TIMELINE_ICON = {
  email: Mail,
  meeting: CalendarClock,
  note: StickyNote,
} as const;

export function RecordView({
  name,
  subtitle,
  badges,
  timeline,
  zone,
  children,
}: {
  name: string;
  subtitle?: string;
  badges?: ReactNode;
  timeline: TimelineEntry[];
  zone: string;
  children?: ReactNode;
}) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <div>
      <header className="record-head">
        <Avatar name={name} />
        <div className="record-id">
          <h1>{name}</h1>
          {subtitle && <p className="record-sub">{subtitle}</p>}
        </div>
        {badges && <div className="record-badges">{badges}</div>}
      </header>
      {children}
      <section aria-label={t("record.timeline")}>
        <h2 className="t-sub">{t("record.timeline")}</h2>
        <ul className="timeline">
          {timeline.map((entry) => {
            const Icon = TIMELINE_ICON[entry.kind];
            return (
              <li key={entry.id}>
                <span className="tl-icon">
                  <Icon aria-hidden />
                </span>
                <span className="tl-body">
                  <span className="tl-title">{entry.title}</span>
                  <span className="tl-meta">
                    <span>{formatDate(entry.atIso, locale, zone)}</span>
                    <ProvenanceTag provenance={entry.provenance} />
                  </span>
                </span>
              </li>
            );
          })}
        </ul>
      </section>
    </div>
  );
}
