import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Badge,
  Button,
  DataTable,
  SectionHeader,
  SegmentedControl,
} from "../design-system/atoms";
import { RecordView, type TimelineEntry } from "../design-system/composed";
import { ProvenanceTag } from "../design-system/trust";
import { useT } from "../i18n";
import { ArchiveAction } from "./archive";
import {
  problemMessage,
  provenanceOf,
  QueryGate,
  throwProblem,
} from "./common";
import { CreateAction, type CreateField, type FormRows } from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";
import { LogActivity } from "./logactivity";
import { MergeAction } from "./merge";
import { RelationshipsTab } from "./relationships";
import { ShareAction } from "./share";
import { StrengthCard } from "./strength";

// Contacts list + person 360 (B-EP09.10a/b). Every row carries its
// provenance chip (captured_by is server truth); the 360 renders the
// per-purpose consent card and evidence-or-omit fields — absent data is
// omitted, never guessed. Search/filter/sort/pagination (P-14), the rich
// create modal (P-15), the If-Match edit form (P-1), and the dedupe
// view-existing link (P-16) are the four shared blocks wired in here.

type Person = components["schemas"]["Person"];
type Activity = components["schemas"]["Activity"];
type CreatePersonRequest = components["schemas"]["CreatePersonRequest"];
type UpdatePersonRequest = components["schemas"]["UpdatePersonRequest"];
type PersonConsentState = components["schemas"]["PersonConsentState"];

