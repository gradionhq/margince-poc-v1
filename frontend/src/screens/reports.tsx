import { type UseQueryResult, useQuery } from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Button,
  Card,
  DataTable,
  SectionHeader,
  SegmentedControl,
  Skeleton,
  StatCard,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { Eyebrow } from "../design-system/eyebrow";
import { StatStrip } from "../design-system/statstrip";
import { formatMoneyOrAbsent, MONEY_ABSENT } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  OverlayUnavailable,
  problemMessageOf,
  QueryGate,
  throwProblem,
  useSorMode,
} from "./common";
import { EntityRef } from "./entityref";
import { QuotasView } from "./quotas";

// Reports (B-EP09.12c, D-11): a picker over three reports — deals-by-stage
// (unweighted next to weighted), forecast (category readings, each showing
// unweighted and weighted, plus the server-derived "slipped" bucket), and
// open deals per company. "Explain this number" opens the executed plan +
// the exact rows the headline reconciles to. Both weighted figures come
// straight off the report's own weighted_amount_minor measure (AC-F1: round
// PER DEAL, then sum) — neither screen re-derives it from the raw total.
//
// All three report bodies render into ONE surface: a titled Card whose trailing
// .card-actions row carries the explain toggle. The segment picked changes what
// the card holds, never what kind of thing the page is.

// One row of the deals-by-stage table: a stage AND a currency, because a stage
// holding deals in two currencies has two totals and no third one that means
// anything. The money measures are nullable for the same reason the currency is
// — a SUM over deals nobody priced is absent, not zero.
type StageAgg = {
  stageId: string;
  stageName: string;
  stagePosition: number;
  count: number;
  rawMinor: number | null;
  weightedMinor: number | null;
  currency: string | null;
};

type ReportKey = "deals-by-stage" | "forecast" | "open-deals-per-company";

// The Reports picker adds the human-set quotas surface alongside the three
// deal reports. Quotas runs its own query lifecycle (no /reports/{report}
// call), so the report machinery below is gated off while it is active.
type Segment = ReportKey | "quotas";

type ReportRow = components["schemas"]["ReportResult"]["rows"][number];
type Derivation = components["schemas"]["ReportDerivation"];
type Stage = components["schemas"]["Stage"];

// The report engine's own name for the currency dimension. Spelled once: it
// reaches the request, every row read and the column header, and a typo in any
// one of them is a cross-currency sum that looks right.
const FIELD_CURRENCY = "currency";

// The group key a row with NO forecast category arrives under. The wire allows
// the field to be null — nobody has said which way the deal is going — and the
// five named categories match none of it.
const UNCATEGORISED = "";

// Every plan here sums money, so every plan groups by currency as well as by its
// own dimension. amount_minor is a minor-unit integer in the deal's own
// currency, so a total spanning currencies is a number with no unit — the sum
// data-semantics §1 r4 forbids and AC-DS-FX1 fails by construction. Grouping is
// the honest answer available today; converting to one base currency is the
// frozen-FX roll-up, a larger capability.
const REPORT_GROUP_BY: Record<ReportKey, string[]> = {
  "deals-by-stage": ["stage_id", FIELD_CURRENCY],
  forecast: ["forecast_category", FIELD_CURRENCY],
  "open-deals-per-company": ["organization_id", FIELD_CURRENCY],
};

// A report row arrives as `{ [key: string]: unknown }`, so every read narrows.
// These keep the narrowing in one place, and keep the distinction the cells
// depend on: an absent measure is not a zero, and an absent currency is not EUR.
function rowCurrency(row: ReportRow): string | null {
  // An empty code is not a currency. Left as "" it renders a blank cell where a
  // code belongs, and it groups apart from null while meaning the same thing —
  // which would give the forecast two bands with the same key.
  return typeof row.currency === "string" && row.currency !== ""
    ? row.currency
    : null;
}

function rowMoney(row: ReportRow, key: string): number | null {
  const value = row[key];
  return value == null ? null : Number(value);
}

function rowCount(row: ReportRow, key: string): number {
  return Number(row[key] ?? 0);
}

// Rows gathered by the currency they are denominated in, in code order so two
// bands do not swap places between runs on the server's row order.
function byCurrency(
  rows: readonly ReportRow[],
): [string | null, ReportRow[]][] {
  const bands = new Map<string | null, ReportRow[]>();
  for (const row of rows) {
    const currency = rowCurrency(row);
    bands.set(currency, [...(bands.get(currency) ?? []), row]);
  }
  return [...bands.entries()].sort(([left], [right]) =>
    (left ?? "").localeCompare(right ?? ""),
  );
}

