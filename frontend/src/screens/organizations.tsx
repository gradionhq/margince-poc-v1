import { useMutation, useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  DataTable,
  SectionHeader,
} from "../design-system/atoms";
import { RecordView } from "../design-system/composed";
import {
  AutonomyDot,
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { useT } from "../i18n";
import {
  coldFieldLabel,
  problemMessage,
  provenanceOf,
  QueryGate,
  throwProblem,
} from "./common";
import { CreateAction, type CreateField, type FormRows } from "./create";
import { EditAction } from "./edit";
import { confidenceLevel } from "./inbox";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";
import { LogActivity } from "./logactivity";
import { activityTimeline } from "./people";

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

function asSizeBand(
  value: string | undefined,
): CreateOrganizationRequest["size_band"] {
  return (SIZE_BAND_OPTIONS as readonly string[]).includes(value ?? "")
    ? (value as CreateOrganizationRequest["size_band"])
    : undefined;
}

// Builds the create-company request body: `domains[]` rows carry
// `{domain, is_primary}` keyed off the repeatable rows channel, scalar
// fields trim to undefined when blank.
export function mapOrgBody(
  values: Record<string, string>,
  rows: FormRows,
): CreateOrganizationRequest {
  const domains = (rows.domains ?? [])
    .filter((row) => (row.domain ?? "").trim().length > 0)
    .map((row) => ({
      domain: row.domain.trim().toLowerCase(),
      is_primary: row.is_primary === "true",
    }));
  return {
    display_name: values.display_name.trim(),
    legal_name: values.legal_name?.trim() || undefined,
    industry: values.industry?.trim() || undefined,
    size_band: asSizeBand(values.size_band),
    domains: domains.length > 0 ? domains : undefined,
    source: "manual",
  };
}

// Builds the PATCH body: only the UpdateOrganizationRequest fields (never
// domains — not in the contract's update shape).
export function mapOrgUpdate(
  values: Record<string, unknown>,
): UpdateOrganizationRequest {
  return {
    display_name: stringField(values.display_name).trim() || undefined,
    legal_name: stringField(values.legal_name).trim() || undefined,
    industry: stringField(values.industry).trim() || undefined,
    size_band: asSizeBand(stringField(values.size_band)),
  };
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
];

async function createCompany(
  values: Record<string, string>,
  rows?: FormRows,
): Promise<Organization> {
  const { data, error } = await api.POST("/organizations", {
    body: mapOrgBody(values, rows ?? {}),
  });
  if (error) {
    throwProblem(error);
  }
  return data;
}

export function CompaniesScreen() {
  const t = useT();
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
        <CreateAction
          label={t("create.company")}
          invalidate="organizations"
          screen="companies"
          create={createCompany}
          resolveExisting={(_code, id) => ({ screen: "companies", id })}
          fields={companyCreateFields}
        />
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
                  <strong>{org.display_name}</strong>
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

export function CompanyScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
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
  const timelineQuery = useQuery({
    queryKey: ["activities", "organization", id],
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
            badges={
              <>
                {org.classification && <Badge>{org.classification}</Badge>}
                <ProvenanceTag provenance={provenanceOf(org.captured_by)} />
                <EditAction
                  label={t("record.edit")}
                  fields={companyEditFields}
                  record={{
                    id: org.id,
                    version: org.version,
                    display_name: org.display_name,
                    legal_name: org.legal_name ?? "",
                    industry: org.industry ?? "",
                    size_band: org.size_band ?? "",
                  }}
                  update={async (values) => {
                    const { data, error } = await api.PATCH(
                      "/organizations/{id}",
                      {
                        params: {
                          path: { id },
                          ...ifMatch(org.version),
                        },
                        body: mapOrgUpdate(values),
                      },
                    );
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
              </>
            }
            timeline={
              timelineQuery.isSuccess
                ? activityTimeline(timelineQuery.data.data)
                : []
            }
          >
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
                      {org.domains.map((domain) => domain.domain).join(", ")}
                    </dd>
                  </div>
                )}
              </dl>
            </section>
            <EnrichCard orgId={org.id} />
            <LogActivity entityType="organization" entityId={org.id} />
          </RecordView>
        )}
      </QueryGate>
    </div>
  );
}
