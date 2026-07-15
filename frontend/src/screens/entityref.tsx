// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { ENTITY, type EntityKind } from "../app/entity";
import { navigate } from "../app/router";

// A cross-record reference rendered as the target's display name plus a
// backlink to its 360, resolved by id. Records point at each other by id
// across the contract (owner, counterparty, partner org, deal); showing the
// raw UUID is honest but unreadable, so this hydrates the name off the record
// read and links through. The id is the fallback — shown (mono, no link)
// while the name loads and whenever the lookup can't resolve one — so a
// reference never renders blank or a dead link.

async function fetchEntityName(
  kind: EntityKind,
  id: string,
): Promise<string | null> {
  // Coerce a missing name to null (never undefined): react-query forbids an
  // undefined resolve, and a record read that somehow lacks its name field
  // should fall back to the id, not crash the query. Each kind reads a
  // different endpoint and a differently-named field, so this stays a
  // straight per-kind switch rather than a generic lookup.
  if (kind === "person") {
    const { data, error } = await api.GET("/people/{id}", {
      params: { path: { id } },
    });
    return error ? null : (data.full_name ?? null);
  }
  if (kind === "organization") {
    const { data, error } = await api.GET("/organizations/{id}", {
      params: { path: { id } },
    });
    return error ? null : (data.display_name ?? null);
  }
  if (kind === "lead") {
    const { data, error } = await api.GET("/leads/{id}", {
      params: { path: { id } },
    });
    return error ? null : (data.full_name ?? null);
  }
  const { data, error } = await api.GET("/deals/{id}", {
    params: { path: { id } },
  });
  return error ? null : (data.name ?? null);
}

export function EntityRef({
  kind,
  id,
}: Readonly<{ kind: EntityKind; id: string | null | undefined }>) {
  const query = useQuery({
    queryKey: [kind, "ref", id],
    queryFn: () => fetchEntityName(kind, id ?? ""),
    enabled: Boolean(id),
    // References change rarely relative to the pages that render them; a short
    // cache keeps a 360 from re-fetching the same name on every hover/refetch.
    staleTime: 60_000,
  });
  if (!id) {
    return <span className="t-mono">—</span>;
  }
  // Only a resolved name is a safe link target; an unresolved id (still
  // loading, or a record the caller can't read) stays plain mono text.
  if (query.data == null) {
    return (
      <span className="t-mono" title={id}>
        {id}
      </span>
    );
  }
  return (
    <button
      type="button"
      className="entity-link"
      onClick={() => navigate(ENTITY[kind].route(id))}
      title={id}
    >
      {query.data}
    </button>
  );
}
