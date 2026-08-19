import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useRef } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { navigate } from "../app/router";
import { Badge, Button } from "../design-system/atoms";
import {
  type BoardColumn,
  type BoardRecord,
  PipelineBoard,
} from "../design-system/composed";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, throwProblem } from "./common";

type Lead = components["schemas"]["Lead"];

export function scoreTone(score: number): "success" | "warn" | undefined {
  if (score >= 60) return "success";
  if (score >= 40) return "warn";
  return undefined;
}

export const LEAD_STATUS_FILTER_OPTIONS = [
  { value: "new", label: "lead.statusNew" },
  { value: "contacted", label: "lead.statusContacted" },
  { value: "engaged", label: "lead.statusEngaged" },
  { value: "promoted", label: "lead.statusPromoted" },
  { value: "disqualified", label: "lead.statusDisqualified" },
] as const;

function statusLabel(status: Lead["status"]): MessageKey | null {
  return (
    LEAD_STATUS_FILTER_OPTIONS.find((option) => option.value === status)
      ?.label ?? null
  );
}

export function StatusBadge({ status }: Readonly<{ status: Lead["status"] }>) {
  const t = useT();
  const label = statusLabel(status);
  return <Badge>{label ? t(label) : status}</Badge>;
}

export function SlaBadge({ state }: Readonly<{ state: Lead["sla_state"] }>) {
  const t = useT();
  if (state === "breached") {
    return <Badge tone="danger">{t("lead.sla.breached")}</Badge>;
  }
  if (state === "at_risk") {
    return <Badge tone="warn">{t("lead.sla.atRisk")}</Badge>;
  }
  return null;
}

export function FirstResponseLine({ lead }: Readonly<{ lead: Lead }>) {
  const t = useT();
  const { locale } = useLocale();
  const zone = Intl.DateTimeFormat().resolvedOptions().timeZone;
  if (lead.first_response_at) {
    return (
      <span className="t-caption">
        {t("lead.sla.answeredAt", {
          at: formatDateTime(lead.first_response_at, locale, zone),
        })}
      </span>
    );
  }
  if (!lead.sla_deadline_at || !lead.sla_state) return null;
  return (
    <span
      className="t-caption"
      style={{
        display: "inline-flex",
        gap: "var(--space-2)",
        alignItems: "baseline",
      }}
    >
      <SlaBadge state={lead.sla_state} />
      {t(
        lead.sla_state === "breached"
          ? "lead.sla.overdueSince"
          : "lead.sla.dueBy",
        { at: formatDateTime(lead.sla_deadline_at, locale, zone) },
      )}
    </span>
  );
}

export const LEAD_SOURCES = [
  { value: "manual", label: "lead.source.manual" },
  { value: "inbound", label: "lead.source.inbound" },
  { value: "webform", label: "lead.source.webform" },
  { value: "referral", label: "lead.source.referral" },
  { value: "import", label: "lead.source.import" },
  { value: "crawl", label: "lead.source.crawl" },
] as const;

export function sourceLabel(
  source: string | null | undefined,
  t: ReturnType<typeof useT>,
): string {
  if (!source) return t("lead.shortfall.noSource");
  const known = LEAD_SOURCES.find((option) => option.value === source);
  if (known) return t(known.label);
  const parts = source.split(":").filter(Boolean);
  const meaningful = parts[0] === "connector" ? parts[1] : parts[0];
  return meaningful
    ? meaningful.charAt(0).toUpperCase() + meaningful.slice(1)
    : t("lead.source.unknown");
}

const LEAD_BOARD_STAGES = [
  { stage: "new", label: "lead.statusNew" },
  { stage: "contacted", label: "lead.statusContacted" },
  { stage: "engaged", label: "lead.statusEngaged" },
] as const;

