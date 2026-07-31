import { useMutation, useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Avatar,
  Badge,
  Button,
  DataTable,
  Disclosure,
  EmptyState,
  SectionHeader,
  SegmentedControl,
  Skeleton,
} from "../design-system/atoms";
import { RecordView } from "../design-system/composed";
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
import { formatDateTime, formatMoney } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { taskWriteKeys } from "./activitykeys";
import { ArchiveAction } from "./archive";
import { AssistantPanel } from "./assistant";
import {
  coldFieldLabel,
  problemMessage,
  provenanceOf,
  QueryGate,
  QueryStates,
  throwProblem,
  useSorMode,
} from "./common";
import {
  DealsCard,
  NextSteps,
  type Org360Result,
  OverlayFallback,
  PeopleCard,
  RECORD_ZONE,
  SignalsCard,
  TagsCard,
  useOrganization360,
} from "./company360";
import { CompanyApprovalsPanel, DecisionsChip } from "./companyapprovals";
import { TimelineActions } from "./compose";
import { ConnectionsCard } from "./connections";
import { CreateAction, type CreateField, type FormRows } from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import { EntityRef } from "./entityref";
import { type FactGroup, factFieldLabelKey, groupFacts } from "./factview";
import { RecordHistoryTab } from "./history";
import { confidenceLevel } from "./inbox";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";
import { LogActivityAction } from "./logactivity";
import { MeetingBrief } from "./meetingbrief";
import { MergeAction } from "./merge";
import { PartnerTab } from "./partners";
import { activityTimeline } from "./people";
import { RelationshipsTab } from "./relationships";
import { ShareAction } from "./share";
import {
  TaskDetailModal,
  TaskQuickActions,
  useTaskUpdate,
} from "./taskactions";

// Companies list + company 360 (B-EP09.10a/b). Firmographics render
// evidence-or-omit: a field with no stored value is absent, never guessed.
// Search/filter/sort/pagination (P-14), the rich create modal (P-15), the
// If-Match edit form (P-1), and the dedupe view-existing link (P-16) are
// wired in here the same way as contacts (people.tsx) — the enrich flow,
// firmographics card, and timeline stay exactly as they were.

type Organization = components["schemas"]["Organization"];
type CreateOrganizationRequest =
  components["schemas"]["CreateOrganizationRequest"];
type UpdateOrganizationRequest =
  components["schemas"]["UpdateOrganizationRequest"];
type CompanyProfileField = components["schemas"]["CompanyProfileField"];
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
    throw new Error(problemMessage(error));
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
  };
  if (!sameDomainSet(desired, current)) {
    body.domains = desired;
  }
  return body;
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

