// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useQuery } from "@tanstack/react-query";
import { api, FIRST_PAGE } from "../api/client";
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

// One reader per kind: each reads a different endpoint and a differently
// named field, so the table is the honest shape — a generic lookup would have
// to guess the field. A missing name coerces to null (never undefined):
// react-query forbids an undefined resolve, and a record that answers without
// its name field has answered.
const NAME_READERS: Record<EntityKind, (id: string) => Promise<string | null>> =
  {
    person: async (id) => {
      const { data, error, response } = await api.GET("/people/{id}", {
        params: { path: { id } },
      });
      if (error) return unnamedOrThrow(error, response);
      return data.full_name ?? null;
    },
    organization: async (id) => {
      const { data, error, response } = await api.GET("/organizations/{id}", {
        params: { path: { id } },
      });
      if (error) return unnamedOrThrow(error, response);
      return data.display_name ?? null;
    },
    lead: async (id) => {
      const { data, error, response } = await api.GET("/leads/{id}", {
        params: { path: { id } },
      });
      if (error) return unnamedOrThrow(error, response);
      return data.full_name ?? data.email ?? null;
    },
    project: async (id) => {
      const { data, error, response } = await api.GET("/projects/{id}", {
        params: { path: { id } },
      });
      if (error) return unnamedOrThrow(error, response);
      return data.name ?? null;
    },
    deal: async (id) => {
      const { data, error, response } = await api.GET("/deals/{id}", {
        params: { path: { id } },
      });
      if (error) return unnamedOrThrow(error, response);
      return data.name ?? null;
    },
  };

function fetchEntityName(kind: EntityKind, id: string): Promise<string | null> {
  return NAME_READERS[kind](id);
}

// Roster lookups share one cache entry across every EntityRef + the Share
// picker: `/users` and `/teams` are workspace-wide lists, so reading one list
// once and finding-by-id is cheaper (and more cacheable) than a per-id GET for
// every rendered reference.

type RosterEntry = User | Team;

// HOW THE ROSTER IS READ, AND WHERE IT STOPS.
//
// Both list endpoints are keyset-paged and the contract caps `limit` at 200, so
// ONE page is not the roster: past 200 members an owner on the second page
// resolved to a raw uuid and every picker built on this hook silently dropped
// everyone above the cap. The walk follows `next_cursor` to the end instead.
//
// It is BOUNDED because the cursor is the server's to mint, and a walk that
// trusts one unconditionally is a page that never paints. ROSTER_WALK_PAGES
// pages of the contract's maximum page size reach 2 000 members — well past the
// largest workspace one installation serves, and already ten SEQUENTIAL round
// trips, which is where opening a picker becomes a wait a reader notices.
// Beyond that the answer is a server-side resolve rather than more client
// pages, so the walk stops and says so: `partial` travels with the entries,
// because a truncation nobody states reads as the whole workspace.
const ROSTER_PAGE_SIZE = 200;
const ROSTER_WALK_PAGES = 10;

/** The roster as one list, plus whether it is the WHOLE list. */
type Roster = { entries: RosterEntry[]; partial: boolean };

async function readRosterPage(
  kind: RosterKind,
  cursor: string | null,
): Promise<{ entries: RosterEntry[]; next: string | null }> {
  // The two endpoints answer differently-typed rows, so each arm reads its own
  // — a shared call would have to assert one shape onto the other.
  if (kind === "user") {
    const { data, error } = await api.GET("/users", {
      params: {
        query: { limit: ROSTER_PAGE_SIZE, ...(cursor ? { cursor } : {}) },
      },
    });
    if (error) throwProblem(error);
    return { entries: data.data, next: data.page.next_cursor ?? null };
  }
  const { data, error } = await api.GET("/teams", {
    params: {
      query: { limit: ROSTER_PAGE_SIZE, ...(cursor ? { cursor } : {}) },
    },
  });
  if (error) throwProblem(error);
  return { entries: data.data, next: data.page.next_cursor ?? null };
}

