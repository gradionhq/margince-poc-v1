import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { clearPendingAuthorize } from "../app/pendingauthorize";
import { Button, EmptyState, Skeleton } from "../design-system/atoms";
import type { Provenance } from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";

// Shared screen plumbing: honest loading / error / empty states (§3a screen-
// state matrix), the captured_by → provenance mapping every list reuses, and
// the ONE /me query the auth gate and every role-aware surface share.

// Authentication and availability are different product states: a failed
// session probe is typed so the auth boundary can render login (401), the
// connection-problem screen (network/5xx), or the installation-unavailable
// screen (503 — pre-bootstrap or a violated singleton invariant) instead of
// collapsing every error into login.
export type AuthProbeKind = "unauthorized" | "connection" | "installation";

export class AuthProbeError extends Error {
  readonly kind: AuthProbeKind;
  constructor(kind: AuthProbeKind, message: string) {
    super(message);
    this.name = "AuthProbeError";
    this.kind = kind;
  }
}

// probeKindFor maps a /me response status onto the boundary state. 503 is
// the middleware's installation-not-ready answer; any other 5xx (or a
// rejected fetch) is a connectivity problem; everything else reads as "no
// session" — the login screen.
function probeKindFor(status: number): AuthProbeKind {
  if (status === 503) return "installation";
  if (status >= 500) return "connection";
  return "unauthorized";
}

// authExitNotice marks a DELIBERATE sign-out so the boundary's next 401
// reads as "signed out", not "session expired". Module-scoped: exactly one
// boundary consumes it.
let authExitNotice: "signed-out" | null = null;

export function consumeAuthExitNotice(): "signed-out" | null {
  const notice = authExitNotice;
  authExitNotice = null;
  return notice;
}

// The session principal (GET /v1/me): identity + effective role keys. One
// spelling, one ["me"] cache entry — the App auth gate, the settings identity
// card, and role-aware affordances all read the same probe. The server binds
// the installation's singleton organization itself (A107/ADR-0061) — the
// probe needs nothing but the session cookie.
export function useMe() {
  return useQuery({
    queryKey: ["me"],
    staleTime: 5 * 60_000,
    // A role change does not revoke live sessions, so this snapshot is the one
    // cache entry that must not sit stale for its full staleTime: the UI now
    // scopes affordances by the grants it carries. "always" rather than `true`
    // deliberately — `true` refetches on focus only once the entry is already
    // stale, which is exactly the five-minute window this needs to close.
    refetchOnWindowFocus: "always",
    retry: false,
    queryFn: async () => {
      const result = await api.GET("/me").catch(() => null);
      if (!result) {
        throw new AuthProbeError("connection", "the API could not be reached");
      }
      const { data, error, response } = result;
      if (error) {
        throw new AuthProbeError(
          probeKindFor(response.status),
          problemMessage(error),
        );
      }
      if (!data?.user) {
        // The contract makes user required on MeResponse — a payload
        // without it is not a session, whatever the status code said; a
        // server answering garbage is an availability problem, not a
        // credentials one.
        throw new AuthProbeError("connection", "malformed /me response");
      }
      return data;
    },
  });
}

// The workspace system-of-record mode, read off the shared ["me"] cache.
// `native` is the safe default (full list capability) while /me is loading
// or if an older server omits the field; the list surfaces gate on `overlay`
// to drop sort/filter dials the incumbent mirror refuses (422). AuthGate
// resolves /me before any list screen mounts, so a screen sees the real value.
export function useSorMode(): "native" | "overlay" {
  return useMe().data?.system_of_record?.mode === "overlay"
    ? "overlay"
    : "native";
}

// The honest "this surface can't be served from the incumbent mirror" state,
// shown in overlay mode where a feature needs a capability the mirror does not
// hold — entity-scoped timelines, relationship strength, the context graph,
// task filtering, the morning brief. It is NOT an error: it is a deliberate,
// documented read-subset gap that closes when the workspace flips to native.
// Rendered in place of the feature so the user never hits "Couldn't load this
// view" for a capability overlay mode was never going to answer.
export function OverlayUnavailable() {
  const t = useT();
  return <EmptyState>{t("overlay.unavailable")}</EmptyState>;
}

