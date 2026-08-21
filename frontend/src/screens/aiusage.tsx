import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan } from "../app/capability";
import {
  Badge,
  Button,
  DataTable,
  Disclosure,
  EmptyState,
} from "../design-system/atoms";
import { Panel, PanelBody } from "../design-system/panel";
import { Meter } from "../design-system/readings";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatMoney, formatNumber } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import { QueryGate, throwProblem, useMe } from "./common";
import "./aiusage.css";

type AiUsage = components["schemas"]["AiUsage"];
type UsageTask = AiUsage["days"][number]["tasks"][number];
type Month = { from: string; to: string };

export function bandTone(band: string): "warn" | "danger" | undefined {
  if (band === "normal") return undefined;
  if (band === "degraded") return "warn";
  return "danger";
}

function bandLabel(
  band: AiUsage["budget"]["band"],
  t: ReturnType<typeof useT>,
) {
  if (band === "degraded") return t("aiusage.band.degraded");
  if (band === "queued") return t("aiusage.band.queued");
  if (band === "normal") return t("aiusage.band.normal");
  return t("aiusage.band.unknown");
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

// The spend table's columns, built once per render of the body rather than
// inline in the JSX: the cost column exists only when the server priced at
// least one call, and a column list is data — DataTable owns the .table-scroll
// wrapper that keeps seven columns inside the card on a phone instead of
// running 630px wide inside 324px of it.
function usageColumns(
  showCost: boolean,
  currency: string,
  locale: Locale,
  t: ReturnType<typeof useT>,
) {
  const columns = [
    {
      key: "task",
      header: t("aiusage.col.task"),
      render: (r: UsageTask) => r.task,
    },
    {
      key: "tier",
      header: t("aiusage.col.tier"),
      render: (r: UsageTask) => r.tier,
    },
    {
      key: "calls",
      header: t("aiusage.col.calls"),
      render: (r: UsageTask) => formatNumber(r.calls, locale),
    },
    {
      key: "cached",
      header: t("aiusage.col.cached"),
      render: (r: UsageTask) => formatNumber(r.cached_hits ?? 0, locale),
    },
    {
      key: "tokensIn",
      header: t("aiusage.col.tokensIn"),
      render: (r: UsageTask) => formatNumber(r.tokens_in, locale),
    },
    {
      key: "tokensOut",
      header: t("aiusage.col.tokensOut"),
      render: (r: UsageTask) => formatNumber(r.tokens_out, locale),
    },
  ];
  if (!showCost) {
    return columns;
  }
  return [
    ...columns,
    {
      key: "cost",
      header: t("aiusage.col.cost"),
      // A row the server did not price is not a row that cost nothing — the
      // marker says we do not know, where a zero would state a figure.
      render: (r: UsageTask) =>
        r.cost_est_minor === undefined
          ? "—"
          : formatMoney(r.cost_est_minor, currency, locale),
    },
  ];
}

// The body is its own component so the per-task rollup can be a useMemo. Inside
// QueryGate's render prop it was an O(days × tasks) fold re-run on every render
// of the card — including the ones a sibling's 60-second clock causes.
function AiUsageBody({
  data,
  month,
  onMonth,
}: Readonly<{
  data: AiUsage;
  month: Month | null;
  onMonth: (next: Month) => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const pct =
    data.budget.monthly_tokens > 0
      ? Math.round(
          (data.budget.spent_tokens / data.budget.monthly_tokens) * 100,
        )
      : 100;
  const rows = useMemo(() => aggregate(data.days), [data.days]);
  const showCost = useMemo(
    () =>
      data.days.some((day) =>
        day.tasks.some((task) => task.cost_est_minor !== undefined),
      ),
    [data.days],
  );
  const currency = data.budget.currency ?? "USD";
  const totalCost = useMemo(
    () => rows.reduce((sum, row) => sum + (row.cost_est_minor ?? 0), 0),
    [rows],
  );

  return (
    <SettingList>
      <SettingRow
        layout="stack"
        label={t("aiusage.budgetMeter")}
        description={t("aiusage.budget", {
          spent: formatNumber(data.budget.spent_tokens, locale),
          budget: formatNumber(data.budget.monthly_tokens, locale),
          pct,
        })}
        control={
          <div className="settingrow-measure aiusage-budget">
            <div className="aiusage-budget-bar">
              {/* pct, not the raw token pair: a workspace with no monthly budget
                  configured reads as fully spent (pct is 100 above), and the bar
                  must say what the caption beside it says. */}
              <Meter value={pct} max={100} label={t("aiusage.budgetMeter")} />
            </div>
            <Badge tone={bandTone(data.budget.band)}>
              {bandLabel(data.budget.band, t)}
            </Badge>
          </div>
        }
      />
      <SettingRow
        label={t("aiusage.monthLabel")}
        control={
          // The two arrows keep their own names: a glyph announces as nothing,
          // and the row's label says which decision this is, not which way each
          // button moves it.
          <>
            <Button
              small
              aria-label={t("aiusage.prevMonth")}
              onClick={() => onMonth(adjacentMonth(month, -1))}
            >
              ‹
            </Button>
            <Button
              small
              aria-label={t("aiusage.nextMonth")}
              disabled={isCurrentMonth(month)}
              onClick={() => onMonth(adjacentMonth(month, 1))}
            >
              ›
            </Button>
          </>
        }
      />
      <SettingRow
        layout="stack"
        label={t("aiusage.spendLabel")}
        // The caveat and the total are what the table says taken together, so
        // they belong to the row's NAMING rather than standing under the table
        // as a caption of their own — a sentence in a control column reads as
        // that control's answer. Absent entirely when the server priced
        // nothing: a total of zero would state a figure this window has none of.
        description={
          showCost ? (
            <>
              {t("aiusage.costNote")} {formatMoney(totalCost, currency, locale)}
            </>
          ) : undefined
        }
        control={
          <div className="settingrow-measure">
            {rows.length === 0 ? (
              <EmptyState>{t("aiusage.empty")}</EmptyState>
            ) : (
              // Seven columns do not fit a phone, and nothing here can be
              // dropped — a spend row is only reconcilable whole. DataTable is
              // what scrolls the TABLE sideways inside the card rather than the
              // page; a hand-rolled <table className="table"> borrowed the look
              // and left the wrapper out.
              <DataTable
                label={t("aiusage.spendLabel")}
                columns={usageColumns(showCost, currency, locale, t)}
                rows={rows}
                rowKey={(row) => `${row.task}-${row.tier}`}
              />
            )}
          </div>
        }
      />
      {/* The per-day breakdown is diagnostic — a reader reconciling one day's
          calls asks for it, and it is noise to everyone else — so it stands in
          the list as its own closed section rather than as a fourth row. */}
      {data.days.length > 0 && (
        <Disclosure summary={t("aiusage.days.show")}>
          {data.days.map((day) => (
            <p key={day.date} className="t-mono">
              {day.date} ·{" "}
              {day.tasks.reduce((sum, task) => sum + task.calls, 0)}{" "}
              {t("aiusage.col.calls")}
            </p>
          ))}
        </Disclosure>
      )}
    </SettingList>
  );
}

export function AiUsageCard() {
  const t = useT();
  const me = useMe();
  // The server treats the AI runtime's spend as operator information and gates
  // this read on automation:update — a write verb guarding a GET, which is why
  // the seat ceiling stays out of it (capability.ts): a read seat may still read.
  const canSee = useCan("automation", "update");
  const [month, setMonth] = useState<Month | null>(null);
  const query = useQuery({
    enabled: canSee,
    queryKey: ["ai-usage", month],
    queryFn: async () => {
      const { data, error } = await api.GET("/ai/usage", {
        params: { query: month ?? {} },
      });
      if (error) throwProblem(error);
      if (!data?.budget || !Array.isArray(data.days)) {
        throw new Error("malformed AI usage response");
      }
      return data;
    },
  });

  if (!canSee) {
    // Withheld, not absent. An absent spend card on the AI page is a claim about
    // the DATA — that nothing has been spent, or that this installation does not
    // meter it — where the truth is only that these figures are not this reader's
    // to see. Gated on the /me probe so the notice waits for the grants rather
    // than flashing while they are in flight, and the query above asks the server
    // for nothing because the answer is already known.
    return (
      <Panel title={t("aiusage.title")}>
        <PanelBody>
          <p className="settings-panel-sub">{t("aiusage.sub")}</p>
          <QueryGate query={me}>
            {() => (
              <EmptyState>
                <p className="t-small">{t("aiusage.withheld")}</p>
              </EmptyState>
            )}
          </QueryGate>
        </PanelBody>
      </Panel>
    );
  }

  // No bottom margin of its own: `.settings-stack` owns the gap between cards.
  return (
    <Panel title={t("aiusage.title")}>
      <PanelBody>
        <p className="settings-panel-sub">{t("aiusage.sub")}</p>
        <QueryGate query={query}>
          {(data) => (
            <AiUsageBody data={data} month={month} onMonth={setMonth} />
          )}
        </QueryGate>
      </PanelBody>
    </Panel>
  );
}