async function walkRoster(kind: RosterKind): Promise<Roster> {
  const entries: RosterEntry[] = [];
  let cursor = FIRST_PAGE;
  for (let page = 0; page < ROSTER_WALK_PAGES; page += 1) {
    // A page that fails throws out of the whole walk (through `throwProblem`
    // inside `readRosterPage`), so react-query holds the read as an error.
    // Caught and dropped here it would come back as a SHORT list that reads as
    // complete — the one shape a caller cannot tell from a small workspace.
    const answered = await readRosterPage(kind, cursor);
    entries.push(...answered.entries);
    // The CURSOR is what the walk can continue with, and `has_more` without one
    // is a list nothing can read the rest of — so both ends of the walk stop
    // here rather than looping on a cursor that will not move.
    cursor = answered.next;
    if (!cursor) {
      return { entries, partial: false };
    }
  }
  return { entries, partial: true };
}

// One cache entry per roster kind, shared by every consumer — the Share
// picker, each owner column, every EntityRef on the page — so the walk runs
// once per minute however many surfaces read it.
function rosterQueryOptions(kind: RosterKind, enabled: boolean) {
  return {
    queryKey: [kind === "user" ? "users" : "teams"],
    queryFn: () => walkRoster(kind),
    enabled,
    staleTime: 60_000,
  };
}

// Both facts off the one entry, for the two callers below that need the
// entries AND whether they are all of them; one observer rather than two.
function useRosterWalk(kind: RosterKind, enabled: boolean) {
  return useQuery(rosterQueryOptions(kind, enabled));
}

/**
 * The roster's entries.
 *
 * Exported so the Share subject picker, the owner pickers and the quota target
 * picker all build off the exact same cache entry EntityRef's own user/team
 * resolution reads — one walk, one cache key, every consumer.
 *
 * A consumer that OFFERS these entries as a list of who exists owes its reader
 * `useRosterPartial` beside it: this result cannot say whether the walk reached
 * the end, and a picker missing people looks exactly like a small workspace.
 */
export function useRoster(kind: RosterKind, enabled: boolean) {
  return useQuery({
    ...rosterQueryOptions(kind, enabled),
    select: (roster: Roster) => roster.entries,
  });
}

/**
 * Whether the roster this surface is showing stopped short of the workspace.
 *
 * A second hook rather than a field on `useRoster`'s result, because it reads
 * the SAME cache entry — same key, no extra request — and the many consumers
 * that only resolve names never have to thread a flag they do not render.
 *
 * False while the walk is still running and false once it finished: a caveat
 * about a list nobody has read yet is noise, and one about a complete list is
 * simply untrue.
 */
export function useRosterPartial(kind: RosterKind, enabled: boolean): boolean {
  const query = useQuery({
    ...rosterQueryOptions(kind, enabled),
    select: (roster: Roster) => roster.partial,
  });
  return query.data === true;
}

/**
 * The words a picker owes its reader when the roster behind it is not the whole
 * workspace: the list they are looking at is part of one, and the colleague
 * they cannot find may be somebody this surface never read.
 *
 * The sentence AND the decision that there is one to say, in one place, so a
 * picker cannot invent a gentler wording for the same gap. Exported as words
 * rather than only as markup for the callers whose slot takes a string: a
 * `Field` hint owns its own paragraph and wires it into the control's
 * `aria-describedby`, and a second paragraph nested inside that one would be
 * invalid markup describing nothing. `RosterPartialNote` is the element form of
 * the same fact, so both stay one authority.
 */
export function useRosterPartialHint(partial: boolean): string | undefined {
  const t = useT();
  return partial ? t("state.partial") : undefined;
}

/**
 * The caveat as its own line, for a picker with room for one beside it.
 *
 * Takes the FACT rather than reading the roster itself — `useRosterPartial` at
 * the call site is the read the surface already made, and a note with a query of
 * its own could arm a fetch the surface deferred. Renders nothing when the walk
 * reached the end, so it can sit unconditionally beside the control it is about.
 *
 * `id` is for the control that names this line in its `aria-describedby`, which
 * is how a caveat that cannot sit next to its control stays attached to it.
 * `aria-live="off"` is the same concern from the other side: a caveat that lands
 * inside somebody else's live region — the bulk bar is one — would be announced
 * as news when the walk finishes, interrupting whatever the reader was doing to
 * report a fact about a list they were not asking about.
 */
