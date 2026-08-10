import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Avatar, Badge, Button } from "../design-system/atoms";
import { ProvenanceTag } from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { provenanceOf, throwProblem, useViewerId } from "./common";
import {
  CreateAction,
  type CreateField,
  type FormRows,
  splitMultiselectValue,
} from "./create";
import { useObjectCustomFields } from "./customfields.form";
import {
  type ListPage,
  type ListQuery,
  ListTable,
  useListQuery,
} from "./listquery";

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

export const LIFECYCLE_LABELS: Record<Lifecycle, MessageKey> = {
  unknown: "org.lifecycle.unknown",
  target: "org.lifecycle.target",
  prospect: "org.lifecycle.prospect",
  opportunity: "org.lifecycle.opportunity",
  customer: "org.lifecycle.customer",
  former_customer: "org.lifecycle.former_customer",
  disqualified: "org.lifecycle.disqualified",
};

export const RELATIONSHIP_TYPE_LABELS: Record<RelationshipType, MessageKey> = {
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
export async function searchOrgTargets(
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
  // Nullable rather than trim-to-undefined, and for the same reason the
  // relationship set is: clearing a LinkedIn URL is an edit. `|| undefined`
  // would read a deletion as "the caller did not mention it" and put the old
  // value straight back.
  if (values.linkedin_url !== undefined) {
    body.linkedin_url = stringField(values.linkedin_url).trim() || null;
  }
  const address = addressPatch(values);
  if (address) {
    body.address = address;
  }
  return body;
}

// The six columns behind Address, flattened into form fields. The wire shape is
// one nested object; the form channel is flat string values, so the two are
// mapped at the boundary (addressFrom / addressPatch) rather than teaching the
// form about nesting for one record type.
const ADDRESS_FIELDS: CreateField[] = [
  { key: "address_line1", label: "create.addressLine1" },
  { key: "address_line2", label: "create.addressLine2" },
  { key: "address_postal_code", label: "create.postalCode" },
  { key: "address_city", label: "create.city" },
  { key: "address_region", label: "create.region" },
  { key: "address_country", label: "create.country" },
];

// addressFrom prefills the six flat fields from the record's nested address.
export function addressFrom(
  address: Organization["address"],
): Record<string, string> {
  return {
    address_line1: address?.line1 ?? "",
    address_line2: address?.line2 ?? "",
    address_postal_code: address?.postal_code ?? "",
    address_city: address?.city ?? "",
    address_region: address?.region ?? "",
    address_country: address?.country ?? "",
  };
}

// addressPatch folds the six flat fields back into the wire's nested object.
//
// A cleared field is sent as null rather than omitted: the caller had the value
// on screen and erased it, which is an edit. Omitting it would silently keep
// what the record held — the failure mode where a user deletes a line, saves,
// and finds it back on reload.
//
// The whole object is omitted only when the form never rendered the fields at
// all, so a surface that does not offer the address cannot blank one.
function addressPatch(
  values: Record<string, unknown>,
): UpdateOrganizationRequest["address"] | undefined {
  if (values.address_line1 === undefined) {
    return undefined;
  }
  const field = (key: string) => stringField(values[key]).trim() || null;
  return {
    line1: field("address_line1"),
    line2: field("address_line2"),
    postal_code: field("address_postal_code"),
    city: field("address_city"),
    region: field("address_region"),
    // ISO-3166 alpha-2, and the server compares on the canonical spelling, so
    // "de" typed in lower case is the same country as "DE".
    country: stringField(values.address_country).trim().toUpperCase() || null,
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
    // The company's own LinkedIn page. A canonical column since ADR-0085/A130,
    // not a custom field, because it carries identity semantics — matching,
    // dedupe, enrichment — and the person side already treats it that way. The
    // server normalizes what is pasted, so a URL copied from any tab of the
    // company page resolves to the one spelling.
    { key: "linkedin_url", label: "create.linkedinUrl" },
    // Where the company actually is. It has been in the API since the record
    // existed and reachable from no form on this page, so a rep who knew the
    // address had nowhere to put it.
    ...ADDRESS_FIELDS,
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

  return (
    <div className="wrap">
      <ListTable
        state={state}
        unit="unit.companies"
        action={
          <>
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
          </>
        }
        columns={[
          {
            key: "name",
            header: t("org.name"),
            cell: (org: Organization) => (
              <span className="avatar-row">
                <Avatar name={org.display_name} src={org.logo_url} tinted />
                <strong>{org.display_name}</strong>
                {org.archived_at && (
                  <Badge tone="warn">{t("record.archived")}</Badge>
                )}
              </span>
            ),
            sort: "display_name",
            fixed: true,
          },
          {
            key: "industry",
            header: t("org.industry"),
            cell: (org: Organization) => org.industry ?? "",
          },
          {
            key: "size",
            header: t("org.size"),
            cell: (org: Organization) => org.size_band ?? "",
          },
          {
            key: "class",
            header: t("org.lifecycle"),
            // classification is retired and no longer written by anything,
            // so a column reading it would show whatever it happened to
            // hold when the split shipped, forever.
            cell: (org: Organization) =>
              org.lifecycle && org.lifecycle !== "unknown" ? (
                <Badge>{t(LIFECYCLE_LABELS[org.lifecycle])}</Badge>
              ) : null,
          },
          {
            key: "provenance",
            header: t("people.capturedBy"),
            cell: (org: Organization) => (
              <ProvenanceTag
                provenance={provenanceOf(org.captured_by, viewerId)}
              />
            ),
          },
        ]}
        rowKey={(org) => org.id}
        rowRoute={(org) => ({ screen: "companies", id: org.id })}
        views={[
          { label: "list.viewAll", sort: "-created_at" },
          { label: "list.viewAZ", sort: "display_name" },
        ]}
      />
    </div>
  );
}

// The EP05 enrich verb on the company 360: one click reads the org's own
// website through the cold-start fetch + no-guess gate and STAGES a 🟡
// proposal — every rendered field carries evidence + confidence or was
// omitted, and nothing writes until the human accepts it in the inbox
// (accept fills only EMPTY fields).
