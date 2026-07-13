// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ifMatch } from "../api/version";
import {
  Badge,
  Button,
  DataTable,
  EmptyState,
  Modal,
  SearchField,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessage, QueryGate, throwProblem } from "./common";
import type { CreateField } from "./create";
import { EditAction } from "./edit";

// The Relationships tab (P-5): the one surface a person/company 360 renders
// its relationship edges through (employment, deal stakeholder, partner-of,
// referred-by, co-sell-with). There is no GET /relationships/{id} in the
// contract — every row is hydrated straight off the list read, so edit and
// remove both act on the row already in hand rather than a re-fetch.

type Relationship = components["schemas"]["Relationship"];
type CreateRelationshipRequest =
  components["schemas"]["CreateRelationshipRequest"];
type RelationshipKind = Relationship["kind"];

// Which 360 this tab is rendered from — fixes which side of the edge is
// "this record" and which is the picked "other side".
export type RelationshipScope =
  | { person_id: string }
  | { organization_id: string };

const RELATIONSHIP_KINDS: readonly RelationshipKind[] = [
  "employment",
  "deal_stakeholder",
  "partner_of",
  "referred_by",
  "co_sell_with",
];

const KIND_LABELS: Record<RelationshipKind, MessageKey> = {
  employment: "rel.kind.employment",
  deal_stakeholder: "rel.kind.dealStakeholder",
  partner_of: "rel.kind.partnerOf",
  referred_by: "rel.kind.referredBy",
  co_sell_with: "rel.kind.coSellWith",
};

const SEARCH_DEBOUNCE_MS = 250;

function scopeQuery(scope: RelationshipScope): {
  person_id?: string;
  organization_id?: string;
} {
  return "person_id" in scope
    ? { person_id: scope.person_id }
    : { organization_id: scope.organization_id };
}

function scopeQueryKey(scope: RelationshipScope): [string, string, string] {
  return "person_id" in scope
    ? ["relationships", "person", scope.person_id]
    : ["relationships", "organization", scope.organization_id];
}

async function fetchRelationships(
  scope: RelationshipScope,
): Promise<Relationship[]> {
  const { data, error } = await api.GET("/relationships", {
    params: { query: scopeQuery(scope) },
  });
  if (error) {
    throw new Error(problemMessage(error));
  }
  return data.data;
}

// The other side of an edge from this scope's point of view. A plain id is
// rendered when no display name is available — there is no batch person/org
// name lookup in the PoC and no GET /relationships/{id} to hydrate one from.
function counterpartyOf(rel: Relationship, scope: RelationshipScope): string {
  const other =
    "person_id" in scope
      ? (rel.organization_id ?? rel.counterparty_org_id ?? rel.deal_id)
      : (rel.person_id ?? rel.counterparty_org_id ?? rel.deal_id);
  return other ?? "—";
}

function dateRange(rel: Relationship, t: (key: MessageKey) => string): string {
  const end = rel.ended_at ?? t("rel.current");
  return rel.started_at ? `${rel.started_at} – ${end}` : end;
}

type Candidate = { id: string; name: string };

