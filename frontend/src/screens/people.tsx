import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import { Badge, DataTable, SectionHeader } from "../design-system/atoms";
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

const contactCreateFields: CreateField[] = [
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
      { key: "email", label: "create.email", type: "email", required: true },
      {
        key: "email_type",
        label: "field.emailType",
        type: "select",
        options: [
          { value: "work", label: "field.emailWork" },
          { value: "personal", label: "field.emailPersonal" },
          { value: "other", label: "field.emailOther" },
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
          { value: "work", label: "field.phoneWork" },
          { value: "mobile", label: "field.phoneMobile" },
          { value: "home", label: "field.phoneHome" },
          { value: "other", label: "field.phoneOther" },
        ],
      },
    ],
    primaryKey: "is_primary",
  },
];

const personEditFields: CreateField[] = [
  { key: "full_name", label: "create.fullName", required: true },
  { key: "first_name", label: "create.firstName" },
  { key: "last_name", label: "create.lastName" },
  { key: "title", label: "create.personTitle" },
  { key: "social.linkedin", label: "create.linkedin" },
];

async function createContact(
  values: Record<string, string>,
  rows?: FormRows,
): Promise<Person> {
  const { data, error } = await api.POST("/people", {
    body: mapPersonBody(values, rows ?? {}),
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
          create={createContact}
          resolveExisting={(_code, id) => ({ screen: "contacts", id })}
          fields={contactCreateFields}
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

export function PersonScreen({ id }: Readonly<{ id: string }>) {
  const t = useT();
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
                {person.archived_at && (
                  <Badge tone="warn">{t("record.archived")}</Badge>
                )}
                <EditAction
                  label={t("record.edit")}
                  fields={personEditFields}
                  record={{
                    id: person.id,
                    version: person.version,
                    full_name: person.full_name,
                    first_name: person.first_name ?? "",
                    last_name: person.last_name ?? "",
                    title: person.title ?? "",
                    "social.linkedin": stringField(person.social?.linkedin),
                  }}
                  update={async (values) => {
                    const { data, error } = await api.PATCH("/people/{id}", {
                      params: {
                        path: { id },
                        ...ifMatch(person.version),
                      },
                      body: mapPersonUpdate(values),
                    });
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
                    const { data, error } = await api.DELETE("/people/{id}", {
                      params: { path: { id } },
                    });
                    if (error) {
                      throwProblem(error);
                    }
                    return data;
                  }}
                  invalidate="people"
                  recordKey="person"
                  onArchived={() => navigate({ screen: "contacts" })}
                />
              </>
            }
            timeline={
              timelineQuery.isSuccess
                ? activityTimeline(timelineQuery.data.data)
                : []
            }
          >
            {person.consent && person.consent.length > 0 && (
              <section
                aria-label={t("person.consent")}
                className="card"
                style={{ marginBottom: 16 }}
              >
                <SectionHeader title={t("person.consent")} />
                <div style={{ display: "flex", gap: 8, flexWrap: "wrap" }}>
                  {person.consent.map((entry) => (
                    <Badge
                      key={entry.purpose_id}
                      tone={entry.state === "granted" ? "success" : "warn"}
                    >
                      {entry.purpose_key ?? entry.purpose_id}: {entry.state}
                    </Badge>
                  ))}
                </div>
              </section>
            )}
            <LogActivity entityType="person" entityId={person.id} />
          </RecordView>
        )}
      </QueryGate>
    </div>
  );
}
