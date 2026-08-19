// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { ENTITY, type EntityKind } from "../app/entity";
import { navigate } from "../app/router";
import { useT } from "../i18n";
import { throwProblem } from "./common";

// A cross-record reference rendered as the target's display name plus a
// backlink to its 360, resolved by id. Records point at each other by id
// across the contract (owner, counterparty, partner org, deal); showing the
// raw UUID is honest but unreadable, so this hydrates the name off the record
// read and links through. A reference that cannot be named renders the id
// (mono, no link) rather than blank or a dead link — on an audit row or a
// history entry that id is the one traceable fact left. A reference whose read
// has not answered YET says so instead, because a name that is coming and a
// name that is never coming are different facts.
//
// `user`/`team` are the one exception to the "resolved name is a link"
// rule: there is no 360 to send them to, so they resolve off the shared
// roster list (`/users` / `/teams`) and always render as plain text, never
// touching the ENTITY registry (which has no `user`/`team` entry).

// The record kinds share the app-wide ENTITY registry (routes + vocabulary);
// user/team are EntityRef-only: they have no 360 to route to, so they resolve
// off the shared roster list and render as plain text.
export type RosterKind = "user" | "team";
export type EntityRefKind = EntityKind | RosterKind;

type User = components["schemas"]["User"];
type Team = components["schemas"]["Team"];

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
        if (error) throwProblem(error);
        return data.data;
      }
      const { data, error } = await api.GET("/teams", {
        params: { query: { limit: 200 } },
      });
      if (error) throwProblem(error);
      return data.data;
    },
    enabled,
    staleTime: 60_000,
  });
}

// The resolved display name only, sharing EntityRef's exact cache entry so
// nothing is fetched twice. Exported for chrome that wants the name as plain
// text rather than as EntityRef's navigating button — the breadcrumb names the
// record you are already looking at, so linking it would go nowhere.
export function useEntityName(
  kind: EntityKind,
  id: string | null | undefined,
): string | null {
  const query = useQuery({
    queryKey: [kind, "ref", id],
    queryFn: () => fetchEntityName(kind, id ?? ""),
    enabled: Boolean(id),
    staleTime: 60_000,
  });
  return query.data ?? null;
}

/**
 * A reference the page cannot put a name to, in the two readings it has.
 *
 * `pending` is a read that has not answered yet, and it is allowed to say so.
 * Once the read has settled, the id is what is left: on the surfaces that keep
 * this fallback — an audit row, a history entry, a record the reader may not
 * open — the id is the one traceable fact, so it stays. What is never honest is
 * painting that id while the name is still on its way, which is how a record
 * page came to show a uuid for a moment on every load.
 */
function UnnamedRef({
  id,
  pending,
}: Readonly<{ id: string; pending: boolean }>) {
  const t = useT();
  if (pending) {
    return <span className="t-caption">{t("common.loading")}</span>;
  }
  return (
    <span className="t-mono" title={id}>
      {id}
    </span>
  );
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
  name,
  asText = false,
}: Readonly<{
  kind: EntityRefKind;
  id: string | null | undefined;
  /**
   * Name the record without linking to it, for a caller that is already a link
   * to the same place — a list row's identity cell. A control nested inside a
   * link is invalid markup, and the second route would go where the first one
   * already goes.
   */
  asText?: boolean;
  // The display name, when the CALLER already has it. A composite read that
  // returns its own labels — the company view's connection graph — would
  // otherwise pay one record fetch per reference and show the raw id until each
  // one lands. Passing it skips the lookup entirely; the link and the id
  // fallback are unchanged.
  name?: string | null;
}>) {
  if (!id) {
    return <span className="t-mono">—</span>;
  }
  // Dispatch on the kind rather than running both resolutions and discarding
  // one. Each branch then owns exactly the read it needs — no query has to be
  // told to stay switched off, and none can report itself as loading when it
  // was never going to run — and `kind` narrows here instead of being asserted
  // inside a body that serves both.
  if (kind === "user" || kind === "team") {
    return <RosterRef kind={kind} id={id} name={name} />;
  }
  return <RecordRef kind={kind} id={id} name={name} asText={asText} />;
}

// A workspace user or team: no 360 exists to send the reader to, so a resolved
// name renders as plain text and the reference never becomes a link.
function RosterRef({
  kind,
  id,
  name,
}: Readonly<{ kind: RosterKind; id: string; name?: string | null }>) {
  // A caller-supplied name wins here exactly as it does for a record: the
  // connection graph returns its own labels, and falling straight through to
  // the roster showed the reader a raw uuid until — and unless — /users
  // resolved it.
  const roster = useRoster(kind, !name);
  const match = roster.data?.find((entry) => entry.id === id);
  const resolved = name || (match ? rosterName(kind, match) : null);
  if (resolved == null) {
    return <UnnamedRef id={id} pending={roster.isPending} />;
  }
  return <span title={id}>{resolved}</span>;
}

// A record with a 360 behind it: a resolved name is also the backlink.
function RecordRef({
  kind,
  id,
  name,
  asText,
}: Readonly<{
  kind: EntityKind;
  id: string;
  name?: string | null;
  asText: boolean;
}>) {
  const query = useQuery({
    queryKey: [kind, "ref", id],
    queryFn: () => fetchEntityName(kind, id),
    // A caller-supplied name skips the lookup; a blank one does not, because a
    // blank is the caller saying it has nothing rather than saying the record
    // is nameless.
    enabled: !name,
    // References change rarely relative to the pages that render them; a short
    // cache keeps a 360 from re-fetching the same name on every hover/refetch.
    staleTime: 60_000,
  });
  // Only a resolved name is a safe link target; a reference with no name —
  // still loading, or a record the caller can't read — never becomes one.
  //
  // An EMPTY supplied name counts as no name: a record whose display field is
  // blank would otherwise render as a button with nothing in it, which is a
  // link a reader can neither read nor find.
  const resolved = name || query.data;
  if (resolved == null) {
    return <UnnamedRef id={id} pending={query.isPending} />;
  }
  if (asText) {
    return <span title={id}>{resolved}</span>;
  }
  return (
    <button
      type="button"
      className="entity-link"
      onClick={() => navigate(ENTITY[kind].route(id))}
      title={id}
    >
      {resolved}
    </button>
  );
}

/**
 * The owner of a record, by name, for a list column.
 *
 * Reads the shared roster cache (one `/users` page, the same entry EntityRef
 * and the Share picker use), so a list of 50 rows costs no extra request. An
 * owner the roster cannot name still renders rather than going blank, because
 * a blank owner column reads as unowned, and unowned is a different fact with
 * its own filter — but it renders as the same unnamed reference every other
 * cross-record reference gets, not as a truncated id, which is a non-answer
 * that has also lost the ability to be looked up.
 */
export function OwnerName({
  ownerId,
  unowned,
}: Readonly<{ ownerId?: string | null; unowned: string }>) {
  const roster = useRoster("user", Boolean(ownerId));
  if (!ownerId) {
    return <span className="t-caption">{unowned}</span>;
  }
  const named = (roster.data ?? []).find((entry) => entry.id === ownerId);
  if (named && "display_name" in named) {
    return <span>{named.display_name}</span>;
  }
  return <UnnamedRef id={ownerId} pending={roster.isPending} />;
}
