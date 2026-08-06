import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import { navigate } from "../app/router";
import {
  Badge,
  DataTable,
  SectionHeader,
  SegmentedControl,
} from "../design-system/atoms";
import { RecordView, type TimelineEntry } from "../design-system/composed";
import { ProvenanceTag } from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { ArchiveAction } from "./archive";
import {
  OverlayUnavailable,
  provenanceOf,
  QueryGate,
  throwProblem,
  useSorMode,
  useViewerId,
} from "./common";
import { TimelineActions } from "./compose";
import { ConsentSection } from "./consent";
import { RecordContextPanel } from "./context";
import { CreateAction, type CreateField, type FormRows } from "./create";
import { CustomFieldsCard } from "./customfields.card";
import { useObjectCustomFields } from "./customfields.form";
import { EditAction } from "./edit";
import { RecordHistoryTab } from "./history";
import {
  ListGate,
  type ListPage,
  type ListQuery,
  ListToolbar,
  useListQuery,
} from "./listquery";
import { LogActivity } from "./logactivity";
import { MergeAction } from "./merge";
import { PersonNetworkCard } from "./network";
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
  t: (key: MessageKey) => string,
): Promise<Person> {
  const { data, error } = await api.POST("/people", {
    body: { ...mapPersonBody(values, rows ?? {}), ...customFields },
  });
  if (error) {
    throwProblem(error, t);
  }
  return data;
}

