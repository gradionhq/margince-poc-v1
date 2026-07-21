import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  SectionHeader,
  SegmentedControl,
  Skeleton,
} from "../design-system/atoms";
import { RecordView } from "../design-system/composed";
import {
  AutonomyDot,
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { formatDateTime, formatMoney } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { ArchiveAction } from "./archive";
import {
  coldFieldLabel,
  OverlayUnavailable,
  problemMessage,
  provenanceOf,
  QueryGate,
  QueryStates,
  throwProblem,
  useSorMode,
} from "./common";
import { TimelineActions } from "./compose";
import { RecordContextPanel } from "./context";
import { CreateAction, type CreateField, type FormRows } from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import { RecordHistoryTab } from "./history";
import { confidenceLevel } from "./inbox";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";
import { LogActivity } from "./logactivity";
import { MergeAction } from "./merge";
import { PartnerTab } from "./partners";
import { activityTimeline } from "./people";
import { RelationshipsTab } from "./relationships";
import { ShareAction } from "./share";
import { StrengthCard } from "./strength";

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
): Promise<Organization> {
  const { data, error } = await api.POST("/organizations", {
    body: { ...mapOrgBody(values, rows ?? {}), ...customFields },
  });
  if (error) {
    throwProblem(error);
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
              createCompany(values, rows, cf.toBody(values))
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
                  <span>
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
function TrustSignals({
  capturedBy,
  confidence,
  evidenceSnippet,
  sourceUrl,
}: Readonly<{
  capturedBy?: string;
  confidence?: number | null;
  evidenceSnippet?: string | null;
  sourceUrl?: string | null;
}>) {
  const level = confidenceLevel(confidence);
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: 8,
        flexWrap: "wrap",
        marginTop: 4,
      }}
    >
      <ProvenanceTag provenance={provenanceOf(capturedBy)} />
      {level && <ConfidenceMeter level={level} />}
      {evidenceSnippet && (
        <EvidenceChip
          evidence={{ snippet: evidenceSnippet, source: sourceUrl ?? "" }}
        />
      )}
    </div>
  );
}

function ProfileFieldRow({ field }: Readonly<{ field: CompanyProfileField }>) {
  const t = useT();
  return (
    <div style={{ marginBottom: 12 }}>
      <span className="t-label">{coldFieldLabel(field.field, t)}</span>
      <div>{field.value}</div>
      <TrustSignals
        capturedBy={field.captured_by}
        confidence={field.confidence}
        evidenceSnippet={field.evidence_snippet}
        sourceUrl={field.source_url}
      />
    </div>
  );
}

// The Firmographics & legal card: the org's confirmed profile fields, rendered
// evidence-or-omit — a field with no stored value is simply absent, never
// guessed. An empty read is stated honestly ("nothing read yet"), never
// fabricated into blank rows. This card carries the region's loading/error
// surface; the sibling facts card stays silent when it has nothing to add.
function ProfileFieldsCard({ orgId }: Readonly<{ orgId: string }>) {
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
      <SectionHeader
        title={t("org.firmographicsLegal")}
        sub={t("org.evidenceOrOmit")}
      />
      <QueryStates query={fieldsQuery}>
        {fieldsQuery.data && fieldsQuery.data.length === 0 ? (
          <p className="t-caption">{t("org.firmographicsEmpty")}</p>
        ) : (
          (fieldsQuery.data ?? []).map((field) => (
            <ProfileFieldRow key={field.field} field={field} />
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
const FACT_CATEGORY_ORDER = [
  "company",
  "offering",
  "market",
  "signal",
] as const;

const FACT_CATEGORY_LABELS: Record<OrganizationFact["category"], MessageKey> = {
  company: "org.factCategory.company",
  offering: "org.factCategory.offering",
  market: "org.factCategory.market",
  signal: "org.factCategory.signal",
};

// One fact row: value plus its trust signals — provenance always, confidence
// whenever graded, and the evidence snippet — the same "confidence is never
// hidden" convention as ProfileFieldRow.
// A fact row shares ProfileFieldRow's vertical label/value/trust-signals
// layout (NOT the horizontal `.firmo` key/value grid, whose `flex-direction:
// column` descendant rule would collapse the TrustSignals footer).
function FactRow({ fact }: Readonly<{ fact: OrganizationFact }>) {
  const t = useT();
  return (
    <div style={{ marginBottom: 12 }}>
      <span className="t-label">{coldFieldLabel(fact.field, t)}</span>
      <div>{fact.value}</div>
      <TrustSignals
        capturedBy={fact.captured_by}
        confidence={fact.confidence}
        evidenceSnippet={fact.evidence_snippet}
        sourceUrl={fact.source_url}
      />
    </div>
  );
}

function FactsCard({ orgId }: Readonly<{ orgId: string }>) {
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
      {FACT_CATEGORY_ORDER.map((category) => {
        const group = facts.filter((fact) => fact.category === category);
        if (group.length === 0) {
          return null;
        }
        return (
          <div key={category} style={{ marginBottom: 16 }}>
            <div
              className="t-label"
              style={{ color: "var(--textMeta)", marginBottom: 8 }}
            >
              {t(FACT_CATEGORY_LABELS[category])}
            </div>
            {group.map((fact) => (
              <FactRow key={`${fact.field}:${fact.value_key}`} fact={fact} />
            ))}
          </div>
        );
      })}
    </section>
  );
}

const COMPANY_TABS = [
  "overview",
  "relationships",
  "partner",
  "rollup",
  "history",
] as const;
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
      ) : overlay ? // Edit/merge/archive all write to a mirrored record — hidden in
      // overlay (every such write answers unsupported_by_sor).
      null : (
        <>
          <EditAction
            label={t("record.edit")}
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
                throwProblem(error);
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
          <ShareAction recordType="organization" recordId={org.id} />
        </>
      )}
    </>
  );
}