// AS-1: sign out. Clears ALL cached tenant data on success, then forces the
// ["me"] probe to re-run → 401 → AuthGate renders the login screen.
//
// "All tenant data" includes the one piece that lives outside the query cache:
// the pending-authorization stash (sessionStorage survives a sign-out that
// doesn't close the tab). Left behind, the next human to sign in on this tab
// would be offered a connection request they never started — and the resume
// banner would send them to a consent screen holding their OWN passports.
//
// Order matters here: queryClient.clear() destroys every Query object in the
// cache, INCLUDING ["me"]'s. If ["me"] were reset only after a full clear(),
// resetQueries would find nothing matching that key to reset (it was already
// removed) — the mounted AuthGate observer would keep rendering its last
// (stale, authenticated) snapshot, since clear() alone never triggers a
// refetch. So instead: drop every OTHER cache entry first (leaving ["me"]
// intact), then resetQueries the shared ["me"] entry specifically — that
// query still exists, has an active (mounted) observer, and resetQueries
// forces it to refetch immediately, landing the AuthGate on 401 → login.
export function useLogout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/auth/logout");
      if (error) throwProblem(error);
    },
    onSuccess: async () => {
      // The next 401 at the boundary is this deliberate exit, not an
      // expired session — the login screen greets it accordingly.
      authExitNotice = "signed-out";
      clearPendingAuthorize();
      queryClient.removeQueries({
        predicate: (query) => query.queryKey[0] !== "me",
      });
      await queryClient.resetQueries({ queryKey: ["me"] });
    },
  });
}

// The minimal read surface QueryGate/QueryStates need. A real react-query
// `UseQueryResult<Data>` is structurally assignable to it, and a hook that
// MERGES several queries (e.g. the decided-approvals fan-out) can return a
// plain object of this shape — no `as unknown as UseQueryResult` lie required.
export interface QueryLike<Data> {
  isPending: boolean;
  isError: boolean;
  error: unknown;
  data: Data | undefined;
  refetch: () => unknown;
}

// The pending/error halves of the screen-state matrix (§3a) — one skeleton
// spelling, one error+retry spelling — shared by every query-backed screen
// regardless of whether it's a plain useQuery or an useInfiniteQuery (both
// expose this same isPending/isError/error/refetch shape). SUCCESS rendering
// stays the caller's job: some screens want QueryGate's generic empty-check,
// others (the History timelines) need custom grouping/pagination that no
// single success renderer could cover.
export function QueryStates({
  query,
  children,
}: Readonly<{
  query: Readonly<{
    isPending: boolean;
    isError: boolean;
    error: unknown;
    refetch: () => unknown;
  }>;
  children: ReactNode;
}>) {
  const t = useT();
  if (query.isPending) {
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
        <Skeleton width="60%" />
        <Skeleton width="90%" />
        <Skeleton width="75%" />
      </div>
    );
  }
  if (query.isError) {
    return (
      <EmptyState>
        <p>{t("common.error")}</p>
        <p className="t-mono" style={{ marginTop: 6 }}>
          {problemMessageOf(query.error, t)}
        </p>
        <Button small onClick={() => query.refetch()} style={{ marginTop: 10 }}>
          {t("common.retry")}
        </Button>
      </EmptyState>
    );
  }
  return <>{children}</>;
}

// The one "Load more" spelling for every keyset-paginated infinite query
// (record history, field history, the settings audit log): a small button
// that fetches the next page and disables itself mid-fetch, rendered only
// while the query still reports another page.
export function LoadMoreButton({
  query,
}: Readonly<{
  query: Readonly<{
    hasNextPage: boolean;
    isFetchingNextPage: boolean;
    fetchNextPage: () => unknown;
  }>;
}>) {
  const t = useT();
  if (!query.hasNextPage) {
    return null;
  }
  return (
    <Button
      small
      disabled={query.isFetchingNextPage}
      onClick={() => query.fetchNextPage()}
      style={{ marginTop: 10 }}
    >
      {t("list.loadMore")}
    </Button>
  );
}