async function fetchPeoplePage(
  query: ListQuery,
  cursor: string | null,
): Promise<ListPage<Person>> {
  const { data, error } = await api.GET("/people", {
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
    // A LIST read's honest-error path only needs a message to render — the
    // dedupe "view existing" link is a create/update-only concern.
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

// Merge-target search (P-2): reuses the list read, mapped down to the
// {id, name} shape MergeAction renders — the caller filters out the source
// row since this fetch has no notion of "the record being merged away".
async function searchPeopleTargets(
  q: string,
): Promise<{ id: string; name: string }[]> {
  const { data, error } = await api.GET("/people", {
    params: { query: { q, limit: 10 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((candidate) => ({
    id: candidate.id,
    name: candidate.full_name,
  }));
}

function asEmailType(value: string | undefined): "work" | "personal" | "other" {
  return value === "personal" || value === "other" ? value : "work";
}

function asPhoneType(
  value: string | undefined,
): "work" | "mobile" | "home" | "other" {
  return value === "mobile" || value === "home" || value === "other"
    ? value
    : "work";
}

// Builds the create-contact request body: scalar fields trim to undefined
// when blank (never sent rather than sent empty), `social.linkedin` folds
// into the `social` object, and each repeatable row becomes an
// emails/phones entry keyed by its position in the list.
export function mapPersonBody(
  values: Record<string, string>,
  rows: FormRows,
): CreatePersonRequest {
  const linkedin = values["social.linkedin"]?.trim();
  const emails = (rows.emails ?? [])
    .filter((row) => (row.email ?? "").trim().length > 0)
    .map((row, index) => ({
      email: row.email.trim(),
      email_type: asEmailType(row.email_type),
      is_primary: row.is_primary === "true",
      position: index,
    }));
  const phones = (rows.phones ?? [])
    .filter((row) => (row.phone ?? "").trim().length > 0)
    .map((row, index) => ({
      phone: row.phone.trim(),
      phone_type: asPhoneType(row.phone_type),
      is_primary: row.is_primary === "true",
      position: index,
    }));
  return {
    full_name: values.full_name.trim(),
    first_name: values.first_name?.trim() || undefined,
    last_name: values.last_name?.trim() || undefined,
    title: values.title?.trim() || undefined,
    social: linkedin ? { linkedin } : undefined,
    emails: emails.length > 0 ? emails : undefined,
    phones: phones.length > 0 ? phones : undefined,
    source: "manual",
  };
}

function stringField(value: unknown): string {
  return typeof value === "string" ? value : "";
}

// Builds the PATCH body: only the UpdatePersonRequest fields (never
// emails/phones — not in the contract's update shape).
export function mapPersonUpdate(
  values: Record<string, unknown>,
): UpdatePersonRequest {
  const linkedin = stringField(values["social.linkedin"]).trim();
  return {
    full_name: stringField(values.full_name).trim() || undefined,
    first_name: stringField(values.first_name).trim() || undefined,
    last_name: stringField(values.last_name).trim() || undefined,
    title: stringField(values.title).trim() || undefined,
    social: linkedin ? { linkedin } : undefined,
  };
}

// Built inside ContactsScreen (not module-level) because the email/phone
// "Type" options are display text, not raw values — fieldControl (create.tsx)
// renders option.label verbatim, so the human-readable string has to be
// resolved via useT() before it reaches CreateField, unlike organizations.tsx's
// size_band options, which are already display-ready raw labels ("1-10").
function contactCreateFields(t: ReturnType<typeof useT>): CreateField[] {
  return [
    { key: "full_name", label: "create.fullName", required: true },
    { key: "first_name", label: "create.firstName" },
    { key: "last_name", label: "create.lastName" },
    { key: "title", label: "create.personTitle" },
    { key: "social.linkedin", label: "create.linkedin" },
    {
      key: "emails",
      label: "create.email",
      type: "repeatable",
      addLabel: "field.addEmail",
      rowFields: [
        {
          key: "email",
          label: "create.email",
          type: "email",
          required: true,
        },
        {
          key: "email_type",
          label: "field.emailType",
          type: "select",
          options: [
            { value: "work", label: t("field.emailWork") },
            { value: "personal", label: t("field.emailPersonal") },
            { value: "other", label: t("field.emailOther") },
          ],
        },
      ],
      primaryKey: "is_primary",
    },
    {
      key: "phones",
      label: "create.phone",
      type: "repeatable",
      addLabel: "field.addPhone",
      rowFields: [
        { key: "phone", label: "create.phone", required: true },
        {
          key: "phone_type",
          label: "field.phoneType",
          type: "select",
          options: [
            { value: "work", label: t("field.phoneWork") },
            { value: "mobile", label: t("field.phoneMobile") },
            { value: "home", label: t("field.phoneHome") },
            { value: "other", label: t("field.phoneOther") },
          ],
        },
      ],
      primaryKey: "is_primary",
    },
  ];
}

const personEditFields: CreateField[] = [
  { key: "full_name", label: "create.fullName", required: true },
  { key: "first_name", label: "create.firstName" },
  { key: "last_name", label: "create.lastName" },
  { key: "title", label: "create.personTitle" },
  { key: "social.linkedin", label: "create.linkedin" },
];

async function createContact(
  values: Record<string, string>,
  rows: FormRows | undefined,
  customFields: Record<string, unknown>,
): Promise<Person> {
  const { data, error } = await api.POST("/people", {
    body: { ...mapPersonBody(values, rows ?? {}), ...customFields },
  });
  if (error) {
    throwProblem(error);
  }
  return data;
}

function useTimeline(
  entityType: "person" | "organization" | "deal",
  id: string,
) {
  return useQuery({
    queryKey: ["activities", entityType, id],
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: {
          query: { entity_type: entityType, entity_id: id, limit: 20 },
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

function timelineKind(kind: string): TimelineEntry["kind"] {
  if (kind === "email") {
    return "email";
  }
  if (kind === "meeting") {
    return "meeting";
  }
  return "note";
}

export function activityTimeline(activities: Activity[]): TimelineEntry[] {
  return activities.map((activity) => ({
    id: activity.id,
    kind: timelineKind(activity.kind),
    title: activity.subject ?? activity.kind,
    atIso: activity.occurred_at,
    provenance: provenanceOf(activity.captured_by),
  }));
}

export function ContactsScreen() {
  const t = useT();
  const cf = useObjectCustomFields("person");
  const state = useListQuery<Person>({
    key: "people",
    initialSort: "-created_at",
    fetchPage: fetchPeoplePage,
  });
  const { query, setQuery } = state;

  return (
    <div className="wrap">
      <div className="list-head">
        <SectionHeader title={t("nav.contacts")} />
        <CreateAction
          label={t("create.contact")}
          invalidate="people"
          screen="contacts"
          create={(values, rows) =>
            createContact(values, rows, cf.toBody(values))
          }
          resolveExisting={(_code, id) => ({ screen: "contacts", id })}
          fields={[...contactCreateFields(t), ...cf.formFields]}
        />
      </div>
      <ListToolbar
        query={query}
        setQuery={setQuery}
        sortOptions={[
          { value: "full_name", label: "people.name" },
          { value: "-created_at", label: "list.sortNewest" },
        ]}
      />
      <ListGate state={state} empty={t("common.empty")}>
        {(rows) => (
          <DataTable
            columns={[
              {
                key: "name",
                header: t("people.name"),
                render: (person: Person) => (
                  <span>
                    <strong>{person.full_name}</strong>
                    {person.title && (
                      <span className="t-caption"> · {person.title}</span>
                    )}
                    {person.archived_at && (
                      <Badge tone="warn">{t("record.archived")}</Badge>
                    )}
                  </span>
                ),
              },
              {
                key: "email",
                header: t("people.email"),
                render: (person: Person) => (
                  <span className="t-mono">
                    {person.emails?.find((email) => email.is_primary)?.email ??
                      person.emails?.[0]?.email ??
                      ""}
                  </span>
                ),
              },
              {
                key: "provenance",
                header: t("people.capturedBy"),
                render: (person: Person) => (
                  <ProvenanceTag
                    provenance={provenanceOf(person.captured_by)}
                  />
                ),
              },
            ]}
            rows={rows}
            rowKey={(person) => person.id}
            onRowClick={(person) =>
              navigate({ screen: "contacts", id: person.id })
            }
          />
        )}
      </ListGate>
    </div>
  );
}

// One consent-purpose row on the Person 360 (P-8/P-9): the state badge, a
// Grant/Withdraw toggle that writes an append-only consent_event through
// POST /people/{id}/consent, and a Double opt-in issuance that mints the
// one-time DOI token a requires_double_opt_in purpose needs before a grant
// takes effect. lawful_basis is intentionally omitted from the toggle body —
// it's optional in RecordConsentRequest and this control has no field for it
// yet. Errors surface verbatim (a DOI-required purpose 422s here rather than
// silently no-opping) so the human sees exactly why the toggle didn't take.
function ConsentRow({
  personId,
  entry,
}: Readonly<{ personId: string; entry: PersonConsentState }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const granted = entry.state === "granted";

  const setState = useMutation({
    mutationFn: async (newState: "granted" | "withdrawn") => {
      const { data, error } = await api.POST("/people/{id}/consent", {
        params: { path: { id: personId } },
        body: { purpose_id: entry.purpose_id, new_state: newState },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["person", personId] });
    },
  });

  const issueDoi = useMutation({
    mutationFn: async () => {
      const { data, error } = await api.POST(
        "/people/{id}/consent/double-opt-in",
        {
          params: { path: { id: personId } },
          body: { purpose_id: entry.purpose_id },
        },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: 4,
        marginBottom: 10,
      }}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          flexWrap: "wrap",
        }}
      >
        <Badge tone={granted ? "success" : "warn"}>
          {entry.purpose_key ?? entry.purpose_id}: {entry.state}
        </Badge>
        <Button
          small
          disabled={setState.isPending}
          onClick={() => setState.mutate(granted ? "withdrawn" : "granted")}
        >
          {granted ? t("consent.withdraw") : t("consent.grant")}
        </Button>
        <Button
          small
          disabled={issueDoi.isPending}
          onClick={() => issueDoi.mutate()}
        >
          {t("consent.doubleOptIn")}
        </Button>
      </div>
      {setState.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {setState.error instanceof Error ? setState.error.message : null}
        </p>
      )}
      {issueDoi.isError && (
        <p className="t-caption" style={{ color: "var(--danger)" }}>
          {issueDoi.error instanceof Error ? issueDoi.error.message : null}
        </p>
      )}
      {issueDoi.data && (
        <p className="t-caption">
          {t("consent.doiIssued")} <code>{issueDoi.data.token}</code> ·{" "}
          {t("consent.doiExpires")}: {issueDoi.data.expires_at}
        </p>
      )}
    </div>
  );
}

const PERSON_TABS = ["overview", "relationships"] as const;
type PersonTab = (typeof PERSON_TABS)[number];

export function PersonScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
  const cf = useObjectCustomFields("person");
  const [tab, setTab] = useState<PersonTab>("overview");
  const personQuery = useQuery({
    queryKey: ["person", id],
    queryFn: async () => {
      const { data, error } = await api.GET("/people/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  const timelineQuery = useTimeline("person", id);

  return (
    <div className="wrap">
      <QueryGate query={personQuery}>
        {(person) => (
          <RecordView
            name={person.full_name}
            subtitle={person.title ?? undefined}
            zone="Europe/Berlin"
            badges={
              <>
                <ProvenanceTag provenance={provenanceOf(person.captured_by)} />
                {person.archived_at ? (
                  // An archived record is read-only: the backend rejects
                  // edit/merge/archive on a non-live row (there is no
                  // unarchive path), so offering those buttons would only
                  // 404. The badge is the whole affordance.
                  <Badge tone="warn">{t("record.archived")}</Badge>
                ) : (
                  <>
                    <EditAction
                      label={t("record.edit")}
                      fields={[...personEditFields, ...cf.formFields]}
                      record={{
                        id: person.id,
                        version: person.version,
                        full_name: person.full_name,
                        first_name: person.first_name ?? "",
                        last_name: person.last_name ?? "",
                        title: person.title ?? "",
                        "social.linkedin": stringField(person.social?.linkedin),
                        ...cf.recordSlice(person),
                      }}
                      update={async (values) => {
                        const { data, error } = await api.PATCH(
                          "/people/{id}",
                          {
                            params: {
                              path: { id },
                              ...ifMatch(person.version),
                            },
                            body: {
                              ...mapPersonUpdate(values),
                              ...cf.toBody(values),
                            },
                          },
                        );
                        if (error) {
                          throwProblem(error);
                        }
                        return data;
                      }}
                      invalidate="people"
                      recordKey="person"
                    />
                    <MergeAction
                      label={t("merge.person")}
                      sourceId={person.id}
                      sourceName={person.full_name}
                      searchTargets={searchPeopleTargets}
                      merge={async (targetId) => {
                        const { data, error } = await api.POST(
                          "/people/{id}/merge",
                          {
                            params: {
                              path: { id: person.id },
                              ...ifMatch(person.version),
                            },
                            body: { target_id: targetId },
                          },
                        );
                        if (error) {
                          throwProblem(error);
                        }
                        return data;
                      }}
                      invalidate="people"
                      recordKey="person"
                      survivorRoute={(targetId) => ({
                        screen: "contacts",
                        id: targetId,
                      })}
                    />
                    <ArchiveAction
                      label={t("record.archive")}
                      confirmText={t("record.archiveConfirm")}
                      archive={async () => {
                        const { data, error } = await api.DELETE(
                          "/people/{id}",
                          {
                            params: { path: { id } },
                          },
                        );
                        if (error) {
                          throwProblem(error);
                        }
                        return data;
                      }}
                      invalidate="people"
                      recordKey="person"
                      onArchived={() => navigate({ screen: "contacts" })}
                    />
                    <ShareAction recordType="person" recordId={person.id} />
                  </>
                )}
              </>
            }
            timeline={
              timelineQuery.isSuccess
                ? activityTimeline(timelineQuery.data.data)
                : []
            }
          >
            <div style={{ marginBottom: 16 }}>
              <SegmentedControl
                options={PERSON_TABS}
                value={tab}
                onChange={setTab}
                labels={{
                  overview: t("tab.overview"),
                  relationships: t("tab.relationships"),
                }}
              />
            </div>
            {tab === "overview" ? (
              <>
                <StrengthCard kind="person" id={person.id} />
                {person.consent && person.consent.length > 0 && (
                  <section
                    aria-label={t("person.consent")}
                    className="card"
                    style={{ marginBottom: 16 }}
                  >
                    <SectionHeader title={t("person.consent")} />
                    <div>
                      {person.consent.map((entry) => (
                        <ConsentRow
                          key={entry.purpose_id}
                          personId={person.id}
                          entry={entry}
                        />
                      ))}
                    </div>
                  </section>
                )}
                <CustomFieldsCard object="person" record={person} />
                <LogActivity entityType="person" entityId={person.id} />
              </>
            ) : (
              <RelationshipsTab scope={{ person_id: person.id }} />
            )}
          </RecordView>
        )}
      </QueryGate>
    </div>
  );
}