function useTimeline(
  entityType: "person" | "organization" | "deal",
  id: string,
) {
  // The timeline is an entity-scoped activity read, a dial the overlay mirror
  // refuses (422) — skip the fetch in overlay; the 360 renders the honest
  // unavailable state in the timeline slot instead.
  const overlay = useSorMode() === "overlay";
  return useQuery({
    queryKey: ["activities", entityType, id],
    enabled: !overlay,
    queryFn: async () => {
      const { data, error } = await api.GET("/activities", {
        params: {
          query: { entity_type: entityType, entity_id: id, limit: 20 },
        },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}

// TIMELINE_KINDS is the backend's activity vocabulary, and the adapter
// passes a kind straight through when the timeline can draw it. Collapsing
// everything but email and meeting into "note" told the reader a call was a
// note; anything genuinely outside the set still falls back to note rather
// than rendering no icon at all.
const TIMELINE_KINDS: readonly TimelineEntry["kind"][] = [
  "email",
  "meeting",
  "note",
  "call",
  "task",
  "whatsapp",
  "telegram",
];

function timelineKind(kind: string): TimelineEntry["kind"] {
  const known = TIMELINE_KINDS.find((candidate) => candidate === kind);
  return known ?? "note";
}

// A timeline row is one line, so a body used as its title has its whitespace
// collapsed and is cut at this many characters.
const TIMELINE_TITLE_MAX = 140;

// timelineTitle is what the row says the activity WAS. A subject is the natural
// title, but a channel message has none — Telegram carries text, not a subject
// line — so a subject-only title rendered the literal word "telegram" on every
// row and made the conversation invisible on the record it belongs to. The
// body is the title for anything that has no subject, which is also why the
// connector fills it for a wordless message ("photo", "voice"): capture's
// messageBody names the kind precisely so this row has something to show.
function timelineTitle(activity: Activity): string {
  const subject = activity.subject?.trim();
  if (subject) {
    return subject;
  }
  // Collapsed rather than trusted: a pasted multi-line message would otherwise
  // break the row's single-line layout.
  const body = activity.body?.replace(/\s+/g, " ").trim();
  if (!body) {
    return activity.kind;
  }
  return body.length > TIMELINE_TITLE_MAX
    ? `${body.slice(0, TIMELINE_TITLE_MAX - 1)}…`
    : body;
}

export function activityTimeline(
  activities: Activity[],
  // Who is reading, so a row this user logged reads as theirs and a
  // colleague's does not. Absent while the session is still resolving.
  viewerUserId?: string,
  renderActions?: (activity: Activity) => ReactNode,
): TimelineEntry[] {
  return activities.map((activity) => ({
    id: activity.id,
    kind: timelineKind(activity.kind),
    title: timelineTitle(activity),
    // Carried beside the rendered title, which may be the body or the kind:
    // bulk grouping needs the message's OWN subject or it folds unrelated
    // subjectless rows together.
    subject: activity.subject,
    // The body is already in the composite read this row came from, so a
    // timeline of unreadable subject lines was a rendering choice, not a
    // limit of what the page knew.
    body: activity.body,
    direction: activity.direction,
    threadKey: activity.thread_key,
    bulkAttested: activity.bulk_mail_attested,
    atIso: activity.occurred_at,
    provenance: provenanceOf(activity.captured_by, viewerUserId),
    actions: renderActions?.(activity),
  }));
}

export function ContactsScreen() {
  const t = useT();
  const viewerId = useViewerId();
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
            createContact(values, rows, cf.toBody(values), t)
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
                    provenance={provenanceOf(person.captured_by, viewerId)}
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

const PERSON_TABS = ["overview", "relationships", "history"] as const;
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
        throwProblem(error);
      }
      return data;
    },
  });
  const timelineQuery = useTimeline("person", id);
  const overlay = useSorMode() === "overlay";
  const viewerId = useViewerId();

  return (
    <div className="wrap">
      <QueryGate query={personQuery}>
        {/* biome-ignore lint/complexity/noExcessiveCognitiveComplexity: this 360 render was already at the ceiling; overlay support adds one necessary mode branch (write affordances are hidden over a read-only mirror). A PersonScreen split is tracked with the overlay SPA follow-up (STATUS.md). */}
        {(person) => (
          <RecordView
            name={person.full_name}
            subtitle={person.title ?? undefined}
            zone="Europe/Berlin"
            badges={
              <>
                <ProvenanceTag
                  provenance={provenanceOf(person.captured_by, viewerId)}
                />
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
                      notice={
                        overlay ? t("overlay.partialWriteBack") : undefined
                      }
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
                    {/* Merge has no incumbent-first projection — the seam
                        refuses it outright (overlay/provider_writes.go
                        Merge) — unlike edit/archive below, which it
                        serves, so it stays hidden here. */}
                    {!overlay && (
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
                            throwProblem(error, t);
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
                    )}
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
                    {/* A record grant probes the native row via
                        auth.EnsureLinkTarget, which a mirrored record has
                        no row for — sharing stays hidden in overlay
                        regardless of record type (see deals.tsx's
                        DealBadges). */}
                    {!overlay && (
                      <ShareAction recordType="person" recordId={person.id} />
                    )}
                  </>
                )}
              </>
            }
            timeline={
              timelineQuery.isSuccess
                ? activityTimeline(
                    timelineQuery.data.data,
                    viewerId,
                    (activity) => (
                      <TimelineActions
                        activity={activity}
                        entityType="person"
                        entityId={id}
                        personId={id}
                      />
                    ),
                  )
                : []
            }
            timelineNotice={overlay ? <OverlayUnavailable /> : undefined}
          >
            <div style={{ marginBottom: 16 }}>
              <SegmentedControl
                options={PERSON_TABS}
                value={tab}
                onChange={setTab}
                labels={{
                  overview: t("tab.overview"),
                  relationships: t("tab.relationships"),
                  history: t("tab.history"),
                }}
              />
            </div>
            {tab === "overview" && (
              <>
                <StrengthCard kind="person" id={person.id} />
                <PersonNetworkCard id={person.id} />
                <ConsentSection personId={person.id} />
                <CustomFieldsCard object="person" record={person} />
                <RecordContextPanel entityType="person" id={person.id} />
                <LogActivity entityType="person" entityId={person.id} />
              </>
            )}
            {tab === "relationships" && (
              <RelationshipsTab scope={{ person_id: person.id }} />
            )}
            {tab === "history" && (
              <RecordHistoryTab kind="person" id={person.id} />
            )}
          </RecordView>
        )}
      </QueryGate>
    </div>
  );
}