export function QueryGate<Data>({
  query,
  empty,
  children,
}: Readonly<{
  query: QueryLike<Data>;
  empty?: (data: Data) => boolean;
  children: (data: Data) => ReactNode;
}>) {
  const t = useT();
  // A QueryLike isn't a discriminated union, so TS can't narrow it: past
  // QueryStates' pending/error guards `data` is present, so key SUCCESS
  // rendering off its presence rather than a react-query `isSuccess` flag
  // the merged fan-out hooks don't expose.
  const data = query.data;
  let success: ReactNode = null;
  if (data !== undefined) {
    success = empty?.(data) ? (
      <EmptyState>{t("common.empty")}</EmptyState>
    ) : (
      children(data)
    );
  }
  return <QueryStates query={query}>{success}</QueryStates>;
}

// captured_by is server-stamped "human:<uuid> | agent:<id> | connector:<name>".
// The tag shows the bare id — never the doubled "agent: agent:<id>" the old
// reassembly produced — and a connector reads as a connector, not an agent.
//
// A human is only "you" when the id is the reader's. Without a viewer id the
// human branch stays unnamed rather than guessing: a caller that cannot say
// who is reading cannot claim the reader typed it. An absent captured_by is
// `unknown` and says so — it used to render as the reader's own typing, which
// is the one attribution nobody can check.
export function provenanceOf(
  capturedBy: string | undefined,
  viewerUserId?: string,
): Provenance {
  if (!capturedBy) {
    return { kind: "unknown" };
  }
  const [source, name] = capturedBy.split(":", 2);
  if (source === "human") {
    return {
      kind: "human",
      self: Boolean(viewerUserId) && name === viewerUserId,
      userId: name,
    };
  }
  const label = name ?? source;
  if (source === "connector") {
    return { kind: "connector", connector: label };
  }
  return { kind: "agent", agent: label };
}

// SOURCE_ORIGIN_KEYS maps the storage-side `source` enum onto the
// human-readable phrase naming WHERE a value came from. Kept separate from
// provenanceOf: `source` and `captured_by` are two different questions
// about the same row (where the value came from vs. who wrote it down), and
// only this one distinguishes a person's own typing from a person confirming
// something the system read for them.
const SOURCE_ORIGIN_KEYS: Record<
  "site_read" | "connector" | "migration",
  MessageKey
> = {
  site_read: "trust.originSiteRead",
  connector: "trust.originConnector",
  migration: "trust.originMigration",
};

// originOf renders the phrase for a non-human source, or undefined for
// "human" (typed) or an absent source — a row with no origin claim gets no
// origin line, same as it gets no evidence mark.
export function originOf(
  source: "human" | "site_read" | "connector" | "migration" | undefined,
  t: ReturnType<typeof useT>,
): string | undefined {
  if (!source || source === "human") {
    return undefined;
  }
  return t(SOURCE_ORIGIN_KEYS[source]);
}

// The reader's own user id, for the provenance tags on this screen. Undefined
// while /me is in flight, which the tags read as "a person, not provably you"
// — the honest reading until the session is known.
export function useViewerId(): string | undefined {
  return useMe().data?.user.id;
}

