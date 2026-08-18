import { useQuery } from "@tanstack/react-query";
import { type ReactNode, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Button,
  Card,
  DataTable,
  SectionHeader,
  SegmentedControl,
  Skeleton,
} from "../design-system/atoms";
import { formatMoneyOrAbsent, MONEY_ABSENT } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import {
  OverlayUnavailable,
  problemMessageOf,
  QueryGate,
  throwProblem,
  useSorMode,
} from "./common";
import { QuotasView } from "./quotas";

// Reports (B-EP09.12c, D-11): a picker over three reports — deals-by-stage
// (unweighted next to weighted), forecast (category tiles, each showing
// unweighted and weighted, plus the server-derived "slipped" bucket), and
// open deals per company. "Explain this number" opens the executed plan +
// the exact rows the headline reconciles to. Both weighted figures come
// straight off the report's own weighted_amount_minor measure (AC-F1: round
// PER DEAL, then sum) — neither screen re-derives it from the raw total.

// One row of the deals-by-stage table: a stage AND a currency, because a
// stage holding deals in two currencies has two totals and no third one that
// means anything. The money measures are nullable for the same reason the
// currency is — a SUM over deals nobody priced is absent, not zero.
type StageAgg = {
  stageId: string;
  stageName: string;
  count: number;
  rawMinor: number | null;
  weightedMinor: number | null;
  currency: string | null;
};

// A report row arrives as `{ [key: string]: unknown }`, so every read narrows.
// These three keep the narrowing in one place, and keep the distinction the
// cells depend on: an absent measure is not a zero, and an absent currency is
// not EUR.
function rowCurrency(row: Record<string, unknown>): string | null {
  return typeof row.currency === "string" ? row.currency : null;
}

function rowMoney(row: Record<string, unknown>, key: string): number | null {
  const value = row[key];
  return value == null ? null : Number(value);
}

function rowCount(row: Record<string, unknown>, key: string): number {
  return Number(row[key] ?? 0);
}

// The grouped deals-by-stage rows as table rows, in pipeline order and then by
// currency code. The report answers in its own row order, which puts a stage's
// two currency rows anywhere relative to each other and the stages in no
// particular sequence — a table a reader scans down has to follow the board.
export function buildStageAggregates(
  rows: readonly Record<string, unknown>[],
  stages: readonly { id: string; name: string; position: number }[],
): StageAgg[] {
  const order = new Map(stages.map((stage) => [stage.id, stage.position]));
  const name = new Map(stages.map((stage) => [stage.id, stage.name]));
  return rows
    .map((row) => {
      const stageId = String(row.stage_id ?? "");
      return {
        stageId,
        stageName: name.get(stageId) ?? stageId,
        count: rowCount(row, "deal_count"),
        rawMinor: rowMoney(row, "raw_minor"),
        // AC-F1: the server's own per-deal-rounded weighted sum
        // (weighted_amount_minor), never round(rawMinor × p / 100)
        // — that rounds the column sum once instead of every deal.
        weightedMinor: rowMoney(row, "weighted_minor"),
        currency: rowCurrency(row),
      };
    })
    .sort((left, right) => {
      // A stage the pipeline no longer carries sorts last rather than first:
      // its rows are still real deals, but they are not part of the ladder the
      // reader is reading down.
      const byStage =
        (order.get(left.stageId) ?? Number.MAX_SAFE_INTEGER) -
        (order.get(right.stageId) ?? Number.MAX_SAFE_INTEGER);
      return byStage !== 0
        ? byStage
        : (left.currency ?? "").localeCompare(right.currency ?? "");
    });
}

type ReportKey = "deals-by-stage" | "forecast" | "open-deals-per-company";

// The Reports picker adds the human-set quotas surface alongside the three
// deal reports. Quotas runs its own query lifecycle (no /reports/{report}
// call), so the report machinery below is gated off while it is active.
type Segment = ReportKey | "quotas";

// The report engine's own name for the currency dimension. Spelled once: it
// reaches the request, the row reads and the column header, and a typo in any
// one of them is a cross-currency sum that looks right.
const fieldCurrency = "currency";