// A report's own name, spelled once: the segment picker and the heading of the
// card that segment opens read the same key, so the tab and the surface behind
// it cannot drift into two names for one report.
const REPORT_LABEL_KEY = {
  "deals-by-stage": "reports.reportDeals",
  forecast: "reports.reportForecast",
  "open-deals-per-company": "reports.reportOpenByCompany",
} as const satisfies Record<ReportKey, string>;

// The line under the segment picker, for the two segments whose copy says
// something the card's own title does not. A segment absent from here gets no
// caption: an explanation of the report beside it is worse than none.
const segmentSub: Partial<Record<Segment, MessageKey>> = {
  "deals-by-stage": "reports.sub",
  quotas: "quotas.sub",
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

// One forecast category as one slot of the strip: the raw total is the reading
// and the probability-weighted total is the basis it was drawn from, which is
// exactly what StatCard's label/value/detail carry. Exported for the Storybook
// task so it renders without a live fetch (mirrors how FxLine in deals.tsx
// typed its `locale`). `weightedMinor` is optional so the slot still renders
// (raw only) for a caller with no weighted figure to hand.
export function ForecastTile({
  label,
  amountMinor,
  weightedMinor,
  currency,
  locale,
}: Readonly<{
  label: string;
  // Both halves are nullable and neither absence has a substitute. A category
  // the report returned no row for has no total in any currency — not a zero,
  // because nothing was measured for it to be zero of — and a figure with no
  // currency cannot be rendered as money at all.
  amountMinor: number | null;
  weightedMinor?: number | null;
  currency: string | null;
  locale: Locale;
}>) {
  const t = useT();
  return (
    <StatCard
      label={label}
      numeric
      value={formatMoneyOrAbsent(amountMinor, currency, locale)}
      detail={
        weightedMinor == null
          ? undefined
          : `${t("reports.weighted")}: ${formatMoneyOrAbsent(weightedMinor, currency, locale)}`
      }
    />
  );
}

// Five money figures read across as one comparison, so they are ONE plate of
// ruled slots rather than five free-standing cards. The banner above them is
// what the surface says about itself — how to read the second figure in every
// slot — which is a Callout, not a paragraph tinted by hand.
// The wire allows a deal to carry no forecast category, and the five named ones
// match none of it — so a slot set built from the enum alone drops those deals
// off the screen entirely: the money is not moved to another slot, it leaves. On
// the demo dataset that was 22 of 27 open deals. The slot appears only where such
// deals exist, so an installation that categorises everything is not asked about
// a state it never reaches.
function uncategorisedSlot(
  band: readonly ReportRow[],
): { key: string; labelKey: MessageKey }[] {
  return band.some((row) => row.forecast_category == null)
    ? [{ key: UNCATEGORISED, labelKey: "deal.fcUncategorised" }]
    : [];
}

function ForecastStrip({
  rows,
  locale,
}: Readonly<{ rows: ReportRow[]; locale: Locale }>) {
  const t = useT();
  return (
    <>
      <Callout tone="info">{t("reports.forecastBanner")}</Callout>
      {/* ONE STRIP PER CURRENCY. A slot carries a single figure, so a category
          holding euros and dong cannot state one — and adding them would be the
          unit-less total data-semantics §1 r4 forbids. Banding by currency keeps
          the five categories reading across as one comparison, which is what the
          strip is for, and makes every figure in a band mean the same thing.

          The band is labelled with the currency code itself rather than a
          translated name: it is what the figures beneath it are denominated in,
          and it is already the word every one of them carries. */}
      {/* No rows at all is not "nothing to draw": it means no open deal reached
          any category, and five slots each saying so is the honest report. A
          blank area under the banner would read as a screen that failed to
          load. The band carries no currency because there are no figures for one
          to belong to. */}
      {(byCurrency(rows).length > 0
        ? byCurrency(rows)
        : ([[null, []]] as [string | null, ReportRow[]][])
      ).map(([currency, band]) => (
        <div
          key={currency ?? UNCATEGORISED}
          style={{ marginTop: "var(--space-4)" }}
        >
          <Eyebrow as="h3">{currency ?? MONEY_ABSENT}</Eyebrow>
          <StatStrip>
            {[...FORECAST_CATEGORIES, ...uncategorisedSlot(band)].map(
              (category) => {
                const row = band.find(
                  (candidate) =>
                    String(candidate.forecast_category ?? UNCATEGORISED) ===
                    category.key,
                );
                return (
                  <ForecastTile
                    key={category.key}
                    label={t(category.labelKey)}
                    amountMinor={row ? rowMoney(row, "raw_minor") : null}
                    weightedMinor={row ? rowMoney(row, "weighted_minor") : null}
                    currency={currency}
                    locale={locale}
                  />
                );
              },
            )}
          </StatStrip>
        </div>
      ))}
    </>
  );
}

function CompanyTable({
  rows,
  locale,
}: Readonly<{ rows: ReportRow[]; locale: Locale }>) {
  const t = useT();
  return (
    <DataTable
      label={t("reports.reportOpenByCompany")}
      columns={[
        {
          key: "company",
          header: t("reports.company"),
          // The report answers with an organization id and nothing else, so the
          // column read `01a0131c-3154-74cb-…` for every row — a company report
          // nobody could read. One record lookup per row, cached by id for a
          // minute and shared with every other reference on screen. The cost is
          // per row and this table is one report page long; the alternative is a
          // table of uuids, which is not a cheaper report but an unusable one.
          render: (row: ReportRow) =>
            typeof row.organization_id === "string" ? (
              <EntityRef kind="organization" id={row.organization_id} />
            ) : (
              ""
            ),
        },
        {
          key: FIELD_CURRENCY,
          header: t("reports.currency"),
          render: (row: ReportRow) => (
            <span className="t-mono">{rowCurrency(row) ?? MONEY_ABSENT}</span>
          ),
        },
        {
          key: "count",
          header: t("reports.openDeals"),
          render: (row: ReportRow) => String(rowCount(row, "deal_count")),
        },
        {
          key: "raw",
          header: t("reports.unweighted"),
          render: (row: ReportRow) => (
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
      rows={rows}
      // A company with deals in two currencies is two rows now, so the
      // organization id alone no longer identifies one.
      rowKey={(row) =>
        row.organization_id != null
          ? `${String(row.organization_id)}:${rowCurrency(row) ?? ""}`
          : String(rows.indexOf(row))
      }
    />
  );
}

// The grouped rows as table rows, in pipeline order and then by currency code.
// The report answers in its own row order, which puts a stage's two currency rows
// anywhere relative to each other — a table a reader scans down has to follow the
// board.
export function buildStageAggregates(
  rows: readonly ReportRow[],
  stages: readonly Stage[],
): StageAgg[] {
  const byId = new Map(stages.map((stage) => [stage.id, stage]));
  return rows
    .map((row) => {
      const stageId = String(row.stage_id ?? "");
      const stage = byId.get(stageId);
      return {
        stageId,
        stageName: stage?.name ?? stageId,
        // A stage the pipeline no longer carries sorts last rather than first:
        // its rows are still real deals, but they are not part of the ladder the
        // reader is reading down.
        stagePosition: stage?.position ?? Number.MAX_SAFE_INTEGER,
        count: rowCount(row, "deal_count"),
        rawMinor: rowMoney(row, "raw_minor"),
        // AC-F1: the server's own per-deal-rounded weighted sum
        // (weighted_amount_minor), never round(rawMinor × p / 100)
        // — that rounds the column sum once instead of every deal.
        weightedMinor: rowMoney(row, "weighted_minor"),
        currency: rowCurrency(row),
      };
    })
    .sort(
      (left, right) =>
        left.stagePosition - right.stagePosition ||
        (left.currency ?? "").localeCompare(right.currency ?? ""),
    );
}

function StageTable({
  rows,
  stages,
  locale,
}: Readonly<{ rows: ReportRow[]; stages: Stage[]; locale: Locale }>) {
  const t = useT();
  const aggregates = buildStageAggregates(rows, stages);
  return (
    <DataTable
      label={t("reports.reportDeals")}
      columns={[
        {
          key: "stage",
          header: t("deals.stage"),
          render: (row: StageAgg) => row.stageName,
        },
        {
          key: FIELD_CURRENCY,
          header: t("reports.currency"),
          render: (row: StageAgg) => (
            <span className="t-mono">{row.currency ?? MONEY_ABSENT}</span>
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
              {formatMoneyOrAbsent(row.rawMinor, row.currency, locale)}
            </span>
          ),
        },
        {
          key: "weighted",
          header: t("reports.weighted"),
          render: (row: StageAgg) => (
            <span className="t-mono">
              {formatMoneyOrAbsent(row.weightedMinor, row.currency, locale)}
            </span>
          ),
        },
      ]}
      rows={aggregates}
      // A stage holding deals in two currencies is two rows, so the stage id
      // alone no longer identifies one.
      rowKey={(row) => `${row.stageId}:${row.currency ?? ""}`}
    />
  );
}

// The source rows the explained figure reconciles to. A section INSIDE the
// explain card's own section, so its heading steps down with the outline
// rather than reading as a peer of the card's title.
function DerivationRows({ derivation }: Readonly<{ derivation: Derivation }>) {
  const t = useT();
  return (
    <>
      <SectionHeader title={t("explain.sources")} level={3} />
      {derivation.rows.length === 0 ? (
        <p className="t-caption">{t("common.empty")}</p>
      ) : (
        <DataTable
          label={t("explain.sources")}
          columns={derivation.columns.map((col) => ({
            key: col,
            header: col,
            render: (row: Record<string, unknown>) => String(row[col] ?? ""),
          }))}
          rows={derivation.rows}
          rowKey={(row) => derivation.rows.indexOf(row).toString()}
        />
      )}
    </>
  );
}

// "Explain this number", as the same titled Card the quotas surface draws for
// the same feature — one route, one feature, one surface.
function ExplainCard({
  id,
  url,
  query,
}: Readonly<{
  // The toggle above points `aria-controls` here, so the card has to carry the
  // id the toggle was given rather than mint one of its own.
  id: string;
  url: string | null;
  query: UseQueryResult<Derivation>;
}>) {
  const t = useT();
  return (
    <Card
      id={id}
      ariaLabel={t("explain.title")}
      title={t("explain.title")}
      sub={query.data?.definition ?? t("reports.planNote")}
    >
      {url == null && <p className="t-caption">{t("common.empty")}</p>}
      {url != null && query.isPending && (
        <div
          style={{
            display: "flex",
            flexDirection: "column",
            gap: "var(--space-2)",
          }}
        >
          <Skeleton width="60%" />
          <Skeleton width="90%" />
        </div>
      )}
      {query.isError && (
        <>
          <p className="t-caption">{problemMessageOf(query.error, t)}</p>
          <div className="card-actions">
            <Button small onClick={() => query.refetch()}>
              {t("common.retry")}
            </Button>
          </div>
        </>
      )}
      {query.data && <DerivationRows derivation={query.data} />}
    </Card>
  );
}

export function ReportsScreen() {
  const t = useT();
  const { locale } = useLocale();
  const [explain, setExplain] = useState(false);
  const explainId = useId();
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
          "deals-by-stage": t(REPORT_LABEL_KEY["deals-by-stage"]),
          forecast: t(REPORT_LABEL_KEY.forecast),
          "open-deals-per-company": t(
            REPORT_LABEL_KEY["open-deals-per-company"],
          ),
          quotas: t("quotas.tab"),
        }}
      />
      {/* Only where the copy is true of the SELECTED segment. `reports.sub`
          explains deals-by-stage's two money columns; printed over the forecast
          or the per-company table it described a report the reader was not
          looking at. The others are named by their card's own title. */}
      {segmentSub[segment] && (
        <span className="sub">{t(segmentSub[segment])}</span>
      )}
    </div>
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
        {(run) => (
          <>
            <Card title={t(REPORT_LABEL_KEY[report])}>
              {report === "forecast" && (
                <ForecastStrip rows={run.rows} locale={locale} />
              )}
              {report === "open-deals-per-company" && (
                <CompanyTable rows={run.rows} locale={locale} />
              )}
              {report === "deals-by-stage" && (
                <StageTable
                  rows={run.rows}
                  stages={pipelineQuery.data?.stages ?? []}
                  locale={locale}
                />
              )}
              <div className="card-actions">
                {/* A toggle, and it says so: the button reveals and hides the
                    card below, so it announces the open state and names what
                    it controls — the same pair the quotas surface draws for
                    the same feature. */}
                <Button
                  small
                  aria-expanded={explain}
                  aria-controls={explainId}
                  onClick={() => setExplain((value) => !value)}
                >
                  {t("explain.open")}
                </Button>
              </div>
            </Card>
            {explain && (
              <ExplainCard
                id={explainId}
                url={derivationUrl}
                query={derivationQuery}
              />
            )}
          </>
        )}
      </QueryGate>
    </div>
  );
}
