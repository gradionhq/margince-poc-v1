import {
  type UseQueryResult,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { ArrowRight, Check, RefreshCw, X } from "lucide-react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { DealCard } from "../design-system/composed";
import { formatDateTime, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage, QueryGate } from "./common";
import { ApprovalRow, usePendingApprovals } from "./inbox";
import "./home.css";

// Home / Morning Brief (B-EP09.12b on the E05 spine): the persisted /brief
// run IS the queue — the §10.1 composite with its factor decomposition (no
// mystery number), evidence-or-omit, per-rep act/dismiss (B-E05.13). Pending
// 🟡 approvals stay on top (nothing sent yet); stalled deals close the page.
// No run yet → an honest generate card; an empty run → honest quiet, never
// invented urgency.

type MorningBrief = components["schemas"]["MorningBrief"];
type MorningBriefItem = components["schemas"]["MorningBriefItem"];

export function useMorningBrief(): UseQueryResult<MorningBrief | null> {
  return useQuery({
    queryKey: ["brief"],
    queryFn: async (): Promise<MorningBrief | null> => {
      const { data, error, response } = await api.GET("/brief");
      if (response.status === 404) {
        return null;
      }
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data ?? null;
    },
  });
}

// The §10.1 factor order is normative (winnability · revenue · timing ·
// momentum · warmth) — displaying it in that order keeps the decomposition
// recognizable across surfaces.
const FACTORS: {
  key: keyof components["schemas"]["MorningBriefFeatureVector"];
  label: MessageKey;
}[] = [
  { key: "winnability", label: "home.factorWinnability" },
  { key: "revenue", label: "home.factorRevenue" },
  { key: "timing", label: "home.factorTiming" },
  { key: "momentum", label: "home.factorMomentum" },
  { key: "warmth", label: "home.factorWarmth" },
];

function FactorBars({
  vector,
  itemId,
}: Readonly<{
  vector: components["schemas"]["MorningBriefFeatureVector"];
  itemId: string;
}>) {
  const t = useT();
  return (
    <div className="brief-factors" title={t("home.why")}>
      {FACTORS.map((factor) => {
        const value = Math.max(0, Math.min(1, vector[factor.key]));
        return (
          <div className="brief-factor" key={`${itemId}-${factor.key}`}>
            <span className="brief-factor-label t-caption">
              {t(factor.label)}
            </span>
            <span className="brief-factor-track">
              <span
                className="brief-factor-fill"
                style={{ width: `${Math.round(value * 100)}%` }}
              />
            </span>
          </div>
        );
      })}
    </div>
  );
}

function useBriefItemMark(itemId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (mark: "act" | "dismiss") => {
      const path =
        mark === "act"
          ? "/brief/items/{itemId}/act"
          : "/brief/items/{itemId}/dismiss";
      const { data, error } = await api.POST(path, {
        params: { path: { itemId } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (updated) => {
      queryClient.setQueryData<MorningBrief | null>(["brief"], (current) =>
        current
          ? {
              ...current,
              items: current.items.map((item) =>
                item.id === updated.id ? updated : item,
              ),
            }
          : current,
      );
    },
  });
}

function BriefItemCard({ item }: Readonly<{ item: MorningBriefItem }>) {
  const t = useT();
  const { locale } = useLocale();
  const mark = useBriefItemMark(item.id);
  const dealQuery = useQuery({
    queryKey: ["deal", item.deal_id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}", {
        params: { path: { id: item.deal_id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const deal = dealQuery.data;
  const settled = item.state !== "new";
  const stateLabel: MessageKey =
    item.state === "acted" ? "home.actedState" : "home.dismissedState";

  return (
    <article
      className={`card brief-deal ${settled ? "settled" : ""}`}
      data-brief-item={item.id}
    >
      <div className="brief-deal-head">
        <span className="brief-rank">#{item.rank}</span>
        <button
          type="button"
          className="brief-deal-name"
          onClick={() => navigate({ screen: "deals", id: item.deal_id })}
        >
          {deal?.name ?? t("home.openDeal")} <ArrowRight aria-hidden />
        </button>
        {deal?.amount_minor != null && (
          <span className="brief-deal-amount">
            {formatMoney(deal.amount_minor, deal.currency ?? "EUR", locale)}
          </span>
        )}
        <span className="brief-score t-mono">
          {t("home.score", { pct: Math.round(item.composite * 100) })}
        </span>
      </div>
      <FactorBars vector={item.feature_vector} itemId={item.id} />
      <div className="brief-deal-foot">
        <Badge>
          {item.evidence_ids.length === 1
            ? t("home.evidenceOne")
            : t("home.evidence", { count: item.evidence_ids.length })}
        </Badge>
        {settled ? (
          <Badge tone={item.state === "acted" ? "success" : "warn"}>
            {t(stateLabel)}
          </Badge>
        ) : (
          <span className="brief-deal-actions">
            <Button
              small
              variant="primary"
              disabled={mark.isPending}
              onClick={() => mark.mutate("act")}
            >
              <Check aria-hidden /> {t("home.act")}
            </Button>
            <Button
              small
              disabled={mark.isPending}
              onClick={() => mark.mutate("dismiss")}
            >
              <X aria-hidden /> {t("home.dismiss")}
            </Button>
          </span>
        )}
      </div>
      {mark.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {mark.error instanceof Error ? mark.error.message : null}
        </p>
      )}
    </article>
  );
}

function honestCountLine(
  t: ReturnType<typeof useT>,
  brief: MorningBrief,
): string {
  if (brief.candidate_count > brief.items.length) {
    return t("home.overflow", {
      shown: brief.items.length,
      count: brief.candidate_count,
    });
  }
  return t("home.honestShort", { count: brief.candidate_count });
}

function BriefSection() {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const briefQuery = useMorningBrief();

  const refresh = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/brief");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.setQueryData(["brief"], data ?? null);
    },
  });

  const refreshButton = (label: MessageKey) => (
    <Button
      small
      disabled={refresh.isPending}
      onClick={() => refresh.mutate()}
      data-testid="brief-refresh"
    >
      <RefreshCw aria-hidden />{" "}
      {refresh.isPending ? t("home.refreshing") : t(label)}
    </Button>
  );

  return (
    <section aria-label={t("home.queue")}>
      <QueryGate query={briefQuery}>
        {(brief) => {
          if (brief === null) {
            return (
              <Card className="brief-none">
                <SectionHeader
                  title={t("home.noneTitle")}
                  sub={t("home.noneBody")}
                />
                {refreshButton("home.generate")}
                {refresh.isError && (
                  <p className="t-caption" style={{ color: "var(--danger)" }}>
                    {refresh.error instanceof Error
                      ? refresh.error.message
                      : null}
                  </p>
                )}
              </Card>
            );
          }
          return (
            <div>
              <div className="brief-runbar">
                <SectionHeader
                  title={t("home.queue")}
                  sub={t("home.asOf", {
                    at: formatDateTime(brief.as_of, locale, "Europe/Berlin"),
                  })}
                />
                {refreshButton("home.refresh")}
              </div>
              {brief.items.length === 0 ? (
                <EmptyState>{t("home.quietRun")}</EmptyState>
              ) : (
                <div className="brief-list">
                  {brief.items.map((item) => (
                    <BriefItemCard key={item.id} item={item} />
                  ))}
                  <p className="t-caption brief-honesty">
                    {honestCountLine(t, brief)}
                  </p>
                </div>
              )}
              {refresh.isError && (
                <p className="t-caption" style={{ color: "var(--danger)" }}>
                  {refresh.error instanceof Error
                    ? refresh.error.message
                    : null}
                </p>
              )}
            </div>
          );
        }}
      </QueryGate>
    </section>
  );
}

export function HomeScreen() {
  const t = useT();
  const approvalsQuery = usePendingApprovals();
  const dealsQuery = useQuery({
    queryKey: ["deals"],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals", {
        params: { query: { limit: 100 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const stalled = (dealsQuery.data?.data ?? []).filter(
    (deal) => deal.stalled && deal.status === "open",
  );

  // The three sections are independent surfaces: a transient approvals or
  // deals failure must never hide a healthy /brief queue (and vice versa),
  // so each renders under its own gate.
  return (
    <div className="wrap narrow">
      <SectionHeader title={t("home.brief")} sub={t("home.sub")} />
      <QueryGate query={approvalsQuery} empty={() => false}>
        {(approvals) =>
          approvals.data.length > 0 ? (
            <section aria-label={t("home.staged")}>
              <SectionHeader
                title={t("home.staged")}
                sub={t("brief.nothingSent")}
              />
              {approvals.data.map((approval) => (
                <ApprovalRow key={approval.id} approval={approval} />
              ))}
            </section>
          ) : null
        }
      </QueryGate>
      <BriefSection />
      {stalled.length > 0 && (
        <section aria-label={t("home.stalled")}>
          <SectionHeader title={t("home.stalled")} />
          <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
            {stalled.map((deal) => (
              <DealCard
                key={deal.id}
                deal={{
                  id: deal.id,
                  name: deal.name,
                  org: "",
                  valueMinor: deal.amount_minor ?? 0,
                  currency: deal.currency ?? "EUR",
                  ageMs: Math.max(
                    0,
                    Date.now() -
                      new Date(
                        deal.last_activity_at ?? deal.created_at,
                      ).getTime(),
                  ),
                  stalled: true,
                }}
                onOpen={() => navigate({ screen: "deals", id: deal.id })}
              />
            ))}
          </div>
        </section>
      )}
    </div>
  );
}
