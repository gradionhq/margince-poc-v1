import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import { Button, DataTable, SectionHeader } from "../design-system/atoms";
import { formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// Reports (B-EP09.12c): deals-by-stage from the typed report plan —
// unweighted (raw sum) beside weighted (probability-scaled), and "explain
// this number" opens the executed plan + the exact rows the headline
// reconciles to. Forecast aggregates are deterministic: no confidence dots
// here, ever. Weighted display uses each stage's win_probability against
// the report's own sums — same page-local rule as the board.

type StageAgg = {
  stageId: string;
  stageName: string;
  probabilityPct: number;
  count: number;
  rawMinor: number;
  currency: string | null;
};

export function ReportsScreen() {
  const t = useT();
  const { locale } = useLocale();
  const [explain, setExplain] = useState(false);

  const pipelineQuery = useQuery({
    queryKey: ["pipelines"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data.find((pipeline) => pipeline.is_default) ?? data.data[0];
    },
  });

  const reportQuery = useQuery({
    queryKey: ["report", "deals-by-stage"],
    queryFn: async () => {
      const { data, error } = await api.POST("/reports/{report}", {
        params: { path: { report: "deals-by-stage" } },
        body: {
          group_by: ["stage_id"],
          aggregates: [
            { fn: "sum", field: "amount_minor", as: "raw_minor" },
            { fn: "count", as: "deal_count" },
          ],
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  return (
    <div className="wrap narrow">
      <SectionHeader title={t("nav.reports")} sub={t("reports.sub")} />
      <QueryGate query={reportQuery}>
        {(report) => {
          const stages = pipelineQuery.data?.stages ?? [];
          const aggregates: StageAgg[] = report.rows.map((row) => {
            const stageId = String(row.stage_id ?? "");
            const stage = stages.find((candidate) => candidate.id === stageId);
            return {
              stageId,
              stageName: stage?.name ?? stageId,
              probabilityPct: stage?.win_probability ?? 0,
              count: Number(row.deal_count ?? 0),
              rawMinor: Number(row.raw_minor ?? 0),
              currency: typeof row.currency === "string" ? row.currency : "EUR",
            };
          });
          return (
            <div>
              <DataTable
                columns={[
                  {
                    key: "stage",
                    header: t("deals.stage"),
                    render: (row: StageAgg) => row.stageName,
                  },
                  {
                    key: "count",
                    header: t("reports.count"),
                    render: (row: StageAgg) => String(row.count),
                  },
                  {
                    key: "raw",
                    header: t("reports.unweighted"),
                    render: (row: StageAgg) => (
                      <span className="t-mono">
                        {formatMoney(
                          row.rawMinor,
                          row.currency ?? "EUR",
                          locale,
                        )}
                      </span>
                    ),
                  },
                  {
                    key: "weighted",
                    header: t("reports.weighted"),
                    render: (row: StageAgg) => (
                      <span className="t-mono">
                        {formatMoney(
                          Math.round((row.rawMinor * row.probabilityPct) / 100),
                          row.currency ?? "EUR",
                          locale,
                        )}
                      </span>
                    ),
                  },
                ]}
                rows={aggregates}
                rowKey={(row) => row.stageId}
              />
              <div style={{ marginTop: 12 }}>
                <Button small onClick={() => setExplain((value) => !value)}>
                  {t("explain.open")}
                </Button>
              </div>
              {explain && (
                <section
                  className="card"
                  style={{ marginTop: 10 }}
                  aria-label={t("explain.title")}
                >
                  <SectionHeader
                    title={t("explain.title")}
                    sub={t("reports.planNote")}
                  />
                  <pre
                    className="t-mono"
                    style={{ overflowX: "auto", fontSize: 11 }}
                  >
                    {JSON.stringify(
                      { plan: report.plan, rows: report.rows },
                      null,
                      2,
                    )}
                  </pre>
                </section>
              )}
            </div>
          );
        }}
      </QueryGate>
    </div>
  );
}
