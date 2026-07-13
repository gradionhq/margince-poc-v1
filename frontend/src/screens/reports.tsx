import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { api } from "../api/client";
import {
  Button,
  Card,
  DataTable,
  SectionHeader,
  SegmentedControl,
  Skeleton,
} from "../design-system/atoms";
import { formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// Reports (B-EP09.12c, D-11): a picker over three reports — deals-by-stage
// (unweighted next to weighted, unchanged since B-EP09.12c), forecast
// (unweighted category tiles — deterministic, no confidence dots — with a
// banner naming the weighted-vs-unweighted distinction), and open deals per
// company. "Explain this number" opens the executed plan + the exact rows
// the headline reconciles to. Weighted display on deals-by-stage uses each
// stage's win_probability against the report's own sums — same page-local
// rule as the board.

type StageAgg = {
  stageId: string;
  stageName: string;
  probabilityPct: number;
  count: number;
  rawMinor: number;
  currency: string | null;
};

type ReportKey = "deals-by-stage" | "forecast" | "open-deals-per-company";

const REPORT_GROUP_BY: Record<ReportKey, string> = {
  "deals-by-stage": "stage_id",
  forecast: "forecast_category",
  "open-deals-per-company": "organization_id",
};

// Parse a server-minted `derivation_url` into the typed derivation query.
// The generated client's derivation query is ONLY `{ by?, agg? }` (no
// predicate params, no index signature), so callers forward just those two;
// the extra predicate keys ride along on the return value for inspection
// only (spec constraint 6: never raw-fetch the URL itself).
export function parseDerivationQuery(
  url: string,
): { by: string[]; agg: string[] } & Record<string, unknown> {
  const qs = new URLSearchParams(url.split("?")[1] ?? "");
  const extra: Record<string, unknown> = {};
  for (const [k, v] of qs.entries()) {
    if (k !== "by" && k !== "agg") extra[k] = v;
  }
  return { ...extra, by: qs.getAll("by"), agg: qs.getAll("agg") };
}

// The derivation URL's path names the report key (prebuilt or saved-report
// id) the typed path param expects.
function derivationReportKey(url: string): string {
  return url.match(/reports\/([^/?]+)\/derivation/)?.[1] ?? "";
}

const FORECAST_CATEGORIES = [
  { key: "commit", labelKey: "deal.fcCommit" },
  { key: "best_case", labelKey: "deal.fcBestCase" },
  { key: "pipeline", labelKey: "deal.fcPipeline" },
  { key: "omitted", labelKey: "deal.fcOmitted" },
] as const;

// Prop-driven money tile for a forecast category — exported for the
// Storybook task so it can render without a live fetch (mirrors how
// FxLine in deals.tsx typed its `locale`).
export function ForecastTile({
  label,
  amountMinor,
  currency,
  locale,
}: Readonly<{
  label: string;
  amountMinor: number;
  currency: string;
  locale: Locale;
}>) {
  return (
    <Card>
      <span className="t-label">{label}</span>
      <p className="t-mono" style={{ fontSize: 22 }}>
        {formatMoney(amountMinor, currency, locale)}
      </p>
    </Card>
  );
}

export function ReportsScreen() {
  const t = useT();
  const { locale } = useLocale();
  const [explain, setExplain] = useState(false);
  const [report, setReport] = useState<ReportKey>("deals-by-stage");

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
    queryKey: ["report", report],
    queryFn: async () => {
      const { data, error } = await api.POST("/reports/{report}", {
        params: { path: { report } },
        body: {
          group_by: [REPORT_GROUP_BY[report]],
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

  // Hooks can't run inside the QueryGate render-prop callback (the run
  // result lives there), so the derivation handle is lifted to the top
  // level from the already-top-level run query.
  const derivationUrl = reportQuery.data?.derivation_url ?? null;
  const derivationQuery = useQuery({
    queryKey: ["derivation", derivationUrl],
    enabled: explain && derivationUrl != null,
    queryFn: async () => {
      const parsed = parseDerivationQuery(derivationUrl ?? "");
      const { data, error } = await api.GET("/reports/{report}/derivation", {
        params: {
          path: { report: derivationReportKey(derivationUrl ?? "") },
          query: { by: parsed.by, agg: parsed.agg },
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
      <SegmentedControl
        options={
          ["deals-by-stage", "forecast", "open-deals-per-company"] as const
        }
        value={report}
        onChange={setReport}
        labels={{
          "deals-by-stage": t("reports.reportDeals"),
          forecast: t("reports.reportForecast"),
          "open-deals-per-company": t("reports.reportOpenByCompany"),
        }}
      />
      <QueryGate query={reportQuery}>
        {(report_) => {
          let body: ReactNode;
          if (report === "forecast") {
            body = (
              <div>
                <p className="t-caption">{t("reports.forecastBanner")}</p>
                <div
                  style={{
                    display: "grid",
                    gridTemplateColumns: "repeat(auto-fit, minmax(160px, 1fr))",
                    gap: 12,
                    marginTop: 10,
                  }}
                >
                  {FORECAST_CATEGORIES.map((category) => {
                    const row = report_.rows.find(
                      (candidate) =>
                        candidate.forecast_category === category.key,
                    );
                    return (
                      <ForecastTile
                        key={category.key}
                        label={t(category.labelKey)}
                        amountMinor={Number(row?.raw_minor ?? 0)}
                        currency={
                          typeof row?.currency === "string"
                            ? row.currency
                            : "EUR"
                        }
                        locale={locale}
                      />
                    );
                  })}
                </div>
              </div>
            );
          } else if (report === "open-deals-per-company") {
            body = (
              <DataTable
                columns={[
                  {
                    key: "company",
                    header: t("reports.company"),
                    render: (row: (typeof report_.rows)[number]) =>
                      String(row.organization_id ?? ""),
                  },
                  {
                    key: "count",
                    header: t("reports.openDeals"),
                    render: (row: (typeof report_.rows)[number]) =>
                      String(row.deal_count ?? 0),
                  },
                  {
                    key: "raw",
                    header: t("reports.unweighted"),
                    render: (row: (typeof report_.rows)[number]) => (
                      <span className="t-mono">
                        {formatMoney(
                          Number(row.raw_minor ?? 0),
                          typeof row.currency === "string"
                            ? row.currency
                            : "EUR",
                          locale,
                        )}
                      </span>
                    ),
                  },
                ]}
                rows={report_.rows}
                rowKey={(row) =>
                  row.organization_id != null
                    ? String(row.organization_id)
                    : String(report_.rows.indexOf(row))
                }
              />
            );
          } else {
            const stages = pipelineQuery.data?.stages ?? [];
            const aggregates: StageAgg[] = report_.rows.map((row) => {
              const stageId = String(row.stage_id ?? "");
              const stage = stages.find(
                (candidate) => candidate.id === stageId,
              );
              return {
                stageId,
                stageName: stage?.name ?? stageId,
                probabilityPct: stage?.win_probability ?? 0,
                count: Number(row.deal_count ?? 0),
                rawMinor: Number(row.raw_minor ?? 0),
                currency:
                  typeof row.currency === "string" ? row.currency : "EUR",
              };
            });
            body = (
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
            );
          }
          return (
            <div>
              {body}
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
                    sub={
                      derivationQuery.data?.definition ?? t("reports.planNote")
                    }
                  />
                  {derivationUrl == null && (
                    <p className="t-caption" style={{ marginTop: 8 }}>
                      {t("common.empty")}
                    </p>
                  )}
                  {derivationUrl != null && derivationQuery.isPending && (
                    <div
                      style={{
                        display: "flex",
                        flexDirection: "column",
                        gap: 8,
                        marginTop: 8,
                      }}
                    >
                      <Skeleton width="60%" />
                      <Skeleton width="90%" />
                    </div>
                  )}
                  {derivationQuery.isError && (
                    <div style={{ marginTop: 8 }}>
                      <p className="t-caption">
                        {derivationQuery.error instanceof Error
                          ? derivationQuery.error.message
                          : t("common.error")}
                      </p>
                      <Button
                        small
                        onClick={() => derivationQuery.refetch()}
                        style={{ marginTop: 6 }}
                      >
                        {t("common.retry")}
                      </Button>
                    </div>
                  )}
                  {derivationQuery.data &&
                    (() => {
                      const derivation = derivationQuery.data;
                      return (
                        <div style={{ marginTop: 10 }}>
                          <SectionHeader title={t("explain.sources")} />
                          {derivation.rows.length === 0 ? (
                            <p className="t-caption">{t("common.empty")}</p>
                          ) : (
                            <DataTable
                              columns={derivation.columns.map((col) => ({
                                key: col,
                                header: col,
                                render: (row: Record<string, unknown>) =>
                                  String(row[col] ?? ""),
                              }))}
                              rows={derivation.rows}
                              rowKey={(row) =>
                                derivation.rows.indexOf(row).toString()
                              }
                            />
                          )}
                        </div>
                      );
                    })()}
                </section>
              )}
            </div>
          );
        }}
      </QueryGate>
    </div>
  );
}