async function searchOrganizationCandidates(q: string): Promise<Candidate[]> {
  const { data, error } = await api.GET("/organizations", {
    params: { query: { q, limit: 10 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((org) => ({ id: org.id, name: org.display_name }));
}

async function searchPersonCandidates(q: string): Promise<Candidate[]> {
  const { data, error } = await api.GET("/people", {
    params: { query: { q, limit: 10 } },
  });
  if (error) {
    throwProblem(error);
  }
  return data.data.map((person) => ({ id: person.id, name: person.full_name }));
}

// The other side of the edge to search: an org-scoped tab picks a person,
// a person-scoped tab picks an org.
function searchOtherSideCandidates(
  isPersonScope: boolean,
  query: string,
): Promise<Candidate[]> {
  return isPersonScope
    ? searchOrganizationCandidates(query)
    : searchPersonCandidates(query);
}

// The "add relationship" affordance: kind + role + start date, plus the
// other-side target picker (mirrors merge.tsx's debounced search-and-pick —
// the source of the edge is fixed by scope, so there is no "exclude self"
// filtering here).
function AddRelationshipAction({
  scope,
}: Readonly<{ scope: RelationshipScope }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const headingId = useId();
  const isPersonScope = "person_id" in scope;
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<RelationshipKind>("employment");
  const [role, setRole] = useState("");
  const [startedAt, setStartedAt] = useState("");
  const [term, setTerm] = useState("");
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [target, setTarget] = useState<Candidate | null>(null);
  const [searchError, setSearchError] = useState<string | null>(null);

  useEffect(() => {
    if (!open) {
      return;
    }
    const query = term.trim();
    if (!query) {
      setCandidates([]);
      setSearchError(null);
      return;
    }
    let cancelled = false;
    const timer = setTimeout(async () => {
      try {
        const results = await searchOtherSideCandidates(isPersonScope, query);
        if (!cancelled) {
          setCandidates(results);
          setSearchError(null);
        }
      } catch (error) {
        if (!cancelled) {
          setCandidates([]);
          setSearchError(
            error instanceof Error ? error.message : "request failed",
          );
        }
      }
    }, SEARCH_DEBOUNCE_MS);
    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [open, term, isPersonScope]);

  const mutation = useMutation({
    mutationFn: async () => {
      if (!target) {
        // The submit button is disabled until a target is picked — this
        // guard only protects against a stale closure, never a real path.
        throw new Error("no target selected");
      }
      const body: CreateRelationshipRequest = {
        kind,
        role: role.trim() || undefined,
        started_at: startedAt || undefined,
        source: "manual",
        is_current_primary: false,
        ...scopeQuery(scope),
        ...(isPersonScope
          ? { organization_id: target.id }
          : { person_id: target.id }),
      };
      const { data, error } = await api.POST("/relationships", { body });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["relationships"] });
      close();
    },
  });

  function close() {
    setOpen(false);
    setKind("employment");
    setRole("");
    setStartedAt("");
    setTerm("");
    setCandidates([]);
    setTarget(null);
    setSearchError(null);
    mutation.reset();
  }

  return (
    <>
      <Button
        small
        onClick={() => setOpen(true)}
        data-testid="add-relationship"
      >
        {t("rel.add")}
      </Button>
      <Modal open={open} onClose={close} labelledBy={headingId}>
        <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
          {t("rel.add")}
        </h2>
        <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
          <div className="field">
            <label className="t-label" htmlFor={`${headingId}-kind`}>
              {t("rel.kind")}
            </label>
            <select
              id={`${headingId}-kind`}
              className="input"
              value={kind}
              onChange={(event) =>
                setKind(event.target.value as RelationshipKind)
              }
            >
              {RELATIONSHIP_KINDS.map((option) => (
                <option key={option} value={option}>
                  {t(KIND_LABELS[option])}
                </option>
              ))}
            </select>
          </div>
          <div className="field">
            <label className="t-label" htmlFor={`${headingId}-role`}>
              {t("rel.role")}
            </label>
            <TextInput
              id={`${headingId}-role`}
              value={role}
              onChange={(event) => setRole(event.target.value)}
            />
          </div>
          <div className="field">
            <label className="t-label" htmlFor={`${headingId}-started`}>
              {t("rel.startedAt")}
            </label>
            <TextInput
              id={`${headingId}-started`}
              type="date"
              value={startedAt}
              onChange={(event) => setStartedAt(event.target.value)}
            />
          </div>
          <p className="t-caption">{t("rel.pickCounterparty")}</p>
          <SearchField
            placeholder={t("merge.searchPlaceholder")}
            aria-label={t("merge.searchPlaceholder")}
            value={term}
            onChange={(event) => {
              setTerm(event.target.value);
              setTarget(null);
            }}
          />
          {searchError && (
            <p className="t-caption" style={{ color: "var(--danger)" }}>
              {searchError}
            </p>
          )}
          <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
            {candidates.map((candidate) => (
              <li key={candidate.id}>
                <button
                  type="button"
                  className="btn btn-ghost"
                  aria-pressed={target?.id === candidate.id}
                  onClick={() => setTarget(candidate)}
                  style={{ width: "100%", textAlign: "left" }}
                >
                  {candidate.name}
                </button>
              </li>
            ))}
          </ul>
          {mutation.isError && (
            <p className="t-caption" style={{ color: "var(--danger)" }}>
              {mutation.error instanceof Error ? mutation.error.message : null}
            </p>
          )}
          <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
            <Button small onClick={close} disabled={mutation.isPending}>
              {t("create.cancel")}
            </Button>
            <Button
              small
              variant="primary"
              disabled={!target || mutation.isPending}
              onClick={() => mutation.mutate()}
              data-testid="add-relationship-submit"
            >
              {t("create.save")}
            </Button>
          </div>
        </div>
      </Modal>
    </>
  );
}

const relationshipEditFields: CreateField[] = [
  { key: "role", label: "rel.role" },
  { key: "started_at", label: "rel.startedAt", type: "date" },
  { key: "ended_at", label: "rel.endedAt", type: "date" },
];

// UpdateRelationshipRequest fields are nullable, but the backend's
// UpdateRelationship applies them via coalesce($n, col) — null means KEEP
// the existing value, not clear it (backend/internal/modules/people/
// relationship.go). So this can SET/CHANGE role/started_at/ended_at to a
// new value, but an emptied field is NOT reachable this way: sending null
// leaves the stored value untouched rather than wiping it. `orNull` still
// avoids sending an empty string over the wire; true clear-support needs a
// backend change (distinguish omit vs. explicit-null) and is out of scope
// here.
function orNull(value: unknown): string | null {
  const text = typeof value === "string" ? value.trim() : "";
  return text.length > 0 ? text : null;
}

export function RelationshipsTab({
  scope,
}: Readonly<{ scope: RelationshipScope }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const headingId = useId();
  const query = useQuery({
    queryKey: scopeQueryKey(scope),
    queryFn: () => fetchRelationships(scope),
  });

  // Two-step confirm, mirroring ArchiveAction (archive.tsx) — Remove is a
  // hard DELETE with no restore path, so it never fires from a single click.
  const [removing, setRemoving] = useState<string | null>(null);

  const remove = useMutation({
    mutationFn: async (id: string) => {
      const { data, error } = await api.DELETE("/relationships/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["relationships"] });
      setRemoving(null);
    },
  });

  return (
    <section className="card">
      <div className="list-head">
        <SectionHeader title={t("tab.relationships")} />
        <AddRelationshipAction scope={scope} />
      </div>
      <QueryGate query={query}>
        {(rows) =>
          rows.length === 0 ? (
            <EmptyState>{t("rel.empty")}</EmptyState>
          ) : (
            <DataTable
              columns={[
                {
                  key: "kind",
                  header: t("rel.kind"),
                  render: (rel: Relationship) => (
                    <Badge>{t(KIND_LABELS[rel.kind])}</Badge>
                  ),
                },
                {
                  key: "role",
                  header: t("rel.role"),
                  render: (rel: Relationship) => rel.role ?? "",
                },
                {
                  key: "counterparty",
                  header: t("rel.counterparty"),
                  render: (rel: Relationship) => (
                    <span className="t-mono">{counterpartyOf(rel, scope)}</span>
                  ),
                },
                {
                  key: "dates",
                  header: t("rel.dates"),
                  render: (rel: Relationship) => dateRange(rel, t),
                },
                {
                  key: "actions",
                  header: "",
                  render: (rel: Relationship) => (
                    <div style={{ display: "flex", gap: 6 }}>
                      <EditAction
                        label={t("record.edit")}
                        fields={relationshipEditFields}
                        record={{
                          id: rel.id,
                          version: rel.version,
                          role: rel.role ?? "",
                          started_at: rel.started_at ?? "",
                          ended_at: rel.ended_at ?? "",
                        }}
                        update={async (values) => {
                          const { data, error } = await api.PATCH(
                            "/relationships/{id}",
                            {
                              params: {
                                path: { id: rel.id },
                                ...ifMatch(rel.version),
                              },
                              body: {
                                role: orNull(values.role),
                                started_at: orNull(values.started_at),
                                ended_at: orNull(values.ended_at),
                              },
                            },
                          );
                          if (error) {
                            throwProblem(error);
                          }
                          return data;
                        }}
                        invalidate="relationships"
                        recordKey="relationship"
                      />
                      <Button
                        small
                        variant="danger"
                        onClick={() => setRemoving(rel.id)}
                        data-testid="remove-relationship"
                      >
                        {t("rel.remove")}
                      </Button>
                    </div>
                  ),
                },
              ]}
              rows={rows}
              rowKey={(rel) => rel.id}
            />
          )
        }
      </QueryGate>
      <Modal
        open={removing !== null}
        onClose={() => {
          setRemoving(null);
          remove.reset();
        }}
        labelledBy={headingId}
      >
        <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
          {t("rel.remove")}
        </h2>
        <p style={{ marginBottom: 16 }}>{t("rel.removeConfirm")}</p>
        {remove.isError && (
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {remove.error instanceof Error ? remove.error.message : null}
          </p>
        )}
        <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
          <Button
            small
            onClick={() => setRemoving(null)}
            disabled={remove.isPending}
          >
            {t("create.cancel")}
          </Button>
          <Button
            small
            variant="danger"
            onClick={() => {
              if (removing) {
                remove.mutate(removing);
              }
            }}
            disabled={remove.isPending}
            data-testid="remove-relationship-confirm"
          >
            {t("rel.remove")}
          </Button>
        </div>
      </Modal>
    </section>
  );
}