export function RosterPartialNote({
  partial,
  id,
}: Readonly<{ partial: boolean; id?: string }>) {
  const hint = useRosterPartialHint(partial);
  if (!hint) {
    return null;
  }
  return (
    <p className="t-caption" id={id} aria-live="off">
      {hint}
    </p>
  );
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
 * What a roster that could not name an id is allowed to say.
 *
 * A roster walked to the end has ANSWERED about this id: whoever holds it is
 * not somebody this reader may list — deactivated, or gone — and the id is what
 * is left to trace, which is the settled `unnamed`. A roster that stopped at
 * its page budget has answered nothing about it: the name may sit on a page
 * nobody read. That reading borrows `failed` — nothing further is coming
 * without a new read, so it is not `pending`, and printing the id would state
 * as settled fact a question this walk never reached.
 *
 * Exported because every surface that names a roster id has to make the same
 * three-way call, and the ones that made it separately made it differently: a
 * picker went on reporting a colleague as departed while the walk beside it had
 * not finished reading. One classification, and each caller decides only what to
 * SAY about it.
 */
export function rosterReading(
  query: Readonly<{ isPending: boolean; isError: boolean }>,
  partial: boolean,
): NameReading {
  const reading = readingOf(query);
  return reading === "unnamed" && partial ? "failed" : reading;
}

/**
 * What to call a roster id the walk could not name.
 *
 * `unlisted` is the CALLER's sentence, because only the caller knows what the id
 * was for — an account's owner, a request's assignee — and it is the one reading
 * that claims the roster answered about them. The other two readings say the
 * same thing wherever they happen: a read still in flight has said nothing yet,
 * and a read that failed or stopped short of the workspace has said nothing
 * about THIS id.
 *
 * A picker whose current value matches no option renders blank (`Select` falls
 * back to its placeholder, and to a non-breaking space without one), which is
 * indistinguishable from unset — so a value the roster cannot name still needs a
 * label, and this is the one that is honest about why it has no name.
 */
export function rosterMissLabel(
  roster: Readonly<{ isPending: boolean; isError: boolean }>,
  partial: boolean,
  t: ReturnType<typeof useT>,
  unlisted: string,
): string {
  const reading = rosterReading(roster, partial);
  if (reading === "pending") {
    return t("common.loading");
  }
  return reading === "failed" ? t("ref.nameLoadFailed") : unlisted;
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
  const roster = useRosterWalk(kind, supplied == null);
  const match = roster.data?.entries.find((entry) => entry.id === id);
  const resolved =
    supplied ?? (match ? usableName(rosterName(kind, match)) : null);
  if (resolved == null) {
    return (
      <UnnamedRef
        id={id}
        reading={rosterReading(roster, roster.data?.partial === true)}
      />
    );
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
 * Reads the shared roster cache (the same walked entry EntityRef and the Share
 * picker use), so a list of 50 rows costs no extra request. An owner the roster
 * cannot name still renders rather than going blank, because a blank owner
 * column reads as unowned, and unowned is a different fact with its own filter
 * — but it renders as the same unnamed reference every other cross-record
 * reference gets, not as a truncated id, which is a non-answer that has also
 * lost the ability to be looked up.
 */
export function OwnerName({
  ownerId,
  unowned,
}: Readonly<{ ownerId?: string | null; unowned: string }>) {
  const roster = useRosterWalk("user", Boolean(ownerId));
  if (!ownerId) {
    return <span className="t-caption">{unowned}</span>;
  }
  const named = roster.data?.entries.find((entry) => entry.id === ownerId);
  if (named && "display_name" in named) {
    return <span>{named.display_name}</span>;
  }
  return (
    <UnnamedRef
      id={ownerId}
      reading={rosterReading(roster, roster.data?.partial === true)}
    />
  );
}
