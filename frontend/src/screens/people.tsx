import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
import { Badge, DataTable, SectionHeader } from "../design-system/atoms";
import { RecordView, type TimelineEntry } from "../design-system/composed";
import { ProvenanceTag } from "../design-system/trust";
import { useT } from "../i18n";
import { problemMessage, provenanceOf, QueryGate } from "./common";
import { CreateRecordModal, NewRecordButton, useCreateRecord } from "./create";

// Contacts list + person 360 (B-EP09.10a/b). Every row carries its
// provenance chip (captured_by is server truth); the 360 renders the
// per-purpose consent card and evidence-or-omit fields — absent data is
// omitted, never guessed.

type Person = components["schemas"]["Person"];
type Activity = components["schemas"]["Activity"];

export function usePeople() {
  return useQuery({
    queryKey: ["people"],
    queryFn: async () => {
      const { data, error } = await api.GET("/people", {
        params: { query: { limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
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
  const query = usePeople();
  const [creating, setCreating] = useState(false);

  const create = useCreateRecord({
    invalidate: "people",
    screen: "contacts",
    onDone: () => setCreating(false),
    create: async (values) => {
      const email = values.email?.trim();
      const { data, error } = await api.POST("/people", {
        body: {
          full_name: values.full_name.trim(),
          title: values.title?.trim() || null,
          ...(email
            ? {
                emails: [
                  {
                    email,
                    email_type: "work" as const,
                    is_primary: true,
                    position: 0,
                  },
                ],
              }
            : {}),
          source: "manual",
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
      <div className="list-head">
        <SectionHeader title={t("nav.contacts")} />
        <NewRecordButton
          label={t("create.contact")}
          onClick={() => setCreating(true)}
        />
      </div>
      <CreateRecordModal
        open={creating}
        onClose={() => setCreating(false)}
        title={t("create.contact")}
        fields={[
          { key: "full_name", label: "create.fullName", required: true },
          { key: "title", label: "create.personTitle" },
          { key: "email", label: "create.email", type: "email" },
        ]}
        pending={create.isPending}
        error={create.isError ? create.error.message : null}
        onSubmit={(values) => create.mutate(values)}
      />
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
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
            rows={page.data}
            rowKey={(person) => person.id}
            onRowClick={(person) =>
              navigate({ screen: "contacts", id: person.id })
            }
          />
        )}
      </QueryGate>
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
              <ProvenanceTag provenance={provenanceOf(person.captured_by)} />
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
          </RecordView>
        )}
      </QueryGate>
    </div>
  );
}