// RFC 7807 bodies carry the honest detail; surface it instead of a generic
// failure so the error state names its cause. `null` says the body carried no
// such text at all — a non-OK response the server sent no body with, or one
// whose body was never RFC 7807 in the first place.
//
// That distinction is the whole reason this sits apart from problemMessage
// below: "the server described this failure" and "the server said nothing a
// reader can use" are different facts, and only the second may be answered
// with catalog copy. A caller that cannot tell them apart either invents copy
// over a real detail or shows a placeholder as though the server had spoken.
//
// A refusal overlay mode causes is a state, not a fault, but it is TWO
// distinct states, not one: `unsupported_by_sor` is a WRITE the mirror
// cannot serve (mutating a mirrored record — create/log-activity/advance/
// merge/promote/disqualify); `unsupported_in_overlay_mode` is a READ whose
// list/sort/filter dial the mirror does not hold (compose/overlayread.go's
// unsupportedOverlayParam — e.g. tasks' `kind` filter). Collapsing both onto
// one "can't serve this write" string would be false for the read case, so a
// caller holding a translator gets copy naming which kind of refusal
// happened. Callers without a translator — and every other problem code —
// keep the server's own detail verbatim, exactly as before.
function problemDetail(
  problem: unknown,
  t?: (key: MessageKey) => string,
): string | null {
  const code = problemCode(problem);
  if (t && code === "unsupported_by_sor") {
    return t("overlay.refused");
  }
  if (t && code === "unsupported_in_overlay_mode") {
    return t("overlay.filterUnsupported");
  }
  if (isRecord(problem)) {
    // A field present but blank is the same fact as an absent one — it puts no
    // words on the screen — so it falls through to the title, and then to the
    // caller's own copy, instead of rendering an error state with nothing in it.
    const detail = readableField(problem.detail);
    const title = readableField(problem.title);
    return detail ?? title;
  }
  return null;
}

function readableField(value: unknown): string | null {
  return typeof value === "string" && value.trim() !== "" ? value : null;
}

export function problemMessage(
  problem: unknown,
  t?: (key: MessageKey) => string,
): string {
  // A body with no reader text still has to answer something here — this is
  // also the message a ProblemError carries into a stack trace, where an empty
  // string would name nothing. problemMessageOf is the reader's path and
  // answers that same body with catalog copy in the reader's own language.
  return problemDetail(problem, t) ?? "request failed";
}

// A create/update whose server error we want to keep STRUCTURED (not just its
// message) throws this — the raw RFC-7807 body rides along so the form can read
// details.existing_id for the dedupe "view existing" link.
export class ProblemError extends Error {
  readonly problem: unknown;
  constructor(problem: unknown, t?: (key: MessageKey) => string) {
    super(problemMessage(problem, t));
    this.name = "ProblemError";
    this.problem = problem;
  }
}

export function throwProblem(
  problem: unknown,
  t?: (key: MessageKey) => string,
): never {
  throw new ProblemError(problem, t);
}

// Pull the collided record's id + code out of a duplicate (409) problem body,
// or null when absent / not a duplicate / the row isn't caller-visible.
export function problemExistingId(
  problem: unknown,
): { id: string; code: string } | null {
  if (!problem || typeof problem !== "object") return null;
  const record = problem as Record<string, unknown>;
  const code = typeof record.code === "string" ? record.code : null;
  const details =
    record.details && typeof record.details === "object"
      ? (record.details as Record<string, unknown>)
      : null;
  const id =
    details && typeof details.existing_id === "string"
      ? details.existing_id
      : null;
  if (code && id) return { id, code };
  return null;
}

// problemCode pulls the RFC-7807 `code` discriminator out of a problem body,
// or null when absent — so a caller keys on the specific server condition
// (e.g. webhooks_not_configured) rather than on the bare HTTP status, which a
// transient dependency failure can share.
export function problemCode(problem: unknown): string | null {
  if (!problem || typeof problem !== "object") return null;
  const record = problem as Record<string, unknown>;
  return typeof record.code === "string" ? record.code : null;
}

// The same discriminator, read off a query/mutation FAILURE rather than a raw
// body: only a ProblemError carries a server problem, so a network exception
// or a thrown Error never claims a server code it doesn't have.
export function problemCodeOf(error: unknown): string | null {
  return error instanceof ProblemError ? problemCode(error.problem) : null;
}

// The ONE way a caught failure becomes words on a screen, on the same terms as
// problemCodeOf: only a ProblemError carries a server problem, and its RFC-7807
// detail is a cause the server composed for a reader. Everything else — a
// rejected fetch, a bug in a handler, a thrown string — reports in wording
// nobody wrote for a user, and often names our own internals, so it never
// reaches the screen: the reader gets the shared failure line instead.
//
// A ProblemError whose body carried no detail or title is in the same
// position: a 502 from a proxy, or a refusal the server answered with no body
// at all, is a failure nobody phrased for a reader. It reads as the shared
// line too rather than as the developer placeholder problemMessage falls back
// to. A body that DOES carry text always keeps it — the server's own words
// can never be replaced from here.
//
// A surface with better words for its own failure passes them as `fallback`:
// the connector card saying it could not read the connectors beats the generic
// line there. That is catalog copy the caller has already translated, which is
// the only other thing allowed through here.
export function problemMessageOf(
  error: unknown,
  t: (key: MessageKey) => string,
  fallback?: string,
): string {
  const detail =
    error instanceof ProblemError ? problemDetail(error.problem, t) : null;
  return detail ?? fallback ?? t("common.errorNoCause");
}