export function CompanyScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const [tab, setTab] = useState<CompanyTab>("overview");
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
  // Entity-scoped activity reads are a dial the overlay mirror refuses (422);
  // skip the fetch and show the honest unavailable notice in the timeline slot.
  const overlay = useSorMode() === "overlay";
  const timelineQuery = useQuery({
    queryKey: ["activities", "organization", id],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: {
          query: { entity_type: "organization", entity_id: id, limit: 20 },
        },
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
          <RecordView
            name={org.display_name}
            subtitle={org.legal_name ?? undefined}
            zone="Europe/Berlin"
            badges={<CompanyActionBadges org={org} />}
            timeline={
              timelineQuery.isSuccess
                ? activityTimeline(timelineQuery.data.data, (activity) => (
                    <TimelineActions
                      activity={activity}
                      entityType="organization"
                      entityId={id}
                    />
                  ))
                : []
            }
            timelineNotice={overlay ? <OverlayUnavailable /> : undefined}
          >
            <div style={{ marginBottom: 16 }}>
              <SegmentedControl
                options={COMPANY_TABS}
                value={tab}
                onChange={setTab}
                labels={{
                  overview: t("tab.overview"),
                  relationships: t("tab.relationships"),
                  partner: t("tab.partner"),
                  rollup: t("tab.rollup"),
                  history: t("tab.history"),
                }}
              />
            </div>
            {tab === "overview" && (
              <>
                <StrengthCard kind="organization" id={org.id} />
                <section className="card" style={{ marginBottom: 16 }}>
                  <SectionHeader
                    title={t("org.firmographics")}
                    sub={t("org.evidenceOrOmit")}
                  />
                  <dl className="firmo">
                    {org.industry && (
                      <div>
                        <dt>{t("org.industry")}</dt>
                        <dd>{org.industry}</dd>
                      </div>
                    )}
                    {org.size_band && (
                      <div>
                        <dt>{t("org.size")}</dt>
                        <dd>{org.size_band}</dd>
                      </div>
                    )}
                    {org.domains && org.domains.length > 0 && (
                      <div>
                        <dt>{t("org.domains")}</dt>
                        <dd className="t-mono">
                          {org.domains
                            .map((domain) => domain.domain)
                            .join(", ")}
                        </dd>
                      </div>
                    )}
                  </dl>
                </section>
                <ProfileFieldsCard orgId={org.id} />
                <FactsCard orgId={org.id} />
                <CustomFieldsCard object="organization" record={org} />
                <EnrichCard orgId={org.id} />
                <DeepReadCard orgId={org.id} />
                <RecordContextPanel entityType="organization" id={org.id} />
                <LogActivity entityType="organization" entityId={org.id} />
              </>
            )}
            {tab === "relationships" && (
              <RelationshipsTab scope={{ organization_id: org.id }} />
            )}
            {tab === "partner" && <PartnerTab organizationId={org.id} />}
            {tab === "rollup" && <HierarchyRollupCard orgId={org.id} />}
            {tab === "history" && (
              <RecordHistoryTab kind="organization" id={org.id} />
            )}
          </RecordView>
        )}
      </QueryGate>
    </div>
  );
}
