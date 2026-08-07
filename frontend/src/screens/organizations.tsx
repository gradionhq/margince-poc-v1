import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Building2,
  CalendarClock,
  Globe,
  UserRound,
  Users,
} from "lucide-react";
import type { KeyboardEvent, ReactNode } from "react";
import { useEffect, useId, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Avatar,
  Badge,
  Button,
  DataTable,
  EmptyState,
  Modal,
  OverflowMenu,
  SectionHeader,
  SegmentedControl,
  Skeleton,
} from "../design-system/atoms";
import {
  RecordView,
  type TimelineEntry,
  type TimelineGroup,
} from "../design-system/composed";
import {
  EvidenceMark,
  type EvidenceMarkSource,
} from "../design-system/evidencemark";
import {
  AutonomyDot,
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { formatDate, formatDateTime, formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { taskWriteKeys } from "./activitykeys";
import { ArchiveAction } from "./archive";
import {
  coldFieldLabel,
  isVersionSkew,
  LoadMoreButton,
  ProblemError,
  problemMessageOf,
  provenanceOf,
  QueryGate,
  QueryStates,
  siteReadKindLabel,
  throwProblem,
  useSorMode,
  useViewerId,
} from "./common";
import {
  AccountBrief,
  DealsCard,
  HealthCard,
  NextSteps,
  type Org360Result,
  OverlayFallback,
  PeopleCard,
  RECORD_ZONE,
  SignalsCard,
  SinceLastVisitStrip,
  StateStrip,
  StateStripSkeleton,
  type SuggestionAction,
  SuggestionsSection,
  TagsCard,
  useAcknowledgeOrganizationView,
  useOrganization360,
} from "./company360";
import { ListAction, NewDealAction, TagAction } from "./companyactions";
import { CompanyApprovalsPanel, DecisionsChip } from "./companyapprovals";
import { ComposeModal, TimelineActions } from "./compose";
import {
  CreateAction,
  type CreateField,
  type FormRows,
  joinMultiselectValue,
  splitMultiselectValue,
} from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import { EntityRef, useRoster } from "./entityref";
import { type FactGroup, factFieldLabelKey, groupFacts } from "./factview";
import { changeTimeline, RecordHistoryTab, useFieldHistory } from "./history";
import { mergeChronology } from "./history.logic";
import { confidenceLevel } from "./inbox";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";
import { LogActivityAction } from "./logactivity";
import { MergeAction } from "./merge";
import { PartnerTab } from "./partners";
import { activityTimeline } from "./people";
import { AddRelationshipAction, RelationshipsTab } from "./relationships";
import { ShareAction } from "./share";
import {
  TaskDetailModal,
  TaskQuickActions,
  useTaskUpdate,
} from "./taskactions";
import { groupChronology } from "./timelinegroups";

// Companies list + company 360 (B-EP09.10a/b). Firmographics render
// evidence-or-omit: a field with no stored value is absent, never guessed.
// Search/filter/sort/pagination (P-14), the rich create modal (P-15), the
// If-Match edit form (P-1), and the dedupe view-existing link (P-16) are
// wired in here the same way as contacts (people.tsx) — the enrich flow,
// firmographics card, and timeline stay exactly as they were.

type Organization = components["schemas"]["Organization"];

// Where the account stands with us, and what it is to us (ADR-0079/A124).
// Typed against the schema unions, so a value added upstream fails the build
// here rather than reaching a reader as a raw enum.
type Lifecycle = NonNullable<Organization["lifecycle"]>;
type RelationshipType = NonNullable<Organization["relationship_types"]>[number];

const LIFECYCLE_LABELS: Record<Lifecycle, MessageKey> = {
  unknown: "org.lifecycle.unknown",
  target: "org.lifecycle.target",
  prospect: "org.lifecycle.prospect",
  opportunity: "org.lifecycle.opportunity",
  customer: "org.lifecycle.customer",
  former_customer: "org.lifecycle.former_customer",
  disqualified: "org.lifecycle.disqualified",
};

const RELATIONSHIP_TYPE_LABELS: Record<RelationshipType, MessageKey> = {
  customer: "org.relType.customer",
  partner: "org.relType.partner",
  supplier: "org.relType.supplier",
  investor: "org.relType.investor",
  portfolio_company: "org.relType.portfolio_company",
  competitor: "org.relType.competitor",
  other: "org.relType.other",
};
type CreateOrganizationRequest =
  components["schemas"]["CreateOrganizationRequest"];
type UpdateOrganizationRequest =
  components["schemas"]["UpdateOrganizationRequest"];
type Organization360View = components["schemas"]["Organization360"];
type OrganizationFact = components["schemas"]["OrganizationFact"];

const SIZE_BAND_OPTIONS = [
  "1-10",
  "11-50",
  "51-200",
  "201-500",
  "501-1000",
  "1001-5000",
  "5000+",
] as const;

async function fetchOrganizationsPage(
  query: ListQuery,
  cursor: string | null,
): Promise<ListPage<Organization>> {
  const { data, error } = await api.GET("/organizations", {
    params: {
      query: {
        q: query.q || undefined,
        sort: query.sort || undefined,
        include_archived: query.includeArchived || undefined,
        cursor: cursor || undefined,
        limit: 50,
        ...query.filters,
      },
    },
  });
  if (error) {
    throwProblem(error);
  }
  return {
    data: data.data,
    page: {
      next_cursor: data.page.next_cursor ?? null,
      has_more: data.page.has_more,
    },
  };
}

function stringField(value: unknown): string {
  return typeof value === "string" ? value : "";
}

// Merge-target search (P-2): mirrors searchPeopleTargets (people.tsx) — the
// caller filters out the source row.
async function searchOrgTargets(
  q: string,
): Promise<{ id: string; name: string }[]> {
  const { data, error } = await api.GET("/organizations", {
    params: { query: { q, limit: 10 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((candidate) => ({
    id: candidate.id,
    name: candidate.display_name,
  }));
}

function asSizeBand(
  value: string | undefined,
): CreateOrganizationRequest["size_band"] {
  return (SIZE_BAND_OPTIONS as readonly string[]).includes(value ?? "")
    ? (value as CreateOrganizationRequest["size_band"])
    : undefined;
}

// The repeatable `domains` rows → the wire `domains[]` shape, shared by the
// create body and the edit patch: blank rows drop out, the domain lowercases,
// and the row's primary radio (a string "true"/"") becomes the boolean flag.
// An empty result is `undefined` — on create that means "no domains", on
// update the field is omitted so the stored set stays untouched (never
// silently cleared).
function mapDomainRows(rows: FormRows): CreateOrganizationRequest["domains"] {
  const domains = mapDomainRowsReplaceSet(rows);
  return domains.length > 0 ? domains : undefined;
}

type DomainPatch = NonNullable<UpdateOrganizationRequest["domains"]>;

// The edit-patch form of the repeatable domains field: always the concrete
// desired set (possibly empty), so a caller can send [] to clear every domain.
// Blank rows drop; the primary radio ("true"/"") becomes the boolean flag.
function mapDomainRowsReplaceSet(rows: FormRows): DomainPatch {
  return (rows.domains ?? [])
    .filter((row) => (row.domain ?? "").trim().length > 0)
    .map((row) => ({
      domain: row.domain.trim().toLowerCase(),
      is_primary: row.is_primary === "true",
    }));
}

// Order-independent set equality: an edit that leaves the domains untouched
// omits the field (sparse PATCH), while any real change — including clearing
// to empty — sends the replace-set.
function sameDomainSet(a: DomainPatch, b: DomainPatch): boolean {
  if (a.length !== b.length) {
    return false;
  }
  const key = (d: DomainPatch[number]) => `${d.domain}:${d.is_primary ? 1 : 0}`;
  const seen = new Set(a.map(key));
  return b.every((d) => seen.has(key(d)));
}

// Builds the create-company request body: `domains[]` rows carry
// `{domain, is_primary}` keyed off the repeatable rows channel, scalar
// fields trim to undefined when blank.
export function mapOrgBody(
  values: Record<string, string>,
  rows: FormRows,
): CreateOrganizationRequest {
  return {
    display_name: values.display_name.trim(),
    legal_name: values.legal_name?.trim() || undefined,
    industry: values.industry?.trim() || undefined,
    size_band: asSizeBand(values.size_band),
    domains: mapDomainRows(rows),
    source: "manual",
  };
}

// Builds the PATCH body: the scalar UpdateOrganizationRequest fields plus the
// domains replace-set from the edit modal's repeatable rows. Domains are sent
// only when the set actually changed from `currentDomains` — an untouched edit
// omits the field (sparse PATCH), and clearing every row sends [] (clear all),
// the two cases the contract's "absent = untouched" vs "[] = clear" distinguish.
export function mapOrgUpdate(
  values: Record<string, unknown>,
  rows: FormRows,
  currentDomains: Organization["domains"] = [],
): UpdateOrganizationRequest {
  const desired = mapDomainRowsReplaceSet(rows);
  const current: DomainPatch = (currentDomains ?? []).map((domain) => ({
    domain: domain.domain,
    is_primary: domain.is_primary,
  }));
  const body: UpdateOrganizationRequest = {
    display_name: stringField(values.display_name).trim() || undefined,
    legal_name: stringField(values.legal_name).trim() || undefined,
    industry: stringField(values.industry).trim() || undefined,
    size_band: asSizeBand(stringField(values.size_band)),
    owner_id: stringField(values.owner_id).trim() || undefined,
  };
  if (!sameDomainSet(desired, current)) {
    body.domains = desired;
  }
  const lifecycle = stringField(values.lifecycle).trim();
  if (lifecycle) {
    body.lifecycle = lifecycle as NonNullable<
      UpdateOrganizationRequest["lifecycle"]
    >;
  }
  // Always sent when the field was rendered, even empty: this is a replace-set,
  // and "the user cleared every type" is an edit, not an absence. The form
  // channel joins a multiselect into one comma string, so an empty string is
  // the honest empty set.
  if (values.relationship_types !== undefined) {
    body.relationship_types = splitMultiselectValue(
      stringField(values.relationship_types),
    ) as NonNullable<UpdateOrganizationRequest["relationship_types"]>;
  }
  return body;
}

// The facts card shows the primary domain as "the website", but the contract
// carries no such scalar field — a domain is one row of the repeatable
// `domains[]` replace-set. Correcting the shown value therefore replaces the
// primary row while every other domain (a secondary, an alias) rides through
// unchanged; clearing it to blank drops the primary row rather than inventing
// an empty one.
export function buildWebsitePatch(
  domains: Organization["domains"],
  newDomain: string,
): NonNullable<UpdateOrganizationRequest["domains"]> {
  const others = (domains ?? [])
    .filter((domain) => !domain.is_primary)
    .map((domain) => ({
      domain: domain.domain,
      is_primary: domain.is_primary,
    }));
  const trimmed = newDomain.trim().toLowerCase();
  return trimmed ? [...others, { domain: trimmed, is_primary: true }] : others;
}

const companyCreateFields: CreateField[] = [
  { key: "display_name", label: "create.displayName", required: true },
  { key: "legal_name", label: "create.legalName" },
  { key: "industry", label: "create.industry" },
  {
    key: "size_band",
    label: "create.sizeBand",
    type: "select",
    options: SIZE_BAND_OPTIONS.map((band) => ({ value: band, label: band })),
  },
  {
    key: "domains",
    label: "org.domains",
    type: "repeatable",
    addLabel: "field.addDomain",
    rowFields: [{ key: "domain", label: "field.domain", required: true }],
    primaryKey: "is_primary",
  },
];

// The edit form, built per-render because the owner options are the live user
// roster.
//
// Stage and relationship types ARE here now: the retired classification could
// not be edited from anywhere, because the update contract carried no such
// field.
// The two vocabularies that replaced classification (ADR-0079/A124): where the
// account stands with us, and what it is to us. Kept in wire order so the
// select reads as a progression rather than an alphabet.
export const LIFECYCLE_OPTIONS = [
  "unknown",
  "target",
  "prospect",
  "opportunity",
  "customer",
  "former_customer",
  "disqualified",
] as const;

export const RELATIONSHIP_TYPE_OPTIONS = [
  "customer",
  "partner",
  "supplier",
  "investor",
  "portfolio_company",
  "competitor",
  "other",
] as const;

// t is threaded in because the option LABELS are catalog keys, not words: the
// field renderer prints option.label as given, so an untranslated key reaches
// the reader as "org.lifecycle.customer".
export function companyEditFields(
  owners: readonly { id: string; display_name: string }[],
  hasOwner: boolean,
  t: (key: MessageKey) => string,
): CreateField[] {
  return [
    { key: "display_name", label: "create.displayName", required: true },
    { key: "legal_name", label: "create.legalName" },
    { key: "industry", label: "create.industry" },
    {
      key: "size_band",
      label: "create.sizeBand",
      type: "select",
      options: SIZE_BAND_OPTIONS.map((band) => ({ value: band, label: band })),
    },
    // Who is accountable for this account. It defaults to whoever created the
    // record and stays there until someone changes it — which, until now,
    // nothing on this page let them do.
    //
    // Required exactly when the account HAS an owner: an optional select
    // offers a blank option, and `UpdateOrganizationRequest.owner_id` cannot
    // carry "unassign" — a null is indistinguishable from an omitted field on
    // the wire. Offering the blank would take the answer and drop it. An
    // account with no owner yet keeps the blank, because there it is the
    // truthful current state rather than an edit we cannot make.
    {
      key: "owner_id",
      label: "co.pulse.owner",
      type: "select",
      required: hasOwner,
      options: owners.map((user) => ({
        value: user.id,
        label: user.display_name,
      })),
    },
    // Where the account stands, and what it is to us — the two questions the
    // retired classification tried to answer with one value, and the reason
    // neither was editable from this page at all.
    {
      key: "lifecycle",
      label: "org.lifecycle",
      type: "select",
      options: LIFECYCLE_OPTIONS.map((value) => ({
        value,
        label: t(LIFECYCLE_LABELS[value]),
      })),
    },
    {
      key: "relationship_types",
      label: "org.relationshipTypes",
      type: "multiselect",
      options: RELATIONSHIP_TYPE_OPTIONS.map((value) => ({
        value,
        label: t(RELATIONSHIP_TYPE_LABELS[value]),
      })),
    },
    {
      key: "domains",
      label: "org.domains",
      type: "repeatable",
      addLabel: "field.addDomain",
      rowFields: [{ key: "domain", label: "field.domain", required: true }],
      primaryKey: "is_primary",
    },
  ];
}

async function createCompany(
  values: Record<string, string>,
  rows: FormRows | undefined,
  customFields: Record<string, unknown>,
  t: (key: MessageKey) => string,
): Promise<Organization> {
  const { data, error } = await api.POST("/organizations", {
    body: { ...mapOrgBody(values, rows ?? {}), ...customFields },
  });
  if (error) {
    throwProblem(error, t);
  }
  return data;
}

export function CompaniesScreen() {
  const t = useT();
  const viewerId = useViewerId();
  const cf = useObjectCustomFields("organization");
  const state = useListQuery<Organization>({
    key: "organizations",
    initialSort: "-created_at",
    fetchPage: fetchOrganizationsPage,
  });
  const { query, setQuery } = state;

  return (
    <div className="wrap">
      <div className="list-head">
        <SectionHeader title={t("nav.companies")} />
        <div className="list-head-actions">
          <Button small onClick={() => navigate({ screen: "partners" })}>
            {t("nav.partners")}
          </Button>
          <CreateAction
            label={t("create.company")}
            invalidate="organizations"
            screen="companies"
            create={(values, rows) =>
              createCompany(values, rows, cf.toBody(values), t)
            }
            resolveExisting={(_code, id) => ({ screen: "companies", id })}
            fields={[...companyCreateFields, ...cf.formFields]}
          />
        </div>
      </div>
      <ListToolbar
        query={query}
        setQuery={setQuery}
        sortOptions={[
          { value: "display_name", label: "org.name" },
          { value: "-created_at", label: "list.sortNewest" },
        ]}
      />
      <ListGate state={state} empty={t("common.empty")}>
        {(rows) => (
          <DataTable
            columns={[
              {
                key: "name",
                header: t("org.name"),
                render: (org: Organization) => (
                  <span className="avatar-row">
                    <Avatar name={org.display_name} src={org.logo_url} tinted />
                    <strong>{org.display_name}</strong>
                    {org.archived_at && (
                      <Badge tone="warn">{t("record.archived")}</Badge>
                    )}
                  </span>
                ),
              },
              {
                key: "industry",
                header: t("org.industry"),
                render: (org: Organization) => org.industry ?? "",
              },
              {
                key: "size",
                header: t("org.size"),
                render: (org: Organization) => org.size_band ?? "",
              },
              {
                key: "class",
                header: t("org.lifecycle"),
                // classification is retired and no longer written by anything,
                // so a column reading it would show whatever it happened to
                // hold when the split shipped, forever.
                render: (org: Organization) =>
                  org.lifecycle && org.lifecycle !== "unknown" ? (
                    <Badge>{t(LIFECYCLE_LABELS[org.lifecycle])}</Badge>
                  ) : null,
              },
              {
                key: "provenance",
                header: t("people.capturedBy"),
                render: (org: Organization) => (
                  <ProvenanceTag
                    provenance={provenanceOf(org.captured_by, viewerId)}
                  />
                ),
              },
            ]}
            rows={rows}
            rowKey={(org) => org.id}
            onRowClick={(org) => navigate({ screen: "companies", id: org.id })}
          />
        )}
      </ListGate>
    </div>
  );
}

// The EP05 enrich verb on the company 360: one click reads the org's own
// website through the cold-start fetch + no-guess gate and STAGES a 🟡
// proposal — every rendered field carries evidence + confidence or was
// omitted, and nothing writes until the human accepts it in the inbox
// (accept fills only EMPTY fields).
function EnrichCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const enrich = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST("/organizations/{id}/enrich", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <div className="list-head">
        <SectionHeader title={t("enrich.title")} sub={t("enrich.sub")} />
        <Button
          small
          disabled={enrich.isPending}
          onClick={() => enrich.mutate()}
        >
          {enrich.isPending ? t("enrich.reading") : t("enrich.cta")}
        </Button>
      </div>
      {enrich.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(enrich.error, t)}
        </p>
      )}
      {enrich.data && (
        <div>
          <p
            style={{
              display: "flex",
              alignItems: "center",
              gap: 8,
              flexWrap: "wrap",
              margin: "6px 0 12px",
            }}
          >
            <AutonomyDot tier="confirm" />
            <span className="t-small">{t("enrich.staged")}</span>
            <Button small onClick={() => navigate({ screen: "inbox" })}>
              {t("enrich.toInbox")}
            </Button>
          </p>
          <p className="t-caption" style={{ marginBottom: 10 }}>
            {t("enrich.from", { url: enrich.data.source_url })}
          </p>
          {enrich.data.fields.map((field) => {
            const level = confidenceLevel(field.confidence);
            return (
              <div key={field.field} style={{ marginBottom: 12 }}>
                <div
                  style={{
                    display: "flex",
                    alignItems: "center",
                    gap: 8,
                    marginBottom: 3,
                  }}
                >
                  <span className="t-label">
                    {coldFieldLabel(field.field, t)}
                  </span>
                  {level && <ConfidenceMeter level={level} />}
                </div>
                <div>{field.value}</div>
                {field.evidence_snippet && (
                  <EvidenceChip
                    evidence={{
                      snippet: field.evidence_snippet,
                      source: field.source_url ?? "",
                    }}
                  />
                )}
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

type SiteReadReport = components["schemas"]["SiteReadReport"];

const SITE_READ_STATUS_LABELS: Record<SiteReadReport["status"], MessageKey> = {
  queued: "deepread.statusQueued",
  deferred: "deepread.statusDeferred",
  running: "deepread.statusRunning",
  done: "deepread.statusDone",
  partial: "deepread.statusPartial",
  cancelled: "deepread.statusCancelled",
  failed: "deepread.statusFailed",
};

const SITE_READ_STOP_LABELS: Record<
  NonNullable<SiteReadReport["stopped_reason"]>,
  MessageKey
> = {
  budget: "deepread.stopBudget",
  page_cap: "deepread.stopPageCap",
  byte_cap: "deepread.stopByteCap",
  deadline: "deepread.stopDeadline",
};

const SITE_READ_SKIP_LABELS: Record<
  components["schemas"]["SiteReadSkip"]["reason"],
  MessageKey
> = {
  robots: "deepread.skipRobots",
  off_domain: "deepread.skipOffDomain",
  page_cap: "deepread.skipPageCap",
  byte_cap: "deepread.skipByteCap",
  unreadable: "deepread.skipUnreadable",
};

// Trims the scheme and clamps long paths so the pages/skips lists stay
// scannable; the full URL survives on the title attribute.
function shortUrl(url: string): string {
  const bare = url.replace(/^https?:\/\//, "");
  return bare.length > 60 ? `${bare.slice(0, 59)}…` : bare;
}

function SiteReadDeferral({ report }: Readonly<{ report: SiteReadReport }>) {
  const t = useT();
  const { locale } = useLocale();
  if (report.status !== "deferred") {
    return null;
  }
  return (
    <p className="t-small" style={{ margin: "var(--space-2) 0 0" }}>
      {report.status_detail}
      {report.next_attempt_at && (
        <>
          {" "}
          {t("deepread.resumesAt", {
            when: formatDateTime(
              report.next_attempt_at,
              locale,
              "Europe/Berlin",
            ),
          })}
        </>
      )}
    </p>
  );
}

// The polled half of the deep read: renders progress while the crawl is in
// flight (3s poll, stops on a terminal status) and the full account when it
// ends — pages read, pages SKIPPED and why, and the stop reason when the
// crawl ended early. The skip/stop rendering is the transparency surface: a
// truncated crawl must never read as complete.
function SiteReadPanel({
  orgId,
  readId,
}: Readonly<{ orgId: string; readId: string }>) {
  const t = useT();
  const reportQuery = useQuery({
    queryKey: ["site-read", orgId, readId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/organizations/{id}/site-reads/{readId}",
        { params: { path: { id: orgId, readId } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    refetchInterval: (query) => {
      const status = query.state.data?.status;
      if (status === "queued" || status === "running") {
        return 3000;
      }
      return status === "deferred" ? 60_000 : false;
    },
  });

  if (reportQuery.isPending) {
    return <Skeleton width="60%" />;
  }
  if (reportQuery.isError) {
    return (
      <p className="t-caption" style={{ color: "var(--danger)" }}>
        {problemMessageOf(reportQuery.error, t)}
      </p>
    );
  }

  const report = reportQuery.data;
  const terminal =
    report.status === "done" ||
    report.status === "partial" ||
    report.status === "failed";

  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <p
        style={{
          display: "flex",
          alignItems: "center",
          gap: "var(--space-2)",
          flexWrap: "wrap",
          margin: 0,
        }}
      >
        <Badge tone={report.status === "failed" ? "danger" : undefined}>
          {t(SITE_READ_STATUS_LABELS[report.status])}
        </Badge>
        <span className="t-small">
          {t(
            report.pages.length === 1
              ? "deepread.pagesSoFar.one"
              : "deepread.pagesSoFar.other",
            { count: report.pages.length },
          )}
        </span>
        {terminal && (
          <span className="t-small">
            {t(
              (report.fact_count ?? 0) === 1
                ? "deepread.factCount.one"
                : "deepread.factCount.other",
              { count: report.fact_count ?? 0 },
            )}
          </span>
        )}
      </p>
      <SiteReadDeferral report={report} />
      {report.stopped_reason && (
        <p style={{ margin: "var(--space-2) 0 0" }}>
          <Badge tone="warn">
            {t("deepread.stoppedEarly", {
              reason: t(SITE_READ_STOP_LABELS[report.stopped_reason]),
            })}
          </Badge>
        </p>
      )}
      {terminal && report.proposal_ids.length > 0 && (
        <p
          style={{
            display: "flex",
            alignItems: "center",
            gap: "var(--space-2)",
            flexWrap: "wrap",
            margin: "var(--space-3) 0 0",
          }}
        >
          <AutonomyDot tier="confirm" />
          <span className="t-small">
            {report.proposal_ids.length === 1
              ? t("deepread.proposalsOne")
              : t("deepread.proposals", { count: report.proposal_ids.length })}
          </span>
          <Button small onClick={() => navigate({ screen: "inbox" })}>
            {t("enrich.toInbox")}
          </Button>
        </p>
      )}
      {terminal && report.pages.length > 0 && (
        <div style={{ marginTop: "var(--space-3)" }}>
          <span className="t-label">{t("deepread.pagesRead")}</span>
          <ul
            className="t-small"
            style={{
              listStyle: "none",
              margin: "var(--space-2) 0 0",
              padding: 0,
              display: "flex",
              flexDirection: "column",
              gap: "var(--space-1)",
            }}
          >
            {report.pages.map((page) => (
              <li key={page.url}>
                <Badge>{siteReadKindLabel(page.kind, t)}</Badge>{" "}
                <span className="t-mono" title={page.url}>
                  {shortUrl(page.url)}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
      {terminal && report.skipped.length > 0 && (
        <div style={{ marginTop: "var(--space-3)" }}>
          <span className="t-label">{t("deepread.skippedPages")}</span>
          <ul
            className="t-small"
            style={{
              listStyle: "none",
              margin: "var(--space-2) 0 0",
              padding: 0,
              display: "flex",
              flexDirection: "column",
              gap: "var(--space-1)",
            }}
          >
            {report.skipped.map((skip) => (
              <li key={skip.url}>
                <span className="t-mono" title={skip.url}>
                  {shortUrl(skip.url)}
                </span>{" "}
                <Badge tone="warn">
                  {t(SITE_READ_SKIP_LABELS[skip.reason])}
                </Badge>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}

// The whole-site deep read (A102/R2), the enrich verb's big sibling: one
// click starts (or joins — idempotent per org+url) a background crawl of the
// company's own site; findings stage as 🟡 proposals for the inbox, nothing
// writes to the record here. 422 (no website on file) and 501 (crawl seam
// unwired) surface their honest cause instead of a generic failure.
function DeepReadCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const [readId, setReadId] = useState<string | null>(null);
  const start = useMutation({
    mutationFn: async () => {
      const { data, error, response } = await api.POST(
        "/organizations/{id}/deep-read",
        { params: { path: { id: orgId } } },
      );
      if (error) {
        // 501 means the crawl seam is unwired, which the server states in its
        // own terms and the card states in the reader's. Either way this stays
        // a problem, so the render below can tell it from a bug in here.
        throwProblem(
          response.status === 501
            ? { title: t("deepread.unavailable") }
            : error,
        );
      }
      return data;
    },
    onSuccess: (started) => setReadId(started.read_id),
  });

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <div className="list-head">
        <SectionHeader title={t("deepread.title")} sub={t("deepread.sub")} />
        <Button small disabled={start.isPending} onClick={() => start.mutate()}>
          {start.isPending ? t("deepread.starting") : t("deepread.cta")}
        </Button>
      </div>
      {start.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {problemMessageOf(start.error, t)}
        </p>
      )}
      {readId && <SiteReadPanel orgId={orgId} readId={readId} />}
    </section>
  );
}

type OrganizationHierarchyRollup =
  components["schemas"]["OrganizationHierarchyRollup"];

// A missing stored FX rate fails the whole rollup read with 422
// fx_rate_unavailable (never a rate-of-1 substitute, never zeros) — this
// marker lets the render branch on that ONE cause without re-parsing the
// problem body a second time.
class FxUnavailableError extends Error {}

async function fetchHierarchyRollup(
  orgId: string,
): Promise<OrganizationHierarchyRollup> {
  const { data, error } = await api.GET(
    "/organizations/{id}/hierarchy-rollup",
    {
      params: { path: { id: orgId }, query: { scope: "tree" } },
    },
  );
  if (error) {
    if (error.code === "fx_rate_unavailable") {
      throw new FxUnavailableError();
    }
    throwProblem(error);
  }
  return data;
}

// P-7: the org hierarchy roll-up (weighted pipeline, current-quarter
// closed-won, 30-day activity, aggregated account count), read-only. Money
// renders only when both amount_minor and currency are present (Money's
// fields are individually optional on the wire) — never a hand-formatted or
// zero-filled figure.
function HierarchyRollupCard({ orgId }: Readonly<{ orgId: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const rollupQuery = useQuery({
    queryKey: ["rollup", orgId],
    queryFn: () => fetchHierarchyRollup(orgId),
  });

  // No skeleton: most accounts are not a group, so this card usually resolves
  // to nothing. Reserving its height on every read would shift the column
  // more often than it would steady it.
  if (rollupQuery.isPending) {
    return null;
  }
  if (rollupQuery.isError) {
    if (rollupQuery.error instanceof FxUnavailableError) {
      return <EmptyState>{t("rollup.fxUnavailable")}</EmptyState>;
    }
    return <EmptyState>{problemMessageOf(rollupQuery.error, t)}</EmptyState>;
  }

  const rollup = rollupQuery.data;
  // A roll-up of one account is that account, and every figure in it is
  // already on the page beside this card. It renders when there is a GROUP
  // to total — including a group whose other members this caller may not
  // read, because "some of it is hidden from you" is exactly the case a
  // total must not stay silent about.
  if (
    rollup.aggregated_account_count <= 1 &&
    rollup.restricted_excluded.length === 0
  ) {
    return null;
  }
  const money = (value: OrganizationHierarchyRollup["weighted_pipeline"]) =>
    value.amount_minor != null && value.currency
      ? formatMoney(value.amount_minor, value.currency, locale)
      : "—";

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader title={t("tab.rollup")} />
      <dl className="firmo">
        <div>
          <dt>{t("rollup.weightedPipeline")}</dt>
          <dd className="t-mono">{money(rollup.weighted_pipeline)}</dd>
        </div>
        <div>
          <dt>{t("rollup.closedWon")}</dt>
          <dd className="t-mono">{money(rollup.closed_won)}</dd>
        </div>
        <div>
          <dt>{t("rollup.activity30d")}</dt>
          <dd>{rollup.activity_count_30d}</dd>
        </div>
        <div>
          <dt>{t("rollup.accounts")}</dt>
          <dd>{rollup.aggregated_account_count}</dd>
        </div>
      </dl>
      {rollup.restricted_excluded.length > 0 && (
        <p className="t-caption" style={{ marginTop: 10 }}>
          {t("rollup.excluded", { count: rollup.restricted_excluded.length })}
        </p>
      )}
      <p className="t-caption" style={{ marginTop: 10 }}>
        {t("rollup.computedAt", {
          when: formatDateTime(rollup.computed_at, locale, "Europe/Berlin"),
        })}
      </p>
    </section>
  );
}

// One confirmed profile field (S-E02): the human field label, the value, and
// a footer that names where it came from — provenance, confidence when the
// read carried one, and the grounding evidence snippet. Mirrors EnrichCard's
// field row, but these are ACCEPTED values on the record, not staged proposals.
// The shared trust-signal footer for an evidence-backed row: provenance
// always, confidence whenever graded, and the evidence snippet when present.
// One spelling for profile fields and facts so the "confidence is never
// hidden" convention can't drift between them.
// derivedSource builds the evidence mark's payload for a value the system
// read rather than a person typed. A value a HUMAN entered gets no mark: the
// record is full of human-entered values, and marking them all would make
// the underline mean nothing.
function derivedSource(
  row: Readonly<{
    captured_by?: string;
    confidence?: number | null;
    evidence_snippet?: string | null;
    source_url?: string | null;
    updated_at?: string;
  }>,
  locale: Locale,
): EvidenceMarkSource | undefined {
  const provenance = provenanceOf(row.captured_by);
  if (provenance.kind === "human") {
    return undefined;
  }
  return {
    provenance,
    confidence: confidenceLevel(row.confidence) ?? undefined,
    snippet: row.evidence_snippet,
    sourceUrl: row.source_url,
    at: row.updated_at
      ? formatDateTime(row.updated_at, locale, RECORD_ZONE)
      : undefined,
  };
}

// PROFILE_FIELD_LABELS names the profile fields as statements ABOUT a company.
//
// The same fields are asked of the reader during onboarding, where the second
// person is right — "What do you sell?" is a question to us. On a prospect's
// record that framing put the reader in the wrong chair: the page appeared to
// be interviewing us about a company we are trying to sell to.
const PROFILE_FIELD_LABELS: Record<string, MessageKey> = {
  display_name: "co.profileField.display_name",
  offer_summary: "co.profileField.offer_summary",
  icp: "co.profileField.icp",
  buying_center: "co.profileField.buying_center",
  value_proposition: "co.profileField.value_proposition",
  usp: "co.profileField.usp",
  customer_pains: "co.profileField.customer_pains",
  desired_outcomes: "co.profileField.desired_outcomes",
  buying_intents: "co.profileField.buying_intents",
  common_objections: "co.profileField.common_objections",
  sales_motion: "co.profileField.sales_motion",
  legal_name: "co.profileField.legal_name",
  registered_address: "co.profileField.registered_address",
  register_vat: "co.profileField.register_vat",
  industry: "co.profileField.industry",
  history: "co.profileField.history",
};

// The onboarding wording is the fallback, so a field added there still reads
// as words rather than as a column name here.
function profileFieldLabel(field: string, t: ReturnType<typeof useT>): string {
  const key = PROFILE_FIELD_LABELS[field];
  return key ? t(key) : coldFieldLabel(field, t);
}

// Facts read from the site, grouped into the four fixed categories. Empty
// categories are omitted and an empty read renders nothing at all — the
// profile card above already carries the region's honest empty state, so a
// second "nothing here" would only be noise.
//
// Ordering and duplicate collapsing live in factview.ts; the category order
// comes from there too, so the card has one source for what it draws.
//
// FACT_PREVIEW is how many rows of a category are shown before the reader asks
// for the rest. A real account returns ninety-odd facts, and rendering them all
// made this card taller than the page it sits beside — at which point nobody
// reads any of it.
const FACT_PREVIEW = 5;

const FACT_CATEGORY_LABELS: Record<OrganizationFact["category"], MessageKey> = {
  company: "org.factCategory.company",
  offering: "org.factCategory.offering",
  market: "org.factCategory.market",
  signal: "org.factCategory.signal",
};

// One fact row: the value carries its own evidence mark, the same
// affordance every derived value on this page uses.
// Why a fact looks wrong, in the words a reader can act on. Typed against the
// schema union, so a rule added upstream fails the build here rather than
// rendering a raw reason code.
type FactSuspectReason = NonNullable<OrganizationFact["suspect_reason"]>;

const FACT_SUSPECT_LABELS: Record<FactSuspectReason, MessageKey> = {
  phone_shaped_location: "co.factSuspect.phoneShapedLocation",
  not_a_phone: "co.factSuspect.notAPhone",
  not_a_year: "co.factSuspect.notAYear",
  not_an_email: "co.factSuspect.notAnEmail",
  not_a_size: "co.factSuspect.notASize",
};

function FactRow({
  fact,
  onOpenHistory,
}: Readonly<{ fact: OrganizationFact; onOpenHistory?: () => void }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <div className="co-field">
      <span className="t-label">{t(factFieldLabelKey(fact.field))}</span>
      <div>
        <EvidenceMark
          value={fact.value}
          source={derivedSource(fact, locale)}
          onOpenHistory={onOpenHistory}
        />
        {/* The value contradicts its own field — a phone number filed as a
            location, a register number filed as a headcount. The fact is still
            shown with its evidence: hiding it would be a worse answer than
            flagging it, and the reader is the one who can tell. */}
        {fact.suspect_reason && (
          <span className="co-fact-suspect">
            <Badge tone="warn">
              {t(FACT_SUSPECT_LABELS[fact.suspect_reason])}
            </Badge>
          </span>
        )}
      </div>
    </div>
  );
}

// One category of facts. Only the first few rows are drawn until the reader
// asks for the rest, and the count of what is hidden is on the button — a
// truncated list with no number reads as "that is everything".
function FactCategory({
  group,
  onOpenHistory,
}: Readonly<{ group: FactGroup; onOpenHistory?: () => void }>) {
  const t = useT();
  const [expanded, setExpanded] = useState(false);
  const hidden = group.facts.length - FACT_PREVIEW;
  const shown = expanded ? group.facts : group.facts.slice(0, FACT_PREVIEW);
  return (
    <div className="co-facts-group">
      <div className="t-label co-facts-heading">
        {t(FACT_CATEGORY_LABELS[group.category])}
      </div>
      {shown.map((fact) => (
        <FactRow
          key={`${fact.field}:${fact.value_key}`}
          fact={fact}
          onOpenHistory={onOpenHistory}
        />
      ))}
      {hidden > 0 && (
        <Button small onClick={() => setExpanded(!expanded)}>
          {expanded
            ? t("co.facts.showLess")
            : t("co.facts.showAll", { count: group.facts.length })}
        </Button>
      )}
    </div>
  );
}

function FactsCard({
  orgId,
  onOpenHistory,
}: Readonly<{ orgId: string; onOpenHistory?: () => void }>) {
  const t = useT();
  const factsQuery = useQuery({
    queryKey: ["org-facts", orgId],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}/facts", {
        params: { path: { id: orgId } },
      });
      if (error) {
        throwProblem(error);
      }
      return data.data ?? [];
    },
  });

  // A read that failed is surfaced, never swallowed as "no facts" — an empty
  // read and a 404/network error must stay distinguishable and retryable.
  if (factsQuery.isError) {
    return (
      <section className="card" style={{ marginBottom: 16 }}>
        <SectionHeader title={t("org.facts")} />
        <QueryStates query={factsQuery}>{null}</QueryStates>
      </section>
    );
  }

  // Otherwise facts are supplementary: while the read is in flight, or if it
  // has nothing to show, the card stays absent rather than flashing a skeleton
  // or an empty shell next to the profile card that owns the region's states.
  const facts = factsQuery.data;
  if (!facts || facts.length === 0) {
    return null;
  }

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader title={t("org.facts")} />
      {groupFacts(facts).map((group) => (
        <FactCategory
          key={group.category}
          group={group}
          onOpenHistory={onOpenHistory}
        />
      ))}
    </section>
  );
}

// Two tabs, not three. The company view is ONE scrolling page: the
// relationship edges and the hierarchy roll-up moved into its rails, where a
// rep reads them alongside everything else instead of hunting for them.
//
// History is no longer one of them. Field changes belong in the account's own
// chronology (the timeline's Changes filter), and the audit spine is an
// inspection of the record rather than part of its story — it opens from the
// header's overflow menu. A tab of equal weight beside the account's story
// said the two were the same kind of question. Partner stays a tab: it is a
// form, not a reading of this account.
const COMPANY_TABS = [
  "overview",
  "context",
  "people",
  "timeline",
  "partner",
] as const;
type CompanyTab = (typeof COMPANY_TABS)[number];

// Partner is not a permanent tab. It renders the partner programme —
// certification, role, margin tier — which is a form about a commercial
// arrangement the overwhelming majority of accounts do not have. A tab that
// is empty on nearly every record teaches the reader to skip the tab strip.
//
// It shows for an account that HAS a partner programme, and for the reader
// who just asked to set one up (the overflow menu switches the tab, which is
// what `tab` already carries) — so the only path to a first partner row is
// still open, and the form stops greeting everyone else.
function companyTabsFor(
  org: Organization,
  tab: CompanyTab,
): readonly CompanyTab[] {
  // Gated on the relationship type, not on `org.partner`: the Organization
  // read does not select the extension row, so that field is always absent
  // and every partner would lose the tab. The type is equivalent and IS
  // returned — an org carries it exactly when it has a programme, which the
  // store enforces in both directions (ADR-0079/A124).
  const isPartner = (org.relationship_types ?? []).includes("partner");
  return isPartner || tab === "partner"
    ? COMPANY_TABS
    : (["overview", "context", "people", "timeline"] as const);
}

// Which slice of the account's chronology is on screen. Activities is what
// happened WITH them, changes is what happened TO the record; a reader who
// wants them in one order picks "all".
const TIMELINE_FILTERS = ["activities", "changes", "all"] as const;
type TimelineFilter = (typeof TIMELINE_FILTERS)[number];

type Activity = components["schemas"]["Activity"];
type ChangesQuery = ReturnType<typeof useFieldHistory>;

// The company 360 badge/action bar. Archived records are read-only: the
// backend rejects edit/merge/archive on a non-live row (there is no unarchive
// path), so those buttons would only 404 — the Archived badge is the whole
// affordance. Extracted from CompanyScreen so its render stays legible.
// The company's edit form. Its own component because it owns three reads the
// rest of the action bar has no use for — the custom-field catalogue, the user
// roster behind the owner picker, and the record slice they prefill.
function CompanyEditAction({
  org,
  overlay,
}: Readonly<{ org: Organization; overlay: boolean }>) {
  const t = useT();
  const cf = useObjectCustomFields("organization");
  const roster = useRoster("user", true);
  // The roster hook serves users and teams alike, so narrow to the entries
  // that actually carry a person's name rather than asserting the shape.
  const owners = (roster.data ?? []).flatMap((entry) =>
    "display_name" in entry
      ? [{ id: entry.id, display_name: entry.display_name }]
      : [],
  );
  // The roster is one page of 200. An owner outside it — a big workspace, a
  // deactivated user — would leave the prefilled select showing a blank it
  // cannot resolve, and since the select is required once an owner is set,
  // saving anything else would then force a reassignment nobody asked for.
  if (org.owner_id && !owners.some((user) => user.id === org.owner_id)) {
    owners.push({ id: org.owner_id, display_name: t("co.owner.notInRoster") });
  }
  return (
    <EditAction
      label={t("record.edit")}
      notice={overlay ? t("overlay.partialWriteBack") : undefined}
      fields={[
        ...companyEditFields(owners, Boolean(org.owner_id), t),
        ...cf.formFields,
      ]}
      record={{
        id: org.id,
        version: org.version,
        display_name: org.display_name,
        owner_id: org.owner_id ?? "",
        legal_name: org.legal_name ?? "",
        industry: org.industry ?? "",
        size_band: org.size_band ?? "",
        // Both stage fields prefill from the live record. relationship_types
        // is a REPLACE-SET: an unseeded multiselect collects as the empty
        // string, which mapOrgUpdate reads as the honest empty set, so saving
        // an unrelated field would clear every type the account has.
        lifecycle: org.lifecycle ?? "",
        relationship_types: joinMultiselectValue(org.relationship_types ?? []),
        // The repeatable domains field prefills from the org's live set;
        // its rows are string-keyed, so the primary flag stringifies to
        // match the "true"/"" the primary radio writes.
        domains: (org.domains ?? []).map((domain) => ({
          domain: domain.domain,
          is_primary: String(domain.is_primary),
        })),
        ...cf.recordSlice(org),
      }}
      update={async (values, rows) => {
        const { data, error } = await api.PATCH("/organizations/{id}", {
          params: {
            path: { id: org.id },
            ...ifMatch(org.version),
          },
          body: {
            ...mapOrgUpdate(values, rows ?? {}, org.domains),
            ...cf.toBody(values),
          },
        });
        if (error) {
          throwProblem(error);
        }
        return data;
      }}
      invalidate="organizations"
      recordKey="organization"
      resolveExisting={(_code, existingId) => ({
        screen: "companies",
        id: existingId,
      })}
    />
  );
}

function CompanyActionBadges({
  org,
  onOpenHistory,
  onSetUpPartner,
}: Readonly<{
  org: Organization;
  onOpenHistory: () => void;
  onSetUpPartner: () => void;
}>) {
  const t = useT();
  const overlay = useSorMode() === "overlay";
  // An archived record is read-only: the backend rejects edit/merge/archive
  // on a non-live row (there is no unarchive path), so those items would only
  // 404. Its history stays readable — what happened to a record is exactly
  // what a reader wants after it has been put away.
  const writable = !org.archived_at;
  // Lifecycle and relationship type were drawn here too, but the state strip
  // right below the masthead already carries them (its Account/Stage cell),
  // and the facts card states the rest — a reader hit the same two facts
  // three times before reaching the page's own content.
  return (
    <>
      {org.archived_at && <Badge tone="warn">{t("record.archived")}</Badge>}
      {/* An archived record read from a mirror offers nothing at all: every
          write is refused and the history is a native read the mirror has no
          row for. Rendering the trigger anyway would open an empty popover. */}
      {(writable || !overlay) && (
        <OverflowMenu label={t("record.moreActions")}>
          {writable && <CompanyEditAction org={org} overlay={overlay} />}
          {/* Merge has no incumbent-first projection — the seam refuses it
            outright (overlay/provider_writes.go Merge) — unlike
            edit/archive above, which it serves, so it stays hidden here. */}
          {writable && !overlay && (
            <MergeAction
              label={t("merge.org")}
              sourceId={org.id}
              sourceName={org.display_name}
              searchTargets={searchOrgTargets}
              merge={async (targetId) => {
                const { data, error } = await api.POST(
                  "/organizations/{id}/merge",
                  {
                    params: {
                      path: { id: org.id },
                      ...ifMatch(org.version),
                    },
                    body: { target_id: targetId },
                  },
                );
                if (error) {
                  throwProblem(error, t);
                }
                return data;
              }}
              invalidate="organizations"
              recordKey="organization"
              survivorRoute={(targetId) => ({
                screen: "companies",
                id: targetId,
              })}
            />
          )}
          {writable && (
            <ArchiveAction
              label={t("record.archive")}
              confirmText={t("record.archiveConfirm")}
              archive={async () => {
                const { data, error } = await api.DELETE(
                  "/organizations/{id}",
                  {
                    params: { path: { id: org.id } },
                  },
                );
                if (error) {
                  throwProblem(error);
                }
                return data;
              }}
              invalidate="organizations"
              recordKey="organization"
              onArchived={() => navigate({ screen: "companies" })}
            />
          )}
          {/* A record grant probes the native row via auth.EnsureLinkTarget,
            which a mirrored record has no row for — sharing stays hidden
            in overlay regardless of record type (see deals.tsx's
            DealBadges). */}
          {writable && !overlay && (
            <ShareAction recordType="organization" recordId={org.id} />
          )}
          {/* The way in to the partner programme for an account that has none.
            The tab only shows once there IS one, so without this the first
            partner row would be unreachable — this is the same form, asked
            for rather than offered. */}
          {writable &&
            !overlay &&
            !(org.relationship_types ?? []).includes("partner") && (
              <Button small onClick={onSetUpPartner}>
                {t("org.partnerSetUp")}
              </Button>
            )}
          {/* The audit spine: who changed this record and when. It reads as an
            inspection of the record rather than part of its story, so it sits
            with the other rare verbs instead of beside the account's own
            timeline. */}
          {!overlay && (
            <Button
              small
              data-testid="company-full-history"
              onClick={onOpenHistory}
            >
              {t("record.fullHistory")}
            </Button>
          )}
        </OverflowMenu>
      )}
    </>
  );
}

/**
 * The cache key the account record is held under. Exported because the
 * shell's Ask surface names the account a reader is looking at, and reading
 * the name out of this cache is what lets it do that without a second
 * request. A shared key rather than a repeated string literal: a rename that
 * missed one copy would fail silently, as a panel that stopped knowing where
 * it was.
 */
export function organizationQueryKey(id: string) {
  return ["organization", id] as const;
}

export function CompanyScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const [tab, setTab] = useState<CompanyTab>("overview");
  const view = useOrganization360(id);
  // Only an assembled 360 counts as a visit: in overlay mode there is no
  // baseline to advance, and a page that never rendered the account is not
  // one the reader saw.
  useAcknowledgeOrganizationView(id, view.data?.state === "ready");
  // The account itself still comes from its own read: the 360 refuses
  // entirely in overlay mode, and the header must render either way.
  const orgQuery = useQuery({
    queryKey: organizationQueryKey(id),
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  return (
    <div className="wrap">
      <QueryGate query={orgQuery}>
        {(org) => (
          <CompanyRecord org={org} view={view} tab={tab} onTab={setTab} t={t} />
        )}
      </QueryGate>
    </div>
  );
}

// CompanyRecord renders the page once the account itself has loaded. Split
// out so the 360's three states — assembling, assembled, refused because the
// workspace reads elsewhere — are handled in one place rather than nested
// inside the account gate.
function CompanyRecord({
  org,
  view,
  tab,
  onTab,
  t,
}: Readonly<{
  org: Organization;
  view: { data?: Org360Result; isPending: boolean; isError: boolean };
  tab: CompanyTab;
  onTab: (next: CompanyTab) => void;
  t: ReturnType<typeof useT>;
}>) {
  const sorMode = useSorMode();
  const assembled = view.data?.state === "ready" ? view.data.view : undefined;
  // Two sources say "this workspace reads elsewhere", and either is enough.
  // The 360 refuses with a 422, and /me reports the mode directly — a
  // workspace that flipped mode after this page cached its read would
  // otherwise keep serving a native-looking company view.
  const overlay = view.data?.state === "overlay" || sorMode === "overlay";
  const visibleTabs = companyTabsFor(org, tab);
  // One tab is not a choice. A segmented control with a single option is a
  // button that does nothing, so the strip disappears entirely rather than
  // asking the reader to pick the page they are already on.
  const tabs =
    visibleTabs.length > 1 ? (
      <div className="co-tabs">
        <SegmentedControl
          options={visibleTabs}
          value={tab}
          onChange={onTab}
          labels={{
            overview: t("tab.overview"),
            context: t("tab.context"),
            people: t("tab.people"),
            timeline: t("tab.timeline"),
            partner: t("tab.partner"),
          }}
        />
      </div>
    ) : null;

  // Both tabs render inside ONE page. Partner used to be a different
  // component tree with no rails, so switching tab unmounted both side
  // columns and every query behind them: the grid re-columned under the
  // reader and the page refetched itself on the way back. Only the middle
  // column's body changes now.
  return (
    <CompanyPage
      org={org}
      view={assembled}
      overlay={overlay}
      loading={view.isPending}
      failed={view.isError}
      tab={tab}
      tabs={tabs}
      onTab={onTab}
    />
  );
}

// companySubtitle is the meta line under the name: what this company is,
// in the words the record already holds. Absent facts are absent, never
// guessed — the same evidence-or-omit rule the firmographics card follows.
// What the record IS, as chips under its name: the domain, the trade, the
// size, the owner. One fact per chip with its own icon, because a reader
// scanning for the domain should find it at a glance rather than parse it
// out of a run-on line of every firmographic joined by middots.
//
// A fact the record does not carry has no chip. An empty chip would be a
// field the reader has to read to discover is empty.
function CompanyIdentityChips({
  org,
}: Readonly<{ org: Organization }>): ReactNode {
  const primary = (org.domains ?? []).find((domain) => domain.is_primary);
  return (
    <div className="co-chips">
      {primary?.domain && (
        <a
          className="co-chip"
          href={`https://${primary.domain}`}
          target="_blank"
          rel="noreferrer noopener"
        >
          <Globe aria-hidden size={12} />
          {primary.domain}
        </a>
      )}
      {/* Industry, size and owner left for the facts card in the column
          below. Said in both places they were the same four values twice on
          one screen, and the masthead is the record's identity — its name and
          the way to reach it — not a second copy of its firmographics. */}
    </div>
  );
}

// The one PATCH shape every inline edit on the facts card shares: the
// If-Match version the page read, a sparse body naming only the field being
// corrected, and the same invalidation sweep so the card, the list and every
// other reader of this record (the 360, an EntityRef pointed at it) pick up
// the new value without a reload. A version conflict (409/412) surfaces as
// the mutation's error rather than retrying — the caller decides what the
// reader sees, this only ships the write.
function useInlineOrganizationSave<T>(
  org: Organization,
  toBody: (value: T) => UpdateOrganizationRequest,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (value: T) => {
      const { data, error } = await api.PATCH("/organizations/{id}", {
        params: { path: { id: org.id }, ...ifMatch(org.version) },
        body: toBody(value),
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["organizations"] });
      queryClient.invalidateQueries({
        queryKey: organizationQueryKey(org.id),
      });
      queryClient.invalidateQueries({ queryKey: ["organization360", org.id] });
      queryClient.invalidateQueries({
        queryKey: ["organization", "ref", org.id],
      });
    },
  });
}

// A version conflict reads as "reload and retry" (edit.versionSkew, the same
// copy the full edit modal already uses for the same 409); any other failure
// is the server's own detail. Neither path swallows the cause.
function inlineSaveError(
  error: unknown,
  t: ReturnType<typeof useT>,
): string | null {
  if (!error) {
    return null;
  }
  if (error instanceof ProblemError && isVersionSkew(error.problem)) {
    return t("edit.versionSkew");
  }
  return problemMessageOf(error, t);
}

// Both inline fields below are a REAL <input> at rest, not a read-only value
// behind a pencil trigger: the affordance IS the browser's own caret and Tab
// stop, so there is no state that a mouse can reach and a keyboard cannot.
// The only visual difference between resting and editable is border colour —
// transparent until hover/focus makes it read as a field (co-editable-field,
// company360.css) — so arriving at the field never nudges a neighbouring row.

type RosterUser = { id: string; display_name: string };

// The owner combobox's key handling, pulled out of the component so its own
// branches (Escape/ArrowUp/ArrowDown/Enter, each a different keyboard-only
// path) don't stack onto the component's rendering logic in one function.
function ownerComboboxKeyDown(
  event: KeyboardEvent<HTMLInputElement>,
  state: Readonly<{
    open: boolean;
    filtered: readonly RosterUser[];
    activeIndex: number;
    closeList: () => void;
    setOpen: (open: boolean) => void;
    setActiveIndex: (next: (index: number) => number) => void;
    pick: (user: RosterUser) => void;
    blurInput: () => void;
  }>,
): void {
  if (event.key === "Escape") {
    event.preventDefault();
    state.closeList();
    state.blurInput();
  } else if (event.key === "ArrowDown") {
    event.preventDefault();
    state.setOpen(true);
    state.setActiveIndex((index) =>
      Math.min(index + 1, state.filtered.length - 1),
    );
  } else if (event.key === "ArrowUp") {
    event.preventDefault();
    state.setOpen(true);
    state.setActiveIndex((index) => Math.max(index - 1, 0));
  } else if (event.key === "Enter" && state.open) {
    event.preventDefault();
    const active = state.filtered[state.activeIndex];
    if (active) {
      state.pick(active);
    }
  }
}

// The owner row: a choice from the workspace roster, never free text — the
// same option source and "no longer in the roster" honesty the full edit
// modal's owner picker already uses, so the two pickers can never disagree
// about who is even offered. The list before a keystroke IS the roster
// (typing only narrows it), because a blank query that matched nothing would
// hide the very suggestions this field exists to offer.
function CompanyOwnerCell({
  org,
  writable,
}: Readonly<{ org: Organization; writable: boolean }>) {
  const t = useT();
  const roster = useRoster("user", true);
  const save = useInlineOrganizationSave(org, (ownerId: string) => ({
    owner_id: ownerId,
  }));
  const listboxId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [activeIndex, setActiveIndex] = useState(0);
  // The just-picked owner, held here until the refetch this save triggers
  // catches up to it — without it the field would show the OLD owner for the
  // round trip a save takes, then jump. A failed save never clears it: the
  // reader's pick has to stay on screen next to the error, not vanish.
  const [picked, setPicked] = useState<{ id: string; name: string } | null>(
    null,
  );

  const owners = (roster.data ?? []).flatMap((entry) =>
    "display_name" in entry
      ? [{ id: entry.id, display_name: entry.display_name }]
      : [],
  );
  if (org.owner_id && !owners.some((user) => user.id === org.owner_id)) {
    owners.push({ id: org.owner_id, display_name: t("co.owner.notInRoster") });
  }

  if (!writable) {
    return org.owner_id ? (
      <EntityRef kind="user" id={org.owner_id} />
    ) : (
      t("co.pulse.unowned")
    );
  }

  const restName =
    picked?.name ??
    (org.owner_id
      ? (owners.find((user) => user.id === org.owner_id)?.display_name ?? "")
      : "");
  const filtered = owners.filter((user) =>
    user.display_name.toLowerCase().includes(query.trim().toLowerCase()),
  );
  const errorText = inlineSaveError(save.error, t);

  function closeList() {
    setOpen(false);
    setQuery("");
    setActiveIndex(0);
  }

  function pick(user: { id: string; display_name: string }) {
    closeList();
    if (user.id === (org.owner_id ?? "")) {
      return;
    }
    save.reset();
    setPicked({ id: user.id, name: user.display_name });
    save.mutate(user.id, { onSuccess: () => setPicked(null) });
  }

  return (
    <div className="co-editable co-owner-combobox">
      <input
        ref={inputRef}
        role="combobox"
        aria-expanded={open}
        aria-controls={open ? listboxId : undefined}
        aria-autocomplete="list"
        aria-activedescendant={
          open && filtered[activeIndex]
            ? `${listboxId}-${filtered[activeIndex].id}`
            : undefined
        }
        aria-label={t("co.company.owner")}
        className="co-editable-field"
        placeholder={t("co.pulse.unowned")}
        value={open ? query : restName}
        onFocus={() => {
          setQuery("");
          setOpen(true);
          setActiveIndex(0);
        }}
        onChange={(event) => {
          setQuery(event.target.value);
          setOpen(true);
          setActiveIndex(0);
        }}
        onBlur={closeList}
        onKeyDown={(event) =>
          ownerComboboxKeyDown(event, {
            open,
            filtered,
            activeIndex,
            closeList,
            setOpen,
            setActiveIndex,
            pick,
            blurInput: () => inputRef.current?.blur(),
          })
        }
      />
      {open && (
        <OwnerListbox
          listboxId={listboxId}
          label={t("co.company.owner")}
          noMatchesLabel={t("co.owner.noMatches")}
          filtered={filtered}
          activeIndex={activeIndex}
          onPick={pick}
        />
      )}
      {errorText && <p className="t-caption co-inline-error">{errorText}</p>}
    </div>
  );
}

// The typeahead's popup, split out of the cell above purely to keep that
// component's own branching (the input's focus/change/keydown wiring) from
// stacking onto the list's (empty state vs. one button per candidate).
function OwnerListbox({
  listboxId,
  label,
  noMatchesLabel,
  filtered,
  activeIndex,
  onPick,
}: Readonly<{
  listboxId: string;
  label: string;
  noMatchesLabel: string;
  filtered: readonly RosterUser[];
  activeIndex: number;
  onPick: (user: RosterUser) => void;
}>) {
  return (
    <div
      id={listboxId}
      role="listbox"
      aria-label={label}
      className="co-owner-listbox"
      // Mousedown, not click, is what steals focus from the input — left
      // unguarded, that focus shift fires the input's own blur (closeList, no
      // pick) BEFORE the option's click ever runs, so every pick would look
      // like the reader clicked outside and back out. Preventing the default
      // here keeps focus on the input; the click still reaches the option
      // underneath.
      onMouseDown={(event) => event.preventDefault()}
    >
      {filtered.length === 0 && (
        <div className="co-owner-empty t-caption">{noMatchesLabel}</div>
      )}
      {filtered.map((user, index) => (
        <button
          key={user.id}
          type="button"
          id={`${listboxId}-${user.id}`}
          role="option"
          aria-selected={index === activeIndex}
          tabIndex={-1}
          className="co-owner-option"
          onClick={() => onPick(user)}
        >
          {user.display_name}
        </button>
      ))}
    </div>
  );
}

// The website row. Rendered only when the org already has a primary domain —
// same rule the read-only chip above followed — so this cell's job is
// correcting that value, never inventing the first one (that stays the full
// edit modal's "Add domain" row). Plain text, not a link: a field that both
// navigates on click and edits on click cannot do either honestly, and this
// one is here to be corrected, not followed — the masthead's identity chip
// stays the click-through to the site.
function CompanyWebsiteCell({
  org,
  primaryDomain,
  writable,
}: Readonly<{ org: Organization; primaryDomain: string; writable: boolean }>) {
  const t = useT();
  const save = useInlineOrganizationSave(org, (domain: string) => ({
    domains: buildWebsitePatch(org.domains, domain),
  }));
  const inputRef = useRef<HTMLInputElement>(null);
  const [value, setValue] = useState(primaryDomain);
  const focusedRef = useRef(false);
  // Escape has to make the blur it triggers a no-op even though the value it
  // just reverted to hasn't reached the DOM yet — the blur handler that fires
  // synchronously off `.blur()` still closes over the PRE-revert render, so
  // comparing `value` there would resend the very edit Escape just cancelled.
  // A ref sidesteps that stale closure entirely.
  const cancelledRef = useRef(false);

  // Synced to the record's own value while the reader is not in the field —
  // this cell's own successful save, or an edit made elsewhere, must not
  // leave a stale draft sitting in an untouched field. A field the reader IS
  // in keeps whatever they typed; syncing then would stomp an edit in
  // progress the moment the record refetches.
  useEffect(() => {
    if (!focusedRef.current) {
      setValue(primaryDomain);
    }
  }, [primaryDomain]);

  if (!writable) {
    return (
      <a
        href={`https://${primaryDomain}`}
        target="_blank"
        rel="noreferrer noopener"
      >
        {primaryDomain}
      </a>
    );
  }

  const errorText = inlineSaveError(save.error, t);

  function commit() {
    const next = value.trim();
    if (next === primaryDomain.trim()) {
      return;
    }
    save.mutate(next);
  }

  return (
    <div className="co-editable">
      <input
        ref={inputRef}
        aria-label={t("co.company.website")}
        className="co-editable-field"
        value={value}
        onFocus={() => {
          focusedRef.current = true;
        }}
        onChange={(event) => setValue(event.target.value)}
        onBlur={() => {
          focusedRef.current = false;
          if (cancelledRef.current) {
            cancelledRef.current = false;
            return;
          }
          commit();
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault();
            cancelledRef.current = true;
            setValue(primaryDomain);
            inputRef.current?.blur();
          } else if (event.key === "Enter") {
            event.preventDefault();
            inputRef.current?.blur();
          }
        }}
      />
      {errorText && <p className="t-caption co-inline-error">{errorText}</p>}
    </div>
  );
}

/**
 * CompanyFactsCard is who this company IS, at the top of the column a reader
 * scans first: the handful of facts they would otherwise go hunting for
 * before a call.
 *
 * It reads the record's own columns, not the site-read profile — those
 * sixteen statements are a page of their own on Context. A row is drawn only
 * when the record carries the value: an "Industry —" line teaches a reader
 * that the field exists and nothing about the company.
 */
function CompanyFactsCard({
  org,
  onOpenHistory,
}: Readonly<{ org: Organization; onOpenHistory?: () => void }>) {
  const t = useT();
  const { locale } = useLocale();
  // The site read's grounded statements, beside the record's own columns.
  // They are the only values on this card that CAN carry evidence — a typed
  // industry has provenance at the record, not at the field — so the mark's
  // presence is the honest difference between what was read and what someone
  // entered, which is exactly what it exists to say.
  const groundedQuery = useQuery({
    queryKey: ["org-profile-fields", org.id],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/organizations/{id}/profile-fields",
        { params: { path: { id: org.id } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data.data ?? [];
    },
  });
  const primary = (org.domains ?? []).find((domain) => domain.is_primary);
  // An archived account is read-only everywhere else on this page (the
  // overflow menu's edit/merge/archive all hide the same way) — the facts
  // card's own edit affordances follow the same rule rather than inventing a
  // second one.
  const writable = !org.archived_at;
  // The facts are CHIPS, not a form. Six label-and-value rows down a narrow
  // column read as something to fill in; the same six as chips read as a
  // company, and the icon carries what the label used to say. The grounded
  // statements below stay rows — they are sentences, and a sentence in a
  // pill is just a pill nobody can scan.
  const rows: {
    key: string;
    icon: ReactNode;
    label: string;
    value: ReactNode;
  }[] = [];
  if (primary?.domain) {
    rows.push({
      key: "website",
      icon: <Globe aria-hidden size={12} />,
      label: t("co.company.website"),
      value: (
        <CompanyWebsiteCell
          org={org}
          primaryDomain={primary.domain}
          writable={writable}
        />
      ),
    });
  }
  if (org.industry) {
    rows.push({
      key: "industry",
      icon: <Building2 aria-hidden size={12} />,
      label: t("co.company.industry"),
      value: org.industry,
    });
  }
  if (org.size_band) {
    rows.push({
      key: "size",
      icon: <Users aria-hidden size={12} />,
      label: t("co.company.size"),
      value: org.size_band,
    });
  }
  rows.push({
    key: "owner",
    icon: <UserRound aria-hidden size={12} />,
    label: t("co.company.owner"),
    value: <CompanyOwnerCell org={org} writable={writable} />,
  });
  rows.push({
    key: "added",
    icon: <CalendarClock aria-hidden size={12} />,
    label: t("co.company.added"),
    value: formatDate(org.created_at, locale, RECORD_ZONE),
  });
  // The legal name is the one fact with no icon that means anything and no
  // short form, so it reads under the chips rather than inside one.
  const grounded = groundedQuery.data ?? [];
  return (
    <section className="card co-card co-lead">
      <h2 className="co-lead-title">{t("co.company.title")}</h2>
      {/* One fact per row, each named. As chips the label lived only in an
          icon, and an icon cannot tell "added" from "last contacted" — a bare
          date beside a calendar glyph is unreadable. The name earns its
          column. */}
      <dl className="co-facts">
        {rows.map((row) => (
          <div key={row.key}>
            <dt>
              {row.icon}
              {row.label}
            </dt>
            <dd>{row.value}</dd>
          </div>
        ))}
      </dl>
      {org.legal_name && <p className="co-legalname">{org.legal_name}</p>}
      {grounded.length > 0 && (
        <dl className="co-facts">
          {grounded.map((field) => (
            <div key={field.field}>
              <dt>{profileFieldLabel(field.field, t)}</dt>
              <dd>
                <EvidenceMark
                  value={field.value}
                  source={derivedSource(field, locale)}
                  onOpenHistory={onOpenHistory}
                />
              </dd>
            </div>
          ))}
        </dl>
      )}
      {/* The read's own three states, each said rather than collapsed into
          the record rows above: a failed read that rendered as "no grounded
          fields" would tell the reader this company has never been read. */}
      {groundedQuery.isPending && <Skeleton width="70%" />}
      {groundedQuery.isError && (
        <p className="t-caption">{problemMessageOf(groundedQuery.error, t)}</p>
      )}
      {groundedQuery.isSuccess && grounded.length === 0 && (
        <p className="t-caption">{t("org.firmographicsEmpty")}</p>
      )}
    </section>
  );
}

// useAccountChronology assembles the middle column's history: what happened
// with this account, what changed about the record, or both in one order.
//
// The two feeds page independently, so "both" is not a concatenation — the
// merge is cut where it stops being provably complete (mergeChronology), and
// the cut is stated rather than left to look like the end of the history.
function useAccountChronology({
  orgId,
  filter,
  activities,
  activitiesHaveMore,
  renderActions,
}: Readonly<{
  orgId: string;
  filter: TimelineFilter;
  activities: Activity[];
  activitiesHaveMore: boolean;
  renderActions: (activity: Activity) => ReactNode;
}>): {
  entries: TimelineEntry[];
  truncated: boolean;
  changes: ChangesQuery;
  // What the CURRENT filter is waiting on or failed at. A query that is
  // switched off never resolves — it reports pending forever — so the caller
  // must not read the query's own flags. Reading them turned the default
  // Activities view into a skeleton that never became a timeline.
  loading: boolean;
  failed: boolean;
  // Whether fetching more CHANGES would lengthen the merged view. When the
  // activity feed is the shorter of the two, it is not: the merge cuts at its
  // oldest row and every extra change page falls below that line.
  changesAreTheLimit: boolean;
} {
  const t = useT();
  const viewerId = useViewerId();
  const wantsChanges = filter !== "activities";
  const changes = useFieldHistory("organization", orgId, {
    enabled: wantsChanges,
  });
  const changeRows = changes.data?.pages.flatMap((page) => page.data) ?? [];
  const activityEntries = activityTimeline(activities, viewerId, renderActions);
  const changeEntries = changeTimeline(
    changeRows,
    (field) => coldFieldLabel(field, t),
    viewerId,
  );
  const loading = wantsChanges && changes.isPending;
  const failed = wantsChanges && changes.isError;

  if (filter === "activities") {
    return {
      entries: activityEntries,
      // The 360 caps this section, and a capped list that says nothing reads
      // as the whole history: a rep looking at the oldest of 25 rows would
      // take it for the day the relationship began.
      truncated: activitiesHaveMore,
      changes,
      loading: false,
      failed: false,
      changesAreTheLimit: false,
    };
  }
  if (filter === "changes") {
    return {
      entries: changeEntries,
      truncated: false,
      changes,
      loading,
      failed,
      changesAreTheLimit: changes.hasNextPage,
    };
  }
  const merged = mergeChronology<TimelineEntry>(
    [
      { rows: activityEntries, hasMore: activitiesHaveMore },
      { rows: changeEntries, hasMore: changes.hasNextPage },
    ],
    (entry) => entry.atIso,
  );
  // The merged view is cut at the newest "oldest loaded" among the feeds that
  // still have more. Another page of changes only reaches the reader when the
  // change feed owns that cut — i.e. its oldest loaded row is not older than
  // the activity feed's.
  const oldest = (rows: TimelineEntry[]) =>
    rows.length > 0
      ? rows.reduce((a, b) => (a.atIso < b.atIso ? a : b)).atIso
      : undefined;
  const oldestChange = oldest(changeEntries);
  const oldestActivity = oldest(activityEntries);
  const changesAreTheLimit =
    changes.hasNextPage &&
    (!activitiesHaveMore ||
      oldestChange === undefined ||
      oldestActivity === undefined ||
      oldestChange >= oldestActivity);

  return {
    entries: merged.rows,
    truncated: merged.truncated,
    changes,
    loading,
    failed,
    changesAreTheLimit,
  };
}

// The four RecordView slots the chronology section fills: the list, the
// filter above it, the load-more and disclosure below it, and the notice that
// replaces the list when there is nothing honest to draw. Assembled here so
// the page's render reads as a layout rather than as four nested ternaries.
// useResetOnRecord is the chronology filter, owned by the ACCOUNT being read
// rather than by the session. When both records are already cached the route
// swaps one company for another without ever unmounting this component, so a
// reader who checked Changes once met Changes on every account afterwards.
function useResetOnRecord(
  recordId: string,
): [TimelineFilter, (next: TimelineFilter) => void] {
  const [filter, setFilter] = useState<TimelineFilter>("activities");
  const [filterFor, setFilterFor] = useState(recordId);
  if (filterFor !== recordId) {
    setFilterFor(recordId);
    setFilter("activities");
  }
  return [filter, setFilter];
}

type ChronologySlots = {
  timeline?: TimelineEntry[];
  timelineGroups?: readonly TimelineGroup[];
  timelineHeader?: ReactNode;
  timelineFooter?: ReactNode;
  timelineNotice?: ReactNode;
  onOpenThread?: (threadKey: string) => void;
};

// ChronologyFilter narrows the account's own history. It sits ABOVE the list
// rather than in the page's tab strip: it scopes this section, and a control
// that looks like a tab reads as a different page.
function ChronologyFilter({
  filter,
  onFilter,
}: Readonly<{
  filter: TimelineFilter;
  onFilter: (next: TimelineFilter) => void;
}>) {
  const t = useT();
  return (
    <div className="co-tabs">
      <SegmentedControl
        options={TIMELINE_FILTERS}
        value={filter}
        onChange={onFilter}
        label={t("co.chronology.label")}
        labels={{
          activities: t("co.chronology.activities"),
          changes: t("co.chronology.changes"),
          all: t("co.chronology.all"),
        }}
      />
    </div>
  );
}

function useChronologySlots({
  org,
  view,
  overlay,
  loading,
  failed,
  active,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  overlay: boolean;
  loading: boolean;
  failed: boolean;
  // Whether the chronology is on screen at all. The Partner tab is a form,
  // so it renders no timeline rather than an empty one.
  active: boolean;
}>): {
  slots: ChronologySlots;
  showChanges: () => void;
} {
  const t = useT();
  const [filter, setFilter] = useResetOnRecord(org.id);

  const history = useAccountChronology({
    orgId: org.id,
    filter,
    activities: view?.activities?.data ?? [],
    activitiesHaveMore: view?.activities?.page.has_more ?? false,
    renderActions: (activity) => (
      <TimelineActions
        activity={activity}
        entityType="organization"
        entityId={org.id}
      />
    ),
  });
  // An evidence mark asks "where did this value come from" — the answer is
  // the record's change history, so the mark turns the timeline to Changes
  // rather than opening a screen of its own.
  const showChanges = () => setFilter("changes");

  if (!active) {
    return { slots: { timelineNotice: <span /> }, showChanges };
  }
  // In overlay mode the refusal is stated once, in the body: repeating it over
  // the timeline would read as two separate things being unavailable rather
  // than one page not being assembled.
  if (overlay) {
    return {
      slots: { timeline: history.entries, timelineNotice: <span /> },
      showChanges,
    };
  }
  return {
    showChanges,
    slots: {
      timeline: history.entries,
      // Conversations, not messages. The account's timeline is where the same
      // exchange showed up three times — a product update to three contacts
      // was three rows, and a five-message thread was five.
      timelineGroups: groupChronology(
        history.entries,
        view?.activities?.page.has_more ?? false,
      ),
      timelineHeader: <ChronologyFilter filter={filter} onFilter={setFilter} />,
      timelineFooter: (
        <>
          {/* Where the merged view stops being complete, said out loud.
              Silence here would read as the end of the account's history. */}
          {history.truncated && (
            <p className="t-small">
              {t(
                filter === "activities"
                  ? "co.chronology.truncatedActivities"
                  : "co.chronology.truncated",
              )}
            </p>
          )}
          {/* Only where fetching more changes actually lengthens the list.
              Under "all" the merge is cut at whichever feed is shorter, so if
              the ACTIVITY feed is the constraint, another page of changes is
              filtered straight back out and the button does nothing. */}
          {(filter === "changes" ||
            (filter === "all" && history.changesAreTheLimit)) && (
            <LoadMoreButton query={history.changes} />
          )}
        </>
      ),
      timelineNotice: chronologyNotice(
        {
          // Per filter, because the two feeds fail independently. A 360 that
          // omitted its activities section says nothing about the change
          // feed, and reporting the Changes view as unavailable on that
          // basis hid rows that had loaded perfectly well.
          loading:
            filter === "changes" ? history.loading : loading || history.loading,
          failed:
            filter === "changes" ? history.failed : failed || history.failed,
          assembled: filter === "changes" ? true : Boolean(view?.activities),
          filter,
        },
        history.entries.length,
        t,
      ),
    },
  };
}

// CompanyPage is the page itself: identity and verbs at the top, then three
// zones — what this company IS on the left, what is HAPPENING in the middle,
// the BUSINESS around it on the right.
//
// All three tabs render here. The rails belong to the ACCOUNT, not to the
// overview, so they stay mounted whichever tab is open and the reader keeps
// the firmographics and the business context while reading the partner form
// or the change history.
function CompanyPage({
  org,
  view,
  overlay,
  loading,
  failed,
  tab,
  tabs,
  onTab,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  overlay: boolean;
  loading: boolean;
  // The composite read failed. Distinct from "still loading" and from "the
  // account is empty", because all three would otherwise draw the same
  // blank page and only one of them is a fact about the account.
  failed: boolean;
  tab: CompanyTab;
  tabs: ReactNode;
  // An evidence mark can be clicked from the Partner tab, where the timeline
  // it wants to filter is not on screen; the page has to come back to the
  // Overview before the filter means anything.
  onTab: (next: CompanyTab) => void;
}>) {
  const t = useT();
  const [openTaskId, setOpenTaskId] = useState<string | null>(null);
  // The two surfaces a suggestion's action opens. Held here because both are
  // page-level: the composer anchors on a timeline message, and the task form
  // is the account's own log-activity surface.
  const [replyToActivityId, setReplyToActivityId] = useState<string | null>(
    null,
  );
  const [taskFormOpen, setTaskFormOpen] = useState(false);
  const [decisionsOpen, setDecisionsOpen] = useState(false);
  const [auditOpen, setAuditOpen] = useState(false);
  // A task written or completed from this page changes the account's timeline,
  // its next steps (both read from the 360) and the standing work queue.
  const taskUpdate = useTaskUpdate(taskWriteKeys("organization", org.id));
  const { slots, showChanges: filterToChanges } = useChronologySlots({
    org,
    view,
    overlay,
    loading,
    failed,
    // The chronology is the body of the overview, not a place to go: what
    // happened with an account is what a rep opened the record to read, and
    // behind a tab it was a click away from every question it answers. The
    // History tab keeps it too, with the filters, for reading further back.
    active: tab === "timeline" || tab === "overview",
  });
  // An evidence mark asks where a value came from, and the answer is the
  // record's change history — which now lives on its own tab.
  const showChanges = () => {
    onTab("timeline");
    filterToChanges();
  };
  return (
    <RecordView
      name={org.display_name}
      avatarSrc={org.logo_url}
      subtitle={<CompanyIdentityChips org={org} />}
      zone={RECORD_ZONE}
      badges={
        <CompanyActionBadges
          org={org}
          onOpenHistory={() => setAuditOpen(true)}
          onSetUpPartner={() => onTab("partner")}
        />
      }
      // The identity block is the record's name and the way to reach it, and
      // nothing else. The old pulse line — the way in, the last two touches —
      // said what the strip and the People card already say, and a decision
      // button under the name made a side errand the loudest thing on the
      // masthead.
      //
      // The composer opens from a button rather than standing open above the
      // page: a whole form in the header's action strip pushed the account's
      // own story below the fold before a word of it was read.
      actions={
        <>
          {tab === "overview" && (
            <DecisionsChip view={view} onOpen={() => setDecisionsOpen(true)} />
          )}
          <LogActivityAction entityType="organization" entityId={org.id} />
        </>
      }
      // Where the account stands and the tabs that switch what is read about
      // it belong to the record's masthead, not to the overview: they were
      // the same on every tab and still redrew themselves inside each one.
      // The skeleton is drawn while the 360 is still in flight, not merely
      // while there is no view: an overlay refusal or a failed read also
      // leaves `view` undefined, and both are settled answers, not a strip
      // still loading.
      strip={
        loading ? (
          <StateStripSkeleton />
        ) : view ? (
          <CompanyStanding view={view} />
        ) : undefined
      }
      tabs={tabs}
      // No full-width band above the columns. Spanning the page, the plate
      // and the brief pushed the account itself below the fold, so a reader
      // who opened the record to glance at the company met two panels of
      // work first. The plate is the FEED now: it heads the right column,
      // above the chronology it belongs with, and the left column carries
      // the company from the first pixel.
      // One left column, the business beside the story: people, deals,
      // standing, then the slower-moving reference (relationships, custom
      // fields) folded under it. Everything the site read produced —
      // sixteen profile statements, the fact groups, the tools that fetch
      // them — stays on the Context tab: folded into this column it was
      // more content than the rest of the page put together.
      // The business column belongs to the OVERVIEW, not to the record.
      // Mounted page-wide it repeated itself on every tab — the People tab
      // drew People twice, and Context read as the overview with a profile
      // bolted to its side. The other tabs are each one subject, and take
      // the width.
      //
      // Overlay refuses the page whole: a business column of cards that each
      // read as an empty account is the half-page the refusal exists to
      // prevent.
      aside={
        tab === "overview" ? (
          <>
            {/* Who they are, before anything about how it is going with
                them: this is the card a reader doing a ten-second scan came
                for. It reads the RECORD's own columns, so it survives
                overlay mode — the mirror refuses the composite read, not the
                account itself. */}
            <CompanyFactsCard org={org} onOpenHistory={showChanges} />
            {!overlay &&
              businessRail({
                org,
                view,
                failed,
                readOnly: Boolean(org.archived_at),
              })}
          </>
        ) : undefined
      }
      asideFirst
      timelineTitle={t("co.story.title")}
      // The chronology is the account's story and belongs to the overview.
      // The Partner tab is a form, so it does not repeat it under itself.
      {...slots}
    >
      {/* Overlay refuses the whole company page, not one tab of it: the
          partner extension and the field history are native records the
          mirror does not hold, so switching tabs must not walk around the
          refusal into reads that can only fail. */}
      {overlay && <OverlayFallback />}
      {!overlay && tab === "partner" && <PartnerTab organizationId={org.id} />}
      {!overlay && tab === "context" && (
        <ContextTab org={org} view={view} onOpenHistory={showChanges} />
      )}
      {tab === "overview" && failed && (
        <EmptyState>{t("co.partial")}</EmptyState>
      )}
      {/* The feed heads the right column: what moved, what is owed, and the
          one line the brief has to add — then the chronology under it. */}
      {tab === "overview" && view && (
        <AccountPlate
          org={org}
          view={view}
          overlay={overlay}
          taskUpdate={taskUpdate}
          onOpenTask={setOpenTaskId}
          onOpenRecord={openCitation}
          onCompose={setReplyToActivityId}
          onLogTask={() => setTaskFormOpen(true)}
        />
      )}
      {/* The People tab gives the account team the whole middle column. The
          rail's card is a summary; this is the roster, with room for the title
          and the last exchange beside each name. */}
      {!overlay && tab === "people" && (
        <PeopleCard view={view} writable={!org.archived_at} orgId={org.id} />
      )}
      <CompanySurfaces
        org={org}
        view={view}
        onOverview={tab === "overview"}
        replyToActivityId={replyToActivityId}
        taskFormOpen={taskFormOpen}
        openTaskId={openTaskId}
        decisionsOpen={decisionsOpen}
        taskUpdate={taskUpdate}
        onCloseReply={() => setReplyToActivityId(null)}
        onCloseTaskForm={() => setTaskFormOpen(false)}
        onCloseTask={() => setOpenTaskId(null)}
        onCloseDecisions={() => setDecisionsOpen(false)}
      />
      {/* The audit spine, opened from the header's overflow menu. It belongs
          to the RECORD, not to a tab, so it opens over whichever tab is up. */}
      <Modal
        open={auditOpen}
        onClose={() => setAuditOpen(false)}
        labelledBy="co-audit-title"
        size="wide"
      >
        <h2 id="co-audit-title" className="t-h2 modal-title">
          {t("record.fullHistory")}
        </h2>
        {/* Mounted only while open: the two history reads behind it are the
            page's most expensive, and nobody who never opens the panel should
            pay for them. */}
        {auditOpen && <RecordHistoryTab kind="organization" id={org.id} />}
        <div className="form-actions">
          <Button onClick={() => setAuditOpen(false)}>{t("fab.close")}</Button>
        </div>
      </Modal>
    </RecordView>
  );
}

// performSuggestion carries out the advice the server named.
//
// It routes rather than acts: each kind opens the governed surface a rep would
// have reached for anyway, prefilled from the evidence the rule fired on.
// Nothing here writes, and nothing is staged — the card advises, the surface
// it opens is where the human decides.
//
// draft_reply and add_task both land on the account's own timeline surfaces,
// which is why the page owns this handler rather than the card: the composer
// and the log-activity form live here.
function performSuggestion(
  action: SuggestionAction,
  open: { compose: (activityId: string) => void; logTask: () => void },
) {
  if (action.kind === "open_deal" && action.deal_id) {
    openCitation("deal", action.deal_id);
    return;
  }
  if (action.kind === "draft_reply" && action.activity_id) {
    open.compose(action.activity_id);
    return;
  }
  if (action.kind === "add_task") {
    open.logTask();
  }
}

// openCitation routes a cited record to its own screen. The brief, the
// prepared answers and the suggestions all cite the same records, so they
// share one route — a second copy would drift and send one card's reader to
// the wrong screen.
export function openCitation(entityType: string, entityId: string) {
  if (entityType === "deal") {
    navigate({ screen: "deals", id: entityId });
  }
  if (entityType === "person") {
    navigate({ screen: "contacts", id: entityId });
  }
}

// businessRail is the right column. A failed composite read must not simply
// remove it: an account page with no people, no deals and no signals reads
// as an account with none of those, which is the one thing the page does not
// know. The rail stays and each card says it could not be loaded — except in
// overlay mode, where the single page-level refusal already covers it.
/**
 * CompanySurfaces are the record's overlays: the composer a suggestion
 * shortcuts to, the task form, an opened task, and the decision queue.
 *
 * Each is mounted only while it is open — the reads behind them are not paid
 * for by a reader who never opens one — and they live together because none
 * of them is part of the page's reading order.
 */
function CompanySurfaces({
  org,
  view,
  onOverview,
  replyToActivityId,
  taskFormOpen,
  openTaskId,
  decisionsOpen,
  taskUpdate,
  onCloseReply,
  onCloseTaskForm,
  onCloseTask,
  onCloseDecisions,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  // The decision queue belongs to the OVERVIEW. Left standing over Partner it
  // put a panel from one tab on top of another, and a reader who switched
  // tabs to get rid of it could not.
  onOverview: boolean;
  replyToActivityId: string | null;
  taskFormOpen: boolean;
  openTaskId: string | null;
  decisionsOpen: boolean;
  taskUpdate: ReturnType<typeof useTaskUpdate>;
  onCloseReply: () => void;
  onCloseTaskForm: () => void;
  onCloseTask: () => void;
  onCloseDecisions: () => void;
}>) {
  return (
    <>
      {/* Anchored on the message a draft_reply suggestion named. It is the
          same modal the timeline's own Reply opens — the advice shortcuts to
          it rather than inventing a second way to answer. */}
      {replyToActivityId && (
        <ComposeModal
          activityId={replyToActivityId}
          entityType="organization"
          entityId={org.id}
          kind="email"
          open
          onClose={onCloseReply}
        />
      )}
      {taskFormOpen && (
        <LogActivityAction
          entityType="organization"
          entityId={org.id}
          initialKind="task"
          openOnMount
          onClose={onCloseTaskForm}
        />
      )}
      {openTaskId && (
        <TaskDetailModal
          activityId={openTaskId}
          onClose={onCloseTask}
          update={taskUpdate}
        />
      )}
      {decisionsOpen && onOverview && (
        <CompanyApprovalsPanel
          orgId={org.id}
          view={view}
          onClose={onCloseDecisions}
        />
      )}
    </>
  );
}

/**
 * AccountPlate is the overview's reading, in the order a rep works: what
 * moved while they were away, then the work waiting on them — their open
 * tasks and the account's advice as one block — and only then the prose
 * brief that describes the account.
 *
 * The plate itself is one surface. As two cards each carrying its own edge
 * the reader assembled it themselves, and the page spends its only elevation
 * here because this is what it is read to act on.
 */
function AccountPlate({
  org,
  view,
  overlay,
  taskUpdate,
  onOpenTask,
  onOpenRecord,
  onCompose,
  onLogTask,
}: Readonly<{
  org: Organization;
  view: Organization360View;
  overlay: boolean;
  taskUpdate: ReturnType<typeof useTaskUpdate>;
  onOpenTask: (activityId: string) => void;
  onOpenRecord: (entityType: string, entityId: string) => void;
  onCompose: (activityId: string) => void;
  onLogTask: () => void;
}>) {
  return (
    <>
      <SinceLastVisitStrip view={view} />
      <div className="co-plate">
        <NextSteps
          view={view}
          onOpenTask={(step) => onOpenTask(step.activity_id)}
          renderAction={(step) => (
            <TaskQuickActions
              activityId={step.activity_id}
              dueAt={step.due_at}
              update={taskUpdate}
            />
          )}
        />
        <SuggestionsSection
          orgId={org.id}
          view={view}
          onOpenRecord={onOpenRecord}
          onPerform={(action) =>
            performSuggestion(action, {
              compose: onCompose,
              logTask: onLogTask,
            })
          }
        />
      </div>
      <AccountBrief
        orgId={org.id}
        view={view}
        enabled={!overlay}
        onOpenRecord={onOpenRecord}
      />
    </>
  );
}

// The standing strip with this screen's own words for a lifecycle and a
// relationship type. A component rather than inline JSX so the page that
// mounts it does not carry the label lookups in its own complexity.
function CompanyStanding({ view }: Readonly<{ view: Organization360View }>) {
  const t = useT();
  return (
    <StateStrip
      view={view}
      lifecycleLabel={(value) =>
        t(LIFECYCLE_LABELS[value as keyof typeof LIFECYCLE_LABELS])
      }
      relationshipLabels={(values) =>
        values
          .map((value) =>
            t(
              RELATIONSHIP_TYPE_LABELS[
                value as keyof typeof RELATIONSHIP_TYPE_LABELS
              ],
            ),
          )
          .join(" · ")
      }
    />
  );
}

/**
 * ContextTab is everything Margince holds about this account, and where it
 * came from: the statements the site read, the facts behind them, the
 * roll-up they aggregate into, and the two reads that fetch more.
 *
 * One destination, because they are one subject. Folded into the record's
 * rail they were more content than the rest of the page put together, behind
 * four grey summary lines that nobody opened before a call.
 */
/**
 * ContextTab is everything about this company a rep consults rather than
 * reads daily: what we know about them, how this record is filed, and the
 * two depths of website read that fill the first of those.
 *
 * The roll-up left for the overview's business column — a weighted pipeline
 * is a commercial reading, not a fact about the company.
 */
function ContextTab({
  org,
  view,
  onOpenHistory,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  onOpenHistory: () => void;
}>) {
  const t = useT();
  const readOnly = Boolean(org.archived_at);
  return (
    <div className="co-context">
      {/* The grounded profile fields moved to the company card on the
          overview, where a reader looking for what this company IS already
          goes. What stays here is the long tail: the read's own facts, the
          two depths of website read, and how the record is filed. */}
      <FactsCard orgId={org.id} onOpenHistory={onOpenHistory} />
      {/* One capability at two depths. Each depth is already a card, so the
          heading is a plain one above them rather than a fourth card holding
          two more — a card inside a card reads as a mistake, not a group. */}
      <h3 className="co-context-group t-label">{t("co.website.title")}</h3>
      <EnrichCard orgId={org.id} />
      <DeepReadCard orgId={org.id} />
      {/* How the record is filed. Each of these ALREADY renders a card with
          its own heading, so wrapping them in a disclosure inside a card
          drew the box twice and the title twice, with the fold's separator
          rule stranded inside the outer edge. They stand as the cards they
          are. Context is the reference tab; nothing here needs hiding from
          a reader who chose to open it. */}
      <TagsCard
        view={view}
        tagAction={readOnly ? undefined : <TagAction orgId={org.id} />}
        listAction={readOnly ? undefined : <ListAction orgId={org.id} />}
      />
      <RelationshipsTab scope={{ organization_id: org.id }} />
      <CustomFieldsCard object="organization" record={org} />
    </div>
  );
}

function businessRail({
  org,
  view,
  failed,
  readOnly,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  failed: boolean;
  // An archived company takes no new deals, tags or list rows, so it shows no
  // verb that would only be refused.
  readOnly: boolean;
}>): ReactNode {
  if (view) {
    return (
      <>
        {/* Who and what, in the order a rep about to reach out asks for them.
            The connections graph and the filing metadata fold away: the graph
            re-lists the people directly above it, and lists and tags are how
            the account is filed rather than anything about the account. */}
        {/* Roles are a write, so the card offers them on the same terms as
            every other verb on this page: never on an archived record. */}
        {/* Who and what the money is: one surface, because a rep weighing an
            account reads them together. Five separately-edged boxes down one
            column read as five subjects and scrolled like it. */}
        {/* One subject, one card. People and Deals shared an edge, so the
            account team and the pipeline read as a single list of mixed
            things — and neither could carry its own verb where the reader
            would look for it. */}
        <PeopleCard
          view={view}
          writable={!readOnly}
          orgId={org.id}
          // Linking someone to the account is an employment edge, which is
          // the relationship the Context tab writes. The verb belongs where
          // the roster is read: an empty People card that cannot add a person
          // asks the reader to go and find the form.
          actions={
            readOnly ? undefined : (
              <AddRelationshipAction scope={{ organization_id: org.id }} />
            )
          }
        />
        <DealsCard
          view={view}
          // The verb sits under the list it changes: a rep who has just read
          // "no open deal on this account" is one click from opening one,
          // rather than leaving for the board to re-find this company there.
          actions={
            readOnly ? undefined : (
              <NewDealAction orgId={org.id} orgName={org.display_name} />
            )
          }
        />
        {/* How the relationship stands, in parts — the decomposition that
            replaced the header's 0-100 score — and what is currently flagged
            about it. Both are readings of the account's condition. */}
        <HealthCard view={view} />
        <SignalsCard orgId={org.id} />
        {/* Connections is deliberately not here. It listed the account owner
            and the same employees the People card already names, against two
            different strength scales and a bare "2" nobody could read — the
            page's own answer to a question no reader had asked. The graph read
            stays (connections.tsx, GET /organizations/{id}/graph): it returns
            as a per-contact "find a route in", which is the question that IS
            worth asking, and only where a route actually exists. */}
        {/* The account's own commercial total, and only when the account is
            a group. Lists, tags, links and the field tooling are how this
            record is FILED rather than anything about the company, so they
            read on the Context tab with the rest of the reference. */}
        <HierarchyRollupCard orgId={org.id} />
      </>
    );
  }
  if (!failed) {
    // Still loading. The rail must occupy its column NOW: RecordView picks its
    // grid template from which zones are present, so a rail that arrives with
    // the read re-columns the page under the reader — the whole middle column
    // and everything above it shift the moment the 360 lands.
    return (
      <>
        <section className="card co-card">
          <Skeleton width="100%" height={96} />
        </section>
        <section className="card co-card">
          <Skeleton width="100%" height={96} />
        </section>
        <section className="card co-card">
          <Skeleton width="100%" height={64} />
        </section>
      </>
    );
  }
  // The read failed, so the cards get NO payload and say so themselves.
  // Handing them a fabricated empty one would mean inventing an as_of the
  // page does not have, and would be indistinguishable from a real answer
  // one refactor later.
  return (
    <>
      <PeopleCard />
      <DealsCard />
      <HealthCard />
      <SignalsCard orgId={org.id} />
    </>
  );
}

// chronologyNotice keeps four things apart that all render as an empty
// list if you let them: still loading, the read failed, the section was
// never in the payload, and the account genuinely has nothing to show. Only
// the last one may say so — the other three would have a rep conclude
// nobody has ever touched this account.
//
// The empty sentence names what the filter was looking for. "Nothing logged
// on this account" under the Changes filter would be a claim about the
// activity feed the reader is not looking at.
function chronologyNotice(
  timeline: {
    loading: boolean;
    failed: boolean;
    assembled: boolean;
    filter: TimelineFilter;
  },
  count: number,
  t: ReturnType<typeof useT>,
): ReactNode {
  if (timeline.loading) {
    return <Skeleton width="100%" height={48} />;
  }
  // A read that failed outright leaves the section unassembled too — the
  // composite never arrived, so `view?.activities` is never there to check —
  // so `failed` is tested first and claims that case as the retryable one.
  // Only once the read is known to have succeeded does `!assembled` mean
  // what it says: the payload arrived and this section was withheld from
  // this caller, which invites no retry.
  if (timeline.failed) {
    return <EmptyState>{t("co.section.unavailable")}</EmptyState>;
  }
  if (!timeline.assembled) {
    return <EmptyState>{t("co.section.restricted")}</EmptyState>;
  }
  if (count > 0) {
    return undefined;
  }
  const empty: Record<TimelineFilter, MessageKey> = {
    activities: "co.timeline.empty",
    changes: "co.chronology.changesEmpty",
    all: "co.chronology.allEmpty",
  };
  return <EmptyState>{t(empty[timeline.filter])}</EmptyState>;
}
