import {
  useInfiniteQuery,
  useMutation,
  useQueries,
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
import type { ListChip } from "../design-system/listsurface";
import type { ListColumn, ListSelection } from "../design-system/listtable";
import { FieldGuard } from "../design-system/rbac";
import {
  useRecordTimeline,
  useTimelineFilters,
} from "../design-system/recordtimeline";
import { Select } from "../design-system/select";
import { TimelineFilterBar } from "../design-system/timelinefilterbar";
import { type Toast, ToastRegion, useToast } from "../design-system/toast";
import { AutonomyDot, ProvenanceTag } from "../design-system/trust";
import {
  formatDate,
  formatDuration,
  formatMoney,
  formatMoneyOrAbsent,
} from "../format/format";
import { toMajorUnits, toMinorUnits } from "../format/minorunits";
import { RECORD_ZONE } from "../format/timezone";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { dealWinKeys } from "./activitykeys";
import { approvalKindLabel } from "./approvalkind";
import { ArchiveAction } from "./archive";
import {
  LoadMoreButton,
  OverlayUnavailable,
  problemFieldErrorsOf,
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
import { DealHealthCard } from "./dealhealth";
import { DealNextAction } from "./dealnextaction";

// The deal page's aside: the next move first, then the Deal Room when the deal
// has one. Its own component so the screen's render stays readable.
function DealAside({
  dealId,
  dealName,
}: Readonly<{ dealId: string; dealName: string }>) {
  return (
    <>
      <DealNextAction dealId={dealId} />
      <DealNextMeeting dealId={dealId} />
      <DealBriefCard dealId={dealId} />
      <DealHealthCard dealId={dealId} />
      <DealRoomAside dealId={dealId} dealName={dealName} />
    </>
  );
}

import { DealBriefCard } from "./dealbrief";
import { DealFiles } from "./dealfiles";
import { DealNextMeeting } from "./dealmeeting";
import {
  DealProjectChip,
  dealProjectFields,
  resolveDealProject,
  StartDeliveryPrompt,
  useOpenProjects,
} from "./dealproject";
import { DealRoomAside } from "./dealroom";
import { EditAction } from "./edit";
import { EntityRef, useEntityName } from "./entityref";
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
import { groupChronology } from "./timelinegroups";

// Deal surfaces (B-EP09.11a/b/c): the five-stage Kanban with drag-to-advance
// (terminal stages are a 🟡 confirm, AC-deal-6), the board↔table segmented
// control over the SAME fetched set (no reload), and the deal 360 with the
// stage stepper and the live pending-approval staged cards. Weighting math
// stays out of the UI beyond same-currency page-local sub-lines: a mixed-
// currency column renders no sum (the FX rule: never sum native minors
// across currencies).

type Deal = components["schemas"]["Deal"];
type Organization = components["schemas"]["Organization"];
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

/** A company's display name and the mark drawn beside it. */
type OrgMark = { name: string; logoUrl?: string | null };

/**
 * Every company the loaded deals name, id → mark (`useOrgMarks` resolves them).
 *
 * A company this reader may not read is in no map: the wire sends
 * `organization_id` as null and names it in `masked_fields`, so what the card
 * needs there is the withheld READING, which the card itself spells as the mask
 * — not a name this screen could supply.
 */
export type CompanyMarks = ReadonlyMap<string, OrgMark>;

/**
 * What the screen knows about the companies its deals name.
 *
 * `unreadable` is the reading the board used to lose. A read that FAILED — a
 * 403 because the reader holds row visibility of the company but no
 * `organization:read` grant, a 5xx, a dropped connection — is not the same fact
 * as a deal that names no company, and collapsing the two told the reader the
 * most misleading of the two. The table has always had this reading through
 * `EntityRef`'s failed state; this is the board's half of it.
 */
export type CompanyNaming = Readonly<{
  marks: CompanyMarks;
  unreadable: ReadonlySet<string>;
}>;

/**
 * What a deal's company reads as on its card, in the four readings it has.
 *
 * Withheld carries the mask, the same control the table's company cell draws. A
 * company the screen has a name for is named. A company whose read FAILED says
 * so, because a deal that names a company the reader could not fetch is not a
 * deal with no company. Only a deal naming no company draws nothing.
 *
 * A name still in flight also draws nothing rather than a uuid: the card's
 * company line is a name a reader recognises and an id is not one. That is the
 * one case where an empty slot is a wait rather than a claim, and it resolves
 * itself.
 */
function dealCompany(
  deal: Deal,
  naming: CompanyNaming,
): Pick<BoardDeal, "org" | "orgLogoUrl" | "orgWithheld" | "orgUnreadable"> {
  if (deal.masked_fields?.includes("organization_id")) {
    return { org: "", orgWithheld: true };
  }
  if (deal.organization_id && naming.unreadable.has(deal.organization_id)) {
    return { org: "", orgUnreadable: true };
  }
  const mark = deal.organization_id
    ? naming.marks.get(deal.organization_id)
    : undefined;
  return { org: mark?.name ?? "", orgLogoUrl: mark?.logoUrl };
}

function toBoardDeal(deal: Deal, naming: CompanyNaming): BoardDeal {
  const since = deal.last_activity_at ?? deal.created_at;
  return {
    id: deal.id,
    name: deal.name,
    ...dealCompany(deal, naming),
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
type CreateDealRequest = components["schemas"]["CreateDealRequest"];

// One deal as the edit form's initial values. Extracted from the badge row
// that renders the form: mapping a record onto form fields is its own job, and
// keeping it inline made a component that already draws badges, an edit dialog
// and an archive verb carry a twentieth concern.
//
// Every absent value becomes "" rather than a default. A currency the FORM
// chose is a currency the SAVE writes, so seeding one made an unpriced deal
// acquire it the moment a reader edited its name — and since amount and
// currency are paired by CHECK, that turned an innocent rename into a refusal.
function dealEditRecord(deal: Deal): Record<
  string,
  string | number | undefined
> & {
  id: string;
  version?: number;
} {
  return {
    id: deal.id,
    version: deal.version,
    name: deal.name,
    // The currency's own scale, not a hundred. amount_minor and currency are
    // NULL together (the deal_amount_currency_pair CHECK), so a priced deal
    // always carries the code — but a row that WITHHELD the currency for row
    // scope while sending the amount would be scaled at the two-digit default
    // and shown wrong. There is no honest figure without its unit, so the field
    // stays empty rather than guessing one.
    amount:
      deal.amount_minor != null && deal.currency
        ? String(toMajorUnits(deal.amount_minor, deal.currency))
        : "",
    currency: deal.currency ?? "",
    owner_id: deal.owner_id ?? "",
    organization_id: deal.organization_id ?? "",
    partner_org_id: deal.partner_org_id ?? "",
    partner_attribution: deal.partner_attribution ?? "",
    forecast_category: deal.forecast_category ?? "",
    expected_close_date: deal.expected_close_date ?? "",
    wait_until: deal.wait_until ?? "",
    project_id: deal.project_id ?? "",
  };
}

// The edit form's values are typed `unknown` per key; the project resolver
// reads the three it needs as strings, and anything else is not one.
function stringValues(values: Record<string, unknown>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [key, value] of Object.entries(values)) {
    if (typeof value === "string") {
      out[key] = value;
    }
  }
  return out;
}

// The attribution the form may send, narrowed the same way the forecast
// category is: the wire type is a closed vocabulary, and a free string from a
// form input is not one until something checks it.
function partnerAttribution(
  v: string,
): UpdateDealRequest["partner_attribution"] {
  switch (v) {
    case "sourced":
      return "sourced";
    case "influenced":
      return "influenced";
    default:
      return null;
  }
}

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

/**
 * The edit form's values as a deal patch.
 *
 * A blank scalar clears the field on the wire (explicit null); a set value
 * trims through. `amount` arrives in major units from the form, the wire is
 * minor units (deal creation applies the same conversion above).
 *
 * `masked` names the fields THIS reader was not shown. A withheld reference
 * arrives as null with `masked_fields` naming it — deliberately, so a reader
 * can tell "you may not see this" from "there is nothing here" — but the form
 * has only the null, and sending it back would ask the server to clear a
 * partner the reader never saw and never touched. So a masked field is left
 * out of the patch entirely: omitted means unchanged.
 */
export function mapDealUpdate(
  values: Record<string, unknown>,
  masked: readonly string[] = [],
): UpdateDealRequest {
  const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");
  const amount = str(values.amount);
  const owner = str(values.owner_id);
  const forecast = str(values.forecast_category);
  const currency = str(values.currency);
  const patch: UpdateDealRequest = {
    name: str(values.name) || undefined,
    amount_minor: amount ? toMinorUnits(Number(amount), currency) : null,
    currency: currency || undefined,
    organization_id: str(values.organization_id) || null,
    owner_id: owner || null,
    partner_org_id: str(values.partner_org_id) || null,
    partner_attribution: partnerAttribution(str(values.partner_attribution)),
    forecast_category: forecastCategory(forecast),
    expected_close_date: str(values.expected_close_date) || null,
    wait_until: str(values.wait_until) || null,
    // Resolved by the screen before mapping: the "new project" answer has
    // already become an id by the time the patch is built.
    project_id: str(values.project_id) || null,
  };
  return withoutMasked(patch, masked);
}

/**
 * The patch minus every field the reader was not shown.
 *
 * `partner_org_id` carries its attribution: the server withholds the pair
 * together, so returning one half of it would decide what a partner nobody
 * could see is owed.
 */
function withoutMasked(
  patch: UpdateDealRequest,
  masked: readonly string[],
): UpdateDealRequest {
  if (masked.length === 0) {
    return patch;
  }
  const out: Record<string, unknown> = { ...patch };
  for (const field of masked) {
    delete out[field];
    if (field === "partner_org_id") {
      delete out.partner_attribution;
    }
    if (field === "amount_minor") {
      delete out.currency;
    }
  }
  return out as UpdateDealRequest;
}

/**
 * The create form's values as the deal-birth body.
 *
 * A deal names its partner at birth rather than only through a later edit: the
 * win that pays the partner can land before anybody revisits the record, and
 * commission accrues on a `sourced` attribution alone. Both partner fields
 * travel here for the same reason the update body carries them — a create that
 * quietly dropped them told the caller its write had succeeded while the
 * partner was gone.
 */
export function mapDealCreate(
  values: Record<string, unknown>,
  pipelineId: string,
): CreateDealRequest {
  const str = (v: unknown) => (typeof v === "string" ? v.trim() : "");
  const amount = str(values.amount);
  const currency = str(values.currency) || "EUR";
  return {
    name: str(values.name),
    pipeline_id: pipelineId,
    stage_id: str(values.stage_id),
    // The UI takes major units; the wire is minor units, at the scale THIS
    // currency carries — a dong has none, so multiplying by a hundred here
    // priced the deal a hundredfold and nothing downstream could tell.
    amount_minor: amount ? toMinorUnits(Number(amount), currency) : null,
    currency,
    organization_id: str(values.organization_id) || null,
    partner_org_id: str(values.partner_org_id) || null,
    // The empty option means the caller made no claim, and null is how that
    // travels: the server then reads a named partner as `sourced`, which is
    // what the option says it does. An attribution naming no partner is
    // refused 422 rather than defaulted — there would be nobody to credit.
    partner_attribution: partnerAttribution(str(values.partner_attribution)),
    expected_close_date: str(values.expected_close_date) || null,
    project_id: str(values.project_id) || null,
    source: "manual",
  };
}

const FORECAST_OPTIONS: { value: string; label: MessageKey }[] = [
  { value: "commit", label: "deal.fcCommit" },
  { value: "best_case", label: "deal.fcBestCase" },
  { value: "pipeline", label: "deal.fcPipeline" },
  { value: "omitted", label: "deal.fcOmitted" },
];

// What a partner did for the deal. Only "sourced" earns commission — a partner
// who helped a deal we already had is recorded, not paid.
//
// The empty value leads and says what leaving it unset MEANS. The server
// defaults a named partner to "sourced", so a bare "Unset" here would let a
// reader believe they had made no claim while the deal quietly became
// commission-eligible. A field that offers the empty value in its own words
// suppresses the generic entry (create.tsx), which is why this one is spelled
// out rather than inherited.
const ATTRIBUTION_OPTIONS: { value: string; label: MessageKey }[] = [
  { value: "", label: "deal.attributionUnset" },
  { value: "sourced", label: "deal.attributionSourced" },
  { value: "influenced", label: "deal.attributionInfluenced" },
];

/**
 * The companies a deal may name as the partner who brought it.
 *
 * Only companies that ARE partners: the picker once offered every
 * organization, which let a deal be attributed to an ordinary customer — and
 * an attribution to a company with no partner row has no margin tier behind
 * it, so it looks attributed and silently never earns anything.
 *
 * An empty list is also the answer to "is there a partner programme here": an
 * installation that has never made a company a partner has no partner
 * question to ask, and the two fields stay off the form entirely.
 */
export function usePartnerOptions(
  orgs: { id: string; display_name: string }[],
): { value: string; label: string }[] {
  const partners = useQuery({
    queryKey: ["partners", "options"],
    queryFn: async () => {
      const { data, error } = await api.GET("/partners", {
        params: { query: { limit: 200 } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data;
    },
    staleTime: 60_000,
  });
  const named = new Map(orgs.map((org) => [org.id, org.display_name]));
  return (partners.data ?? []).flatMap((partner) => {
    const label = named.get(partner.organization_id);
    // A partner whose organization the caller cannot read is left out rather
    // than offered as a bare id: picking it would name a company the picker
    // could not show them.
    return label ? [{ value: partner.organization_id, label }] : [];
  });
}

/**
 * The two fields that record a deal coming through a partner, for both the
 * create form and the edit form — one spelling, so the two cannot drift.
 *
 * Nothing at all when the installation has no partners AND the deal names
 * none: an empty picker asks a question with no answers in it, and a form that
 * shows partner controls to a company running no partner programme is claiming
 * a feature it does not have.
 *
 * `attributed` is what keeps that from eating data. A form omits nothing a
 * record already carries: the pickable list is one capped page of companies, so
 * a deal's own partner can be missing from it, and dropping the field would
 * blank the stored partner the next time anybody edited anything else on the
 * deal — taking the commission attribution with it, silently.
 *
 * The attribution follows the partner rather than standing beside it. What a
 * partner DID is a question about a partner, so it appears once one is named
 * and disappears when the choice is cleared — asking it first offers a claim
 * with nobody to attach it to.
 */
function partnerFields(
  t: (k: MessageKey) => string,
  partnerOptions: { value: string; label: string }[],
  attributed?: { id: string; label: string },
  // The reader was not shown this deal's partner (`masked_fields` names it).
  // Checked BEFORE the empty-list exit: a withheld partner is a fact about
  // this deal, so it is stated whether or not the installation has partners
  // left to offer.
  withheld = false,
): CreateField[] {
  if (withheld) {
    // One entry, and it names the field as withheld. A picker seeded blank
    // over a partner the reader never saw invites them to choose a different
    // one, and the attribution travels with the partner — so there is nothing
    // here to attribute either. `mapDealUpdate` leaves both out of the patch.
    return [
      {
        key: "partner_org_id",
        label: "deal.partnerOrg",
        type: "select",
        options: [{ value: "", label: t("deal.partnerWithheld") }],
      },
    ];
  }
  if (partnerOptions.length === 0 && !attributed) {
    return [];
  }
  // The deal's own partner is always among the choices, even when the capped
  // page of partners this form could offer does not include it. A select whose
  // stored value is not an option shows blank, and saving that blank clears the
  // partner nobody meant to touch.
  const options =
    attributed && !partnerOptions.some((o) => o.value === attributed.id)
      ? [{ value: attributed.id, label: attributed.label }, ...partnerOptions]
      : partnerOptions;
  return [
    {
      key: "partner_org_id",
      label: "deal.partnerOrg",
      type: "select",
      options,
    },
    // Commission accrues on "sourced" only, so this is the field that decides
    // whether a win pays the partner.
    {
      key: "partner_attribution",
      label: "deal.partnerAttribution",
      type: "select",
      showWhen: (values) => Boolean(values.partner_org_id),
      options: ATTRIBUTION_OPTIONS.map((o) => ({
        value: o.value,
        label: t(o.label),
      })),
    },
  ];
}

/**
 * The partner a deal already names, ready to stand as its own option.
 *
 * Falls back to the raw id when the org list cannot name it — an unreadable or
 * off-page company. Ugly, and correct: the alternative is a blank select whose
 * save clears an attribution the reader never touched.
 */
function attributedPartner(
  deal: Deal,
  orgs: { id: string; display_name: string }[],
): { id: string; label: string } | undefined {
  if (!deal.partner_org_id) {
    return undefined;
  }
  const named = orgs.find((org) => org.id === deal.partner_org_id);
  return {
    id: deal.partner_org_id,
    label: named?.display_name ?? deal.partner_org_id,
  };
}

/**
 * The company picker on a deal's edit form, in the three readings a stored
 * company has.
 *
 * Withheld is the reading that offers no company at all. The reader was not
 * shown which company this is, so a picker seeded blank invites them to pick
 * one and silently re-point the deal away from the company a colleague linked;
 * its single entry names the field as withheld instead, and `mapDealUpdate`
 * leaves the field out of the patch entirely.
 *
 * A company the pickable page cannot reach is still an option, for the reason
 * the partner field carries the same rule: a select whose stored value is not
 * among its options shows blank, and saving that blank clears a link nobody
 * meant to touch.
 */
function companyEditField(
  t: (k: MessageKey) => string,
  opts: {
    orgs: { id: string; display_name: string }[];
    masked: readonly string[];
    currentCompany?: { id: string; label: string };
  },
): CreateField {
  if (opts.masked.includes("organization_id")) {
    return {
      key: "organization_id",
      label: "create.organization",
      type: "select",
      options: [{ value: "", label: t("deal.companyWithheld") }],
    };
  }
  const options = opts.orgs.map((org) => ({
    value: org.id,
    label: org.display_name,
  }));
  const current = opts.currentCompany;
  return {
    key: "organization_id",
    label: "create.organization",
    type: "select",
    options:
      current && !options.some((option) => option.value === current.id)
        ? [{ value: current.id, label: current.label }, ...options]
        : options,
  };
}

export function dealEditFields(
  t: (k: MessageKey) => string,
  opts: {
    orgs: { id: string; display_name: string }[];
    partnerOptions: { value: string; label: string }[];
    // The partner this deal ALREADY names, when it names one. Keeps the field
    // (and its stored value) on a form whose pickable list does not reach it.
    attributedPartner?: { id: string; label: string };
    // The company this deal already names, resolved even when the pickable
    // page does not reach it — the same rule as `attributedPartner`, for the
    // same reason.
    currentCompany?: { id: string; label: string };
    // The fields of THIS deal the reader was not shown, as `masked_fields`
    // named them. A withheld reference is offered as withheld rather than as
    // an empty picker.
    masked: readonly string[];
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
    companyEditField(t, opts),
    // Both partner fields are absent where no company has been made a partner:
    // there is no partner programme to attribute anything to, and an empty
    // picker is a question the reader cannot answer.
    ...partnerFields(
      t,
      opts.partnerOptions,
      opts.attributedPartner,
      opts.masked.includes("partner_org_id"),
    ),
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

/**
 * The company marks the board draws, for every company its cards name.
 *
 * The create form's picker reads ONE capped page of organizations, and the
 * board took its marks from exactly that page — so a deal whose company fell
 * outside it drew a card with no company row at all, which a reader reads as a
 * deal nobody has linked. The set that has to be resolvable is the set the
 * loaded deals actually name, so the ids that page did not cover are read one
 * at a time and cached per id: reading the same board again, or scrolling back
 * over the same companies, costs no further request.
 *
 * A withheld company is never among them — the wire sends no id to read — so
 * this cannot turn a mask into a name.
 */
function useOrgMarks(
  deals: Deal[],
  page: Organization[],
  pageSettled: boolean,
): CompanyNaming {
  const fromPage = new Map<string, OrgMark>(
    page.map((org) => [
      org.id,
      { name: org.display_name, logoUrl: org.logo_url },
    ]),
  );
  // Nothing is fanned out until the picker's page has ANSWERED. The two reads
  // are issued together and settle in no fixed order, so on every render where
  // the deals have arrived and the organizations have not, `fromPage` is empty
  // and every company a loaded deal names looks unresolved — one request each,
  // for a page that is about to answer most of them. A cold board paint fired
  // up to a hundred, and nothing un-sends a request.
  const unnamed = pageSettled
    ? [
        ...new Set(
          deals.flatMap((deal) =>
            deal.organization_id && !fromPage.has(deal.organization_id)
              ? [deal.organization_id]
              : [],
          ),
        ),
      ]
    : [];
  const reads = useQueries({
    queries: unnamed.map((id) => ({
      queryKey: ["organizations", "mark", id],
      queryFn: async (): Promise<OrgMark | null> => {
        const { data, error, response } = await api.GET("/organizations/{id}", {
          params: { path: { id } },
        });
        if (error) {
          // A 404 is an ANSWER — the company is archived, or row scope hides
          // its existence from this reader — and no retry turns it into a
          // name, so the card has no company to draw. Every other failure is a
          // read that never arrived and throws, so it is held as an error
          // rather than settled as an absence. The same rule the shared
          // reference resolver states (screens/entityref.tsx).
          if (response.status === 404) {
            return null;
          }
          throwProblem(error);
        }
        return { name: data.display_name, logoUrl: data.logo_url };
      },
      // A company's name and mark change far more rarely than the board
      // refetches, so a card that already has one does not ask again.
      staleTime: 60_000,
    })),
  });
  const marks = new Map(fromPage);
  const unreadable = new Set<string>();
  reads.forEach((read, index) => {
    const id = unnamed[index];
    if (!id) {
      return;
    }
    if (read.data) {
      marks.set(id, read.data);
      return;
    }
    // The error the queryFn deliberately threw rather than settling as an
    // absence. Read here, or the card it belongs to says "no company" — which
    // is the one thing this read exists to stop it saying.
    if (read.isError) {
      unreadable.add(id);
    }
  });
  return { marks, unreadable };
}

export function buildColumns(
  stages: Stage[],
  deals: Deal[],
  totals: Map<string, StageTotals>,
  naming: CompanyNaming,
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
        deals: stageDeals.map((deal) => toBoardDeal(deal, naming)),
        // The true count, not the loaded page's — falls back to the page
        // count while totals are still loading, so the column shows SOME
        // number rather than a misleading 0.
        count: stageTotals?.count ?? stageDeals.length,
        sumHidden: stageTotals?.sumHidden ?? false,
      };
    });
}

/**
 * One of a deal's company references, as a table cell.
 *
 * Three readings, and only the last of them is blank. Withheld reads as
 * withheld: the wire sends the id as null and names the field in
 * `masked_fields`, so the null is a refusal rather than an absence, and a cell
 * that drew nothing would state the opposite. A reference the reader may see
 * resolves to the company's name. A deal that names no company has an empty
 * cell, which is the one case where empty is the truth.
 *
 * `asText` because the row is already the link to the deal: a control nested
 * inside one is invalid markup, and the second route would go where the first
 * one already goes.
 */
function CompanyCell({
  deal,
  field,
}: Readonly<{ deal: Deal; field: "organization_id" | "partner_org_id" }>) {
  if (deal.masked_fields?.includes(field)) {
    return <FieldGuard mode="masked" />;
  }
  const id = deal[field];
  if (!id) {
    return null;
  }
  return <EntityRef kind="organization" id={id} asText />;
}

// The amount's three readings, in one place because two tables draw them: the
// native list and the overlay mirror. Withheld is not empty — the mirror
// carries the same `masked_fields` the native list does, and reading a refused
// amount as an unpriced deal is the defect this cell exists to prevent.
function AmountCell({
  deal,
  locale,
}: Readonly<{ deal: Deal; locale: Locale }>) {
  if (deal.masked_fields?.includes("amount_minor")) {
    return <FieldGuard mode="masked" />;
  }
  if (deal.amount_minor == null || !deal.currency) {
    return null;
  }
  return (
    <span className="t-mono">
      {formatMoney(deal.amount_minor, deal.currency, locale)}
    </span>
  );
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
      // The company the deal is with. Withheld is not empty: the wire sends a
      // null `organization_id` and names the field in `masked_fields` when the
      // reader may not read that company, and a blank cell cannot be told
      // apart from a deal nobody has linked.
      //
      // No `sort`, for the reason the partner column below carries none: the
      // API's sortable vocabulary does not include it, and a header that
      // looked sortable and refused would be worse than one that never
      // offered.
      key: "company",
      header: t("create.organization"),
      cell: (deal) => <CompanyCell deal={deal} field="organization_id" />,
    },
    {
      // Which partner brought the deal, when one did. Optional: a workspace
      // that runs no partner programme has an empty column, and hiding it
      // per-row is worse in a list than an empty cell — a column that comes
      // and goes cannot be scanned down.
      //
      // It carries no `sort`, because the API's sortable vocabulary is a fixed
      // five-field set that does not include it. That limitation is not this
      // column's to fix (see the sorting issue), and a header that looked
      // sortable and refused would be worse than one that never offered.
      key: "partner",
      header: t("deal.partnerOrg"),
      cell: (deal) => <CompanyCell deal={deal} field="partner_org_id" />,
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
      cell: (deal) => <AmountCell deal={deal} locale={locale} />,
    },
    {
      key: "close",
      header: t("deals.close"),
      sort: "expected_close_date",
      cell: (deal) =>
        deal.expected_close_date
          ? formatDate(deal.expected_close_date, locale, RECORD_ZONE)
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

/**
 * The closed vocabulary for winning a deal with no contract behind it.
 *
 * The type comes from the generated contract, and the labels are a Record over
 * it, so adding a member to `crm.yaml` stops this file compiling until the new
 * member has a label — rather than leaving a choice the server accepts and no
 * screen offers.
 */
type WonReason = NonNullable<
  NonNullable<
    components["schemas"]["AdvanceDealRequest"]["won_without_contract_reason"]
  >
>;

const WON_REASON_LABELS: Record<WonReason, MessageKey> = {
  purchase_order: "deals.winReasonPurchaseOrder",
  verbal: "deals.winReasonVerbal",
  renewal_by_email: "deals.winReasonRenewalByEmail",
  imported: "deals.winReasonImported",
  other: "deals.winReasonOther",
};

// Display order — deliberately not the contract's, which is a storage list.
// This one puts the answers a rep reaches for first. The Record above is what
// guarantees the set is complete; this only decides the sequence.
const WON_REASONS: readonly WonReason[] = [
  "purchase_order",
  "verbal",
  "renewal_by_email",
  "imported",
  "other",
];

// Narrows the Select's plain string back to the vocabulary. The control is
// built from WON_REASONS, so this never rejects in practice — but a cast would
// make that an assumption instead of a check, and the value goes on to be
// stored as an assertion about how a deal closed.
function asWonReason(value: string): WonReason | "" {
  const known = WON_REASONS.find((reason) => reason === value);
  return known ?? "";
}

/**
 * Whether a detail carries any visible character, matching the rule the server
 * applies (`saysSomething` in win_evidence.go).
 *
 * `trim()` alone is not the same test. A zero-width space is not whitespace to
 * either language, so a detail of "​" would pass a trim check here, enable
 * Confirm, and then be refused by the server — the reader having explained
 * precisely nothing, which is the state the vocabulary exists to prevent.
 */
function saysSomething(text: string): boolean {
  return /\P{White_Space}/u.test(text.replace(/\p{Cf}/gu, ""));
}

// The one member that explains nothing on its own, so the server demands a
// detail after it (`WonReasonDetailRequiredError`).
const WON_REASON_NEEDING_DETAIL: WonReason = "other";

// The server's refusal when a win names neither a contract nor a reason. The
// dialog keys on THIS rather than on the 422 status: an advance can be refused
// for reasons that have nothing to do with evidence, and asking "how was it
// won?" after a version conflict would be nonsense.
const WIN_EVIDENCE_REQUIRED = "win_evidence_required";

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
  // Why this win has no signed contract behind it. Absent on the ordinary win,
  // where the contract IS the answer — the server distinguishes the two, and
  // that distinction is what makes "how many won deals have no paper" a
  // question reports can answer.
  wonWithoutContractReason?: WonReason;
  wonWithoutContractDetail?: string;
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
/**
 * What a terminal advance says about HOW the deal closed, on top of the stage
 * and status every advance carries.
 *
 * A lost deal states its reason. A won deal states one only when there is no
 * signed contract to point at — the server looks for the contract first, and a
 * win that has one says nothing here, which is what keeps "won with paper" and
 * "won without it" distinguishable in reports.
 */
function closingFields(input: AdvanceInput) {
  if (input.toStage.semantic === "lost") {
    return { lost_reason: input.lostReason };
  }
  if (input.toStage.semantic === "won" && input.wonWithoutContractReason) {
    return {
      won_without_contract_reason: input.wonWithoutContractReason,
      won_without_contract_detail: input.wonWithoutContractDetail,
    };
  }
  return {};
}

function useAdvanceDeal(toast: Toast) {
  const t = useT();
  const queryClient = useQueryClient();
  return useMutation({
    // No single record: one instance serves every card on the board, and which
    // deal is moving is known at mutate() time rather than here, so the agent
    // rail names the write without naming a record (app/agentrail-copy.ts).
    mutationKey: ["deal-edit"],
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
          ...closingFields(input),
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
      // A win moves the deal's project into delivery in the same server
      // write, so the project page and list are stale the moment this
      // returns. Without this a reader who follows the project chip within
      // the 30s stale window reads a won deal on a project still being
      // pursued — the contradiction the server's one-transaction move exists
      // to prevent.
      for (const queryKey of dealWinKeys(deal)) {
        queryClient.invalidateQueries({ queryKey });
      }
      toast.show(t("deals.advanced", { stage: input.toStage.name }));
    },
  });
}

// Won reads success, lost reads danger, an open deal carries no status tone.
// Exported so the partner page's sourced-deals panel reads a deal's status the
// same way the board and the deal record do — a second mapping is how the same
// status came to render in two colours on two screens.
export function dealStatusTone(
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

  const partnerOptions = usePartnerOptions(orgsQuery.data?.data ?? []);

  const openProjects = useOpenProjects();

  const createDeal = async (values: Record<string, string>) => {
    const pipeline = effectivePipeline;
    if (!pipeline) {
      throwProblem(null);
    }
    // A project asked for on the form is born first, on the deal's company,
    // so the deal can name it at birth.
    const projectId = await resolveDealProject(
      values,
      values.organization_id?.trim() || null,
      t,
    );
    const { data, error } = await api.POST("/deals", {
      body: {
        ...mapDealCreate(
          { ...values, project_id: projectId ?? "" },
          pipeline.id,
        ),
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
        // The body of work this deal is about, chosen or started here: a
        // project begins during the deal, in its initiative phase.
        ...dealProjectFields(t, openProjects),
        // A deal brought by a partner is attributed at birth, not by editing
        // it afterwards: the win that pays them can come before anybody thinks
        // to revisit the record.
        ...partnerFields(t, partnerOptions),
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

  // The save action rides beside the dials on the table only, while READING a
  // saved view works on both: a view holds a sort as well as its filters, and
  // the board offers no way to see or change a sort — its order is the
  // pipeline's stage order. Saving from there would pin an ordering the reader
  // never chose into a view they will restore on the table.
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
  // Every company the CARDS name. The picker's capped page answers most of them
  // for free; the rest are resolved by id (useOrgMarks), so no card is left
  // standing over a company the board simply failed to look up.
  //
  // Only the board asks: the table names its companies through the same
  // per-record reference every other cross-record cell uses, and handing it
  // this map as well would read each company twice.
  const orgMarks = useOrgMarks(
    view === "board" ? loadedDeals : [],
    orgsQuery.data?.data ?? [],
    orgsQuery.isSuccess,
  );

  // The row checkboxes live inside the grid's identity cell, and the board
  // draws no rows to put one in. Offered while the board is showing, the bulk
  // bar would name rows the reader can neither see nor deselect.
  const rowSelection: ListSelection<Deal> | undefined =
    view === "board"
      ? undefined
      : {
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
              // The rows that went through leave the selection; the ones that
              // refused stay in it, named, so the reader can retry them once
              // the list has refetched their versions.
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
        };

  // The board is the surface's alternate BODY, not a surface of its own. The
  // saved views, the chips and the archived toggle all describe the ONE query
  // both views read, and a board that replaced the surface took them with it —
  // leaving the reader looking at a narrowed pipeline with nothing on screen
  // saying what narrowed it, or how to widen it again.
  //
  // It carries its own count and its own continuation control, because
  // `bodyOwnsPaging` withholds the surface's: those two belong to the paged grid
  // this body does not draw, and the board holds every card loaded so far at
  // once rather than a page of them.
  const boardBody =
    view === "board" ? (
      <>
        {dealsQuery.data && (
          <p className="t-caption">
            {t("board.count", { count: loadedDeals.length })}
          </p>
        )}
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
      </>
    ) : undefined;

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
      ) : (
        <ListTable
          state={dealsListState}
          unit="deals.unit"
          columns={dealColumns(t, locale, stageName)}
          rowKey={(deal) => deal.id}
          rowRoute={(deal) => ({ screen: "deals", id: deal.id })}
          searchable={false}
          action={createAction}
          tools={view === "board" ? dealTools : tableTools}
          body={boardBody}
          bodyOwnsPaging={view === "board"}
          // The pipeline picker is screen state, not a filter, so switching it
          // changes every row without touching `filters`. Naming it here is
          // what puts the reader back on page 1.
          scopeKey={effectivePipeline?.id ?? ""}
          dataChips={dealChips}
          dataViews={savedViews}
          selection={rowSelection}
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
        onConfirm={(input) =>
          // mutateAsync REJECTS on failure; this dialog wants the outcome, and
          // an unhandled rejection in a click handler is not one. onError still
          // runs, so the screen's own error surface is unaffected.
          advance.mutateAsync(input).then(
            () => null,
            (error: unknown) => error,
          )
        }
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
  // Resolves when the advance settles, so this dialog acts on the outcome of
  // THIS attempt. It returns the error rather than throwing it: the caller's
  // own error surface still reports the failure, and a rejection here would be
  // an unhandled one in an event handler.
  onConfirm: (input: AdvanceInput) => Promise<unknown>;
}>) {
  const t = useT();
  const tierMap = useAgentTierMap();
  const [lostReason, setLostReason] = useState("");
  const [wonReason, setWonReason] = useState<WonReason | "">("");
  const [wonDetail, setWonDetail] = useState("");
  const [submitting, setSubmitting] = useState(false);
  // The deal this dialog has been told has no contract behind it, pinned to the
  // exact attempt that was refused.
  //
  // Read off the shared mutation's `error` instead, the refusal outlives the
  // deal that earned it: cancel here, open Won on a DIFFERENT deal, and the
  // reason panel greets a deal that may well have a contract — and the server
  // takes a stated reason at its word without looking for one, so that deal is
  // recorded as won-without-paper when it was not. That falsifies the exact
  // count the reason vocabulary exists to make truthful.
  const [refusedDealId, setRefusedDealId] = useState<string | null>(null);

  // EVERY way out of this dialog clears what was typed — the buttons, Escape,
  // and the backdrop alike. The component stays mounted between openings, so a
  // reason typed and then abandoned would otherwise still be sitting there the
  // next time a deal is closed, and it would describe a different deal.
  const dismiss = () => {
    setLostReason("");
    setWonReason("");
    setWonDetail("");
    setRefusedDealId(null);
    onClose();
  };

  const needsLostReason = pending?.toStage.semantic === "lost";
  // The reason panel appears only once the server has asked for it, and only
  // for the deal it asked about. A win with a signed contract is one click,
  // exactly as before: making every rep justify a win the paperwork already
  // explains is how a required field becomes a field everyone fills with the
  // same lie.
  const needsWonReason =
    pending?.toStage.semantic === "won" && refusedDealId === pending.dealId;
  const detailMissing =
    wonReason === WON_REASON_NEEDING_DETAIL && !saysSomething(wonDetail);

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
          {needsLostReason && (
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
          {needsWonReason && (
            <WonReasonFields
              reason={wonReason}
              detail={wonDetail}
              onReason={(next) => {
                setWonReason(next);
                // The detail belongs to "Something else" alone. Kept across a
                // change of reason it would sit invisibly behind a field the
                // reader can no longer see, which is not a state they can
                // correct.
                if (next !== WON_REASON_NEEDING_DETAIL) {
                  setWonDetail("");
                }
              }}
              onDetail={setWonDetail}
            />
          )}
          <div className="actions">
            <Button onClick={dismiss}>{t("deals.cancel")}</Button>
            <Button
              variant="primary"
              disabled={
                submitting ||
                (needsLostReason && lostReason.trim() === "") ||
                (needsWonReason && (wonReason === "" || detailMissing))
              }
              onClick={async () => {
                setSubmitting(true);
                const error = await onConfirm({
                  dealId: pending.dealId,
                  version: pending.version,
                  toStage: pending.toStage,
                  lostReason: lostReason.trim() || undefined,
                  ...wonAnswer(needsWonReason, wonReason, wonDetail),
                });
                setSubmitting(false);
                // ONE refusal keeps this dialog open: the server saying this
                // win names no evidence, because the answer to that is a field
                // the reader can fill in right here. Every other outcome closes
                // it — a success has nothing left to ask, and a 403 or a 409
                // has no answer this dialog can offer, so holding it open would
                // trap the reader behind a modal whose error text renders on
                // the screen underneath it.
                if (error && winEvidenceRefused(error)) {
                  setRefusedDealId(pending.dealId);
                  return;
                }
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

/**
 * The won-without-contract answer as the advance should carry it, or nothing.
 *
 * The detail rides ONLY with the reason that needs one. Sent alongside any
 * other reason it would be stored anyway — the server writes both columns as
 * given — so a reader who typed a detail under "Something else" and then chose
 * "On a purchase order" would leave text on the deal, and in its audit trail,
 * that they had every reason to believe they had discarded when the field
 * disappeared.
 */
function wonAnswer(active: boolean, reason: WonReason | "", detail: string) {
  if (!active || reason === "") {
    return {};
  }
  const explained = reason === WON_REASON_NEEDING_DETAIL;
  return {
    wonWithoutContractReason: reason,
    wonWithoutContractDetail: explained ? detail.trim() : undefined,
  };
}

// Whether a failed advance is the server asking how a contract-less deal was
// won. Keyed on the field code, not the 422: an advance is refused for several
// reasons, and only this one has an answer the reader can give here.
function winEvidenceRefused(error: unknown): boolean {
  return problemFieldErrorsOf(error).some(
    (fault) => fault.code === WIN_EVIDENCE_REQUIRED,
  );
}

// The reason a deal was won with no paper behind it: a closed vocabulary, plus
// the free-text detail the one open-ended member needs.
function WonReasonFields({
  reason,
  detail,
  onReason,
  onDetail,
}: Readonly<{
  reason: WonReason | "";
  detail: string;
  onReason: (value: WonReason | "") => void;
  onDetail: (value: string) => void;
}>) {
  const t = useT();
  return (
    <>
      <p className="t-caption" style={{ marginTop: "var(--space-2)" }}>
        {t("deals.winNoEvidence")}
      </p>
      <div className="field" style={{ marginTop: "var(--space-2)" }}>
        <span className="t-label" id="won-reason-label">
          {t("deals.winReason")}
        </span>
        <Select
          aria-labelledby="won-reason-label"
          placeholder={t("deals.winReasonPick")}
          value={reason}
          onChange={(value) => onReason(asWonReason(value))}
          options={WON_REASONS.map((option) => ({
            value: option,
            label: t(WON_REASON_LABELS[option]),
          }))}
        />
      </div>
      {reason === WON_REASON_NEEDING_DETAIL && (
        <div className="field" style={{ marginTop: "var(--space-2)" }}>
          <span className="t-label" id="won-detail-label">
            {t("deals.winReasonDetail")}
          </span>
          <TextInput
            aria-labelledby="won-detail-label"
            value={detail}
            onChange={(event) => onDetail(event.target.value)}
          />
        </div>
      )}
    </>
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
        label={t("nav.deals")}
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
            render: (deal: Deal) => <AmountCell deal={deal} locale={locale} />,
          },
          {
            key: "close",
            header: t("deals.close"),
            render: (deal: Deal) =>
              deal.expected_close_date
                ? formatDate(deal.expected_close_date, locale, RECORD_ZONE)
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
        date: fxRateDate ? formatDate(fxRateDate, locale, RECORD_ZONE) : "—",
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
    mutationKey: ["deal-edit", dealId],
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
  // Reads the same cached partner list the deals list built, so opening Edit
  // costs no extra request.
  const partnerOptions = usePartnerOptions(orgs);
  const masked = deal.masked_fields ?? [];
  // The company this deal names, resolvable whether or not the picker's capped
  // page reached it. The page answers first; only a company it does not carry
  // is read by id, through the SAME cache entry the subtitle's own reference
  // already fills, so the common case costs nothing.
  const companyOnPage = orgs.find((org) => org.id === deal.organization_id);
  const companyById = useEntityName(
    "organization",
    companyOnPage ? null : deal.organization_id,
  );
  const currentCompany = deal.organization_id
    ? {
        id: deal.organization_id,
        // The raw id is the floor rather than the aim: ugly, and still better
        // than a blank picker whose save clears the company nobody touched.
        label:
          companyOnPage?.display_name ??
          companyById.name ??
          deal.organization_id,
      }
    : undefined;
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
  const openProjects = useOpenProjects();
  const projectById = useEntityName("project", deal.project_id);
  const currentProject = deal.project_id
    ? { id: deal.project_id, label: projectById.name ?? deal.project_id }
    : undefined;
  return (
    <>
      <Badge tone={dealStatusTone(deal.status)}>{deal.status}</Badge>
      <DealProjectChip deal={deal} />
      <EditAction
        disabledReasonId={refusedByArchive}
        label={t("deal.edit")}
        notice={overlay ? t("overlay.partialWriteBack") : undefined}
        fields={[
          ...dealEditFields(t, {
            orgs,
            partnerOptions,
            attributedPartner: attributedPartner(deal, orgs),
            currentCompany,
            masked,
            me: meId,
            currentOwner: deal.owner_id ?? null,
            // EMPTY, not a default. `dealEditFields` only uses this to put the
            // record's own currency at the head of the option list, and a deal
            // nobody has priced has none to put there.
            currency: deal.currency ?? "",
          }),
          ...(masked.includes("project_id")
            ? []
            : dealProjectFields(t, openProjects, currentProject)),
          ...cf.formFields,
        ]}
        record={{ ...dealEditRecord(deal), ...cf.recordSlice(deal) }}
        update={async (values) => {
          // The company the form SUBMITS, not the one the deal had: a
          // project started here belongs to the company the save names.
          const submitted = stringValues(values);
          const projectId = await resolveDealProject(
            submitted,
            submitted.organization_id?.trim() || null,
            t,
          );
          const { data, error } = await api.PATCH("/deals/{id}", {
            params: {
              path: { id: deal.id },
              ...ifMatch(requireVersion(deal.version)),
            },
            body: {
              ...mapDealUpdate(
                { ...values, project_id: projectId ?? "" },
                masked,
              ),
              ...cf.toBody(values),
            },
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
            label={t("deal.offers")}
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

const DEAL_TABS = ["overview", "files", "history"] as const;
type DealTab = (typeof DEAL_TABS)[number];

type Relationship = components["schemas"]["Relationship"];

// The deal 360's "overview" pane, split out of DealScreen so the tab switch
// doesn't push the render-prop closure over the cognitive-complexity budget.
// Every prop here is a value already resolved by DealScreen — no new
// fetches, no behavior change from the pre-tab layout.
/**
 * The one line of joined facts under a deal's name: what it is worth, whose
 * deal it is, and — when one brought it — which partner.
 *
 * The partner was editable in the form and rendered nowhere, so a deal that a
 * partner sourced looked identical to one we won alone. That is the fact the
 * commission is computed from, and a figure a partner is paid on has to be
 * visible on the record it came from.
 *
 * Each reference goes through EntityRef, which resolves the name and links to
 * the record — and withholds both when the reader may not open it, which is
 * why the ids are not printed as a fallback.
 */
function DealSubtitle({
  deal,
  locale,
}: Readonly<{ deal: Deal; locale: Locale }>) {
  const t = useT();
  // Joined with a visible separator rather than left as bare adjacent spans:
  // record-sub is a plain text line with no gap of its own, so three spans
  // render as one run-on string ("€48,000.00Acme Corpvia Northgate"). The
  // facts are assembled first so only the ones that exist are separated —
  // a leading or doubled "·" is how an absent company announces itself.
  //
  // A withheld fact NAMES the field it withholds. On a line of joined facts a
  // bare mask says only "something here is hidden", and the amount, the company
  // and the partner are three different things to be refused.
  const masked = deal.masked_fields ?? [];
  const facts: ReactNode[] = [];
  if (masked.includes("amount_minor")) {
    facts.push(
      <>
        {t("deals.amount")} <FieldGuard mode="masked" />
      </>,
    );
  } else if (deal.amount_minor != null && deal.currency) {
    facts.push(formatMoney(deal.amount_minor, deal.currency, locale));
  }
  if (masked.includes("organization_id")) {
    facts.push(
      <>
        {t("create.organization")} <FieldGuard mode="masked" />
      </>,
    );
  } else if (deal.organization_id) {
    facts.push(<EntityRef kind="organization" id={deal.organization_id} />);
  }
  if (masked.includes("partner_org_id")) {
    // No attribution word here: what the partner did is withheld WITH the
    // partner, so naming one would decide what a partner nobody could see is
    // owed.
    facts.push(
      <>
        {t("deal.partnerOrg")} <FieldGuard mode="masked" />
      </>,
    );
  } else if (deal.partner_org_id) {
    facts.push(
      <>
        {/* Sourced and influenced are paid differently, so the line says
            which one rather than a neutral "partner: X" that hides the
            distinction the commission turns on. */}
        {t(
          deal.partner_attribution === "influenced"
            ? "deal.partnerInfluenced"
            : "deal.partnerSourced",
        )}{" "}
        <EntityRef kind="organization" id={deal.partner_org_id} />
      </>,
    );
  }
  return (
    <>
      {facts.map((fact, i) => (
        // The index is the identity here: these are positional facts about one
        // deal, not a reorderable list, and two of them can render the same
        // company when a partner sells to itself.
        // biome-ignore lint/suspicious/noArrayIndexKey: positional facts, never reordered
        <span key={i}>
          {i > 0 && <span aria-hidden="true"> · </span>}
          {fact}
        </span>
      ))}
    </>
  );
}

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
      {/* A won deal with no project, on a company with exactly one open
          project, is offered that project once. Nothing else here asks. */}
      {!overlay && <StartDeliveryPrompt deal={deal} />}
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
  // Asked here rather than inside the aside, because an element is truthy
  // whatever it renders: a slot filled with a component that draws nothing
  // still reserves the aside column and its landmark.
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
  const [timelineFilters, setTimelineFilters] = useTimelineFilters(id);
  const timelineQuery = useRecordTimeline("deal", id, {
    filters: timelineFilters,
  });
  const timelineEntries = activityTimeline(
    timelineQuery.activities,
    viewerId,
    (activity) => (
      <TimelineActions activity={activity} entityType="deal" entityId={id} />
    ),
  );
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
              subtitle={<DealSubtitle deal={deal} locale={locale} />}
              zone={RECORD_ZONE}
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
              timeline={timelineEntries}
              timelineGroups={groupChronology(
                timelineEntries,
                timelineQuery.hasNextPage,
              )}
              timelineHeader={
                overlay ? undefined : (
                  <TimelineFilterBar
                    value={timelineFilters}
                    onChange={setTimelineFilters}
                  />
                )
              }
              timelineFooter={<LoadMoreButton query={timelineQuery} />}
              timelineNotice={timelineZoneNotice(
                { overlay, pending: timelineQuery.isPending },
                t,
              )}
              aside={
                overlay ? undefined : (
                  <DealAside dealId={id} dealName={deal.name} />
                )
              }
              asideLabel={t("nba.title")}
            >
              <div style={{ marginBottom: 16 }}>
                <SegmentedControl
                  options={DEAL_TABS}
                  value={tab}
                  onChange={setTab}
                  labels={{
                    overview: t("tab.overview"),
                    files: t("tab.documents"),
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
              {tab === "files" && !overlay && <DealFiles dealId={deal.id} />}
              {tab === "files" && overlay && <OverlayUnavailable />}
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
                onConfirm={(input) =>
                  advance.mutateAsync(input).then(
                    () => null,
                    (error: unknown) => error,
                  )
                }
              />
              <ToastRegion toast={toast} />
            </RecordView>
          );
        }}
      </QueryGate>
    </div>
  );
}
