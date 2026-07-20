import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { formatMoney, formatNumber } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

type AiUsage = components["schemas"]["AiUsage"];
type UsageTask = AiUsage["days"][number]["tasks"][number];
type Month = { from: string; to: string };

export function bandTone(band: string): "warn" | "danger" | undefined {
  if (band === "degraded") return "warn";
  if (band === "queued") return "danger";
  return undefined;
}

function bandLabel(
  band: AiUsage["budget"]["band"],
  t: ReturnType<typeof useT>,
) {
  if (band === "degraded") return t("aiusage.band.degraded");
  if (band === "queued") return t("aiusage.band.queued");
  return t("aiusage.band.normal");
}

function adjacentMonth(month: Month | null, offset: number): Month {
  const seed = month ? new Date(`${month.from}T00:00:00Z`) : new Date();
  const first = new Date(
    Date.UTC(seed.getUTCFullYear(), seed.getUTCMonth() + offset, 1),
  );
  const last = new Date(
    Date.UTC(first.getUTCFullYear(), first.getUTCMonth() + 1, 0),
  );
  return {
    from: first.toISOString().slice(0, 10),
    to: last.toISOString().slice(0, 10),
  };
}

function isCurrentMonth(month: Month | null): boolean {
  if (month === null) return true;
  return month.from.slice(0, 7) >= new Date().toISOString().slice(0, 7);
}

function aggregate(days: AiUsage["days"]): UsageTask[] {
  const rows = new Map<string, UsageTask>();
  for (const day of days) {
    for (const task of day.tasks) {
      const key = `${task.task}\u0000${task.tier}`;
      const current = rows.get(key);
      if (!current) {
        rows.set(key, { ...task });
        continue;
      }
      current.calls += task.calls;
      current.cached_hits =
        (current.cached_hits ?? 0) + (task.cached_hits ?? 0);
      current.tokens_in += task.tokens_in;
      current.tokens_out += task.tokens_out;
      if (task.cost_est_minor !== undefined) {
        current.cost_est_minor =
          (current.cost_est_minor ?? 0) + task.cost_est_minor;
      }
    }
  }
  return [...rows.values()];
}

export function AiUsageCard() {
  const t = useT();
  const { locale } = useLocale();
  const [month, setMonth] = useState<Month | null>(null);
  const [showDays, setShowDays] = useState(false);
  const query = useQuery({
    queryKey: ["ai-usage", month],
    queryFn: async () => {
      const { data, error } = await api.GET("/ai/usage", {
        params: { query: month ?? {} },
      });
      if (error) throw new Error(problemMessage(error));
      if (!data?.budget || !Array.isArray(data.days)) {
        throw new Error("malformed AI usage response");
      }
      return data;
    },
  });

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader title={t("aiusage.title")} sub={t("aiusage.sub")} />
      <QueryGate query={query}>
        {(data) => {
          const pct =
            data.budget.monthly_tokens > 0
              ? Math.round(
                  (data.budget.spent_tokens / data.budget.monthly_tokens) * 100,
                )
              : 100;
          const rows = aggregate(data.days);
          const showCost = data.days.some((day) =>
            day.tasks.some((task) => task.cost_est_minor !== undefined),
          );
          const currency = data.budget.currency ?? "USD";
          const totalCost = rows.reduce(
            (sum, row) => sum + (row.cost_est_minor ?? 0),
            0,
          );
          return (
            <>
              <div
                style={{
                  display: "flex",
                  justifyContent: "space-between",
                  gap: "var(--space-3)",
                }}
              >
                <div style={{ flex: 1 }}>
                  <div className="meterbar">
                    <span style={{ width: `${Math.min(100, pct)}%` }} />
                  </div>
                  <p className="sub">
                    {t("aiusage.budget", {
                      spent: formatNumber(data.budget.spent_tokens, locale),
                      budget: formatNumber(data.budget.monthly_tokens, locale),
                      pct,
                    })}
                  </p>
                </div>
                <Badge tone={bandTone(data.budget.band)}>
                  {bandLabel(data.budget.band, t)}
                </Badge>
              </div>
              <div
                style={{
                  display: "flex",
                  gap: "var(--space-2)",
                  margin: "var(--space-3) 0",
                }}
              >
                <Button
                  small
                  aria-label={t("aiusage.prevMonth")}
                  onClick={() => setMonth(adjacentMonth(month, -1))}
                >
                  ‹
                </Button>
                <Button
                  small
                  aria-label={t("aiusage.nextMonth")}
                  disabled={isCurrentMonth(month)}
                  onClick={() => setMonth(adjacentMonth(month, 1))}
                >
                  ›
                </Button>
              </div>
              {rows.length === 0 ? (
                <EmptyState>{t("aiusage.empty")}</EmptyState>
              ) : (
                <table className="table">
                  <thead>
                    <tr>
                      <th>{t("aiusage.col.task")}</th>
                      <th>{t("aiusage.col.tier")}</th>
                      <th>{t("aiusage.col.calls")}</th>
                      <th>{t("aiusage.col.cached")}</th>
                      <th>{t("aiusage.col.tokensIn")}</th>
                      <th>{t("aiusage.col.tokensOut")}</th>
                      {showCost && <th>{t("aiusage.col.cost")}</th>}
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((row) => (
                      <tr key={`${row.task}-${row.tier}`}>
                        <td>{row.task}</td>
                        <td>{row.tier}</td>
                        <td>{formatNumber(row.calls, locale)}</td>
                        <td>{formatNumber(row.cached_hits ?? 0, locale)}</td>
                        <td>{formatNumber(row.tokens_in, locale)}</td>
                        <td>{formatNumber(row.tokens_out, locale)}</td>
                        {showCost && (
                          <td>
                            {row.cost_est_minor === undefined
                              ? "—"
                              : formatMoney(
                                  row.cost_est_minor,
                                  currency,
                                  locale,
                                )}
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
              {showCost && (
                <p className="t-caption">
                  {t("aiusage.costNote")}{" "}
                  {formatMoney(totalCost, currency, locale)}
                </p>
              )}
              {data.days.length > 0 && (
                <Button small onClick={() => setShowDays((value) => !value)}>
                  {showDays ? t("aiusage.days.hide") : t("aiusage.days.show")}
                </Button>
              )}
              {showDays &&
                data.days.map((day) => (
                  <p key={day.date} className="t-mono">
                    {day.date} ·{" "}
                    {day.tasks.reduce((sum, task) => sum + task.calls, 0)}{" "}
                    {t("aiusage.col.calls")}
                  </p>
                ))}
            </>
          );
        }}
      </QueryGate>
    </section>
  );
}