const companyEditFields: CreateField[] = [
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
                header: t("org.classification"),
                render: (org: Organization) =>
                  org.classification ? (
                    <Badge>{org.classification}</Badge>
                  ) : null,
              },
              {
                key: "provenance",
                header: t("people.capturedBy"),
                render: (org: Organization) => (
                  <ProvenanceTag provenance={provenanceOf(org.captured_by)} />
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
        throw new Error(problemMessage(error));
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
          {enrich.error instanceof Error ? enrich.error.message : null}
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

const SITE_READ_KIND_LABELS: Record<
  components["schemas"]["SiteReadPage"]["kind"],
  MessageKey
> = {
  home: "deepread.kindHome",
  impressum: "deepread.kindImpressum",
  about: "deepread.kindAbout",
  team: "deepread.kindTeam",
  services: "deepread.kindServices",
  products: "deepread.kindProducts",
  contact: "deepread.kindContact",
  other: "deepread.kindOther",
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
        throw new Error(problemMessage(error));
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
        {reportQuery.error.message}
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
                <Badge>{t(SITE_READ_KIND_LABELS[page.kind])}</Badge>{" "}
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
        throw new Error(
          response.status === 501
            ? t("deepread.unavailable")
            : problemMessage(error),
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
          {start.error instanceof Error ? start.error.message : null}
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
    throw new Error(problemMessage(error));
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

  if (rollupQuery.isPending) {
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <Skeleton width="60%" />
        <Skeleton width="90%" />
        <Skeleton width="75%" />
      </div>
    );
  }
  if (rollupQuery.isError) {
    if (rollupQuery.error instanceof FxUnavailableError) {
      return <EmptyState>{t("rollup.fxUnavailable")}</EmptyState>;
    }
    return <EmptyState>{rollupQuery.error.message}</EmptyState>;
  }

  const rollup = rollupQuery.data;
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

function ProfileFieldRow({
  field,
  onOpenHistory,
}: Readonly<{ field: CompanyProfileField; onOpenHistory?: () => void }>) {
  const t = useT();
  const { locale } = useLocale();
  return (
    <div className="co-field">
      <span className="t-label">{profileFieldLabel(field.field, t)}</span>
      <div>
        <EvidenceMark
          value={field.value}
          source={derivedSource(field, locale)}
          onOpenHistory={onOpenHistory}
        />
      </div>
    </div>
  );
}

// The Firmographics & legal card: the org's confirmed profile fields, rendered
// evidence-or-omit — a field with no stored value is simply absent, never
// guessed. An empty read is stated honestly ("nothing read yet"), never
// fabricated into blank rows. This card carries the region's loading/error
// surface; the sibling facts card stays silent when it has nothing to add.
function ProfileFieldsCard({
  orgId,
  onOpenHistory,
}: Readonly<{ orgId: string; onOpenHistory?: () => void }>) {
  const t = useT();
  const fieldsQuery = useQuery({
    queryKey: ["org-profile-fields", orgId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/organizations/{id}/profile-fields",
        { params: { path: { id: orgId } } },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data.data ?? [];
    },
  });

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <SectionHeader title={t("co.profile.title")} />
      <QueryStates query={fieldsQuery}>
        {fieldsQuery.data && fieldsQuery.data.length === 0 ? (
          <p className="t-caption">{t("org.firmographicsEmpty")}</p>
        ) : (
          (fieldsQuery.data ?? []).map((field) => (
            <ProfileFieldRow
              key={field.field}
              field={field}
              onOpenHistory={onOpenHistory}
            />
          ))
        )}
      </QueryStates>
    </section>
  );
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
        throw new Error(problemMessage(error));
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

// Three tabs, not five. The company view is ONE scrolling page: the
// relationship edges and the hierarchy roll-up moved into its rails, where a
// rep reads them alongside everything else instead of hunting for them.
// History and Partner stay tabs because each is a different question, asked
// rarely, with its own surface.
const COMPANY_TABS = ["overview", "partner", "history"] as const;
type CompanyTab = (typeof COMPANY_TABS)[number];

// The company 360 badge/action bar. Archived records are read-only: the
// backend rejects edit/merge/archive on a non-live row (there is no unarchive
// path), so those buttons would only 404 — the Archived badge is the whole
// affordance. Extracted from CompanyScreen so its render stays legible.
function CompanyActionBadges({ org }: Readonly<{ org: Organization }>) {
  const t = useT();
  const cf = useObjectCustomFields("organization");
  const overlay = useSorMode() === "overlay";
  return (
    <>
      {org.classification && <Badge>{org.classification}</Badge>}
      <ProvenanceTag provenance={provenanceOf(org.captured_by)} />
      {org.archived_at ? (
        <Badge tone="warn">{t("record.archived")}</Badge>
      ) : (
        <>
          <EditAction
            label={t("record.edit")}
            notice={overlay ? t("overlay.partialWriteBack") : undefined}
            fields={[...companyEditFields, ...cf.formFields]}
            record={{
              id: org.id,
              version: org.version,
              display_name: org.display_name,
              legal_name: org.legal_name ?? "",
              industry: org.industry ?? "",
              size_band: org.size_band ?? "",
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
          {/* Merge has no incumbent-first projection — the seam refuses it
              outright (overlay/provider_writes.go Merge) — unlike
              edit/archive above, which it serves, so it stays hidden here. */}
          {!overlay && (
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
          <ArchiveAction
            label={t("record.archive")}
            confirmText={t("record.archiveConfirm")}
            archive={async () => {
              const { data, error } = await api.DELETE("/organizations/{id}", {
                params: { path: { id: org.id } },
              });
              if (error) {
                throwProblem(error);
              }
              return data;
            }}
            invalidate="organizations"
            recordKey="organization"
            onArchived={() => navigate({ screen: "companies" })}
          />
          {/* A record grant probes the native row via auth.EnsureLinkTarget,
              which a mirrored record has no row for — sharing stays hidden
              in overlay regardless of record type (see deals.tsx's
              DealBadges). */}
          {!overlay && (
            <ShareAction recordType="organization" recordId={org.id} />
          )}
        </>
      )}
    </>
  );
}

export function CompanyScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const [tab, setTab] = useState<CompanyTab>("overview");
  const view = useOrganization360(id);
  // The account itself still comes from its own read: the 360 refuses
  // entirely in overlay mode, and the header must render either way.
  const orgQuery = useQuery({
    queryKey: ["organization", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/organizations/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
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
  const tabs = (
    <div className="co-tabs">
      <SegmentedControl
        options={COMPANY_TABS}
        value={tab}
        onChange={onTab}
        labels={{
          overview: t("tab.overview"),
          partner: t("tab.partner"),
          history: t("tab.history"),
        }}
      />
    </div>
  );

  // Every tab renders inside ONE page. Partner and History used to be a
  // different component tree with no rails, so switching tab unmounted both
  // side columns and every query behind them: the grid re-columned under the
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
      onOpenHistory={() => onTab("history")}
    />
  );
}

// companySubtitle is the meta line under the name: what this company is,
// in the words the record already holds. Absent facts are absent, never
// guessed — the same evidence-or-omit rule the firmographics card follows.
function companySubtitle(org: Organization): string | undefined {
  const primary = (org.domains ?? []).find((domain) => domain.is_primary);
  const parts = [
    org.industry,
    primary?.domain,
    org.size_band,
    org.legal_name,
  ].filter((part): part is string => Boolean(part));
  return parts.length > 0 ? parts.join(" · ") : undefined;
}

// CompanyPulse is the one-line state of the relationship: how warm it is and
// who carries it, when it was last touched, and who owns it. Each part is
// omitted when the 360 could not answer it, so the line never implies a
// number the reader was not allowed to see.
function CompanyPulse({
  org,
  view,
  onOpenDecisions,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  // The overview owns the decisions panel, so only it can offer the way in.
  // The other tabs render the same pulse line without the chip rather than a
  // button that has nothing to open.
  onOpenDecisions?: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const strength = view?.strength;
  // The strength section is what carries BOTH the score and the last touch.
  // Withheld or absent, the line says nothing about either: "never
  // contacted" read off missing data is a business conclusion the page has
  // no basis for, and it is the one a rep would act on.
  const strengthKnown = Boolean(
    view && !view.sections_omitted?.includes("strength"),
  );
  return (
    <>
      {strength && <StrengthPulse strength={strength} />}
      {strengthKnown && (
        <span>
          {strength?.last_interaction
            ? t("co.pulse.lastTouch", {
                when: formatDateTime(
                  strength.last_interaction,
                  locale,
                  RECORD_ZONE,
                ),
              })
            : t("co.pulse.neverTouched")}
        </span>
      )}
      <span>
        {org.owner_id ? (
          <EntityRef kind="user" id={org.owner_id} />
        ) : (
          t("co.pulse.unowned")
        )}
      </span>
      <ProvenanceTag provenance={provenanceOf(org.captured_by)} />
      {/* What is waiting on a human decision here, and the way to make it.
          The count was a badge that led nowhere: a reader told that 27
          decisions are owed and given no way to pay them learns only that the
          page keeps score. */}
      {onOpenDecisions && (
        <DecisionsChip view={view} onOpen={onOpenDecisions} />
      )}
    </>
  );
}

// StrengthPulse renders the score and, when there is one, the contact who
// carries it. The contributor's NAME is a live lookup, so the sentence is
// assembled from two translated halves around it rather than interpolating
// an empty placeholder and appending the name after the full stop — which
// broke word order in English and worse in German.
function StrengthPulse({
  strength,
}: Readonly<{ strength: NonNullable<Organization360View["strength"]> }>) {
  const t = useT();
  if (!strength.contributor_person_id) {
    // A dormant account: no contact has ever interacted, so there is no
    // relationship to attribute and no number worth leading with.
    return <span>{t("co.pulse.noStrength")}</span>;
  }
  return (
    <span>
      {t("co.pulse.strengthLead", { score: strength.score })}{" "}
      <EntityRef kind="person" id={strength.contributor_person_id} />{" "}
      {t("co.pulse.strengthTail", { count: strength.contact_count })}
    </span>
  );
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
  onOpenHistory,
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
  // An evidence mark's "full history" opens the record's History tab, which
  // is local state on this screen rather than a route — so the mark is
  // handed the switch instead of a link it could not build.
  onOpenHistory: () => void;
}>) {
  const t = useT();
  const timeline = view?.activities?.data ?? [];
  const [openTaskId, setOpenTaskId] = useState<string | null>(null);
  const [decisionsOpen, setDecisionsOpen] = useState(false);
  // A task written or completed from this page changes the account's timeline,
  // its next steps (both read from the 360) and the standing work queue.
  const taskUpdate = useTaskUpdate(taskWriteKeys("organization", org.id));
  return (
    <RecordView
      name={org.display_name}
      avatarSrc={org.logo_url}
      subtitle={companySubtitle(org)}
      zone={RECORD_ZONE}
      badges={<CompanyActionBadges org={org} />}
      pulse={
        <CompanyPulse
          org={org}
          view={view}
          onOpenDecisions={() => setDecisionsOpen(true)}
        />
      }
      // The composer opens from a button rather than standing open above the
      // page: a whole form in the header's action strip pushed the account's
      // own story below the fold before a word of it was read.
      actions={
        <LogActivityAction entityType="organization" entityId={org.id} />
      }
      rail={
        overlay ? undefined : (
          <>
            <ProfileFieldsCard orgId={org.id} onOpenHistory={onOpenHistory} />
            <FactsCard orgId={org.id} onOpenHistory={onOpenHistory} />
            <RelationshipsTab scope={{ organization_id: org.id }} />
            {/* One-off tools and configuration, folded away. Standing open
                they carried the same weight as the facts a rep opens the page
                for, and they are used a fraction as often. */}
            <Disclosure summary={t("co.tools.title")}>
              <CustomFieldsCard object="organization" record={org} />
              <HierarchyRollupCard orgId={org.id} />
              <EnrichCard orgId={org.id} />
              <DeepReadCard orgId={org.id} />
            </Disclosure>
          </>
        )
      }
      aside={businessRail({ org, view, overlay, failed })}
      // The timeline is the account's story and belongs to the overview. The
      // Partner tab is a form and History has its own change list, so neither
      // repeats it under itself.
      timeline={
        tab === "overview"
          ? activityTimeline(timeline, (activity) => (
              <TimelineActions
                activity={activity}
                entityType="organization"
                entityId={org.id}
              />
            ))
          : undefined
      }
      // In overlay mode the refusal is stated once, in the body: repeating it
      // over the timeline would read as two separate things being
      // unavailable rather than one page not being assembled.
      timelineNotice={
        overlay || tab !== "overview" ? (
          <span />
        ) : (
          timelineNoticeFor(
            { loading, failed, assembled: Boolean(view?.activities) },
            timeline.length,
            t,
          )
        )
      }
    >
      {tabs}
      {/* Overlay refuses the whole company page, not one tab of it: the
          partner extension and the field history are native records the
          mirror does not hold, so switching tabs must not walk around the
          refusal into reads that can only fail. */}
      {overlay && <OverlayFallback />}
      {!overlay && tab === "partner" && <PartnerTab organizationId={org.id} />}
      {!overlay && tab === "history" && (
        <RecordHistoryTab kind="organization" id={org.id} />
      )}
      {tab === "overview" && failed && (
        <EmptyState>{t("co.partial")}</EmptyState>
      )}
      {/* The brief leads: what this account looks like right now, before the
          cards that report it field by field. It absorbed the standalone
          "since your last visit" block, because two cards each claiming to
          say what the state is made the reader arbitrate between them. */}
      {tab === "overview" && view && (
        <MeetingBrief view={view} orgId={org.id} onOpenRecord={openCitation} />
      )}
      {tab === "overview" && (
        <AssistantPanel
          orgId={org.id}
          enabled={!overlay}
          onOpenRecord={openCitation}
        />
      )}
      {tab === "overview" && view && (
        <NextSteps
          view={view}
          onOpenTask={(step) => setOpenTaskId(step.activity_id)}
          renderAction={(step) => (
            <TaskQuickActions
              activityId={step.activity_id}
              dueAt={step.due_at}
              update={taskUpdate}
            />
          )}
        />
      )}
      {openTaskId && (
        <TaskDetailModal
          activityId={openTaskId}
          onClose={() => setOpenTaskId(null)}
          update={taskUpdate}
        />
      )}
      {decisionsOpen && (
        <CompanyApprovalsPanel
          orgId={org.id}
          view={view}
          onClose={() => setDecisionsOpen(false)}
        />
      )}
    </RecordView>
  );
}

// openCitation routes a cited record to its own screen. The brief, the
// prepared answers and the suggestions all cite the same records, so they
// share one route — a second copy would drift and send one card's reader to
// the wrong screen.
function openCitation(entityType: string, entityId: string) {
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
function businessRail({
  org,
  view,
  overlay,
  failed,
}: Readonly<{
  org: Organization;
  view?: Organization360View;
  overlay: boolean;
  failed: boolean;
}>): ReactNode {
  if (overlay) {
    return undefined;
  }
  if (view) {
    return (
      <>
        <PeopleCard view={view} />
        <ConnectionsCard orgId={org.id} />
        <DealsCard view={view} />
        <SignalsCard orgId={org.id} />
        <TagsCard view={view} />
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
      {/* The connections card reads its own endpoint, so a failed 360 tells it
          nothing — it still tries, and says so itself if its own read fails. */}
      <ConnectionsCard orgId={org.id} />
      <DealsCard />
      <SignalsCard orgId={org.id} />
      <TagsCard />
    </>
  );
}

// timelineNoticeFor keeps four things apart that all render as an empty
// list if you let them: still loading, the read failed, the section was
// never in the payload, and the account genuinely has nothing logged. Only
// the last one may say so — the other three would have a rep conclude
// nobody has ever touched this account.
function timelineNoticeFor(
  timeline: { loading: boolean; failed: boolean; assembled: boolean },
  count: number,
  t: ReturnType<typeof useT>,
): ReactNode {
  if (timeline.loading) {
    return <Skeleton width="100%" height={48} />;
  }
  if (timeline.failed || !timeline.assembled) {
    return <EmptyState>{t("co.section.unavailable")}</EmptyState>;
  }
  return count === 0 ? (
    <EmptyState>{t("co.timeline.empty")}</EmptyState>
  ) : undefined;
}