// The counterpart of that rule: the ONE place a failure the reader is NOT
// shown reaches the console, so a production report of generic copy is still
// diagnosable. A ProblemError is skipped — its detail is already on the screen
// in the reader's own words, and logging it would report one failure twice
// while adding nothing.
//
// Wired ONCE, as the client's mutation-cache sink (app/queryclient.ts,
// FE-PARAM-4), never per mutation and never as a render-time call or an effect
// watching `isError`. The cache observes every mutation the application runs,
// so no screen has to remember this and none can lose it; and because
// react-query runs a mutation to completion independently of whichever
// component started it, the sink fires exactly once per actual failure —
// including the one where the reader leaves mid-flight and the component that
// would have hosted an effect is already unmounted when the request settles.
export function logUnexpectedError(error: unknown): void {
  if (!(error instanceof ProblemError)) {
    console.error(error);
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

// One assertion a 422 makes about one submitted field. The server states the
// condition HERE, not at the top level: every validation problem carries the
// same top-level `code` of "validation_error", so `problemCode` cannot tell
// two refusals apart and only the field + code pair names the rule that fired.
export type FieldProblem = Readonly<{
  field: string;
  code: string;
  message: string;
}>;

// The top-level code every 422 carries — httperr.Validation is the only
// emitter of the per-field `details.errors[]` shape below.
const VALIDATION_PROBLEM_CODE = "validation_error";

// Pull `details.errors[]` out of a validation problem body, dropping any entry
// that is not a complete {field, code, message} — a partial entry cannot be
// matched on, and inventing empty strings for its holes would let a caller key
// on a rule the server never asserted.
//
// The validation code is required, not incidental: `details` is a free-form
// RFC-7807 extension every problem may carry, so reading an `errors` array off
// any body at all would let an unrelated failure that happens to spell one be
// read as the server asserting a rule about a submitted field.
export function problemFieldErrors(problem: unknown): FieldProblem[] {
  if (!isRecord(problem) || problem.code !== VALIDATION_PROBLEM_CODE) return [];
  if (!isRecord(problem.details)) return [];
  const errors = problem.details.errors;
  if (!Array.isArray(errors)) return [];
  const out: FieldProblem[] = [];
  for (const entry of errors) {
    if (!isRecord(entry)) continue;
    const { field, code, message } = entry;
    if (
      typeof field === "string" &&
      typeof code === "string" &&
      typeof message === "string"
    ) {
      out.push({ field, code, message });
    }
  }
  return out;
}

// The same per-field assertions read off a query/mutation FAILURE, on the same
// terms as problemCodeOf: only a ProblemError carries a server problem, so a
// network exception never claims field errors it doesn't have.
export function problemFieldErrorsOf(error: unknown): FieldProblem[] {
  return error instanceof ProblemError ? problemFieldErrors(error.problem) : [];
}

// A 409 whose code names the If-Match precondition failure — the record
// changed under the caller since the form was opened. Distinguished from
// problemExistingId's duplicate-collision code so the edit form can show the
// "reload and retry" copy instead of the raw server detail.
export function isVersionSkew(problem: unknown): boolean {
  if (!problem || typeof problem !== "object") return false;
  const record = problem as Record<string, unknown>;
  return record.code === "version_skew";
}

// A 409 whose code names the "already decided" race — another caller (or
// the same one, replayed) already approved/rejected this staged item before
// this request landed. Distinguished from version_skew: the row itself
// didn't change, the DECISION already happened, so the honest response is
// to drop the stale pending row rather than offer a re-stage retry.
export function isAlreadyDecided(problem: unknown): boolean {
  if (!problem || typeof problem !== "object") return false;
  const record = problem as Record<string, unknown>;
  return record.code === "already_decided";
}

// A 409 whose code names the consent suppression gate: the send's recipients
// have no active `granted` person_consent for the purpose it falls under
// (default-deny per purpose, A22/ADR-0011). Distinguished from RBAC (403) and
// validation (422) so the composer can point the user at the consent surface
// rather than showing a raw server detail.
export function isConsentNotGranted(problem: unknown): boolean {
  if (!problem || typeof problem !== "object") return false;
  const record = problem as Record<string, unknown>;
  return record.code === "consent_not_granted";
}

// The cold-start / enrichment field vocabulary (compose/enrichextract.go)
// rendered as human labels; an unmapped field falls back to its key with the
// underscores spaced out — readable, never raw snake_case.
const COLD_FIELD_LABELS: Record<string, MessageKey> = {
  // display_name is the company form's own field, not one a read-back can
  // ground — it shares this map so both surfaces name it the same way.
  display_name: "ob.field.display_name",
  offer_summary: "ob.field.offer_summary",
  icp: "ob.field.icp",
  buying_center: "ob.field.buying_center",
  value_proposition: "ob.field.value_proposition",
  usp: "ob.field.usp",
  customer_pains: "ob.field.customer_pains",
  desired_outcomes: "ob.field.desired_outcomes",
  buying_intents: "ob.field.buying_intents",
  common_objections: "ob.field.common_objections",
  sales_motion: "ob.field.sales_motion",
  legal_name: "ob.field.legal_name",
  registered_address: "ob.field.registered_address",
  register_vat: "ob.field.register_vat",
  industry: "ob.field.industry",
  history: "ob.field.history",
};

export function coldFieldLabel(
  field: string,
  t: (key: MessageKey) => string,
): string {
  const key = COLD_FIELD_LABELS[field];
  return key ? t(key) : field.replace(/_/g, " ");
}

// For pure (non-rendering) callers that carry the label key until a component
// translates it — same map, same fallback contract as coldFieldLabel.
export function coldFieldLabelKey(field: string): MessageKey | undefined {
  return COLD_FIELD_LABELS[field];
}

/**
 * What kind of page the crawl was looking at, in the reader's words. The enum
 * is closed and both read shapes carry it (`SiteReadPage.kind`, required, and
 * `CompanySiteReadPage.kind`, optional), so the vocabulary lives here once: a
 * company page, a deep-read report and the onboarding dossier must not name the
 * same page three different ways.
 */
const SITE_READ_KIND_LABELS: Record<
  components["schemas"]["SiteReadPage"]["kind"],
  MessageKey
> = {
  home: "deepread.kindHome",
  impressum: "deepread.kindImpressum",
  about: "deepread.kindAbout",
  team: "deepread.kindTeam",
  services: "deepread.kindServices",
  products: "deepread.kindProducts",
  contact: "deepread.kindContact",
  other: "deepread.kindOther",
};

export function siteReadKindLabel(
  kind: components["schemas"]["SiteReadPage"]["kind"],
  t: (key: MessageKey) => string,
): string {
  return t(SITE_READ_KIND_LABELS[kind]);
}

/**
 * The same vocabulary for a caller that already has a label of its own and only
 * wants a better one. An absent kind and "other" both answer undefined: they say
 * nothing the caller's own wording does not, and "Other" in place of a real name
 * reads as information when it is not.
 */
// The same map seen as a plain lookup, for callers whose kind is only a string
// at compile time. Widening an assignment costs nothing and keeps the map above
// exhaustive over the enum — a cast at the call site would give up both.
const KIND_LABELS_BY_NAME: Readonly<Record<string, MessageKey>> =
  SITE_READ_KIND_LABELS;

export function namedSiteReadKind(
  kind: string | null | undefined,
): MessageKey | undefined {
  if (!kind || kind === "other") {
    return undefined;
  }
  return KIND_LABELS_BY_NAME[kind];
}
