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
// has not answered YET, or whose read came back refused, says so instead:
// a name that is coming, a name that is never coming, and a name nobody could
// read at all are three different facts.
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

/**
 * What a read that carried no name is allowed to mean.
 *
 * A 404 is an ANSWER: the record is gone, or row-scope hides its existence from
 * this reader (the API hides a row it may not see rather than admitting it),
 * and no amount of waiting or asking again will produce a name. That is the
 * settled reading the id fallback exists for.
 *
 * Every other failure — a 403 on the object, a 5xx, a dropped connection — is a
 * read that never arrived, and it THROWS so react-query holds it as an error.
 * Flattened to null it would be indistinguishable from the answer above, and
 * the two say opposite things about whether this is worth asking again.
 */
function unnamedOrThrow(error: unknown, response: Response): null {
  if (response.status === 404) {
    return null;
  }
  throwProblem(error);
}

async function fetchEntityName(
  kind: EntityKind,
  id: string,
): Promise<string | null> {
  // A missing name coerces to null (never undefined): react-query forbids an
  // undefined resolve, and a record that answers without its name field has
  // answered. Each kind reads a different endpoint and a differently-named
  // field, so this stays a straight per-kind switch rather than a generic
  // lookup.
  if (kind === "person") {
    const { data, error, response } = await api.GET("/people/{id}", {
      params: { path: { id } },
    });
    if (error) return unnamedOrThrow(error, response);
    return data.full_name ?? null;
  }
  if (kind === "organization") {
    const { data, error, response } = await api.GET("/organizations/{id}", {
      params: { path: { id } },
    });
    if (error) return unnamedOrThrow(error, response);
    return data.display_name ?? null;
  }
  if (kind === "lead") {
    const { data, error, response } = await api.GET("/leads/{id}", {
      params: { path: { id } },
    });
    if (error) return unnamedOrThrow(error, response);
    return data.full_name ?? data.email ?? null;
  }
  const { data, error, response } = await api.GET("/deals/{id}", {
    params: { path: { id } },
  });
  if (error) return unnamedOrThrow(error, response);
  return data.name ?? null;
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
): { name: string | null; reading: NameReading } {
  const query = useQuery({
    queryKey: [kind, "ref", id],
    queryFn: () => fetchEntityName(kind, id ?? ""),
    enabled: Boolean(id),
    staleTime: 60_000,
  });
  // The reading travels with the name, because a caller handed only `null`
  // cannot tell a name that is still coming from one that will never come, and
  // every caller that has had to guess has guessed the id.
  return { name: usableName(query.data), reading: readingOf(query) };
}

/**
 * The three readings of a reference the page cannot put a name to.
 *
 * `pending` is a read that has not answered yet, and it is allowed to say so.
 * `unnamed` is a read that ANSWERED and carried no name — a record with a blank
 * display field, or one the API will not admit exists (see `unnamedOrThrow`);
 * there the id is what is left, and on the surfaces that keep this fallback —
 * an audit row, a history entry, a record the reader may not open — it is the
 * one traceable fact, so it stays. `failed` is a read that never arrived, and
 * it may not borrow either spelling: painting the id while the name is still on
 * its way is how a record page came to show a uuid for a moment on every load,
 * and painting it for a 403 or a 500 states as settled fact a question nothing
 * answered.
 */
type NameReading = "pending" | "failed" | "unnamed";

function readingOf(
  query: Readonly<{ isPending: boolean; isError: boolean }>,
): NameReading {
  if (query.isPending) {
    return "pending";
  }
  return query.isError ? "failed" : "unnamed";
}

/**
 * A caller-supplied or read-back name is usable only when it says something.
 *
 * Blank and whitespace-only are the same claim — the source has nothing —
 * rather than a record whose name is a space, so neither skips the lookup and
 * neither becomes a label. A button carrying one is a link a reader can neither
 * read nor find.
 */
function usableName(name: string | null | undefined): string | null {
  const trimmed = name?.trim();
  return trimmed ? trimmed : null;
}

function UnnamedRef({
  id,
  reading,
}: Readonly<{ id: string; reading: NameReading }>) {
  const t = useT();
  if (reading === "pending") {
    return <span className="t-caption">{t("common.loading")}</span>;
  }
  if (reading === "failed") {
    // The id stays reachable through the title rather than printed as the
    // value: on the line it reads as what the read came back with, and the
    // read came back with nothing.
    return (
      <span className="t-caption" title={id}>
        {t("ref.nameLoadFailed")}
      </span>
    );
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
  const supplied = usableName(name);
  const roster = useRoster(kind, supplied == null);
  const match = roster.data?.find((entry) => entry.id === id);
  const resolved =
    supplied ?? (match ? usableName(rosterName(kind, match)) : null);
  if (resolved == null) {
    return <UnnamedRef id={id} reading={readingOf(roster)} />;
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
  // A caller-supplied name skips the lookup; a blank one does not, because a
  // blank is the caller saying it has nothing rather than saying the record is
  // nameless. `usableName` is what decides that, once, so the value that
  // switches the read off is the same value that gets rendered.
  const supplied = usableName(name);
  const query = useQuery({
    queryKey: [kind, "ref", id],
    queryFn: () => fetchEntityName(kind, id),
    enabled: supplied == null,
    // References change rarely relative to the pages that render them; a short
    // cache keeps a 360 from re-fetching the same name on every hover/refetch.
    staleTime: 60_000,
  });
  // Only a resolved name is a safe link target; a reference with no name —
  // still loading, refused, or a record that carries none — never becomes one.
  const resolved = supplied ?? usableName(query.data);
  if (resolved == null) {
    return <UnnamedRef id={id} reading={readingOf(query)} />;
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
  return <UnnamedRef id={ownerId} reading={readingOf(roster)} />;
}
