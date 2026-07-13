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
import { EntityRef, type EntityRefKind } from "./entityref";

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

// The other side of an edge from this scope's point of view, as a typed
// record reference EntityRef can hydrate into a name + backlink. The far end
// follows the edge shape (migration 0007 rel_*_shape) AND the scope: a
// person's 360 sees its employment (→org) and deal_stakeholder (→deal) edges;
// an org's 360 sees employment (→person) and the org↔org edges. Critically,
// the org list filter matches an org↔org edge on EITHER end
// (organization_id OR counterparty_org_id), so the far org is whichever id is
// not this scope's own — never the record itself.
export function counterpartyRef(
  rel: Relationship,
  scope: RelationshipScope,
): { kind: EntityRefKind; id: string } | null {
  if ("person_id" in scope) {
    if (rel.deal_id) {
      return { kind: "deal", id: rel.deal_id };
    }
    return rel.organization_id
      ? { kind: "organization", id: rel.organization_id }
      : null;
  }
  if (rel.person_id) {
    return { kind: "person", id: rel.person_id };
  }
  const far = [rel.counterparty_org_id, rel.organization_id].find(
    (orgId) => orgId != null && orgId !== scope.organization_id,
  );
  return far ? { kind: "organization", id: far } : null;
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

// /deals has no free-text `q` in the contract (only structured filters), so
// the stakeholder picker fetches a recent page and matches the typed term
// against the deal name client-side. Deals past that page aren't reached — an
// accepted PoC limit, scoped to the manual deal_stakeholder edge.
const DEAL_PICKER_PAGE = 50;

async function searchDealCandidates(q: string): Promise<Candidate[]> {
  const { data, error } = await api.GET("/deals", {
    params: { query: { limit: DEAL_PICKER_PAGE } },
  });
  if (error) {
    throwProblem(error);
  }
  const needle = q.toLowerCase();
  return data.data
    .filter((deal) => deal.name.toLowerCase().includes(needle))
    .slice(0, 10)
    .map((deal) => ({ id: deal.id, name: deal.name }));
}

function searchByEntity(
  entity: EntityRefKind,
  query: string,
): Promise<Candidate[]> {
  switch (entity) {
    case "organization":
      return searchOrganizationCandidates(query);
    case "person":
      return searchPersonCandidates(query);
    case "deal":
      return searchDealCandidates(query);
  }
}

// A creatable edge from this scope: the kind, which entity fills the picked
// endpoint, and which request field carries its id — all fixed by (scope,
// kind) per the rel_*_shape CHECKs (migration 0007). The anchor endpoint
// comes from scope (scopeQuery); this describes the rest.
export type EdgeOption = {
  kind: RelationshipKind;
  entity: EntityRefKind;
  field: "organization_id" | "person_id" | "counterparty_org_id" | "deal_id";
};

// Only the kinds a scope can actually anchor are offered — a person anchors
// employment (→org) and deal_stakeholder (→deal); an org anchors employment
// (→person) and the three org↔org kinds (→counterparty org). Offering the
// rest would only earn an endpoint-shape 422.
export function edgeOptions(scope: RelationshipScope): EdgeOption[] {
  if ("person_id" in scope) {
    return [
      { kind: "employment", entity: "organization", field: "organization_id" },
      { kind: "deal_stakeholder", entity: "deal", field: "deal_id" },
    ];
  }
  return [
    { kind: "employment", entity: "person", field: "person_id" },
    {
      kind: "partner_of",
      entity: "organization",
      field: "counterparty_org_id",
    },
    {
      kind: "referred_by",
      entity: "organization",
      field: "counterparty_org_id",
    },
    {
      kind: "co_sell_with",
      entity: "organization",
      field: "counterparty_org_id",
    },
  ];
}

// Typed carrier for the picked endpoint id — a computed-key spread would
// widen to an index signature the request type won't accept.
export function endpointBody(
  field: EdgeOption["field"],
  id: string,
): Partial<CreateRelationshipRequest> {
  switch (field) {
    case "organization_id":
      return { organization_id: id };
    case "person_id":
      return { person_id: id };
    case "counterparty_org_id":
      return { counterparty_org_id: id };
    case "deal_id":
      return { deal_id: id };
  }
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
  const options = edgeOptions(scope);
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState<RelationshipKind>(options[0].kind);
  const [role, setRole] = useState("");
  const [startedAt, setStartedAt] = useState("");
  const [term, setTerm] = useState("");
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [target, setTarget] = useState<Candidate | null>(null);
  const [searchError, setSearchError] = useState<string | null>(null);
  // Kind fixes the picked endpoint; a stale kind can't outlive its scope
  // because the tab remounts per record, so the first option is always valid.
  const endpoint = options.find((o) => o.kind === kind) ?? options[0];
  const entity = endpoint.entity;

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
        const results = await searchByEntity(entity, query);
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
  }, [open, term, entity]);

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
        ...endpointBody(endpoint.field, target.id),
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

  // Switching kind can switch the target entity (org→deal→person), so any
  // pending pick and search results from the old entity must clear.
  function selectKind(next: RelationshipKind) {
    setKind(next);
    setTerm("");
    setCandidates([]);
    setTarget(null);
    setSearchError(null);
  }

  function close() {
    setOpen(false);
    setKind(options[0].kind);
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
                selectKind(event.target.value as RelationshipKind)
              }
            >
              {options.map((option) => (
                <option key={option.kind} value={option.kind}>
                  {t(KIND_LABELS[option.kind])}
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
          {target && (
            <p style={{ marginBottom: 4 }}>
              {t("rel.addConfirm", {
                target: target.name,
                kind: t(KIND_LABELS[kind]),
              })}
            </p>
          )}
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
                  render: (rel: Relationship) => {
                    const ref = counterpartyRef(rel, scope);
                    return ref ? (
                      <EntityRef kind={ref.kind} id={ref.id} />
                    ) : (
                      <span className="t-mono">—</span>
                    );
                  },
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
