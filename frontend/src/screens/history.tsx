import {
  type InfiniteData,
  type UseInfiniteQueryResult,
  useInfiniteQuery,
} from "@tanstack/react-query";
import { type ReactNode, useEffect, useMemo, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import type { EntityKind } from "../app/entity";
import { Button, EmptyState, SegmentedControl } from "../design-system/atoms";
import {
  EvidenceChip,
  FieldDiff,
  PassportChip,
  type Provenance,
  ProvenanceTag,
  toEvidence,
} from "../design-system/trust";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { LoadMoreButton, problemMessage, QueryStates } from "./common";
import {
  type ActorFacet,
  distinctFields,
  type FieldGroup,
  groupByField,
} from "./history.logic";
import "./history.css";

// The per-record plain-language change list (B-EP09.x): every audit_log row
// for one record, rendered as a `summary` line the endpoint already wrote in
// prose — this panel never re-derives wording from before/after diffs, it
// just attributes and paginates what the contract hands back.

type AuditHistoryEntry = components["schemas"]["AuditHistoryEntry"];
type AuditHistoryListResponse =
  components["schemas"]["AuditHistoryListResponse"];

export function useRecordHistory(
  kind: EntityKind,
  id: string,
): UseInfiniteQueryResult<InfiniteData<AuditHistoryListResponse>> {
  return useInfiniteQuery({
    queryKey: ["record-history", kind, id],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET(
        "/records/{entity_type}/{id}/history",
        {
          params: {
            path: { entity_type: kind, id },
            query: { limit: 20, ...(pageParam ? { cursor: pageParam } : {}) },
          },
        },
      );
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });
}

// captured_by-style strings aren't on this projection — both the record-level
// and field-level history rows already split actor_type/actor_id, so the
// Provenance maps straight off actor_type (system/connector read as "agent":
// neither is a human typing). Structurally typed off just those two fields so
// it serves AuditHistoryEntry and FieldHistoryEntry alike.
function provenanceOfEntry(
  entry: Pick<AuditHistoryEntry, "actor_type" | "actor_id">,
): Provenance {
  return entry.actor_type === "human"
    ? { kind: "human" }
    : { kind: "agent", agent: entry.actor_id };
}

function HistoryEntryRow({
  entry,
  locale,
}: Readonly<{
  entry: AuditHistoryEntry;
  locale: ReturnType<typeof useLocale>["locale"];
}>) {
  const t = useT();
  return (
    <li>
      <span className="tl-body">
        <span className="tl-title">
          {entry.summary}
          {entry.on_behalf_of_name && (
            <span className="history-onbehalf">
              {" "}
              {t("history.onBehalfOf", { name: entry.on_behalf_of_name })}
            </span>
          )}
        </span>
        <span className="tl-meta">
          <span>
            {formatDateTime(entry.occurred_at, locale, "Europe/Berlin")}
          </span>
          <ProvenanceTag provenance={provenanceOfEntry(entry)} />
        </span>
      </span>
    </li>
  );
}

export function RecordHistory({
  kind,
  id,
}: Readonly<{ kind: EntityKind; id: string }>) {
  const t = useT();
  const { locale } = useLocale();
  const query = useRecordHistory(kind, id);
  const entries = query.data?.pages.flatMap((page) => page.data) ?? [];

  // Honest state matrix (§3a): the pending/error halves are QueryStates'
  // (shared with FieldHistoryTimeline and QueryGate); empty vs. the list is
  // this component's own success rendering.
  let body: ReactNode;
  if (entries.length === 0) {
    body = <EmptyState>{t("history.empty")}</EmptyState>;
  } else {
    body = (
      <>
        <ul className="timeline">
          {entries.map((entry) => (
            <HistoryEntryRow key={entry.id} entry={entry} locale={locale} />
          ))}
        </ul>
        <LoadMoreButton query={query} />
      </>
    );
  }

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <QueryStates query={query}>{body}</QueryStates>
    </section>
  );
}

