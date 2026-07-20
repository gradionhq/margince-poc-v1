import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  AttainmentRing,
  Button,
  Card,
  DataTable,
  EmptyState,
  Skeleton,
} from "../design-system/atoms";
import { formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import { ProblemError, problemMessage, QueryGate } from "./common";
import { EntityRef } from "./entityref";
import {
  ArchiveQuotaAction,
  EditTargetAction,
  SetTargetAction,
} from "./quotas.forms";
import "./quotas.css";

// Quotas & attainment (RD-T06): a human-set revenue target and its
// server-computed attainment. Everything numeric — band, attainment_pct,
// gap_minor, pace_pct, closed_won_minor — arrives from the server verbatim;
// the only UI computations are the ring's dash-offset (in the atom), the pace
// ahead/behind compare, and the integer-euro parse on target entry. Money in
// the attainment view is the WORKSPACE BASE currency (QuotaAttainment.currency),
// not Quota.currency — labelled as such.

type Quota = components["schemas"]["Quota"];
type QuotaAttainment = components["schemas"]["QuotaAttainment"];

export function useQuotas() {
  return useQuery({
    queryKey: ["quotas"],
    queryFn: async () => {
      const { data, error } = await api.GET("/quotas", {
        params: { query: {} },
      });
      if (error) throw new Error(problemMessage(error));
      return data.data;
    },
  });
}

// Attainment is a separate sub-resource read; a 422 (target zero /
// computation failed) must surface distinctly, so the error is thrown as a
// ProblemError carrying the structured body and the section branches on its
// code rather than collapsing to a generic failure.
function useAttainment(quotaId: string) {
  return useQuery({
    queryKey: ["quota-attainment", quotaId],
    enabled: quotaId !== "",
    queryFn: async (): Promise<QuotaAttainment> => {
      const { data, error } = await api.GET("/quotas/{id}/attainment", {
        params: { path: { id: quotaId } },
      });
      if (error) {
        // throwProblem keeps the RFC 7807 body so the code is branchable.
        throw new ProblemError(error);
      }
      return data;
    },
  });
}

function problemCode(problem: unknown): string | null {
  if (problem && typeof problem === "object") {
    const code = (problem as Record<string, unknown>).code;
    if (typeof code === "string") return code;
  }
  return null;
}

// Pace is the consumer's compare (the field carries period progress only): a
// met band reads "target met"; otherwise attainment at or past the elapsed
// fraction is ahead, short of it is behind. Never recomputes the band.
type PaceState = "met" | "ahead" | "behind";
function paceState(attainment: QuotaAttainment): PaceState {
  if (attainment.band === "met") return "met";
  return attainment.attainment_pct >= attainment.pace_pct ? "ahead" : "behind";
}

export function PaceLine({
  attainment,
}: Readonly<{ attainment: QuotaAttainment }>) {
  const t = useT();
  const state = paceState(attainment);
  const pct = Math.round(attainment.attainment_pct);
  const pace = Math.round(attainment.pace_pct);
  const message =
    state === "met"
      ? t("quotas.pace.met", { pct })
      : state === "ahead"
        ? t("quotas.pace.ahead", { pct, pace })
        : t("quotas.pace.behind", { pct, pace });
  const dotColor = state === "behind" ? "var(--away)" : "var(--online)";
  return (
    <div className="pace-line">
      <span className="pace-dot" style={{ background: dotColor }} />
      <span>{message}</span>
    </div>
  );
}

// The signed closed-won / target / gap block. The gap is signed on the wire
// (positive once over target); the display prefixes "+" for a non-negative
// gap — the negative sign already rides the formatted figure.
export function AttainmentNumbers({
  attainment,
  locale,
}: Readonly<{ attainment: QuotaAttainment; locale: Locale }>) {
  const t = useT();
  const currency = attainment.currency;
  const gap = attainment.gap_minor;
  const gapText = (gap >= 0 ? "+" : "") + formatMoney(gap, currency, locale);
  return (
    <div className="attain-numbers">
      <div className="attain-row">
        <span className="k strong">{t("quotas.closedWon")}</span>
        <span className="v t-mono">
          {formatMoney(attainment.closed_won_minor, currency, locale)}
        </span>
      </div>
      <div className="attain-div" />
      <div className="attain-row">
        <span className="k">{t("quotas.target")}</span>
        <span className="v t-mono">
          {formatMoney(attainment.target_minor, currency, locale)}
        </span>
      </div>
      <div className="attain-row">
        <span className="k">{t("quotas.gap")}</span>
        <span className="v t-mono">{gapText}</span>
      </div>
    </div>
  );
}

// The "Explain this number" decomposition, rendered from the server figures
// verbatim: the counted total is closed_won_minor (never the client sum of
// contributing_deals, though the contract guarantees they're equal).
function ExplainBox({
  attainment,
  locale,
}: Readonly<{ attainment: QuotaAttainment; locale: Locale }>) {
  const t = useT();
  const currency = attainment.currency;
  const sum = formatMoney(attainment.closed_won_minor, currency, locale);
  const target = formatMoney(attainment.target_minor, currency, locale);
  const pct = Math.round(attainment.attainment_pct);
  const count = attainment.contributing_deals.length;
  return (
    <div className="explain-box">
      <span>{t("quotas.explain.formula")}</span>
      <span>{t("quotas.explain.closedWon", { sum, count })}</span>
      <span>{t("quotas.explain.target", { target })}</span>
      <span>{t("quotas.explain.result", { sum, target, pct })}</span>
      <span className="t-caption">{t("quotas.explain.exclusions")}</span>
    </div>
  );
}

function AttainmentCard({
  attainment,
  locale,
}: Readonly<{ attainment: QuotaAttainment; locale: Locale }>) {
  const t = useT();
  const [showExplain, setShowExplain] = useState(false);
  return (
    <Card className="attain-card">
      <AttainmentRing
        pct={attainment.attainment_pct}
        band={attainment.band}
        caption={t("quotas.attained")}
      />
      <div className="attain-body">
        <AttainmentNumbers attainment={attainment} locale={locale} />
        <PaceLine attainment={attainment} />
        <div className="attain-meta">
          <Button small onClick={() => setShowExplain((value) => !value)}>
            {t("explain.open")}
          </Button>
          <span className="computed-chip">{t("quotas.computed")}</span>
        </div>
        {showExplain && <ExplainBox attainment={attainment} locale={locale} />}
        <p className="t-caption">
          {t("quotas.baseCurrencyNote", { currency: attainment.currency })}
        </p>
      </div>
    </Card>
  );
}

// The per-deal breakdown. Deal names resolve best-effort through the shared
// EntityRef (cached GET /deals/{id}); an unresolved id renders as a mono id,
// never a fabricated name. The footer total is the authoritative
// closed_won_minor.
export function ContributingDeals({
  attainment,
  locale,
}: Readonly<{ attainment: QuotaAttainment; locale: Locale }>) {
  const t = useT();
  const currency = attainment.currency;
  return (
    <Card>
      <div className="section-header">
        <h2>{t("quotas.contributing.title")}</h2>
        <span className="sub">{t("quotas.contributing.subtitle")}</span>
      </div>
      <DataTable
        columns={[
          {
            key: "deal",
            header: t("quotas.contributing.deal"),
            render: (row: QuotaAttainment["contributing_deals"][number]) => (
              <EntityRef kind="deal" id={row.deal_id} />
            ),
          },
          {
            key: "amount",
            header: t("quotas.contributing.amount"),
            render: (row: QuotaAttainment["contributing_deals"][number]) => (
              <span className="t-mono">
                {formatMoney(row.base_value_minor, currency, locale)}
              </span>
            ),
          },
        ]}
        rows={attainment.contributing_deals}
        rowKey={(row) => row.deal_id}
      />
      <div className="quota-foot">
        <span className="k">{t("quotas.contributing.total")}</span>
        <span className="v t-mono">
          {formatMoney(attainment.closed_won_minor, currency, locale)}
        </span>
      </div>
      <p className="t-caption" style={{ textAlign: "right" }}>
        {t("quotas.contributing.caption")}
      </p>
    </Card>
  );
}

// The honest refusal / error card — target-zero and compute-failed render the
// server detail and deliberately draw NO ring (a guessed figure would be worse
// than none). A recoverable failure carries a Retry.
function AttainmentRefusal({
  title,
  detail,
  onRetry,
}: Readonly<{ title: string; detail: string; onRetry?: () => void }>) {
  const t = useT();
  return (
    <Card>
      <div className="attain-refusal">
        <b>{title}</b>
        <p className="t-caption">{detail}</p>
        {onRetry && (
          <Button small onClick={onRetry} style={{ alignSelf: "flex-start" }}>
            {t("common.retry")}
          </Button>
        )}
      </div>
    </Card>
  );
}

function AttainmentSkeleton() {
  return (
    <Card className="attain-card">
      <Skeleton width={160} height={160} />
      <div className="attain-body">
        <Skeleton width="70%" />
        <Skeleton width="50%" />
        <Skeleton width="60%" />
      </div>
    </Card>
  );
}

function AttainmentSection({
  query,
  locale,
}: Readonly<{
  query: UseQueryResult<QuotaAttainment>;
  locale: Locale;
}>) {
  const t = useT();
  if (query.isPending) {
    return <AttainmentSkeleton />;
  }
  if (query.isError) {
    const problem =
      query.error instanceof ProblemError ? query.error.problem : null;
    const code = problemCode(problem);
    const detail =
      query.error instanceof Error ? query.error.message : t("common.error");
    if (code === "attainment_target_zero") {
      return (
        <AttainmentRefusal title={t("quotas.err.targetZero")} detail={detail} />
      );
    }
    if (code === "attainment_computation_failed") {
      return (
        <AttainmentRefusal
          title={t("quotas.err.computeFailed")}
          detail={detail}
          onRetry={() => query.refetch()}
        />
      );
    }
    return (
      <AttainmentRefusal
        title={t("common.error")}
        detail={detail}
        onRetry={() => query.refetch()}
      />
    );
  }
  const attainment = query.data;
  return (
    <>
      <AttainmentCard attainment={attainment} locale={locale} />
      <ContributingDeals attainment={attainment} locale={locale} />
    </>
  );
}

function QuotaRowLabel({
  quota,
  locale,
}: Readonly<{ quota: Quota; locale: Locale }>) {
  const t = useT();
  const isOwner = quota.owner_id != null;
  const role = isOwner ? t("quotas.role.owner") : t("quotas.role.team");
  return (
    <div>
      <div className="quota-row-top">
        <span className="quota-row-name">
          <EntityRef
            kind={isOwner ? "user" : "team"}
            id={isOwner ? quota.owner_id : quota.team_id}
          />
        </span>
        <span className="t-mono quota-row-target">
          {formatMoney(quota.target_minor, quota.currency, locale)}
        </span>
      </div>
      <span className="quota-row-sub t-caption">
        {role} ·{" "}
        {t("quotas.periodRange", {
          start: quota.period_start,
          end: quota.period_end,
        })}
      </span>
    </div>
  );
}

function QuotaSelector({
  list,
  activeId,
  onSelect,
  onCreated,
}: Readonly<{
  list: Quota[];
  activeId: string;
  onSelect: (id: string) => void;
  onCreated: (id: string) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <Card>
      <div className="section-header">
        <h2>{t("quotas.tab")}</h2>
        <SetTargetAction label={t("quotas.target.new")} onCreated={onCreated} />
      </div>
      <div className="quota-list">
        {list.map((quota) => (
          <button
            key={quota.id}
            type="button"
            className={quota.id === activeId ? "quota-row active" : "quota-row"}
            aria-pressed={quota.id === activeId}
            onClick={() => onSelect(quota.id)}
          >
            <QuotaRowLabel quota={quota} locale={locale} />
          </button>
        ))}
      </div>
    </Card>
  );
}

export function ScopeNote() {
  const t = useT();
  return (
    <Card>
      <div className="section-header">
        <h2>{t("quotas.scopeNote.title")}</h2>
        <span className="sub">{t("quotas.scopeNote.flag")}</span>
      </div>
      <p className="t-caption">{t("quotas.scopeNote.body")}</p>
    </Card>
  );
}

function TargetRail({
  quota,
  onArchived,
}: Readonly<{ quota: Quota; onArchived: () => void }>) {
  const t = useT();
  return (
    <Card>
      <div className="section-header">
        <h2>{t("quotas.target.title")}</h2>
      </div>
      <div className="rail-actions">
        <EditTargetAction label={t("quotas.target.edit")} quota={quota} />
        <p className="t-caption">{t("quotas.target.note")}</p>
        <p className="t-caption">{t("quotas.target.sideFixed")}</p>
      </div>
      <div className="rail-div" />
      <ArchiveQuotaAction quota={quota} onArchived={onArchived} />
    </Card>
  );
}

function EmptyQuota({
  onCreated,
}: Readonly<{ onCreated: (id: string) => void }>) {
  const t = useT();
  return (
    <EmptyState>
      <b>{t("quotas.empty.title")}</b>
      <p className="t-caption" style={{ margin: "8px 0 14px" }}>
        {t("quotas.empty.body")}
      </p>
      <SetTargetAction label={t("quotas.empty.cta")} onCreated={onCreated} />
    </EmptyState>
  );
}

function QuotasBody({
  list,
  active,
  onSelect,
  onCreated,
  onArchived,
}: Readonly<{
  list: Quota[];
  active: Quota;
  onSelect: (id: string) => void;
  onCreated: (id: string) => void;
  onArchived: () => void;
}>) {
  const { locale } = useLocale();
  const attainment = useAttainment(active.id);
  return (
    <div className="qgrid">
      <div className="colstack">
        <QuotaSelector
          list={list}
          activeId={active.id}
          onSelect={onSelect}
          onCreated={onCreated}
        />
        <AttainmentSection query={attainment} locale={locale} />
        <ScopeNote />
      </div>
      <div className="colstack">
        <TargetRail quota={active} onArchived={onArchived} />
      </div>
    </div>
  );
}

export function QuotasView() {
  const quotasQuery = useQuotas();
  const [selectedId, setSelectedId] = useState<string | null>(null);
  return (
    <QueryGate query={quotasQuery}>
      {(list) => {
        if (list.length === 0) {
          return <EmptyQuota onCreated={setSelectedId} />;
        }
        // First quota selected by default; a stale selection (e.g. after an
        // archive) falls back to the first row rather than a blank detail.
        const active = list.find((quota) => quota.id === selectedId) ?? list[0];
        return (
          <QuotasBody
            list={list}
            active={active}
            onSelect={setSelectedId}
            onCreated={setSelectedId}
            onArchived={() => setSelectedId(null)}
          />
        );
      }}
    </QueryGate>
  );
}