// Every plan here sums money, so every plan groups by currency as well as by
// its own dimension. amount_minor is a minor-unit integer in the deal's own
// currency: a total spanning currencies is a number with no unit, which
// data-semantics §1 r4 forbids and AC-DS-FX1 fails by construction. Grouping
// is the honest answer available today — converting to one base currency is
// the frozen-FX roll-up, a larger capability.
const REPORT_GROUP_BY: Record<ReportKey, string[]> = {
  "deals-by-stage": ["stage_id", fieldCurrency],
  forecast: ["forecast_category", fieldCurrency],
  "open-deals-per-company": ["organization_id", fieldCurrency],
};

type ReportAggregate = NonNullable<
  components["schemas"]["RunReportRequest"]["aggregates"]
>[number];

// Which aggregates each report's own vocabulary serves (report.go's
// per-spec measures) — `weighted_amount_minor` only exists where a stage
// join computes it (deals-by-stage, forecast); requesting it against
// open-deals-per-company's narrower vocabulary would 422.
const REPORT_AGGREGATES: Record<ReportKey, ReportAggregate[]> = {
  "deals-by-stage": [
    { fn: "sum", field: "amount_minor", as: "raw_minor" },
    { fn: "sum", field: "weighted_amount_minor", as: "weighted_minor" },
    { fn: "count", as: "deal_count" },
  ],
  forecast: [
    { fn: "sum", field: "amount_minor", as: "raw_minor" },
    { fn: "sum", field: "weighted_amount_minor", as: "weighted_minor" },
    { fn: "count", as: "deal_count" },
  ],
  "open-deals-per-company": [
    { fn: "sum", field: "amount_minor", as: "raw_minor" },
    { fn: "count", as: "deal_count" },
  ],
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

// forecast_category dimension values (report.go's forecastCategoryExpr):
// the four the deal itself can carry, plus the server-derived "slipped" —
// a claimed commit/best_case deal whose close date is past, missing, or
// still provisional (formulas §11). Omitting it here doesn't shrink the
// total; it moves the deal's amount into no tile at all.
const FORECAST_CATEGORIES = [
  { key: "commit", labelKey: "deal.fcCommit" },
  { key: "best_case", labelKey: "deal.fcBestCase" },
  { key: "pipeline", labelKey: "deal.fcPipeline" },
  { key: "omitted", labelKey: "deal.fcOmitted" },
  { key: "slipped", labelKey: "deal.fcSlipped" },
] as const;

// The group key a deal with NO forecast category arrives under. The wire allows
// the field to be null — nobody has said which way the deal is going — and the
// five named categories match none of it, so a tile set built from the enum alone
// drops those deals entirely: the money is not moved to another tile, it leaves
// the screen. The report is a forecast, so pipeline it silently omits is the one
// error it must not make.
const UNCATEGORISED = "";

// One currency's readings for one forecast category.
export type CategoryAmount = {
  currency: string | null;
  rawMinor: number | null;
  weightedMinor: number | null;
};

// Prop-driven money tile for a forecast category — exported for the
// Storybook task so it can render without a live fetch (mirrors how
// FxLine in deals.tsx typed its `locale`).
//
// It takes a LIST of readings, one per currency, because the report groups by
// currency: a category holding euro and dong deals has two totals and no third
// one that means anything, so the tile shows both rather than adding them. A
// category the report returned no row for shows no total — not a zero, because
// nothing was measured in any currency for it to be zero of.
export function ForecastTile({
  label,
  amounts,
  locale,
}: Readonly<{
  label: string;
  amounts: readonly CategoryAmount[];
  locale: Locale;
}>) {
  const t = useT();
  return (
    <Card>
      <span className="t-label">{label}</span>
      {amounts.length === 0 && (
        <p className="t-mono t-display">{MONEY_ABSENT}</p>
      )}
      {amounts.map((amount, index) => (
        // Each currency's raw figure and its weighted line are ONE reading, and
        // the air between readings is what says so. Stacked flush, a tile
        // holding two currencies reads as four numbers in a column rather than
        // as two answers — and the currency symbol alone is a thin thing to
        // hang that distinction on. Inline rather than a class because this
        // screen has no stylesheet of its own, and a class with no home is
        // styled only by whichever sheet a sibling screen happened to load.
        <div
          key={amount.currency ?? MONEY_ABSENT}
          style={index > 0 ? { marginTop: "var(--space-2)" } : undefined}
        >
          <p className="t-mono t-display">
            {formatMoneyOrAbsent(amount.rawMinor, amount.currency, locale)}
          </p>
          {amount.weightedMinor != null && (
            <p className="t-mono t-caption">
              {t("reports.weighted")}:{" "}
              {formatMoneyOrAbsent(
                amount.weightedMinor,
                amount.currency,
                locale,
              )}
            </p>
          )}
        </div>
      ))}
    </Card>
  );
}

// The grouped forecast rows, gathered into one entry per category. Currencies
// are ordered by code so a category's two readings do not swap places between
// runs on the server's row order.
export function groupForecastAmounts(
  rows: readonly Record<string, unknown>[],
): Map<string, CategoryAmount[]> {
  const byCategory = new Map<string, CategoryAmount[]>();
  for (const row of rows) {
    const category = String(row.forecast_category ?? "");
    const amounts = byCategory.get(category) ?? [];
    amounts.push({
      currency: rowCurrency(row),
      rawMinor: rowMoney(row, "raw_minor"),
      weightedMinor: rowMoney(row, "weighted_minor"),
    });
    byCategory.set(category, amounts);
  }
  for (const amounts of byCategory.values()) {
    amounts.sort((left, right) =>
      (left.currency ?? "").localeCompare(right.currency ?? ""),
    );
  }
  return byCategory;
}

export function ReportsScreen() {
  const t = useT();
  const { locale } = useLocale();
  const [explain, setExplain] = useState(false);
  const [segment, setSegment] = useState<Segment>("deals-by-stage");
  // The report machinery needs a valid ReportKey; while "quotas" is active the
  // report/pipeline queries are disabled, so this fallback key is inert.
  const report: ReportKey = segment === "quotas" ? "deals-by-stage" : segment;
  // Deal reports aggregate over the pipeline/stage structure the overlay mirror
  // does not hold (the report endpoints answer 422 unsupported_by_sor in
  // overlay), so the report segments show the honest unavailable state; the
  // native quotas tab still works.
  const overlay = useSorMode() === "overlay";
  const reportActive = segment !== "quotas" && !overlay;

  const pipelineQuery = useQuery({
    queryKey: ["pipelines"],
    enabled: reportActive,
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data.find((pipeline) => pipeline.is_default) ?? data.data[0];
    },
  });

  const reportQuery = useQuery({
    queryKey: ["report", report],
    enabled: reportActive,
    queryFn: async () => {
      const { data, error } = await api.POST("/reports/{report}", {
        params: { path: { report } },
        body: {
          group_by: REPORT_GROUP_BY[report],
          aggregates: REPORT_AGGREGATES[report],
        },
      });
      if (error) {
        throwProblem(error);
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
    enabled: reportActive && explain && derivationUrl != null,
    queryFn: async () => {
      // parsed carries by/agg PLUS every equality predicate from the handle
      // (group-key values + plan filters). The endpoint treats each extra key
      // as a predicate, so forward the whole object — dropping the predicates
      // would explain the wrong slice (or 422 on a bound grouping dimension).
      const parsed = parseDerivationQuery(derivationUrl ?? "");
      const { data, error } = await api.GET("/reports/{report}/derivation", {
        params: {
          path: { report: derivationReportKey(derivationUrl ?? "") },
          query: parsed,
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  // Header + segment picker are shared by both the report bodies and the
  // quotas surface; factored so the report body below stays at one depth
  // (quotas takes an early return rather than nesting the whole report tree).
  const header = (
    <>
      <SectionHeader
        title={t("nav.reports")}
        sub={segment === "quotas" ? t("quotas.sub") : t("reports.sub")}
      />
      <div className="filter-tabs">
        <SegmentedControl
          options={
            [
              "deals-by-stage",
              "forecast",
              "open-deals-per-company",
              "quotas",
            ] as const
          }
          value={segment}
          onChange={setSegment}
          labels={{
            "deals-by-stage": t("reports.reportDeals"),
            forecast: t("reports.reportForecast"),
            "open-deals-per-company": t("reports.reportOpenByCompany"),
            quotas: t("quotas.tab"),
          }}
        />
      </div>
    </>
  );

  if (segment === "quotas") {
    return (
      <div className="wrap">
        {header}
        <QuotasView />
      </div>
    );
  }

  if (overlay) {
    return (
      <div className="wrap">
        {header}
        <OverlayUnavailable />
      </div>
    );
  }

  return (
    <div className="wrap">
      {header}
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
                    gap: "var(--space-3)",
                    marginTop: "var(--space-3)",
                  }}
                >
                  {(() => {
                    const byCategory = groupForecastAmounts(report_.rows);
                    const uncategorised = byCategory.get(UNCATEGORISED) ?? [];
                    return [
                      ...FORECAST_CATEGORIES.map((category) => (
                        <ForecastTile
                          key={category.key}
                          label={t(category.labelKey)}
                          amounts={byCategory.get(category.key) ?? []}
                          locale={locale}
                        />
                      )),
                      // Only when such deals exist: an installation whose deals
                      // are all categorised should not be shown an empty sixth
                      // tile asking about a state it never reaches.
                      ...(uncategorised.length > 0
                        ? [
                            <ForecastTile
                              key={UNCATEGORISED}
                              label={t("deal.fcUncategorised")}
                              amounts={uncategorised}
                              locale={locale}
                            />,
                          ]
                        : []),
                    ];
                  })()}
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
                    key: fieldCurrency,
                    header: t("reports.currency"),
                    render: (row: (typeof report_.rows)[number]) => (
                      <span className="t-mono">
                        {rowCurrency(row) ?? MONEY_ABSENT}
                      </span>
                    ),
                  },
                  {
                    key: "count",
                    header: t("reports.openDeals"),
                    render: (row: (typeof report_.rows)[number]) =>
                      String(rowCount(row, "deal_count")),
                  },
                  {
                    key: "raw",
                    header: t("reports.unweighted"),
                    render: (row: (typeof report_.rows)[number]) => (
                      <span className="t-mono">
                        {formatMoneyOrAbsent(
                          rowMoney(row, "raw_minor"),
                          rowCurrency(row),
                          locale,
                        )}
                      </span>
                    ),
                  },
                ]}
                rows={report_.rows}
                // A company with deals in two currencies is two rows now, so
                // the organization id alone no longer identifies one.
                rowKey={(row) =>
                  row.organization_id != null
                    ? `${String(row.organization_id)}:${rowCurrency(row) ?? ""}`
                    : String(report_.rows.indexOf(row))
                }
              />
            );
          } else {
            const stages = pipelineQuery.data?.stages ?? [];
            const aggregates = buildStageAggregates(report_.rows, stages);
            body = (
              <DataTable
                columns={[
                  {
                    key: "stage",
                    header: t("deals.stage"),
                    render: (row: StageAgg) => row.stageName,
                  },
                  {
                    key: fieldCurrency,
                    header: t("reports.currency"),
                    render: (row: StageAgg) => (
                      <span className="t-mono">
                        {row.currency ?? MONEY_ABSENT}
                      </span>
                    ),
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
                        {formatMoneyOrAbsent(
                          row.rawMinor,
                          row.currency,
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
                        {formatMoneyOrAbsent(
                          row.weightedMinor,
                          row.currency,
                          locale,
                        )}
                      </span>
                    ),
                  },
                ]}
                rows={aggregates}
                // A stage holding deals in two currencies is two rows, so the
                // stage id alone no longer identifies one.
                rowKey={(row) => `${row.stageId}:${row.currency ?? ""}`}
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
                <Card
                  style={{ marginTop: "var(--space-3)" }}
                  ariaLabel={t("explain.title")}
                  title={t("explain.title")}
                  sub={
                    derivationQuery.data?.definition ?? t("reports.planNote")
                  }
                >
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
                        {problemMessageOf(derivationQuery.error, t)}
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
                </Card>
              )}
            </div>
          );
        }}
      </QueryGate>
    </div>
  );
}