// The per-field old→new diff view (B-EP09.x): every field-change row the
// audit spine projects for one record, grouped by field and narrowable by
// actor and field — both filters are server-side query params (part of the
// queryKey), never a client-side re-slice of an already-fetched page.

type FieldHistoryEntry = components["schemas"]["FieldHistoryEntry"];
type FieldHistoryListResponse =
  components["schemas"]["FieldHistoryListResponse"];

const ACTOR_FACETS = ["all", "human", "agent"] as const;

export function useFieldHistory(
  kind: EntityKind,
  id: string,
  opts: Readonly<{ field?: string; actorType?: "human" | "agent" }>,
): UseInfiniteQueryResult<InfiniteData<FieldHistoryListResponse>> {
  const { field, actorType } = opts;
  return useInfiniteQuery({
    queryKey: ["field-history", kind, id, field ?? "", actorType ?? ""],
    initialPageParam: null as string | null,
    queryFn: async ({ pageParam }) => {
      const { data, error } = await api.GET("/field-history", {
        params: {
          query: {
            entity_type: kind,
            entity_id: id,
            limit: 20,
            ...(pageParam ? { cursor: pageParam } : {}),
            ...(field ? { field } : {}),
            ...(actorType ? { actor_type: actorType } : {}),
          },
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    getNextPageParam: (last) => last.page.next_cursor ?? null,
  });
}

// Every actor type gets a base ProvenanceTag (human/agent — system and
// connector read as "agent", same as the record-level HistoryEntryRow and
// settings.tsx's AuditLogRow), so no actor ever renders a blank attribution;
// the passport/evidence chips layer on top only when the change carries them.
function ChangeWho({ change }: Readonly<{ change: FieldHistoryEntry }>) {
  const evidence = toEvidence(change.evidence);
  return (
    <span className="who">
      <ProvenanceTag provenance={provenanceOfEntry(change)} />
      {change.passport_id && <PassportChip id={change.passport_id} />}
      {evidence && <EvidenceChip evidence={evidence} />}
    </span>
  );
}

function FieldGroupSection({ group }: Readonly<{ group: FieldGroup }>) {
  const { locale } = useLocale();
  return (
    <div className="fgroup">
      <div className="fgroup-head">{group.field}</div>
      <ul>
        {group.changes.map((change) => (
          <li key={change.id} className="change">
            <FieldDiff
              oldValue={change.old_value ?? null}
              newValue={change.new_value ?? null}
            />
            <span className="tl-meta">
              {formatDateTime(change.changed_at, locale, "Europe/Berlin")}
            </span>
            <ChangeWho change={change} />
          </li>
        ))}
      </ul>
    </div>
  );
}

export function FieldHistoryTimeline({
  kind,
  id,
}: Readonly<{ kind: EntityKind; id: string }>) {
  const t = useT();
  const [actorFacet, setActorFacet] = useState<ActorFacet>("all");
  const [fieldFilter, setFieldFilter] = useState<string | undefined>(undefined);
  // Accumulates every field name this record has ever shown, across facet/
  // field narrowing — a chip the user has already discovered stays clickable
  // even after a fetch that only returned one field's rows.
  const [fieldOptions, setFieldOptions] = useState<string[]>([]);

  // This component isn't remounted on navigation between records of the same
  // kind (App.tsx keys screens by route, not by record id), so without an
  // explicit reset the accumulator above would keep carrying the previous
  // record's field names onto the newly-viewed one. Adjusted during render
  // (React's documented pattern for resetting state on a prop change) rather
  // than in an Effect, so the reset always lands before the accumulate Effect
  // below reads `fieldOptions`, even when the new record's data is already
  // cached and ready on the very render the id changes.
  const recordKey = `${kind}:${id}`;
  const [resetFor, setResetFor] = useState(recordKey);
  if (resetFor !== recordKey) {
    setResetFor(recordKey);
    setFieldOptions([]);
  }

  const query = useFieldHistory(kind, id, {
    field: fieldFilter,
    actorType: actorFacet === "all" ? undefined : actorFacet,
  });
  // The Agent/Human facet already narrows server-side via the actor_type
  // query param (part of the queryKey, so a facet change refetches) — these
  // rows are trusted as-is rather than re-sliced client-side, which also
  // keeps pagination (hasNextPage) honest against what the server counted.
  const entries = useMemo(
    () => query.data?.pages.flatMap((page) => page.data) ?? [],
    [query.data],
  );

  useEffect(() => {
    if (entries.length === 0) {
      return;
    }
    const discovered = distinctFields(entries);
    setFieldOptions((prev) => {
      const next = [...prev];
      for (const field of discovered) {
        if (!next.includes(field)) {
          next.push(field);
        }
      }
      return next;
    });
  }, [entries]);

  const isFiltered = actorFacet !== "all" || fieldFilter !== undefined;
  const clearFilters = () => {
    setActorFacet("all");
    setFieldFilter(undefined);
  };

  // Honest state matrix (§3a): the pending/error halves are QueryStates';
  // filter-empty (a narrowing that found nothing) vs. truly empty (no edits
  // at all) is this component's own success rendering.
  let body: ReactNode;
  if (entries.length === 0 && isFiltered) {
    body = (
      <EmptyState>
        <p>{t("history.filterEmpty")}</p>
        <Button small onClick={clearFilters} style={{ marginTop: 10 }}>
          {t("history.clearFilter")}
        </Button>
      </EmptyState>
    );
  } else if (entries.length === 0) {
    body = <EmptyState>{t("history.fieldEmpty")}</EmptyState>;
  } else {
    const groups = groupByField(entries);
    body = (
      <>
        {groups.map((group) => (
          <FieldGroupSection key={group.field} group={group} />
        ))}
        <LoadMoreButton query={query} />
      </>
    );
  }

  const actorLabels: Record<ActorFacet, string> = {
    all: t("history.actorAll"),
    human: t("history.actorHuman"),
    agent: t("history.actorAgent"),
  };

  return (
    <section className="card" style={{ marginBottom: 16 }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          flexWrap: "wrap",
          gap: 10,
          marginBottom: 12,
        }}
      >
        <SegmentedControl
          options={ACTOR_FACETS}
          value={actorFacet}
          onChange={setActorFacet}
          labels={actorLabels}
        />
        {fieldOptions.length > 0 && (
          <div style={{ display: "flex", flexWrap: "wrap", gap: 6 }}>
            <Button
              small
              variant={fieldFilter === undefined ? "primary" : "ghost"}
              onClick={() => setFieldFilter(undefined)}
            >
              {t("history.allFields")}
            </Button>
            {fieldOptions.map((field) => (
              <Button
                key={field}
                small
                variant={fieldFilter === field ? "primary" : "ghost"}
                onClick={() => setFieldFilter(field)}
              >
                {field}
              </Button>
            ))}
          </div>
        )}
      </div>
      <QueryStates query={query}>{body}</QueryStates>
    </section>
  );
}

// The record-level entry point (B-EP09.x): a SegmentedControl toggling
// between the plain-language change list and the per-field diff timeline —
// two projections of the same audit spine, never fetched simultaneously.
const HISTORY_TABS = ["changes", "fields"] as const;
type HistoryTab = (typeof HISTORY_TABS)[number];

export function RecordHistoryTab({
  kind,
  id,
}: Readonly<{ kind: EntityKind; id: string }>) {
  const t = useT();
  const [tab, setTab] = useState<HistoryTab>("changes");
  const tabLabels: Record<HistoryTab, string> = {
    changes: t("history.tabChanges"),
    fields: t("history.tabFields"),
  };

  return (
    <div>
      <SegmentedControl
        options={HISTORY_TABS}
        value={tab}
        onChange={setTab}
        labels={tabLabels}
      />
      {tab === "changes" ? (
        <RecordHistory kind={kind} id={id} />
      ) : (
        <FieldHistoryTimeline kind={kind} id={id} />
      )}
    </div>
  );
}
