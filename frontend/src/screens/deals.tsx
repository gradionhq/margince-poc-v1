import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  type Dispatch,
  type DragEvent,
  type ReactNode,
  type SetStateAction,
  useId,
  useMemo,
  useRef,
  useState,
} from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch, requireVersion } from "../api/version";
import { approvalDotTier, useAgentTierMap, verbTier } from "../app/autonomy";
import { navigate } from "../app/router";
import { useInstallationSettings } from "../app/uploadlimit";
import { activityTimeline } from "../design-system/activitytimeline";
import {
  Badge,
  Button,
  Card,
  DataTable,
  EmptyState,
  Modal,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import {
  type BoardColumn,
  type BoardDeal,
  type BoardMoneyColumn,
  PipelineBoard,
  RecordView,
} from "../design-system/composed";
import { type ListChip, ListSurface } from "../design-system/listsurface";
import type { ListColumn } from "../design-system/listtable";
import { FieldGuard } from "../design-system/rbac";
import { Select } from "../design-system/select";
import { type Toast, ToastRegion, useToast } from "../design-system/toast";
import { AutonomyDot, ProvenanceTag } from "../design-system/trust";
import {
  formatDate,
  formatDuration,
  formatMoney,
  formatMoneyOrAbsent,
} from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { approvalKindLabel } from "./approvalkind";
import { ArchiveAction } from "./archive";
import {
  LoadMoreButton,
  OverlayUnavailable,
  problemMessageOf,
  provenanceOf,
  QueryGate,
  QueryStates,
  throwProblem,
  timelineZoneNotice,
  useMe,
  useSorMode,
  useViewerId,
} from "./common";
import { TimelineActions } from "./compose";
import { RecordContextPanel } from "./context";
import type { CreateField } from "./create";
import { CreateAction } from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { DealBulkBar } from "./dealbulk";
import { EditAction } from "./edit";
import { RecordHistoryTab } from "./history";
import { usePendingApprovals } from "./inbox.queries";
import {
  LIST_PAGE_SIZES,
  type ListQuery,
  type ListState,
  ListTable,
} from "./listquery";
import { LogActivity } from "./logactivity";
import { DealCoverageCard } from "./network";
import { SaveViewAction, useSavedViewTabs } from "./savedviews";
import { ShareAction } from "./share";

// Deal surfaces (B-EP09.11a/b/c): the five-stage Kanban with drag-to-advance
// (terminal stages are a 🟡 confirm, AC-deal-6), the board↔table segmented
// control over the SAME fetched set (no reload), and the deal 360 with the
// stage stepper and the live pending-approval staged cards. Weighting math
// stays out of the UI beyond same-currency page-local sub-lines: a mixed-
// currency column renders no sum (the FX rule: never sum native minors
// across currencies).

type Deal = components["schemas"]["Deal"];
type Stage = components["schemas"]["Stage"];
type Pipeline = components["schemas"]["Pipeline"];
type Offer = components["schemas"]["Offer"];

/**
 * The pipeline a record belongs to, falling back to the default.
 *
 * `pipelineId` is the deal's own. Without it this answered the DEFAULT
 * pipeline for every deal, which was harmless while the stepper only drew
 * stage names but is not once those stages are the moves on offer: the stages
 * of a pipeline the deal is not in are targets the server refuses as a
 * pipeline mismatch.
 */
function usePipeline(pipelineId?: string | null) {
  return useQuery({
    queryKey: ["pipelines", pipelineId ?? "default"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throwProblem(error);
      }
      const pipeline =
        (pipelineId &&
          data.data.find((candidate) => candidate.id === pipelineId)) ||
        data.data.find((candidate) => candidate.is_default) ||
        data.data[0];
      if (!pipeline) {
        throw new Error("no pipeline");
      }
      return pipeline;
    },
  });
}

// The plural read over ALL pipelines (D-9's selector) — a DISTINCT cache key
// from usePipeline's ["pipelines"] (which DealScreen still reads as a single
// Pipeline). Sharing the key would let the cache hold either shape depending
// on which screen loaded last; ["pipelines","all"] still gets refreshed by
// any mutation that invalidates the ["pipelines"] prefix (react-query prefix
// matching), so freshness is preserved without a shape collision.
// enabled is false in overlay mode: the overlay deals view renders no
// pipeline board or picker (a stage-less mirror has no pipelines to show),
// so it never needs this fetch.
function usePipelines(enabled: boolean) {
  return useQuery({
    queryKey: ["pipelines", "all"],
    enabled,
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
  });
}

type DealFilters = {
  pipelineId: string;
  sort: string;
  includeArchived: boolean;
  filters: Record<string, string>;
  // Overlay mode reads a mirror that refuses every dial below (sort, and the
  // pipeline/stage/owner/org filters) with a 422 — so in overlay we send none
  // of them and let the deals list come back flat. The screen forces the table
  // view and hides the pickers to match (a stage-keyed board cannot place a
  // mirror deal, whose pipeline/stage is null in overlay, OVA-MAP-6).
  overlay: boolean;
};

// dealsQueryParams builds the native board's /deals query — the full dial
// set (pipeline/stage/owner/org filters + sort). It is never called in
// overlay mode (useDeals is disabled there and OverlayDealsTable sends its
// own overlay-shaped params), so it carries no overlay branch.
function dealsQueryParams(f: DealFilters) {
  const { filters } = f;
  return {
    limit: 100,
    include_archived: f.includeArchived || undefined,
    pipeline_id: f.pipelineId || undefined,
    sort: f.sort || undefined,
    stage_id: filters.stage_id || undefined,
    owner_id: filters.owner_id || undefined,
    organization_id: filters.organization_id || undefined,
    stalled: filters.stalled === "true" ? true : undefined,
    partner_sourced: filters.partner_sourced === "true" ? true : undefined,
  };
}

// The board is not paginated — limit:100 is an honest documented cap (a
// live Kanban reads one screenful, not a keyset walk). Disabled in overlay
// mode: there the flat mirror table paginates through OverlayDealsTable
// (its own keyset walk), so this single-page native query does not fetch.
/**
 * The deals the board and the table share.
 *
 * A keyset walk rather than one fixed page. The board draws a column per
 * stage out of whatever this holds, so a single capped read meant a busy
 * stage quietly showed a fraction of its cards while its header — which
 * comes from the deals-by-stage report over EVERY matching deal — went on
 * naming the true count. A column saying "40 deals" above six cards is the
 * one thing a pipeline view must not do.
 */
