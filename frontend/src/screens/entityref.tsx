// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate, type Route } from "../app/router";
import { problemMessage } from "./common";

// A cross-record reference rendered as the target's display name plus a
// backlink to its 360, resolved by id. Records point at each other by id
// across the contract (owner, counterparty, partner org, deal); showing the
// raw UUID is honest but unreadable, so this hydrates the name off the record
// read and links through. The id is the fallback — shown (mono, no link)
// while the name loads and whenever the lookup can't resolve one — so a
// reference never renders blank or a dead link.
//
// `user`/`team` are the one exception to the "resolved name is a link"
// rule: there is no 360 to send them to, so they resolve off the shared
// roster list (`/users` / `/teams`) and always render as plain text, never
// touching `ROUTE_OF` (which has no `user`/`team` entry).

export type EntityRefKind =
  | "person"
  | "organization"
  | "deal"
  | "lead"
  | "user"
  | "team";

type RecordKind = "person" | "organization" | "deal" | "lead";
export type RosterKind = "user" | "team";

const ROUTE_OF: Record<RecordKind, (id: string) => Route> = {
  person: (id) => ({ screen: "contacts", id }),
  organization: (id) => ({ screen: "companies", id }),
  deal: (id) => ({ screen: "deals", id }),
  lead: (id) => ({ screen: "leads", id }),
};

type User = components["schemas"]["User"];
type Team = components["schemas"]["Team"];

async function fetchEntityName(
  kind: RecordKind,
  id: string,
): Promise<string | null> {
  // Coerce a missing name to null (never undefined): react-query forbids an
  // undefined resolve, and a record read that somehow lacks its name field
  // should fall back to the id, not crash the query.
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
    return error ? null : (data.full_name ?? data.email ?? null);
  }
  const { data, error } = await api.GET("/deals/{id}", {
    params: { path: { id } },
  });
  return error ? null : (data.name ?? null);
}

// Roster lookups share one cache entry across every EntityRef + the Share
// picker: `/users` and `/teams` are small workspace-wide lists, so paging one
// list once and finding-by-id is cheaper (and more cacheable) than a per-id
// GET for every rendered reference.
// Exported so the Share subject picker (screens/share.tsx) can build a
// merged users+teams roster off the exact same cache entry EntityRef's own
// user/team resolution reads — one fetch, one cache key, both consumers.
export function useRoster(kind: RosterKind, enabled: boolean) {
  return useQuery({
    queryKey: [kind === "user" ? "users" : "teams"],
    queryFn: async (): Promise<Array<User | Team>> => {
      if (kind === "user") {
        const { data, error } = await api.GET("/users", {
          params: { query: { limit: 200 } },
        });
        if (error) throw new Error(problemMessage(error));
        return data.data;
      }
      const { data, error } = await api.GET("/teams", {
        params: { query: { limit: 200 } },
      });
      if (error) throw new Error(problemMessage(error));
      return data.data;
    },
    enabled,
    staleTime: 60_000,
  });
}

function rosterName(kind: RosterKind, entry: User | Team): string | null {
  if (kind === "user") {
    return (entry as User).display_name ?? null;
  }
  return (entry as Team).name ?? null;
}

export function EntityRef({
  kind,
  id,
}: Readonly<{ kind: EntityRefKind; id: string | null | undefined }>) {
  const isRoster = kind === "user" || kind === "team";
  // Both queries are called unconditionally (rules of hooks) and gated with
  // `enabled` instead — only the branch matching `kind` actually fetches.
  const recordQuery = useQuery({
    queryKey: [kind, "ref", id],
    queryFn: () => fetchEntityName(kind as RecordKind, id ?? ""),
    enabled: Boolean(id) && !isRoster,
    // References change rarely relative to the pages that render them; a short
    // cache keeps a 360 from re-fetching the same name on every hover/refetch.
    staleTime: 60_000,
  });
  const rosterQuery = useRoster(
    isRoster ? (kind as RosterKind) : "user",
    Boolean(id) && isRoster,
  );

  if (!id) {
    return <span className="t-mono">—</span>;
  }

  if (isRoster) {
    const rosterKind = kind as RosterKind;
    const match = rosterQuery.data?.find((entry) => entry.id === id);
    const name = match ? rosterName(rosterKind, match) : null;
    // No 360 exists for a user/team, so this never becomes a link — only the
    // id-vs-resolved-name fallback applies.
    if (name == null) {
      return (
        <span className="t-mono" title={id}>
          {id}
        </span>
      );
    }
    return <span title={id}>{name}</span>;
  }

  // Only a resolved name is a safe link target; an unresolved id (still
  // loading, or a record the caller can't read) stays plain mono text.
  if (recordQuery.data == null) {
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
      onClick={() => navigate(ROUTE_OF[kind as RecordKind](id))}
      title={id}
    >
      {recordQuery.data}
    </button>
  );
}
