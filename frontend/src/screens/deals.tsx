import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import {
  type Dispatch,
  type DragEvent,
  type SetStateAction,
  useMemo,
  useRef,
  useState,
} from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { approvalDotTier, useAgentTierMap, verbTier } from "../app/autonomy";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  Modal,
  SectionHeader,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import {
  type BoardColumn,
  type BoardDeal,
  PipelineBoard,
  RecordView,
} from "../design-system/composed";
import { AutonomyDot } from "../design-system/trust";
import { formatDate, formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { ArchiveAction } from "./archive";
import {
  LoadMoreButton,
  OverlayUnavailable,
  problemMessage,
  QueryGate,
  throwProblem,
  useMe,
  useSorMode,
} from "./common";
import { TimelineActions } from "./compose";
import { RecordContextPanel } from "./context";
import type { CreateField } from "./create";
import { CreateAction } from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import { RecordHistoryTab } from "./history";
import { type ListQuery, ListToolbar } from "./listquery";
import { LogActivity } from "./logactivity";
import { activityTimeline } from "./people";
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

function usePipeline() {
  return useQuery({
    queryKey: ["pipelines"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      const pipeline =
        data.data.find((candidate) => candidate.is_default) ?? data.data[0];
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
function usePipelines() {
  return useQuery({
    queryKey: ["pipelines", "all"],
    queryFn: async () => {
      const { data, error } = await api.GET("/pipelines", {
        params: { query: {} },
      });
      if (error) {
        throw new Error(problemMessage(error));
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

// dealsQueryParams builds the /deals query. Overlay reads a mirror that 422s
// every dial except the two the cache can honor, so overlay mode sends only
// those and the list comes back flat (the screen forces the table view and
// hides the pickers to match — a stage-keyed board cannot place a mirror deal,
// whose pipeline/stage is null in overlay, OVA-MAP-6).
function dealsQueryParams(f: DealFilters) {
  const base = { limit: 100, include_archived: f.includeArchived || undefined };
  if (f.overlay) {
    return base;
  }
  const { filters } = f;
  return {
    ...base,
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
function useDeals(f: DealFilters) {
  return useQuery({
    queryKey: ["deals", f],
    enabled: !f.overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/deals", {
        params: { query: dealsQueryParams(f) },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
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
        throw new Error(problemMessage(error));
      }
      return data;
    },
    getNextPageParam: (last) =>
      last.page?.has_more ? (last.page.next_cursor ?? undefined) : undefined,
  });
  return (
    <QueryGate
      query={query}
      empty={(data) => data.pages.every((p) => p.data.length === 0)}
    >
      {(data) => (
        <>
          <DealTable deals={data.pages.flatMap((p) => p.data)} stages={[]} />
          <LoadMoreButton query={query} />
        </>
      )}
    </QueryGate>
  );
}

function toBoardDeal(deal: Deal): BoardDeal {
  const since = deal.last_activity_at ?? deal.created_at;
  return {
    id: deal.id,
    name: deal.name,
    org: "",
    valueMinor: deal.amount_minor ?? 0,
    currency: deal.currency ?? "EUR",
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

export function buildColumns(stages: Stage[], deals: Deal[]): BoardColumn[] {
  return [...stages]
    .sort((a, b) => a.position - b.position)
    .map((stage) => {
      const stageDeals = deals.filter((deal) => deal.stage_id === stage.id);
      const currencies = new Set(
        stageDeals.map((deal) => deal.currency ?? "EUR"),
      );
      const currency = currencies.size === 1 ? [...currencies][0] : null;
      // Sub-lines are page-local, same-currency display sums only; a mixed
      // column shows no figure rather than a cross-currency lie.
      const raw = currency
        ? stageDeals.reduce((sum, deal) => sum + (deal.amount_minor ?? 0), 0)
        : null;
      return {
        stage: stage.id,
        label: stage.name,
        probabilityPct: stage.win_probability,
        rawMinor: raw ?? 0,
        weightedMinor:
          raw === null ? 0 : Math.round((raw * stage.win_probability) / 100),
        currency: currency ?? "EUR",
        deals: stageDeals.map(toBoardDeal),
        sumHidden: raw === null,
        semantic: stage.semantic,
      } as BoardColumn & { sumHidden: boolean; semantic: Stage["semantic"] };
    });
}

const DEAL_SORT_OPTIONS: { value: string; label: MessageKey }[] = [
  { value: "name", label: "people.name" },
  { value: "-created_at", label: "deals.sortNewest" },
  { value: "expected_close_date", label: "deals.sortClose" },
  { value: "-amount_minor", label: "deals.sortAmount" },
];

type PendingAdvance = {
  dealId: string;
  toStage: Stage;
};

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
// (pipeline/stage/org names) — FilterSpec's option label is a MessageKey, so
// these three cannot go through ListToolbar (Gotcha 3). Each writes into the
// same ListQuery.filters bag ListToolbar reads, deleting the key on a blank
// choice so the two stay in one coherent query state.
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

function DealFilterSelects({
  pipelines,
  pipelineId,
  setPipelineId,
  stages,
  orgs,
  query,
  setQuery,
}: Readonly<{
  pipelines: Pipeline[];
  pipelineId: string;
  setPipelineId: (id: string) => void;
  stages: Stage[];
  orgs: { id: string; display_name: string }[];
  query: ListQuery;
  setQuery: Dispatch<SetStateAction<ListQuery>>;
}>) {
  const t = useT();
  return (
    <div className="list-toolbar">
      <select
        className="input"
        aria-label={t("deals.pipeline")}
        value={pipelineId}
        onChange={(event) => setPipelineId(event.target.value)}
      >
        <option value="">{t("deals.pipeline")}</option>
        {pipelines.map((pipeline) => (
          <option key={pipeline.id} value={pipeline.id}>
            {pipeline.name}
          </option>
        ))}
      </select>
      <select
        className="input"
        aria-label={t("deals.stage")}
        value={query.filters.stage_id ?? ""}
        onChange={(event) =>
          setOrClearFilter(setQuery, "stage_id", event.target.value)
        }
      >
        <option value="">{t("deals.filterStageAll")}</option>
        {stages.map((stage) => (
          <option key={stage.id} value={stage.id}>
            {stage.name}
          </option>
        ))}
      </select>
      <select
        className="input"
        aria-label={t("create.organization")}
        value={query.filters.organization_id ?? ""}
        onChange={(event) =>
          setOrClearFilter(setQuery, "organization_id", event.target.value)
        }
      >
        <option value="">{t("deals.filterOrgAll")}</option>
        {orgs.map((org) => (
          <option key={org.id} value={org.id}>
            {org.display_name}
          </option>
        ))}
      </select>
    </div>
  );
}

// The deals header — the board/table toggle and pipeline/stage/owner/org
// pickers (all board-only, and all dials the overlay mirror refuses), plus the
// always-present ListToolbar. Split out of DealsScreen so that screen's render
// stays legible; overlay mode renders only the toolbar (which self-gates).
function DealsHeader({
  overlay,
  view,
  setView,
  pipelines,
  pipelineId,
  setPipelineId,
  stages,
  orgs,
  query,
  setQuery,
  meUserId,
}: Readonly<{
  overlay: boolean;
  view: "board" | "table";
  setView: (v: "board" | "table") => void;
  pipelines: Pipeline[];
  pipelineId: string;
  setPipelineId: (id: string) => void;
  stages: Stage[];
  orgs: { id: string; display_name: string }[];
  query: ListQuery;
  setQuery: Dispatch<SetStateAction<ListQuery>>;
  meUserId: string;
}>) {
  const t = useT();
  return (
    <>
      {/* The stage-keyed board + its pipeline/stage/owner pickers all rely on
          dials the overlay mirror refuses, so overlay mode shows neither —
          just the flat table below. */}
      {!overlay && (
        <>
          <div style={{ marginBottom: "var(--space-3)" }}>
            <SegmentedControl
              options={["board", "table"] as const}
              value={view}
              onChange={setView}
              labels={{
                board: t("deals.viewBoard"),
                table: t("deals.viewTable"),
              }}
            />
          </div>
          <DealFilterSelects
            pipelines={pipelines}
            pipelineId={pipelineId}
            setPipelineId={(id) => {
              // A stage belongs to one pipeline; switching pipeline strands any
              // stage_id filter (its <select> blanks out but useDeals would
              // still forward the old id and filter a foreign stage → 0 rows).
              setPipelineId(id);
              setOrClearFilter(setQuery, "stage_id", "");
            }}
            stages={stages}
            orgs={orgs}
            query={query}
            setQuery={setQuery}
          />
        </>
      )}
      <ListToolbar
        query={query}
        setQuery={setQuery}
        searchable={false}
        showArchivedToggle
        sortOptions={DEAL_SORT_OPTIONS}
        filters={[
          {
            kind: "select",
            key: "stalled",
            label: "deals.filterStalled",
            placeholder: "deals.filterStalledAll",
            options: [{ value: "true", label: "deals.filterStalled" }],
          },
          {
            kind: "select",
            key: "owner_id",
            label: "deals.filterOwnerMe",
            placeholder: "deals.filterOwnerAll",
            options: [{ value: meUserId, label: "deals.filterOwnerMe" }],
          },
          {
            kind: "select",
            key: "partner_sourced",
            label: "deals.filterPartnerSourced",
            placeholder: "deals.filterPartnerAll",
            options: [{ value: "true", label: "deals.filterPartnerSourced" }],
          },
        ]}
      />
    </>
  );
}

// biome-ignore lint/complexity/noExcessiveCognitiveComplexity: this screen was already at the ceiling; overlay support adds one necessary mode branch (board is unavailable over a stage-less mirror). The header is already extracted; a full DealsScreen split is tracked with the overlay SPA follow-up (STATUS.md).
export function DealsScreen({
  startCreating = false,
}: Readonly<{ startCreating?: boolean }>) {
  const t = useT();
  const cf = useObjectCustomFields("deal");
  const queryClient = useQueryClient();
  const pipelinesQuery = usePipelines();
  const meQuery = useMe();
  const tierMap = useAgentTierMap();
  const overlay = useSorMode() === "overlay";
  const [pipelineId, setPipelineId] = useState("");
  const [query, setQuery] = useState<ListQuery>({
    q: "",
    sort: "",
    includeArchived: false,
    filters: {},
  });
  const effectivePipeline: Pipeline | undefined =
    pipelinesQuery.data?.find((p) => p.id === pipelineId) ??
    pipelinesQuery.data?.find((p) => p.is_default) ??
    pipelinesQuery.data?.[0];
  const dealsQuery = useDeals({
    pipelineId: effectivePipeline?.id ?? "",
    sort: query.sort,
    includeArchived: query.includeArchived,
    filters: query.filters,
    overlay,
  });
  // A stage-keyed board cannot place a mirror deal (its pipeline/stage is the
  // null pipeline/stage), so overlay mode opens on the flat table and hides the toggle
  // (below) — the mode is fixed for the page's life, so a static initial value
  // is enough.
  const [view, setView] = useState<"board" | "table">(
    overlay ? "table" : "board",
  );
  const [pending, setPending] = useState<PendingAdvance | null>(null);
  const [lostReason, setLostReason] = useState("");
  const [toast, setToast] = useState<string | null>(null);
  const dragging = useRef<string | null>(null);
  const lastDragEnd = useRef(0);

  const advance = useMutation({
    mutationFn: async (input: {
      dealId: string;
      toStage: Stage;
      lostReason?: string;
    }) => {
      const terminal = input.toStage.semantic !== "open";
      const { data, error } = await api.POST("/deals/{id}/advance", {
        params: { path: { id: input.dealId } },
        body: {
          to_stage_id: input.toStage.id,
          ...(terminal ? { status: input.toStage.semantic } : {}),
          ...(input.toStage.semantic === "lost"
            ? { lost_reason: input.lostReason }
            : {}),
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (_deal, input) => {
      queryClient.invalidateQueries({ queryKey: ["deals"] });
      setToast(t("deals.advanced", { stage: input.toStage.name }));
      setTimeout(() => setToast(null), 3500);
    },
  });

  const stages = effectivePipeline?.stages ?? [];

  const orgsQuery = useQuery({
    queryKey: ["organizations"],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const createDeal = async (values: Record<string, string>) => {
    const pipeline = effectivePipeline;
    if (!pipeline) {
      throw new Error(problemMessage(null));
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
      throw new Error(problemMessage(error));
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
    if (toStage.semantic === "open") {
      advance.mutate({ dealId, toStage });
    } else {
      // Terminal-stage advance is a 🟡 confirm (AC-deal-6).
      setLostReason("");
      setPending({ dealId, toStage });
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

  return (
    <div className="wrap">
      <div className="list-head">
        <SectionHeader title={t("nav.deals")} />
        {/* Create writes a native deal — the mirror refuses it
            (unsupported_by_sor), so the affordance is hidden in overlay,
            matching the board mutations. */}
        {!overlay && openStages.length > 0 && (
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
        )}
      </div>
      <DealsHeader
        overlay={overlay}
        view={view}
        setView={setView}
        pipelines={pipelinesQuery.data ?? []}
        pipelineId={effectivePipeline?.id ?? ""}
        setPipelineId={setPipelineId}
        stages={stages}
        orgs={orgsQuery.data?.data ?? []}
        query={query}
        setQuery={setQuery}
        meUserId={meQuery.data?.user.id ?? ""}
      />
      {overlay ? (
        // Overlay mode: the flat, keyset-paginated mirror table (its own
        // infinite query) — no pipeline board, no stage columns.
        <OverlayDealsTable includeArchived={query.includeArchived} />
      ) : (
        <QueryGate query={pipelinesQuery}>
          {() =>
            effectivePipeline ? (
              <QueryGate query={dealsQuery}>
                {(page) => {
                  const columns = buildColumns(
                    effectivePipeline.stages ?? [],
                    page.data,
                  );
                  return view === "board" ? (
                    <PipelineBoard
                      columns={columns}
                      onOpen={openDeal}
                      cardDragHandlers={cardDragHandlers}
                      columnDropHandlers={columnDropHandlers}
                    />
                  ) : (
                    <DealTable
                      deals={page.data}
                      stages={effectivePipeline.stages ?? []}
                    />
                  );
                }}
              </QueryGate>
            ) : null
          }
        </QueryGate>
      )}
      {advance.isError && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 10 }}
        >
          {advance.error instanceof Error ? advance.error.message : null}
        </p>
      )}
      {toast && (
        <div className="toast-region">
          <output className="toast">
            <span className="dot dot-auto" />
            {toast}
          </output>
        </div>
      )}
      <Modal
        open={pending !== null}
        onClose={() => setPending(null)}
        labelledBy="advance-title"
      >
        {pending && (
          <>
            <p className="t-sub" id="advance-title">
              <AutonomyDot tier={verbTier("progress_deal", tierMap)} />{" "}
              {t("deals.confirmAdvance", { stage: pending.toStage.name })}
            </p>
            <p className="t-caption" style={{ marginTop: 6 }}>
              {t("deals.confirmTerminal", { status: pending.toStage.semantic })}
            </p>
            {pending.toStage.semantic === "lost" && (
              <div className="field" style={{ marginTop: 10 }}>
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
              <Button onClick={() => setPending(null)}>
                {t("deals.cancel")}
              </Button>
              <Button
                variant="primary"
                disabled={
                  pending.toStage.semantic === "lost" &&
                  lostReason.trim() === ""
                }
                onClick={() => {
                  advance.mutate({
                    dealId: pending.dealId,
                    toStage: pending.toStage,
                    lostReason: lostReason.trim() || undefined,
                  });
                  setPending(null);
                }}
              >
                {t("deals.confirm")}
              </Button>
            </div>
          </>
        )}
      </Modal>
    </div>
  );
}

function DealTable({
  deals,
  stages,
}: Readonly<{ deals: Deal[]; stages: Stage[] }>) {
  const t = useT();
  const { locale } = useLocale();
  const [sortKey, setSortKey] = useState<"name" | "amount" | "close">("name");
  const [descending, setDescending] = useState(false);
  const stageName = useMemo(
    () => new Map(stages.map((stage) => [stage.id, stage.name])),
    [stages],
  );

  const sorted = useMemo(() => {
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
  }, [deals, sortKey, descending]);

  const sortBy = (key: typeof sortKey) => {
    if (key === sortKey) {
      setDescending((value) => !value);
    } else {
      setSortKey(key);
      setDescending(false);
    }
  };

  return (
    <div>
      <div style={{ display: "flex", gap: 6, marginBottom: 8 }}>
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
        rows={sorted}
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
  fxRateToBase,
  fxRateDate,
  locale,
}: Readonly<{
  amountMinor: number;
  fxRateToBase: string;
  fxRateDate: string | null;
  locale: Locale;
}>) {
  const t = useT();
  const baseMinor = Math.round(amountMinor * Number(fxRateToBase));
  return (
    <p className="t-caption">
      {t("deal.fxBase", {
        value: formatMoney(baseMinor, "EUR", locale),
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
  openStages,
}: Readonly<{ dealId: string; openStages: Stage[] }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);
  const [stageId, setStageId] = useState<string | null>(null);
  const reopen = useMutation({
    mutationFn: async (toStageId: string) => {
      const { data, error } = await api.POST("/deals/{id}/advance", {
        params: { path: { id: dealId } },
        body: { to_stage_id: toStageId, status: "open" },
      });
      if (error) {
        throw new Error(problemMessage(error));
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
      <Button small data-testid="reopen-open" onClick={() => setOpen(true)}>
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
            {reopen.error instanceof Error ? reopen.error.message : null}
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
                reopen.mutate(stageId);
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
// archived deal is read-only (no edit/merge/archive path exists server-side
// for a non-live row), so it renders the status badge alone.
function DealBadges({
  deal,
  orgs,
  meId,
  openStages,
}: Readonly<{
  deal: Deal;
  orgs: { id: string; display_name: string }[];
  meId: string;
  openStages: Stage[];
}>) {
  const t = useT();
  const cf = useObjectCustomFields("deal");
  // Edit/archive/reopen/share are all hidden in overlay; only the status
  // badge (a read) stays. Edit/archive/reopen write to a mirrored deal
  // (unsupported_by_sor). Share is hidden too: a record grant probes the
  // native deal row (auth.EnsureLinkTarget), which a mirror deal has no row
  // in, so the grant 404s — and overlay visibility is governed by
  // mirror_visibility, which record_grant does not feed.
  const overlay = useSorMode() === "overlay";
  if (deal.archived_at != null) {
    return <Badge tone={dealStatusTone(deal.status)}>{deal.status}</Badge>;
  }
  return (
    <>
      <Badge tone={dealStatusTone(deal.status)}>{deal.status}</Badge>
      {!overlay && (
        <>
          <EditAction
            label={t("deal.edit")}
            fields={[
              ...dealEditFields(t, {
                orgs,
                me: meId,
                currentOwner: deal.owner_id ?? null,
                currency: deal.currency ?? "EUR",
              }),
              ...cf.formFields,
            ]}
            record={{
              id: deal.id,
              version: deal.version,
              name: deal.name,
              amount:
                deal.amount_minor != null
                  ? String(deal.amount_minor / 100)
                  : "",
              currency: deal.currency ?? "EUR",
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
                params: { path: { id: deal.id }, ...ifMatch(deal.version) },
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
        </>
      )}
      {!overlay && <ShareAction recordType="deal" recordId={deal.id} />}
      {!overlay && (deal.status === "won" || deal.status === "lost") && (
        <ReopenAction dealId={deal.id} openStages={openStages} />
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
  if (approvals.length === 0) {
    return null;
  }
  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader title={t("deal.pendingApprovals")} />
      {approvals.map((approval) => (
        <div
          key={approval.id}
          className="staging-card"
          style={{ marginBottom: 8 }}
        >
          <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
            <AutonomyDot tier={approvalDotTier(approval.kind, tierMap)} />
            <span className="t-label">{approval.kind}</span>
            <span className="t-small">{approval.proposed_by}</span>
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
    </section>
  );
}

function OffersPanel({
  offers,
  creating,
  locale,
  onCreate,
}: Readonly<{
  offers: Offer[] | undefined;
  creating: boolean;
  locale: Locale;
  onCreate: () => void;
}>) {
  const t = useT();
  // Offers are read (and created) against a mirrored deal — the list read 404s
  // and creation would write, both refused in overlay. Show the honest
  // unavailable state instead of an empty panel with a New-offer button.
  const overlay = useSorMode() === "overlay";
  if (overlay) {
    return (
      <section className="card" style={{ marginBottom: "var(--space-4)" }}>
        <SectionHeader title={t("deal.offers")} />
        <OverlayUnavailable />
      </section>
    );
  }
  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <div className="list-head">
        <SectionHeader title={t("deal.offers")} />
        <Button small disabled={creating} onClick={onCreate}>
          {t("deal.newOffer")}
        </Button>
      </div>
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
    </section>
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
  onCreateOffer,
  overlay,
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
  onCreateOffer: () => void;
  overlay: boolean;
}>) {
  const t = useT();
  return (
    <>
      {deal.fx_rate_to_base != null && (
        <FxLine
          amountMinor={deal.amount_minor ?? 0}
          fxRateToBase={deal.fx_rate_to_base}
          fxRateDate={deal.fx_rate_date ?? null}
          locale={locale}
        />
      )}
      {stages.length > 0 && (
        <nav className="stepper" aria-label={t("deals.stage")}>
          {stages.map((stage) => (
            <span
              key={stage.id}
              className={stage.id === deal.stage_id ? "step current" : "step"}
              aria-current={stage.id === deal.stage_id ? "step" : undefined}
            >
              {stage.name}
            </span>
          ))}
        </nav>
      )}
      <DealApprovals approvals={dealApprovals} decide={onDecide} />
      {/* Stakeholders are a relationship read the mirror does not serve. In
          overlay show the honest unavailable state (never any cached native
          rows), matching the timeline and offers panels. */}
      {overlay ? (
        <section className="card" style={{ marginBottom: "var(--space-4)" }}>
          <SectionHeader title={t("deal.stakeholders")} />
          <OverlayUnavailable />
        </section>
      ) : (
        stakeholders &&
        stakeholders.length > 0 && (
          <section className="card" style={{ marginBottom: "var(--space-4)" }}>
            <SectionHeader title={t("deal.stakeholders")} />
            <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
              {stakeholders.map((stakeholder) => (
                <Badge key={stakeholder.id}>
                  {stakeholder.role ??
                    stakeholder.person_id ??
                    stakeholder.kind}
                </Badge>
              ))}
            </div>
          </section>
        )
      )}
      <OffersPanel
        offers={offers}
        creating={creatingOffer}
        locale={locale}
        onCreate={onCreateOffer}
      />
      <CustomFieldsCard object="deal" record={deal} />
      <RecordContextPanel entityType="deal" id={deal.id} />
      <LogActivity entityType="deal" entityId={deal.id} />
    </>
  );
}

export function DealScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<DealTab>("overview");
  const dealQuery = useQuery({
    queryKey: ["deal", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/deals/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const pipelineQuery = usePipeline();
  const me = useMe();
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
        throw new Error(problemMessage(error));
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
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const approvalsQuery = useQuery({
    queryKey: ["approvals", "pending"],
    queryFn: async () => {
      const { data, error } = await api.GET("/approvals", {
        params: { query: { status: "pending", limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const timelineQuery = useQuery({
    queryKey: ["activities", "deal", id],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: { query: { entity_type: "deal", entity_id: id, limit: 20 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
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
        throw new Error(problemMessage(error));
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
        throw new Error(problemMessage(error));
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
        throw new Error(problemMessage(error));
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
                />
              }
              timeline={
                timelineQuery.isSuccess
                  ? activityTimeline(timelineQuery.data.data, (activity) => (
                      <TimelineActions
                        activity={activity}
                        entityType="deal"
                        entityId={id}
                      />
                    ))
                  : []
              }
              timelineNotice={overlay ? <OverlayUnavailable /> : undefined}
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
                  onCreateOffer={() =>
                    createOffer.mutate(deal.currency ?? "EUR")
                  }
                  overlay={overlay}
                />
              )}
              {tab === "history" && !overlay && (
                <RecordHistoryTab kind="deal" id={deal.id} />
              )}
              {tab === "history" && overlay && <OverlayUnavailable />}
            </RecordView>
          );
        }}
      </QueryGate>
    </div>
  );
}