function useDeals(f: DealFilters) {
  return useInfiniteQuery({
    queryKey: ["deals", f],
    enabled: !f.overlay,
    // `as` steers useInfiniteQuery's TPageParam generic to the cursor type,
    // exactly as OverlayDealsTable does and for the same reason: a bare
    // `undefined` infers TPageParam=undefined and the string cursor no longer
    // type-checks.
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/deals", {
        params: { query: { ...dealsQueryParams(f), cursor: pageParam } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    getNextPageParam: (last) =>
      last.page?.has_more ? (last.page.next_cursor ?? undefined) : undefined,
  });
}

// dealsByStageReportFilters translates the board's own filter dials into
// the deals-by-stage report's filter shape — the SAME dials dealsQueryParams
// sends to /deals, so a card's stage total and the cards shown for it never
// disagree about which deals are in scope. stage_id is deliberately absent:
// the report is grouped BY stage_id, which already answers "per stage".
function dealsByStageReportFilters(f: DealFilters): Record<string, unknown> {
  const { filters } = f;
  const out: Record<string, unknown> = {};
  if (f.pipelineId) out.pipeline_id = f.pipelineId;
  if (filters.owner_id) out.owner_id = filters.owner_id;
  if (filters.organization_id) out.organization_id = filters.organization_id;
  if (filters.stalled === "true") out.stalled = true;
  if (filters.partner_sourced === "true") out.partner_sourced = true;
  return out;
}

// useStageTotals reads the board's per-column totals from the
// deals-by-stage report — a full aggregate over every matching deal, not
// just the capped page useDeals fetches. Grouped by
// [stage_id, currency] so a mixed-currency stage arrives as more than one
// row, which buildStageTotals reads as "hide the sum" — the report never
// includes archived deals (like every report), so the totals reflect the
// live pipeline regardless of the board's "show archived" toggle.
function useStageTotals(f: DealFilters) {
  return useQuery({
    // Under ["deals"] on purpose, so the ONE invalidation every deal mutation
    // already fires refreshes the column headers along with the cards. Keyed
    // apart from them, a moved card sat under a header still counting it in
    // the stage it left — and a foreign-currency deal arriving in a
    // single-currency stage left the old sum standing, which is the mixed-
    // currency refusal not happening.
    queryKey: ["deals", "by-stage-totals", f],
    enabled: !f.overlay,
    queryFn: async () => {
      const { data, error } = await api.POST("/reports/{report}", {
        params: { path: { report: "deals-by-stage" } },
        body: {
          group_by: ["stage_id", "currency"],
          aggregates: [
            { fn: "count", as: "deals" },
            { fn: "sum", field: "amount_minor", as: "raw_minor" },
            {
              fn: "sum",
              field: "weighted_amount_minor",
              as: "weighted_minor",
            },
          ],
          filters: dealsByStageReportFilters(f),
        },
      });
      if (error) {
        throwProblem(error);
      }
      return buildStageTotals(data.rows);
    },
  });
}

// OverlayDealsTable is the overlay-mode deals view: a flat mirror table
// (a stage-keyed board cannot place a mirror deal, whose pipeline/stage is
// null — OVA-MAP-6) that walks the keyset cursor the API returns
// (page.next_cursor / page.has_more) with a Load-more affordance, rather
// than the native board's honest one-screenful cap. Overlay reads 422 every
// sort/filter dial, so it sends only limit + include_archived + cursor.
function OverlayDealsTable({
  includeArchived,
}: Readonly<{ includeArchived: boolean }>) {
  const query = useInfiniteQuery({
    queryKey: ["deals", "overlay", includeArchived],
    // `as` steers useInfiniteQuery's TPageParam generic to the cursor type:
    // a bare `undefined` infers TPageParam=undefined, which then rejects the
    // string cursor getNextPageParam returns (the whole query's data type
    // collapses to unknown). A typed local does not carry through the
    // generic inference — so the assertion is load-bearing here, not
    // cosmetic. biome (the frontend gate) does not flag it.
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/deals", {
        params: {
          query: {
            limit: 100,
            include_archived: includeArchived || undefined,
            cursor: pageParam,
          },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    getNextPageParam: (last) =>
      last.page?.has_more ? (last.page.next_cursor ?? undefined) : undefined,
  });
  const t = useT();
  // Once ANY page has loaded, render the table — a later Load-more failure
  // must NOT discard the rows already fetched (routing the whole thing
  // through QueryGate would show the full error state on any page error,
  // throwing away usable results). Only the INITIAL load goes through
  // QueryStates' pending/error; a failed next page leaves the table up and
  // re-enables the Load-more button to retry.
  const pages = query.data?.pages ?? [];
  if (pages.length === 0) {
    return <QueryStates query={query}>{null}</QueryStates>;
  }
  const deals = pages.flatMap((p) => p.data);
  if (deals.length === 0) {
    return <EmptyState>{t("common.empty")}</EmptyState>;
  }
  return (
    <>
      <DealTable deals={deals} stages={[]} sortable={false} />
      <LoadMoreButton query={query} />
    </>
  );
}

/** id → the company's display name and resolved mark, for the deals whose
 *  organization is among the ones this screen has read. */
type OrgMarks = ReadonlyMap<string, { name: string; logoUrl?: string | null }>;

function toBoardDeal(deal: Deal, orgs?: OrgMarks): BoardDeal {
  const since = deal.last_activity_at ?? deal.created_at;
  const org = deal.organization_id
    ? orgs?.get(deal.organization_id)
    : undefined;
  return {
    id: deal.id,
    name: deal.name,
    // Empty when the company is not among the organizations read here; the card
    // then shows the deal alone rather than a blank chip beside it.
    org: org?.name ?? "",
    orgLogoUrl: org?.logoUrl,
    // Both halves as the wire sent them. Nobody has priced every deal, and a
    // card that filled in either half would state a figure this deal does not
    // have — a zero amount, or a euro sign over an unknown currency.
    valueMinor: deal.amount_minor ?? null,
    currency: deal.currency ?? null,
    ageMs: Math.max(0, Date.now() - new Date(since).getTime()),
    stalled: deal.stalled ?? false,
    archived: deal.archived_at != null,
  };
}

type UpdateDealRequest = components["schemas"]["UpdateDealRequest"];

function forecastCategory(v: string): UpdateDealRequest["forecast_category"] {
  switch (v) {
    case "commit":
      return "commit";
    case "best_case":
      return "best_case";
    case "pipeline":
      return "pipeline";
    case "omitted":
      return "omitted";
    default:
      return null;
  }
}

// A blank scalar clears the field on the wire (explicit null); a set value
// trims through. `amount` arrives in major units from the form, the wire is
// minor units (deal creation applies the same conversion above).
export function mapDealUpdate(
  values: Record<string, unknown>,
): UpdateDealRequest {
  const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");
  const amount = str(values.amount);
  const owner = str(values.owner_id);
  const forecast = str(values.forecast_category);
  return {
    name: str(values.name) || undefined,
    amount_minor: amount ? Math.round(Number(amount) * 100) : null,
    currency: str(values.currency) || undefined,
    organization_id: str(values.organization_id) || null,
    owner_id: owner || null,
    partner_org_id: str(values.partner_org_id) || null,
    forecast_category: forecastCategory(forecast),
    expected_close_date: str(values.expected_close_date) || null,
    wait_until: str(values.wait_until) || null,
  };
}

const FORECAST_OPTIONS: { value: string; label: MessageKey }[] = [
  { value: "commit", label: "deal.fcCommit" },
  { value: "best_case", label: "deal.fcBestCase" },
  { value: "pipeline", label: "deal.fcPipeline" },
  { value: "omitted", label: "deal.fcOmitted" },
];

export function dealEditFields(
  t: (k: MessageKey) => string,
  opts: {
    orgs: { id: string; display_name: string }[];
    me: string;
    currentOwner: string | null;
    currency: string;
  },
): CreateField[] {
  const currencies = ["EUR", "USD", "GBP", "CHF"];
  if (opts.currency && !currencies.includes(opts.currency)) {
    currencies.unshift(opts.currency);
  }
  const ownerOptions = [
    ...(opts.currentOwner && opts.currentOwner !== opts.me
      ? [{ value: opts.currentOwner, label: t("deal.ownerKeep") }]
      : []),
    { value: opts.me, label: t("deal.ownerMe") },
    { value: "", label: t("deal.ownerUnassign") },
  ];
  const orgOptions = opts.orgs.map((o) => ({
    value: o.id,
    label: o.display_name,
  }));
  return [
    { key: "name", label: "create.dealName", required: true },
    { key: "amount", label: "create.amount", type: "number" },
    {
      key: "currency",
      label: "create.currency",
      type: "select",
      required: true,
      options: currencies.map((c) => ({ value: c, label: c })),
    },
    {
      key: "owner_id",
      label: "deal.ownerMe",
      type: "select",
      options: ownerOptions,
    },
    {
      key: "organization_id",
      label: "create.organization",
      type: "select",
      options: orgOptions,
    },
    {
      key: "partner_org_id",
      label: "deal.partnerOrg",
      type: "select",
      options: orgOptions,
    },
    {
      key: "forecast_category",
      label: "deal.forecastCategory",
      type: "select",
      options: FORECAST_OPTIONS.map((o) => ({
        value: o.value,
        label: t(o.label),
      })),
    },
    { key: "expected_close_date", label: "create.expectedClose", type: "date" },
    { key: "wait_until", label: "deal.waitUntil", type: "date" },
  ];
}

// StageTotals is one stage's count/raw/weighted total, sourced from the
// deals-by-stage report rather than the board's own (capped) card fetch —
// a pipeline with more deals than the board's one-screenful cap was
// showing a confidently wrong sum.
export type StageTotals = {
  count: number;
  // Null where the report stated no figure. A stage can hold a real count of
  // deals nobody has priced, and its `SUM` then arrives as null with no
  // currency beside it — which is not the zero a naive read makes of it.
  rawMinor: number | null;
  weightedMinor: number | null;
  currency: string | null;
  sumHidden: boolean;
};

// A report cell as a figure, or nothing. An absent or non-numeric cell is the
// report declining to state an amount, and `Number(null)` turns that into a 0
// the server never sent.
function reportMinor(value: unknown): number | null {
  if (value == null || value === "") {
    return null;
  }
  const amount = Number(value);
  return Number.isFinite(amount) ? amount : null;
}

// buildStageTotals shapes a deals-by-stage report grouped by
// `["stage_id","currency"]` into one entry per stage. More than one
// currency row for a stage means the sum is genuinely cross-currency — the
// same rule the board has always applied, decided here from the report's
// full row set rather than from whichever cards happened to load.
export function buildStageTotals(
  rows: Record<string, unknown>[],
): Map<string, StageTotals> {
  const byStage = new Map<string, Record<string, unknown>[]>();
  for (const row of rows) {
    const stageId = String(row.stage_id ?? "");
    const forStage = byStage.get(stageId) ?? [];
    forStage.push(row);
    byStage.set(stageId, forStage);
  }
  const totals = new Map<string, StageTotals>();
  for (const [stageId, stageRows] of byStage) {
    const count = stageRows.reduce(
      (sum, row) => sum + Number(row.deals ?? 0),
      0,
    );
    const mixed = stageRows.length > 1;
    const single = stageRows[0];
    // ONE row is not the same fact as one CURRENCY. A stage whose deals are all
    // unpriced groups into a single row whose `currency` is null, so the
    // cross-currency test says nothing about it — and its figures belong to no
    // currency at all. Naming EUR there is indistinguishable from a real EUR
    // total, which is the reading a rep would act on.
    const currency =
      !mixed && typeof single.currency === "string" && single.currency !== ""
        ? single.currency
        : null;
    totals.set(stageId, {
      count,
      rawMinor: currency ? reportMinor(single.raw_minor) : null,
      weightedMinor: currency ? reportMinor(single.weighted_minor) : null,
      currency,
      sumHidden: mixed,
    });
  }
  return totals;
}

export function buildColumns(
  stages: Stage[],
  deals: Deal[],
  totals: Map<string, StageTotals>,
  orgs?: OrgMarks,
): BoardMoneyColumn[] {
  return [...stages]
    .sort((a, b) => a.position - b.position)
    .map((stage) => {
      const stageDeals = deals.filter((deal) => deal.stage_id === stage.id);
      const stageTotals = totals.get(stage.id);
      return {
        stage: stage.id,
        label: stage.name,
        probabilityPct: stage.win_probability,
        // No totals row yet — the report is still in flight, or this stage was
        // not in it. Either way the figure is unknown rather than zero, and the
        // column draws it as absent while still stating the count below.
        rawMinor: stageTotals?.rawMinor ?? null,
        weightedMinor: stageTotals?.weightedMinor ?? null,
        currency: stageTotals?.currency ?? null,
        deals: stageDeals.map((deal) => toBoardDeal(deal, orgs)),
        // The true count, not the loaded page's — falls back to the page
        // count while totals are still loading, so the column shows SOME
        // number rather than a misleading 0.
        count: stageTotals?.count ?? stageDeals.length,
        sumHidden: stageTotals?.sumHidden ?? false,
      };
    });
}

// The table-view column set. Module-level (not inlined in DealsScreen,
// which is already at the cognitive-complexity ceiling) — stage_id → name
// and amount/close formatting are the only per-row logic, everything else
// is direct field access. Only amount_minor and expected_close_date are in
// the deals list's sortable vocabulary (data-model.md DM-VOCAB-3); name,
// stage and status carry no `sort` because the API has no column for them.
function dealColumns(
  t: ReturnType<typeof useT>,
  locale: Locale,
  stageName: Map<string, string>,
): ListColumn<Deal>[] {
  return [
    {
      key: "name",
      header: t("people.name"),
      cell: (deal) => deal.name,
      fixed: true,
    },
    {
      key: "stage",
      header: t("deals.stage"),
      // stage_id is null for an overlay-mirror deal (OVA-MAP-6) — no native
      // stage row to name; a native deal always has one.
      cell: (deal) =>
        deal.stage_id ? (stageName.get(deal.stage_id) ?? "") : "",
    },
    {
      key: "amount",
      header: t("deals.amount"),
      numeric: true,
      sort: "amount_minor",
      cell: (deal) =>
        // Withheld is not empty: a masked amount keeps its cell and says so,
        // where an unpriced deal shows nothing.
        deal.masked_fields?.includes("amount_minor") ? (
          <FieldGuard mode="masked" />
        ) : deal.amount_minor != null && deal.currency ? (
          <span className="t-mono">
            {formatMoney(deal.amount_minor, deal.currency, locale)}
          </span>
        ) : null,
    },
    {
      key: "close",
      header: t("deals.close"),
      sort: "expected_close_date",
      cell: (deal) =>
        deal.expected_close_date
          ? formatDate(deal.expected_close_date, locale, "Europe/Berlin")
          : null,
    },
    {
      // How long since anything happened on this deal. It is the figure a
      // forecast argument rests on — an amount with no recent signal behind it
      // is a number nobody can defend — and the server already flags the ones
      // that have gone quiet, so the row says so rather than leaving the reader
      // to subtract dates.
      key: "last_signal",
      header: t("deals.lastSignal"),
      numeric: true,
      sort: "last_activity_at",
      cell: (deal) =>
        deal.last_activity_at ? (
          <span className="deal-signal">
            {formatDuration(
              Math.max(
                0,
                Date.now() - new Date(deal.last_activity_at).getTime(),
              ),
              locale,
            )}
            {deal.stalled && <Badge tone="warn">{t("deal.stalled")}</Badge>}
          </span>
        ) : (
          <span className="t-caption">{t("deals.lastSignalNone")}</span>
        ),
    },
    {
      key: "status",
      header: t("lead.status"),
      cell: (deal) => (
        <Badge tone={dealStatusTone(deal.status)}>{deal.status}</Badge>
      ),
    },
  ];
}

type PendingAdvance = {
  dealId: string;
  // Carried through the confirm rather than looked up when it closes: the write
  // pins the deal as it stood on the board the reader dropped it on, so a stage
  // change made while the dialog was open fails loud instead of being erased.
  version: number | undefined;
  toStage: Stage;
};

type AdvanceInput = {
  dealId: string;
  version: number | undefined;
  toStage: Stage;
  lostReason?: string;
};

/**
 * The ONE way this screen advances a deal, shared by the board's drag and the
 * record page's stepper.
 *
 * An advance is a write like any other, so it is pinned like any other: the
 * version the reader's own card or record was drawn from rides the variables,
 * and two people moving one deal at the same moment no longer both succeed —
 * the second reads the version the first replaced and fails 409 version_skew
 * instead of quietly undoing a stage change nobody saw.
 *
 * The toast is the CALLER'S, not this hook's: `useToast` is local state, so an
 * instance minted here would be a second one the caller's `ToastRegion` never
 * renders, and every confirmation would be shown to nobody.
 */
function useAdvanceDeal(toast: Toast) {
  const t = useT();
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (input: AdvanceInput) => {
      const terminal = input.toStage.semantic !== "open";
      const { data, error } = await api.POST("/deals/{id}/advance", {
        params: {
          path: { id: input.dealId },
          ...ifMatch(requireVersion(input.version)),
        },
        body: {
          to_stage_id: input.toStage.id,
          ...(terminal ? { status: input.toStage.semantic } : {}),
          ...(input.toStage.semantic === "lost"
            ? { lost_reason: input.lostReason }
            : {}),
        },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: (deal, input) => {
      // The advanced deal goes into the cache SYNCHRONOUSLY, before the
      // refetch the invalidation schedules. isPending clears the moment this
      // returns, so a reader who clicks a second stage immediately would
      // otherwise pin the version this write just replaced and read a 409 they
      // did not cause.
      if (deal) {
        queryClient.setQueryData(["deal", input.dealId], deal);
      }
      queryClient.invalidateQueries({ queryKey: ["deals"] });
      queryClient.invalidateQueries({ queryKey: ["deal", input.dealId] });
      toast.show(t("deals.advanced", { stage: input.toStage.name }));
    },
  });
}

// Won reads success, lost reads danger, an open deal carries no status tone.
function dealStatusTone(
  status: Deal["status"],
): "success" | "danger" | undefined {
  if (status === "won") {
    return "success";
  }
  if (status === "lost") {
    return "danger";
  }
  return undefined;
}

// Bespoke selects for the filters whose option labels are runtime strings
// (pipeline/stage/org names) — a chip's option label is a MessageKey, so
// these three cannot go through ListTable's chips. Each writes into the
// same ListQuery.filters bag the table's chips read, deleting the key on a
// blank choice so the two stay in one coherent query state.
function setOrClearFilter(
  setQuery: Dispatch<SetStateAction<ListQuery>>,
  key: string,
  value: string,
) {
  setQuery((q) => {
    const next = { ...q.filters };
    if (value) {
      next[key] = value;
    } else {
      delete next[key];
    }
    return { ...q, filters: next };
  });
}

// The company filter's own value source: a workspace holds more organizations
// than any fixed list should offer, so the value step searches /organizations
// by name instead of one this screen happened to fetch for something else.
async function searchCompanies(
  query: string,
): Promise<readonly { value: string; label: string }[]> {
  const { data, error } = await api.GET("/organizations", {
    params: { query: { q: query, limit: 20 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((org) => ({ value: org.id, label: org.display_name }));
}

// Whether the reader has narrowed this list themselves.
//
// The same question `SaveViewAction` asks before it offers to save, asked here
// because the pipeline has to be folded into the saved query WITHOUT being the
// thing that makes the query look narrowed: a pipeline is always selected, so
// counting it would offer to save the default list.
function narrowsTheDealList(query: ListQuery): boolean {
  return (
    Boolean(query.q) ||
    Boolean(query.sort) ||
    query.includeArchived ||
    Object.values(query.filters).some(Boolean)
  );
}

// The stage and company filters. The stage list is loaded whole already (a
// pipeline has few stages), so it stays a fixed chip; the company filter
// searches rather than listing (see searchCompanies above). Both are still
// filters, so they read as the same chip as every other one instead of as a
// native select sitting among them.
function dealFilterChips(
  stages: Stage[],
  t: ReturnType<typeof useT>,
): ListChip[] {
  return [
    {
      key: "stage_id",
      label: t("deals.stage"),
      allLabel: t("deals.filterStageAll"),
      options: stages.map((stage) => ({ value: stage.id, label: stage.name })),
    },
    {
      key: "organization_id",
      label: t("create.organization"),
      allLabel: t("deals.filterOrgAll"),
      options: [],
      search: searchCompanies,
    },
  ];
}

// How the deals are shown rather than which ones: the board/table switch and
// the pipeline being looked at. Both sit on the right with the table's own
// display controls. All board-only dials the overlay mirror refuses, so
// overlay never calls this.
function DealViewTools({
  view,
  setView,
  pipelines,
  pipelineId,
  setPipelineId,
  setQuery,
}: Readonly<{
  view: "board" | "table";
  setView: (v: "board" | "table") => void;
  pipelines: Pipeline[];
  pipelineId: string;
  setPipelineId: (id: string) => void;
  setQuery: Dispatch<SetStateAction<ListQuery>>;
}>) {
  const t = useT();
  return (
    <>
      <SegmentedControl
        options={["board", "table"] as const}
        value={view}
        onChange={setView}
        labels={{
          board: t("deals.viewBoard"),
          table: t("deals.viewTable"),
        }}
      />
      {/* Both views read one pipeline: the table binds the same query the
          board does, and its stage chip offers that pipeline's stages. So the
          picker stands in both — hidden on the table it locked the reader to a
          pipeline they could neither see nor change. */}
      <Select
        className="input"
        aria-label={t("deals.pipeline")}
        placeholder={t("deals.pipeline")}
        value={pipelineId}
        onChange={(next) => {
          // A stage belongs to one pipeline; switching pipeline strands any
          // stage_id filter (the chip blanks out but useDeals would still
          // forward the old id and filter a foreign stage → 0 rows).
          setPipelineId(next);
          setOrClearFilter(setQuery, "stage_id", "");
        }}
        options={pipelines.map((pipeline) => ({
          value: pipeline.id,
          label: pipeline.name,
        }))}
      />
    </>
  );
}

// biome-ignore lint/complexity/noExcessiveCognitiveComplexity: this screen was already at the ceiling; overlay support adds one necessary mode branch (board is unavailable over a stage-less mirror). The header is already extracted; a full DealsScreen split is tracked with the overlay SPA follow-up (STATUS.md).
export function DealsScreen({
  startCreating = false,
}: Readonly<{ startCreating?: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  const cf = useObjectCustomFields("deal");
  const overlay = useSorMode() === "overlay";
  const pipelinesQuery = usePipelines(!overlay);
  const meQuery = useMe();
  const savedViews = useSavedViewTabs("deals");
  const [pipelineId, setPipelineId] = useState("");
  const [query, setQuery] = useState<ListQuery>({
    q: "",
    sort: "",
    includeArchived: false,
    filters: {},
    // The deal list reads its own fixed page (the board is capped at 100 and
    // documented as such), so this is the shape's default rather than a dial
    // the footer offers.
    perPage: LIST_PAGE_SIZES[0],
  });
  const effectivePipeline: Pipeline | undefined =
    pipelinesQuery.data?.find((p) => p.id === pipelineId) ??
    pipelinesQuery.data?.find((p) => p.is_default) ??
    pipelinesQuery.data?.[0];
  const dealFilters: DealFilters = {
    pipelineId: effectivePipeline?.id ?? "",
    sort: query.sort,
    includeArchived: query.includeArchived,
    filters: query.filters,
    overlay,
  };
  const dealsQuery = useDeals(dealFilters);
  // The board's column totals: a per-stage server aggregate
  // over EVERY matching deal, not just the capped page useDeals fetches —
  // built from the SAME filter dials so cards and totals never disagree
  // about which deals are in view.
  const stageTotalsQuery = useStageTotals(dealFilters);
  // A stage-keyed board cannot place a mirror deal (its pipeline/stage is the
  // null pipeline/stage), so overlay mode opens on the flat table and hides the toggle
  // (below) — the mode is fixed for the page's life, so a static initial value
  // is enough.
  const [view, setView] = useState<"board" | "table">(
    overlay ? "table" : "board",
  );
  const [pending, setPending] = useState<PendingAdvance | null>(null);
  // Bulk selection, by deal id. Cleared after any bulk run except for the rows
  // that refused, since every other row's version has moved.
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const toast = useToast();
  const dragging = useRef<string | null>(null);
  const lastDragEnd = useRef(0);

  const advance = useAdvanceDeal(toast);

  const stages = effectivePipeline?.stages ?? [];
  const stageName = new Map(stages.map((stage) => [stage.id, stage.name]));
  // Only ids the list currently holds count as selected: a row that left the
  // result set (refetched away, filtered out, archived by this very run) must
  // not linger as an invisible selection nobody can clear.
  // Every page walked so far, in one list. The board draws its columns from
  // this and the table renders it directly, so both surfaces grow together as
  // the reader asks for more.
  const loadedDeals = (dealsQuery.data?.pages ?? []).flatMap(
    (page) => page.data,
  );
  const selectedRows = loadedDeals.filter((deal) => selected.has(deal.id));
  const liveSelection = new Set(selectedRows.map((deal) => deal.id));

  // The table's own dials over the same keyset walk the board reads, so
  // "load more" on either surface advances both.
  const dealsListState: ListState<Deal> = {
    rows: loadedDeals,
    query,
    setQuery,
    isPending: dealsQuery.isPending,
    isError: dealsQuery.isError,
    error: dealsQuery.error,
    refetch: () => dealsQuery.refetch(),
    hasMore: dealsQuery.hasNextPage,
    loadMore: () => {
      dealsQuery.fetchNextPage();
    },
  };

  const orgsQuery = useQuery({
    queryKey: ["organizations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  const createDeal = async (values: Record<string, string>) => {
    const pipeline = effectivePipeline;
    if (!pipeline) {
      throwProblem(null);
    }
    const amount = values.amount?.trim();
    const { data, error } = await api.POST("/deals", {
      body: {
        name: values.name.trim(),
        pipeline_id: pipeline.id,
        stage_id: values.stage_id,
        // The UI takes major units; the wire is minor units.
        amount_minor: amount ? Math.round(Number(amount) * 100) : null,
        currency: values.currency || "EUR",
        organization_id: values.organization_id || null,
        expected_close_date: values.expected_close_date || null,
        source: "manual",
        ...cf.toBody(values),
      },
    });
    if (error) {
      throwProblem(error, t);
    }
    return data;
  };

  // Open-stage targets only: a deal is born open (INV-CLOSE-PAST twin rule);
  // won/lost are reached through the confirmed advance, never at create.
  const openStages = stages.filter((stage) => stage.semantic === "open");

  const requestAdvance = (dealId: string, stageId: string) => {
    const toStage = stages.find((stage) => stage.id === stageId);
    if (!toStage) {
      return;
    }
    // The version the reader saw. The cards and this lookup read the one deal
    // array this render was handed, so the precondition names the row as it was
    // drawn on the board rather than whatever it has become since — which is the
    // whole claim optimistic concurrency makes.
    const version = loadedDeals.find((deal) => deal.id === dealId)?.version;
    if (toStage.semantic === "open") {
      advance.mutate({ dealId, version, toStage });
    } else {
      // Terminal-stage advance is a 🟡 confirm (AC-deal-6).
      setPending({ dealId, version, toStage });
    }
  };

  // Board interactions are hoisted here so the render-prop tree below doesn't
  // nest their event callbacks past the readable depth.
  const openDeal = (deal: BoardDeal) => {
    if (Date.now() - lastDragEnd.current > 250) {
      navigate({ screen: "deals", id: deal.id });
    }
  };

  const cardDragHandlers = (deal: BoardDeal) => ({
    draggable: true as const,
    onDragStart: (event: DragEvent) => {
      dragging.current = deal.id;
      event.dataTransfer.setData("text/plain", deal.id);
    },
  });

  const columnDropHandlers = (column: BoardColumn) => ({
    onDragOver: (event: DragEvent) => {
      event.preventDefault();
      (event.currentTarget as HTMLElement).classList.add("droptarget");
    },
    onDragLeave: (event: DragEvent) => {
      (event.currentTarget as HTMLElement).classList.remove("droptarget");
    },
    onDrop: (event: DragEvent) => {
      event.preventDefault();
      (event.currentTarget as HTMLElement).classList.remove("droptarget");
      const dealId =
        event.dataTransfer.getData("text/plain") || dragging.current;
      dragging.current = null;
      lastDragEnd.current = Date.now();
      if (dealId) {
        requestAdvance(dealId, column.stage);
      }
    },
  });

  // Create writes a native deal — the mirror refuses it (unsupported_by_sor),
  // so the affordance is hidden in overlay, matching the board mutations.
  // Shared between the board and the table surface: whichever view is
  // showing, the action lives in the surface's own header, not a wrap sibling.
  const createAction = !overlay && openStages.length > 0 && (
    <CreateAction
      label={t("create.deal")}
      invalidate="deals"
      screen="deals"
      create={createDeal}
      startOpen={startCreating}
      fields={[
        { key: "name", label: "create.dealName", required: true },
        { key: "amount", label: "create.amount", type: "number" },
        {
          key: "currency",
          label: "create.currency",
          type: "select",
          required: true,
          options: ["EUR", "USD", "GBP", "CHF"].map((code) => ({
            value: code,
            label: code,
          })),
        },
        {
          key: "stage_id",
          label: "create.stage",
          type: "select",
          required: true,
          options: openStages.map((stage) => ({
            value: stage.id,
            label: stage.name,
          })),
        },
        {
          key: "organization_id",
          label: "create.organization",
          type: "select",
          options: (orgsQuery.data?.data ?? []).map((org) => ({
            value: org.id,
            label: org.display_name,
          })),
        },
        {
          key: "expected_close_date",
          label: "create.expectedClose",
          type: "date",
        },
        ...cf.formFields,
      ]}
    />
  );

  // The board/table switch and the pipeline/stage/org pickers, shared between
  // the two non-overlay surfaces so the reader sees the same dials whichever
  // view is showing.
  const dealTools = (
    <DealViewTools
      view={view}
      setView={setView}
      pipelines={pipelinesQuery.data ?? []}
      pipelineId={effectivePipeline?.id ?? ""}
      setPipelineId={setPipelineId}
      setQuery={setQuery}
    />
  );

  // The save action rides beside the dials on the table only. A saved view
  // restores a sort and a set of filters, and the board reads neither: its
  // order is the pipeline's stage order, so a view restored there would
  // silently change nothing a reader could see.
  //
  // The pipeline goes in as a filter because it is the strongest dial on this
  // screen and it lives in its own state, outside `query`. Left out, a view
  // saved while looking at one pipeline would restore against whichever
  // pipeline happened to be showing — a different list under the saved name.
  //
  // It is added only once the reader has narrowed something else. A pipeline is
  // always selected, so folding it in unconditionally would make every list
  // look narrowed and offer to save the default view, which is the clutter
  // SaveViewAction's own check exists to prevent.
  const savableQuery = narrowsTheDealList(dealsListState.query)
    ? {
        ...dealsListState.query,
        filters: {
          ...dealsListState.query.filters,
          pipeline_id: effectivePipeline?.id ?? "",
        },
      }
    : dealsListState.query;
  const tableTools = (
    <>
      {dealTools}
      <SaveViewAction resource="deals" query={savableQuery} />
    </>
  );
  const dealChips = dealFilterChips(stages, t);
  // The companies this screen already read for the create form's picker carry
  // their resolved marks, so the board can show them without a second read.
  // That read is capped, so a deal whose company falls outside it draws its
  // card without a company row — the company filter is a separate search and
  // is not what fills this map.
  const orgMarks: OrgMarks = new Map(
    (orgsQuery.data?.data ?? []).map((org) => [
      org.id,
      { name: org.display_name, logoUrl: org.logo_url },
    ]),
  );

  return (
    <div className="wrap">
      {overlay ? (
        // Overlay mode: the flat, keyset-paginated mirror table (its own
        // infinite query) — no pipeline board, no stage columns. The mirror
        // holds no archived rows, so this toggle is a harmless no-op there —
        // kept anyway so overlay mode loses no control it had.
        <>
          <label className="lt-toggle">
            <input
              type="checkbox"
              checked={query.includeArchived}
              onChange={(event) =>
                setQuery((q) => ({
                  ...q,
                  includeArchived: event.target.checked,
                }))
              }
            />
            {t("list.showArchived")}
          </label>
          <OverlayDealsTable includeArchived={query.includeArchived} />
        </>
      ) : view === "board" ? (
        <ListSurface
          action={createAction}
          count={
            dealsQuery.data && t("board.count", { count: loadedDeals.length })
          }
          tools={dealTools}
          chips={dealChips}
          chosen={query.filters}
          onChipChange={(key, value) => setOrClearFilter(setQuery, key, value)}
          // The board shows archived deals on the same toggle the table uses:
          // without it, a deal archived by mistake could only be found — and
          // so only be restored — by leaving the board.
          archived={{
            checked: query.includeArchived,
            onChange: (next) =>
              setQuery((q) => ({ ...q, includeArchived: next })),
          }}
        >
          <QueryGate query={pipelinesQuery}>
            {() =>
              effectivePipeline ? (
                // Only the INITIAL load goes through the gate. An infinite
                // query reports isError when ANY page fails, later ones
                // included, so keeping the gate around a loaded board would
                // let one failed "load more" throw away every card already on
                // screen. Past the first page the board stands and the button
                // retries — exactly what OverlayDealsTable does above, and for
                // the same reason.
                (dealsQuery.data?.pages ?? []).length === 0 ? (
                  <QueryGate query={dealsQuery}>{() => null}</QueryGate>
                ) : (
                  <>
                    <PipelineBoard
                      columns={buildColumns(
                        effectivePipeline.stages ?? [],
                        loadedDeals,
                        stageTotalsQuery.data ?? new Map(),
                        orgMarks,
                      )}
                      onOpen={openDeal}
                      cardDragHandlers={cardDragHandlers}
                      columnDropHandlers={columnDropHandlers}
                    />
                    <LoadMoreButton query={dealsQuery} />
                  </>
                )
              ) : null
            }
          </QueryGate>
        </ListSurface>
      ) : (
        <ListTable
          state={dealsListState}
          unit="deals.unit"
          columns={dealColumns(t, locale, stageName)}
          rowKey={(deal) => deal.id}
          rowRoute={(deal) => ({ screen: "deals", id: deal.id })}
          searchable={false}
          action={createAction}
          tools={tableTools}
          dataChips={dealChips}
          dataViews={savedViews}
          selection={{
            selected: liveSelection,
            // A closed or archived deal takes no bulk write: archiving it is
            // done or meaningless, and moving it between open stages would be
            // the silent reopen the stepper already refuses.
            selectable: (deal) =>
              deal.archived_at == null && deal.status === "open",
            onToggle: (deal) =>
              setSelected((prev) => {
                const next = new Set(prev);
                if (next.has(deal.id)) {
                  next.delete(deal.id);
                } else {
                  next.add(deal.id);
                }
                return next;
              }),
            label: (deal) => t("deals.bulkSelectRow", { name: deal.name }),
            bar: (
              <DealBulkBar
                deals={selectedRows}
                stages={stages}
                // The rows that went through leave the selection; the ones
                // that refused stay in it, named, so the reader can retry
                // them once the list has refetched their versions.
                onDone={(outcomes) =>
                  setSelected(
                    new Set(
                      outcomes
                        .filter((outcome) => outcome.error)
                        .map((outcome) => outcome.id),
                    ),
                  )
                }
              />
            ),
          }}
          chips={[
            {
              key: "stalled",
              label: "deals.filterStalled",
              allLabel: "deals.filterStalledAll",
              options: [{ value: "true", label: "deals.filterStalled" }],
            },
            // Offered only once the viewer's own id is known. An option whose
            // value is still "" reads as "clear this filter" to the table, so
            // picking "Only mine" mid-load would quietly narrow nothing.
            ...(meQuery.data
              ? [
                  {
                    key: "owner_id",
                    label: "deals.filterOwnerMe" as const,
                    allLabel: "deals.filterOwnerAll" as const,
                    options: [
                      {
                        value: meQuery.data.user.id,
                        label: "deals.filterOwnerMe" as const,
                      },
                    ],
                  },
                ]
              : []),
            {
              key: "partner_sourced",
              label: "deals.filterPartnerSourced",
              allLabel: "deals.filterPartnerAll",
              options: [{ value: "true", label: "deals.filterPartnerSourced" }],
            },
          ]}
          views={[{ label: "deals.sortNewest", sort: "-created_at" }]}
        />
      )}
      {advance.isError && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 10 }}
        >
          {problemMessageOf(advance.error, t)}
        </p>
      )}
      <ToastRegion toast={toast} />
      <ConfirmAdvanceModal
        pending={pending}
        onClose={() => setPending(null)}
        onConfirm={(input) => advance.mutate(input)}
      />
    </div>
  );
}

/**
 * The 🟡 confirm a terminal advance goes through (AC-deal-6), wherever the
 * advance was asked for — the board's drag or the record page's stepper.
 *
 * Closing a deal is the one stage move that cannot be undone by moving it
 * back, so the question is asked in ONE place: a second copy of this dialog is
 * how the two surfaces would end up disagreeing about whether a lost deal
 * needs a reason.
 */
function ConfirmAdvanceModal({
  pending,
  onClose,
  onConfirm,
}: Readonly<{
  pending: PendingAdvance | null;
  onClose: () => void;
  onConfirm: (input: AdvanceInput) => void;
}>) {
  const t = useT();
  const tierMap = useAgentTierMap();
  const [lostReason, setLostReason] = useState("");

  // EVERY way out of this dialog clears the reason — the buttons, Escape, and
  // the backdrop alike. The component stays mounted between openings, so a
  // reason typed and then abandoned would otherwise still be sitting there the
  // next time a deal is closed, and it would describe a different deal.
  const dismiss = () => {
    setLostReason("");
    onClose();
  };

  return (
    <Modal open={pending !== null} onClose={dismiss} labelledBy="advance-title">
      {pending && (
        <>
          <p className="t-sub" id="advance-title">
            <AutonomyDot tier={verbTier("progress_deal", tierMap)} />{" "}
            {t("deals.confirmAdvance", { stage: pending.toStage.name })}
          </p>
          <p className="t-caption" style={{ marginTop: "var(--space-2)" }}>
            {t("deals.confirmTerminal", { status: pending.toStage.semantic })}
          </p>
          {pending.toStage.semantic === "lost" && (
            <div className="field" style={{ marginTop: "var(--space-2)" }}>
              <span className="t-label" id="lost-reason-label">
                {t("deals.lostReason")}
              </span>
              <TextInput
                aria-labelledby="lost-reason-label"
                value={lostReason}
                onChange={(event) => setLostReason(event.target.value)}
              />
            </div>
          )}
          <div className="actions">
            <Button onClick={dismiss}>{t("deals.cancel")}</Button>
            <Button
              variant="primary"
              disabled={
                pending.toStage.semantic === "lost" && lostReason.trim() === ""
              }
              onClick={() => {
                onConfirm({
                  dealId: pending.dealId,
                  version: pending.version,
                  toStage: pending.toStage,
                  lostReason: lostReason.trim() || undefined,
                });
                dismiss();
              }}
            >
              {t("deals.confirm")}
            </Button>
          </div>
        </>
      )}
    </Modal>
  );
}

function DealTable({
  deals,
  stages,
  sortable = true,
}: Readonly<{ deals: Deal[]; stages: Stage[]; sortable?: boolean }>) {
  const t = useT();
  const { locale } = useLocale();
  const [sortKey, setSortKey] = useState<"name" | "amount" | "close">("name");
  const [descending, setDescending] = useState(false);
  const stageName = useMemo(
    () => new Map(stages.map((stage) => [stage.id, stage.name])),
    [stages],
  );

  const sorted = useMemo(() => {
    // When the table isn't sortable (the paginated overlay table), skip the
    // copy+sort entirely — pagination grows this array every load-more, and
    // the sorted result would only be discarded in favor of cursor order.
    if (!sortable) {
      return deals;
    }
    const compareDeals = (a: Deal, b: Deal): number => {
      if (sortKey === "amount") {
        return (a.amount_minor ?? 0) - (b.amount_minor ?? 0);
      }
      if (sortKey === "close") {
        return (a.expected_close_date ?? "").localeCompare(
          b.expected_close_date ?? "",
        );
      }
      return a.name.localeCompare(b.name);
    };
    const rows = [...deals];
    rows.sort((a, b) => {
      const compare = compareDeals(a, b);
      return descending ? -compare : compare;
    });
    return rows;
  }, [deals, sortKey, descending, sortable]);

  const sortBy = (key: typeof sortKey) => {
    if (key === sortKey) {
      setDescending((value) => !value);
    } else {
      setSortKey(key);
      setDescending(false);
    }
  };

  // Client-side sort is honest only over the WHOLE set. The paginated
  // overlay table holds just the pages loaded so far (and the mirror walks
  // an external_id cursor, not the sort key), so sorting a partial subset
  // would present a misleading order — the caller passes sortable={false}
  // there, and `sorted` returns the rows in cursor order untouched.
  const rows = sorted;

  return (
    <div>
      {sortable && (
        <div
          style={{
            display: "flex",
            gap: "var(--space-2)",
            marginBottom: "var(--space-2)",
          }}
        >
          <Button small onClick={() => sortBy("name")}>
            {t("people.name")}
          </Button>
          <Button small onClick={() => sortBy("amount")}>
            {t("deals.amount")}
          </Button>
          <Button small onClick={() => sortBy("close")}>
            {t("deals.close")}
          </Button>
        </div>
      )}
      <DataTable
        columns={[
          {
            key: "name",
            header: t("people.name"),
            render: (deal: Deal) => deal.name,
          },
          {
            key: "stage",
            header: t("deals.stage"),
            // stage_id is null for an overlay-mirror deal (OVA-MAP-6) — no
            // native stage row to name; a native deal always has one.
            render: (deal: Deal) =>
              deal.stage_id ? (stageName.get(deal.stage_id) ?? "") : "",
          },
          {
            key: "amount",
            header: t("deals.amount"),
            render: (deal: Deal) =>
              deal.amount_minor != null && deal.currency ? (
                <span className="t-mono">
                  {formatMoney(deal.amount_minor, deal.currency, locale)}
                </span>
              ) : null,
          },
          {
            key: "close",
            header: t("deals.close"),
            render: (deal: Deal) =>
              deal.expected_close_date
                ? formatDate(deal.expected_close_date, locale, "Europe/Berlin")
                : null,
          },
          {
            key: "status",
            header: t("lead.status"),
            render: (deal: Deal) => (
              <Badge tone={dealStatusTone(deal.status)}>{deal.status}</Badge>
            ),
          },
        ]}
        rows={rows}
        rowKey={(deal) => deal.id}
        onRowClick={(deal) => navigate({ screen: "deals", id: deal.id })}
      />
    </div>
  );
}

// The FX-converted base-currency sub-line (D-14): shown only when the deal
// carries a frozen fx_rate_to_base (won/lost deals freeze it at close; open
// deals in a non-base currency may not have one yet). Prop-driven and
// exported so a later Storybook task can render it without a live fetch.
export function FxLine({
  amountMinor,
  baseCurrency,
  fxRateToBase,
  fxRateDate,
  locale,
}: Readonly<{
  amountMinor: number | null;
  // The installation's own base currency, from its settings. Not a constant:
  // an installation whose base is not the euro was reading a euro sign over a
  // figure converted into something else, which is the one error a converted
  // figure must not make. Null while the settings read is in flight or refused
  // — an unnamed base is not a euro base.
  baseCurrency: string | null;
  fxRateToBase: string;
  fxRateDate: string | null;
  locale: Locale;
}>) {
  const t = useT();
  // A deal carrying a rate but no amount converts to nothing, not to zero.
  const baseMinor =
    amountMinor == null ? null : Math.round(amountMinor * Number(fxRateToBase));
  return (
    <p className="t-caption">
      {t("deal.fxBase", {
        value: formatMoneyOrAbsent(baseMinor, baseCurrency, locale),
        rate: fxRateToBase,
        date: fxRateDate
          ? formatDate(fxRateDate, locale, "Europe/Berlin")
          : "—",
      })}
    </p>
  );
}

// Reopens a won/lost deal back to an open-semantic stage — the same advance
// mutation shape the board drag uses, with status:"open" forced. Split out
// of DealBadges for the same readability reason as the other header actions.
function ReopenAction({
  dealId,
  dealVersion,
  openStages,
  disabledReasonId,
}: Readonly<{
  dealId: string;
  // The version the header this button sits in was rendered from, so the reopen
  // pins the deal the reader was looking at. Stated by the caller rather than
  // read here: this action holds no query of its own to read a fresh one from,
  // and a fresh one would be the wrong answer anyway.
  dealVersion: number | undefined;
  openStages: Stage[];
  // The id of the sentence saying why this reopen is refused, when it is.
  // STATE-4a: a control blocked by the record's STATE rather than by a
  // permission stays visible and says why, because the reason is the
  // information and hiding the control hides a fact the reader needs.
  disabledReasonId?: string;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [stageId, setStageId] = useState<string | null>(null);
  const reopen = useMutation({
    // Stage and version both ride the variables: a version read out of the
    // closure would be the one from the render before this dialog opened, and a
    // reopen that pins the wrong version either fails for no reason the reader
    // can see or lands on a deal somebody else has since moved.
    mutationFn: async (input: {
      toStageId: string;
      version: number | undefined;
    }) => {
      const { data, error } = await api.POST("/deals/{id}/advance", {
        params: {
          path: { id: dealId },
          ...ifMatch(requireVersion(input.version)),
        },
        body: { to_stage_id: input.toStageId, status: "open" },
      });
      if (error) {
        throwProblem(error, t);
      }
      return data;
    },
    onSuccess: () => {
      setOpen(false);
      queryClient.invalidateQueries({ queryKey: ["deal", dealId] });
      queryClient.invalidateQueries({ queryKey: ["deals"] });
    },
  });
  return (
    <>
      <Button
        small
        reasonId={disabledReasonId}
        data-testid="reopen-open"
        onClick={() => setOpen(true)}
      >
        {t("deal.reopen")}
      </Button>
      <Modal
        open={open}
        onClose={() => setOpen(false)}
        labelledBy="reopen-title"
      >
        <p className="t-sub" id="reopen-title">
          {t("deal.reopenPick")}
        </p>
        <div
          style={{
            display: "flex",
            gap: 6,
            flexWrap: "wrap",
            margin: "10px 0",
          }}
        >
          {openStages.map((s) => (
            <Button
              key={s.id}
              small
              aria-pressed={stageId === s.id}
              data-testid={`reopen-stage-${s.id}`}
              onClick={() => setStageId(s.id)}
            >
              {s.name}
            </Button>
          ))}
        </div>
        {reopen.isError && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {problemMessageOf(reopen.error, t)}
          </p>
        )}
        <div className="actions">
          <Button small onClick={() => setOpen(false)}>
            {t("deals.cancel")}
          </Button>
          <Button
            small
            variant="primary"
            data-testid="reopen-confirm"
            disabled={!stageId || reopen.isPending}
            onClick={() => {
              if (stageId) {
                reopen.mutate({ toStageId: stageId, version: dealVersion });
              }
            }}
          >
            {t("deal.reopenConfirm")}
          </Button>
        </div>
      </Modal>
    </>
  );
}

// The status badge plus the edit/archive affordances — split out of
// DealScreen's render so the record-view callback stays readably small. An
// archived deal is read-only (no edit/archive/advance path exists server-side
// for a non-live row), so its verbs render REFUSED rather than missing: the
// page's one sentence about the archive says why, and each of them points at
// it (STATE-4a). A missing control says nothing about the deal, while a
// refused one names the reason.
function DealBadges({
  deal,
  orgs,
  meId,
  openStages,
  archivedReasonId,
}: Readonly<{
  deal: Deal;
  orgs: { id: string; display_name: string }[];
  meId: string;
  openStages: Stage[];
  // The id of the page's sentence about this deal being archived. Every verb
  // the archive refuses points at that one element instead of printing the
  // same line four times.
  archivedReasonId: string;
}>) {
  const t = useT();
  const cf = useObjectCustomFields("deal");
  // The seam serves update and archive for a mirrored deal (write-back
  // projects onto the incumbent, overlay/provider_writes.go), so Edit and
  // Archive render in overlay too. Reopen and share stay hidden: reopen
  // dials advance under the hood, which the seam refuses outright (a mirror
  // deal carries no native pipeline/stage, OVA-MAP-6), and a record grant
  // probes the native deal row (auth.EnsureLinkTarget), which a mirror deal
  // has no row in, so the grant 404s — overlay visibility is governed by
  // mirror_visibility, which record_grant does not feed.
  const overlay = useSorMode() === "overlay";
  // One fact refuses every write below, so it is named once. Undefined while
  // the deal is live, which is what leaves the verbs pressable.
  const refusedByArchive = deal.archived_at ? archivedReasonId : undefined;
  return (
    <>
      <Badge tone={dealStatusTone(deal.status)}>{deal.status}</Badge>
      <EditAction
        disabledReasonId={refusedByArchive}
        label={t("deal.edit")}
        notice={overlay ? t("overlay.partialWriteBack") : undefined}
        fields={[
          ...dealEditFields(t, {
            orgs,
            me: meId,
            currentOwner: deal.owner_id ?? null,
            // EMPTY, not a default. `dealEditFields` only uses this to put the
            // record's own currency at the head of the option list, and a deal
            // nobody has priced has none to put there.
            currency: deal.currency ?? "",
          }),
          ...cf.formFields,
        ]}
        record={{
          id: deal.id,
          version: deal.version,
          name: deal.name,
          amount:
            deal.amount_minor != null ? String(deal.amount_minor / 100) : "",
          // A currency the FORM chose is a currency the SAVE writes: mapDealUpdate
          // sends whatever this holds, so seeding it with a default made an
          // unpriced deal acquire one the moment a reader edited its name. The
          // amount is already sent as null in that case, and the two columns are
          // paired by CHECK, so the invented currency did not merely mislabel the
          // record — it made an innocent rename fail.
          currency: deal.currency ?? "",
          owner_id: deal.owner_id ?? "",
          organization_id: deal.organization_id ?? "",
          partner_org_id: deal.partner_org_id ?? "",
          forecast_category: deal.forecast_category ?? "",
          expected_close_date: deal.expected_close_date ?? "",
          wait_until: deal.wait_until ?? "",
          ...cf.recordSlice(deal),
        }}
        update={async (values) => {
          const { data, error } = await api.PATCH("/deals/{id}", {
            params: {
              path: { id: deal.id },
              ...ifMatch(requireVersion(deal.version)),
            },
            body: { ...mapDealUpdate(values), ...cf.toBody(values) },
          });
          if (error) {
            throwProblem(error);
          }
          return data;
        }}
        invalidate="deals"
        recordKey="deal"
      />
      <ArchiveAction
        disabledReasonId={refusedByArchive}
        label={t("deal.archive")}
        confirmText={t("deal.archiveConfirm")}
        archive={async () => {
          const { data, error } = await api.DELETE("/deals/{id}", {
            params: { path: { id: deal.id } },
          });
          if (error) {
            throwProblem(error);
          }
          return data;
        }}
        invalidate="deals"
        recordKey="deal"
        onArchived={() => navigate({ screen: "deals" })}
      />
      {!overlay && (
        <ShareAction
          recordType="deal"
          recordId={deal.id}
          disabledReasonId={refusedByArchive}
        />
      )}
      {/* Reopen answers a CLOSED deal, so an open one has no reason to be
          told about it — absent, not refused. An archived closed deal keeps
          it, refused: the reader came asking whether this can come back. */}
      {!overlay && (deal.status === "won" || deal.status === "lost") && (
        <ReopenAction
          dealId={deal.id}
          dealVersion={deal.version}
          openStages={openStages}
          disabledReasonId={refusedByArchive}
        />
      )}
    </>
  );
}

type Approval = components["schemas"]["Approval"];

// The live 🟡 confirm-first staging queue for this deal — split out of
// DealScreen's render for the same readability reason as DealBadges above.
function DealApprovals({
  approvals,
  decide,
}: Readonly<{
  approvals: Approval[];
  decide: (input: {
    approvalId: string;
    verdict: "approve" | "reject";
  }) => void;
}>) {
  const t = useT();
  const tierMap = useAgentTierMap();
  const viewerId = useViewerId();
  if (approvals.length === 0) {
    return null;
  }
  return (
    <Card
      title={t("deal.pendingApprovals")}
      style={{ marginBottom: "var(--space-4)" }}
    >
      {approvals.map((approval) => (
        <div
          key={approval.id}
          className="staging-card"
          style={{ marginBottom: 8 }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <AutonomyDot tier={approvalDotTier(approval.kind, tierMap)} />
            {/* The same two facts the approvals inbox states, said the same
                way. Printed off the wire they read `advance_deal` and
                `agent:capture` — the vocabulary the API speaks, on a page
                whose reader never sees the API. */}
            <span className="t-label">
              {approvalKindLabel(approval.kind, t)}
            </span>
            <ProvenanceTag
              provenance={provenanceOf(approval.proposed_by, viewerId)}
            />
          </div>
          <div className="approval-gate">
            <Button
              variant="primary"
              small
              onClick={() =>
                decide({ approvalId: approval.id, verdict: "approve" })
              }
            >
              {t("trust.accept")}
            </Button>
            <Button
              small
              onClick={() =>
                decide({ approvalId: approval.id, verdict: "reject" })
              }
            >
              {t("trust.dismiss")}
            </Button>
          </div>
        </div>
      ))}
    </Card>
  );
}

// Exported so a render test can reach the refusal state directly. It is not as
// self-contained as FxLine — it reads the system-of-record mode, so it needs a
// query client around it — but whether the New-offer control is refused depends
// on its props alone.
export function OffersPanel({
  offers,
  creating,
  locale,
  dealCurrency,
  onCreate,
}: Readonly<{
  offers: Offer[] | undefined;
  creating: boolean;
  locale: Locale;
  // An offer is written in the DEAL's currency, so a deal nobody has priced
  // has nothing to write one in. Null refuses the control and says why rather
  // than creating an offer denominated in a currency the code chose.
  dealCurrency: string | null;
  onCreate: (currency: string) => void;
}>) {
  const t = useT();
  // Offers are read (and created) against a mirrored deal — the list read 404s
  // and creation would write, both refused in overlay. Show the honest
  // unavailable state instead of an empty panel with a New-offer button.
  const overlay = useSorMode() === "overlay";
  if (overlay) {
    return (
      <Card title={t("deal.offers")} style={{ marginBottom: "var(--space-4)" }}>
        <OverlayUnavailable />
      </Card>
    );
  }
  return (
    <Card
      title={t("deal.offers")}
      actions={
        <Button
          small
          // `reason` disables the control AND points at the explanation. Passing
          // `disabled` beside it would cancel the refusal it sets, so the
          // in-flight case stays on `disabled` and the state case on `reason`.
          // An empty code is as absent as a null one — `formatMoneyOrAbsent`
          // already treats it that way, and Intl throws on it.
          disabled={Boolean(dealCurrency) && creating}
          reason={dealCurrency ? undefined : t("deal.offerNeedsCurrency")}
          onClick={() => {
            if (dealCurrency) {
              onCreate(dealCurrency);
            }
          }}
        >
          {t("deal.newOffer")}
        </Button>
      }
      style={{ marginBottom: "var(--space-4)" }}
    >
      {offers &&
        (offers.length > 0 ? (
          <DataTable
            columns={[
              {
                key: "offer_number",
                header: t("deal.offerNumber"),
                render: (offer: Offer) => offer.offer_number,
              },
              {
                key: "revision",
                header: t("deal.offerRevision"),
                render: (offer: Offer) => String(offer.revision),
              },
              {
                key: "status",
                header: t("lead.status"),
                render: (offer: Offer) => <Badge>{offer.status}</Badge>,
              },
              {
                key: "gross",
                header: t("deals.amount"),
                render: (offer: Offer) => (
                  <span className="t-mono">
                    {formatMoney(offer.gross_minor, offer.currency, locale)}
                  </span>
                ),
              },
            ]}
            rows={offers}
            rowKey={(offer) => offer.id}
            onRowClick={(offer) => navigate({ screen: "offers", id: offer.id })}
          />
        ) : (
          <EmptyState>{t("deal.offersEmpty")}</EmptyState>
        ))}
    </Card>
  );
}

const DEAL_TABS = ["overview", "history"] as const;
type DealTab = (typeof DEAL_TABS)[number];

type Relationship = components["schemas"]["Relationship"];

// The deal 360's "overview" pane, split out of DealScreen so the tab switch
// doesn't push the render-prop closure over the cognitive-complexity budget.
// Every prop here is a value already resolved by DealScreen — no new
// fetches, no behavior change from the pre-tab layout.
function DealOverviewPane({
  deal,
  stages,
  dealApprovals,
  onDecide,
  stakeholders,
  offers,
  creatingOffer,
  locale,
  baseCurrency,
  onCreateOffer,
  overlay,
  onAdvance,
  advancing,
  advanceRefused,
}: Readonly<{
  deal: Deal;
  stages: Stage[];
  dealApprovals: Approval[];
  onDecide: (input: {
    approvalId: string;
    verdict: "approve" | "reject";
  }) => void;
  stakeholders: Relationship[] | undefined;
  offers: Offer[] | undefined;
  creatingOffer: boolean;
  locale: Locale;
  baseCurrency: string | null;
  onCreateOffer: (currency: string) => void;
  overlay: boolean;
  onAdvance: (toStage: Stage) => void;
  /** One advance at a time: a second click while the first is in flight would
   * send a second write pinned to the same version, and the loser reads as a
   * conflict the reader never caused. */
  advancing: boolean;
  /** Where this deal cannot be moved at all — archived (restore it first), or
   * mirrored from an incumbent that refuses the write. */
  advanceRefused: boolean;
}>) {
  const t = useT();
  return (
    <>
      {deal.fx_rate_to_base != null && (
        <FxLine
          amountMinor={deal.amount_minor ?? null}
          baseCurrency={baseCurrency}
          fxRateToBase={deal.fx_rate_to_base}
          fxRateDate={deal.fx_rate_date ?? null}
          locale={locale}
        />
      )}
      {/* A group, not a nav: these buttons move the deal, they do not take the
          reader anywhere, and a landmark in the navigation list that writes
          when you press it misdescribes what it does. */}
      {stages.length > 0 && (
        <fieldset className="stepper" aria-label={t("deals.stage")}>
          {stages.map((stage) =>
            stage.id === deal.stage_id ? (
              <span key={stage.id} className="step current" aria-current="step">
                {stage.name}
              </span>
            ) : (
              // Where the deal is now is a fact, not a choice, so the current
              // stage stays a marker. Every other stage is the move to it —
              // which is what makes a deal closable from its own page rather
              // than only by dragging its card on the board.
              <button
                key={stage.id}
                type="button"
                className="step"
                disabled={advancing || advanceRefused}
                onClick={() => onAdvance(stage)}
              >
                {stage.name}
              </button>
            ),
          )}
        </fieldset>
      )}
      <DealApprovals approvals={dealApprovals} decide={onDecide} />
      {/* Above the stakeholder list on purpose: the findings are ABOUT those
          seats, and a rep who read the list first has already formed the
          impression the flags exist to correct. */}
      <DealCoverageCard id={deal.id} />
      {/* Stakeholders are a relationship read the mirror does not serve. In
          overlay show the honest unavailable state (never any cached native
          rows), matching the timeline and offers panels. */}
      {overlay ? (
        <Card
          title={t("deal.stakeholders")}
          style={{ marginBottom: "var(--space-4)" }}
        >
          <OverlayUnavailable />
        </Card>
      ) : (
        stakeholders &&
        stakeholders.length > 0 && (
          <Card
            title={t("deal.stakeholders")}
            style={{ marginBottom: "var(--space-4)" }}
          >
            <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
              {stakeholders.map((stakeholder) => (
                <Badge key={stakeholder.id}>
                  {stakeholder.role ??
                    stakeholder.person_id ??
                    stakeholder.kind}
                </Badge>
              ))}
            </div>
          </Card>
        )
      )}
      <OffersPanel
        offers={offers}
        creating={creatingOffer}
        locale={locale}
        dealCurrency={deal.currency ?? null}
        onCreate={onCreateOffer}
      />
      <CustomFieldsCard object="deal" record={deal} />
      <RecordContextPanel entityType="deal" id={deal.id} />
      <LogActivity entityType="deal" entityId={deal.id} />
    </>
  );
}

// The page's ONE sentence about this deal being archived, said across the
// whole header rather than repeated beside each of the four verbs the archive
// refuses. Nothing at all while the deal is live — `undefined` rather than an
// element that renders null, because RecordView reserves the band's space for
// anything it is handed, and a page that always kept the gap would read as a
// record with something to say about itself and nothing said.
function archivedDealBand(
  deal: Deal,
  reasonId: string,
  t: ReturnType<typeof useT>,
): ReactNode | undefined {
  if (deal.archived_at == null) {
    return undefined;
  }
  return (
    <p id={reasonId} className="t-caption">
      {t("deal.archivedReadOnly")}
    </p>
  );
}

export function DealScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  // Minted here because the band that carries the sentence and the verbs that
  // point at it are two different slots of the same header.
  const archivedReasonId = useId();
  const [tab, setTab] = useState<DealTab>("overview");
  const [pending, setPending] = useState<PendingAdvance | null>(null);
  const toast = useToast();
  const advance = useAdvanceDeal(toast);
  const dealQuery = useQuery({
    queryKey: ["deal", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const pipelineQuery = usePipeline(dealQuery.data?.pipeline_id);
  // One shared singleton read (the ["installation-settings"] key), not a
  // per-deal request: the FX line has to name the base currency it converted
  // into, and nothing on the deal itself carries it.
  const baseCurrency = useInstallationSettings().data?.base_currency ?? null;
  const me = useMe();
  const viewerId = useViewerId();
  // Overlay serves a read-only mirror: entity-scoped activity reads (timeline)
  // and the deal's stakeholders/offers sub-resources 422/404, and offer
  // creation would write to a mirrored deal. Gate all of it on this.
  const overlay = useSorMode() === "overlay";
  const orgs = useQuery({
    queryKey: ["organizations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const stakeholdersQuery = useQuery({
    queryKey: ["deal-stakeholders", id],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/stakeholders", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  // The shared pending-approvals hook, not a second query on the same key: a
  // private queryFn under ["approvals","pending"] takes over the cache entry the
  // Inbox and the rail badge read, and this one stopped at the first page and
  // kept expired rows — so a visit here could silently cap the badge at 50 and
  // count approvals nobody can act on.
  const approvalsQuery = usePendingApprovals();
  const timelineQuery = useQuery({
    queryKey: ["activities", "deal", id],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: { query: { entity_type: "deal", entity_id: id, limit: 20 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const offersQuery = useQuery({
    queryKey: ["deal-offers", id],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}/offers", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
  const createOffer = useMutation({
    mutationFn: async (currency: string) => {
      const { data, error } = await api.POST("/deals/{id}/offers", {
        params: { path: { id } },
        body: { currency, source: "manual" },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (offer: Offer) => {
      navigate({ screen: "offers", id: offer.id });
    },
  });
  const decide = useMutation({
    mutationFn: async (input: {
      approvalId: string;
      verdict: "approve" | "reject";
    }) => {
      const path =
        input.verdict === "approve"
          ? "/approvals/{id}/approve"
          : "/approvals/{id}/reject";
      const { error } = await api.POST(path, {
        params: { path: { id: input.approvalId } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ["approvals", "pending"] }),
  });

  return (
    <div className="wrap">
      <QueryGate query={dealQuery}>
        {(deal) => {
          const stages = [...(pipelineQuery.data?.stages ?? [])].sort(
            (a, b) => a.position - b.position,
          );
          const openStages = stages.filter(
            (stage) => stage.semantic === "open",
          );
          const dealApprovals = (approvalsQuery.data?.data ?? []).filter(
            (approval) => approval.target_entity_id === deal.id,
          );
          return (
            <RecordView
              name={deal.name}
              subtitle={
                deal.amount_minor != null && deal.currency
                  ? formatMoney(deal.amount_minor, deal.currency, locale)
                  : undefined
              }
              zone="Europe/Berlin"
              badges={
                <DealBadges
                  deal={deal}
                  orgs={orgs.data?.data ?? []}
                  meId={me.data?.user.id ?? ""}
                  openStages={openStages}
                  archivedReasonId={archivedReasonId}
                />
              }
              band={archivedDealBand(deal, archivedReasonId, t)}
              timeline={
                timelineQuery.isSuccess
                  ? activityTimeline(
                      timelineQuery.data.data,
                      viewerId,
                      (activity) => (
                        <TimelineActions
                          activity={activity}
                          entityType="deal"
                          entityId={id}
                        />
                      ),
                    )
                  : []
              }
              timelineNotice={timelineZoneNotice(
                { overlay, pending: timelineQuery.isPending },
                t,
              )}
            >
              <div style={{ marginBottom: 16 }}>
                <SegmentedControl
                  options={DEAL_TABS}
                  value={tab}
                  onChange={setTab}
                  labels={{
                    overview: t("tab.overview"),
                    history: t("tab.history"),
                  }}
                />
              </div>
              {tab === "overview" && (
                <DealOverviewPane
                  deal={deal}
                  stages={stages}
                  dealApprovals={dealApprovals}
                  onDecide={(input) => decide.mutate(input)}
                  stakeholders={stakeholdersQuery.data?.data}
                  offers={offersQuery.data?.data}
                  creatingOffer={createOffer.isPending}
                  locale={locale}
                  baseCurrency={baseCurrency}
                  onCreateOffer={(currency) => createOffer.mutate(currency)}
                  overlay={overlay}
                  advancing={advance.isPending}
                  // An archived deal is not moved through the pipeline, and
                  // the mirror answers an advance with unsupported_by_sor —
                  // a control that can only fail is worse than none.
                  //
                  // A CLOSED deal is refused here too, but for a different
                  // reason: reopening is its own deliberate action, with a
                  // dialog that says the close date and the frozen rate are
                  // being cleared. A stepper button that reopened silently
                  // would be a second, quieter door to the same write.
                  advanceRefused={
                    deal.archived_at != null ||
                    overlay ||
                    deal.status !== "open"
                  }
                  onAdvance={(toStage) => {
                    // The version this record was drawn from, exactly as the
                    // board pins the version its card was drawn from: the
                    // write names the deal as the reader saw it, so a change
                    // made elsewhere meanwhile fails loud.
                    const input = {
                      dealId: deal.id,
                      version: deal.version,
                      toStage,
                    };
                    if (toStage.semantic === "open") {
                      advance.mutate(input);
                    } else {
                      setPending(input);
                    }
                  }}
                />
              )}
              {tab === "history" && !overlay && (
                <RecordHistoryTab kind="deal" id={deal.id} />
              )}
              {tab === "history" && overlay && <OverlayUnavailable />}
              {advance.isError && (
                <p
                  className="t-caption"
                  style={{
                    color: "var(--danger)",
                    marginTop: "var(--space-2)",
                  }}
                >
                  {problemMessageOf(advance.error, t)}
                </p>
              )}
              <ConfirmAdvanceModal
                pending={pending}
                onClose={() => setPending(null)}
                onConfirm={(input) => advance.mutate(input)}
              />
              <ToastRegion toast={toast} />
            </RecordView>
          );
        }}
      </QueryGate>
    </div>
  );
}