function LeadCard({
  lead,
  onOpen,
  dragHandlers,
}: Readonly<{
  lead: Lead;
  onOpen: (lead: Lead) => void;
  dragHandlers?: {
    draggable: true;
    onDragStart: (event: React.DragEvent) => void;
    onDragEnd: () => void;
  };
}>) {
  const t = useT();
  return (
    <button
      type="button"
      className="deal-card"
      data-lead={lead.id}
      onClick={() => onOpen(lead)}
      {...dragHandlers}
    >
      <span className="deal-name">
        {lead.full_name ?? lead.email ?? t("nav.leads")}
      </span>
      {lead.company_name && (
        <span className="deal-org">
          <span className="deal-org-name">{lead.company_name}</span>
        </span>
      )}
      <span className="deal-meta">
        <Badge tone={scoreTone(lead.score)}>
          {t("lead.score")}: {lead.score}
        </Badge>
        <SlaBadge state={lead.sla_state} />
        {lead.title && <span>{lead.title}</span>}
      </span>
      <span className="deal-meta">
        <span>{sourceLabel(lead.source, t)}</span>
        <span>
          {lead.next_task_subject ?? t("lead.noNextTask")}
          {lead.open_task_count
            ? ` · ${t("lead.openTaskCount", { count: lead.open_task_count })}`
            : ""}
        </span>
      </span>
    </button>
  );
}

export function LeadBoard({
  rows,
  onMoved,
  hasMore,
  loadMore,
}: Readonly<{
  rows: Lead[];
  onMoved: () => void;
  hasMore: boolean;
  loadMore: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const dragging = useRef<string | null>(null);
  const lastDragEnd = useRef(0);
  const move = useMutation({
    mutationFn: async (moved: {
      id: string;
      version?: number;
      status: "new" | "contacted" | "engaged";
    }) => {
      const { data, error } = await api.PATCH("/leads/{id}", {
        // Refused rather than sent unpinned: a row the server did not version is
        // one this client can make no concurrency claim about.
        params: {
          path: { id: moved.id },
          ...ifMatch(requireVersion(moved.version)),
        },
        body: { status: moved.status },
      });
      if (error) throwProblem(error, t);
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["leads"] });
      onMoved();
    },
    onError: () => {
      queryClient.invalidateQueries({ queryKey: ["leads"] });
    },
  });

  const live = rows.filter(
    (lead) =>
      lead.status === "new" ||
      lead.status === "contacted" ||
      lead.status === "engaged",
  );
  const columns: BoardColumn<BoardRecord>[] = LEAD_BOARD_STAGES.map((stage) => {
    const held = live.filter((lead) => lead.status === stage.stage);
    return {
      stage: stage.stage,
      label: t(stage.label),
      count: held.length,
      deals: held.map((lead) => ({ id: lead.id, name: "" })),
    };
  });
  const leadsById = new Map(live.map((lead) => [lead.id, lead]));

  return (
    <>
      {move.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(move.error, t)}
        </p>
      )}
      {rows.length > 0 && live.length === 0 && (
        <p className="t-caption">{t("lead.boardTerminalOnly")}</p>
      )}
      <PipelineBoard
        variant="plain"
        columns={columns}
        countLabel={(count) => t("lead.boardCount", { count })}
        renderCard={(card) => {
          const lead = leadsById.get(card.id);
          if (!lead) return null;
          return (
            <LeadCard
              lead={lead}
              onOpen={(opened) => {
                if (Date.now() - lastDragEnd.current > 250) {
                  navigate({ screen: "leads", id: opened.id });
                }
              }}
              dragHandlers={{
                draggable: true,
                onDragStart: (event) => {
                  dragging.current = lead.id;
                  event.dataTransfer.setData("text/plain", lead.id);
                },
                onDragEnd: () => {
                  dragging.current = null;
                  lastDragEnd.current = Date.now();
                },
              }}
            />
          );
        }}
        columnDropHandlers={(column) => ({
          onDragOver: (event) => {
            event.preventDefault();
            event.currentTarget.classList.add("droptarget");
          },
          onDragLeave: (event) => {
            event.currentTarget.classList.remove("droptarget");
          },
          onDrop: (event) => {
            event.preventDefault();
            event.currentTarget.classList.remove("droptarget");
            const id =
              event.dataTransfer.getData("text/plain") || dragging.current;
            dragging.current = null;
            lastDragEnd.current = Date.now();
            const lead = id ? leadsById.get(id) : undefined;
            const target = LEAD_BOARD_STAGES.find(
              (stage) => stage.stage === column.stage,
            );
            if (lead && target && lead.status !== target.stage) {
              move.mutate({
                id: lead.id,
                version: lead.version,
                status: target.stage,
              });
            }
          },
        })}
      />
      {hasMore && (
        <Button small onClick={loadMore}>
          {t("list.loadMore")}
        </Button>
      )}
    </>
  );
}
