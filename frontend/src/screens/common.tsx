import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { clearPendingAuthorize } from "../app/pendingauthorize";
import { Button, EmptyState, PendingBody } from "../design-system/atoms";
import type { Provenance } from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import "./common.css";

// Shared screen plumbing: honest loading / error / empty states (§3a screen-
// state matrix), the captured_by → provenance mapping every list reuses, and
// the ONE /me query the auth gate and every role-aware surface share.

// Authentication and availability are different product states: a failed
// session probe is typed so the auth boundary can render login (401), the
// connection-problem screen (network/5xx), or the installation-unavailable
// screen (503 — pre-bootstrap or a violated singleton invariant) instead of
// collapsing every error into login.
export type AuthProbeKind =
  | "unauthorized"
  | "connection"
  | "installation"
  // The account is authenticated but still using a password its operator
  // chose, and the server refuses every route but the one that replaces it.
  // A distinct kind because the remedy is distinct: sending this to the login
  // screen would loop — the credentials are correct, and using them again
  // lands in the same refusal.
  | "must-change-password";

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
function probeKindFor(status: number, code?: string): AuthProbeKind {
  if (status === 503) return "installation";
  if (status >= 500) return "connection";
  if (status === 403 && code === "password_change_required") {
    return "must-change-password";
  }
  return "unauthorized";
}

// codeInProblemBody reads the machine code straight off a problem BODY. The
// exported problemCodeOf below takes a thrown ProblemError instead; here the
// probe still holds the decoded body, and the code is what separates a refusal
// the caller can act on from one they cannot.
function codeInProblemBody(error: unknown): string | undefined {
  if (typeof error === "object" && error !== null && "code" in error) {
    const { code } = error as { code?: unknown };
    return typeof code === "string" ? code : undefined;
  }
  return undefined;
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
          probeKindFor(response.status, codeInProblemBody(error)),
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

/**
 * What a record's timeline zone shows when it has no entries to show YET.
 *
 * The zone takes an array, and an empty array is a claim: "nothing has happened
 * to this record". While the read is in flight that claim is simply false, and
 * the entries then arrive underneath a heading the reader has already accepted
 * as complete — which both pops and pushes the rest of the column down.
 *
 * `undefined` for the ordinary case, because the zone's own renderer is right
 * once there is something to render. Overlay mode wins over the wait: a
 * capability the mirror will never answer is not a wait at all.
 */
export function timelineZoneNotice(
  state: Readonly<{ overlay: boolean; pending: boolean }>,
  t: ReturnType<typeof useT>,
): ReactNode {
  if (state.overlay) {
    return <OverlayUnavailable />;
  }
  if (state.pending) {
    return <PendingBody label={t("record.timelineLoading")} lines={5} />;
  }
  return undefined;
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
  pendingLabel,
  pendingLines,
  children,
}: Readonly<{
  query: Readonly<{
    isPending: boolean;
    isError: boolean;
    error: unknown;
    refetch: () => unknown;
  }>;
  // What this particular read is fetching, and how much room it will need.
  // Both optional because most screens are answering the generic question and
  // the generic answer is honest for them; a screen whose body is a table or a
  // timeline says so, or its content lands in a third of the space the
  // placeholder held and pushes the page down as it arrives.
  pendingLabel?: string;
  pendingLines?: number;
  children: ReactNode;
}>) {
  const t = useT();
  if (query.isPending) {
    return (
      <PendingBody
        label={pendingLabel ?? t("common.loading")}
        lines={pendingLines}
      />
    );
  }
  if (query.isError) {
    return (
      <EmptyState>
        {/* role="alert" — the assertive live region — because this subtree
            MOUNTS carrying its message: a polite region inserted together with
            its text is frequently never announced, while an assertive one is
            announced on insertion, which is why confirmmodal.tsx marks its
            mutation failure the same way. Headline and cause share ONE region so
            the reader hears a whole failure rather than two fragments; Retry
            stays outside it, since a live region reads out its contents and the
            button is something to reach, not something to hear. */}
        <div role="alert">
          <p>{t("common.error")}</p>
          <p className="t-mono" style={{ marginTop: "var(--space-2)" }}>
            {problemMessageOf(query.error, t)}
          </p>
        </div>
        <Button
          small
          onClick={() => query.refetch()}
          style={{ marginTop: "var(--space-3)" }}
        >
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
      className="load-more"
      disabled={query.isFetchingNextPage}
      onClick={() => query.fetchNextPage()}
    >
      {t("list.loadMore")}
    </Button>
  );
}

export function QueryGate<Data>({
  query,
  empty,
  pendingLabel,
  pendingLines,
  children,
}: Readonly<{
  query: QueryLike<Data>;
  empty?: (data: Data) => boolean;
  // Forwarded to QueryStates: the gate adds the empty check, not a second
  // pending state, so there is one place a screen names its wait.
  pendingLabel?: string;
  pendingLines?: number;
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
  return (
    <QueryStates
      query={query}
      pendingLabel={pendingLabel}
      pendingLines={pendingLines}
    >
      {success}
    </QueryStates>
  );
}

// captured_by is server-stamped and carries one of the four kinds the contract
// enumerates: "human:<uuid> | agent:<id> | connector:<name> | system:<id>".
// Each reads as the kind of actor it IS — a connector as a connector, a
// background job as the system, an agent as an agent — because telling those
// apart is the whole reason a provenance tag is on the screen. A kind this app
// does not know is `unknown` rather than any of them: naming the wrong kind of
// actor is a claim about who to ask, and "not recorded" is the only thing still
// true when the string is unreadable.
//
// A human is only "you" when the id is the reader's. Without a viewer id the
// human branch stays unnamed rather than guessing: a caller that cannot say
// who is reading cannot claim the reader typed it. An absent captured_by is
// `unknown` and says so — it used to render as the reader's own typing, which
// is the one attribution nobody can check.
//
// The kind is the FIRST segment and everything after it belongs to that kind,
// which is why the leading colon is found by index: `split(":", 2)` truncates at
// the limit instead of putting the remainder in the last element, so
// `connector:ext:dispact-connector:<uuid>` parses to `["connector", "ext"]` and
// every extension unit's tag reads the literal word "ext".
//
// NOTHING opaque is handed on for a tag to print. A uuid names nothing a reader
// can act on, so it is dropped rather than rendered: a passport id leaves the
// agent tag unnamed, and a connector's member id is dropped a segment at a time.
// The human remainder is the exception and is kept WHOLE, because it is compared
// against the reader's own id and never printed — the tag resolves it through
// the caller's `renderUser` or says a person entered it — and an id truncated to
// its first segment is how a colleague's entry would come to read "typed by
// you".
export function provenanceOf(
  capturedBy: string | undefined,
  viewerUserId?: string,
): Provenance {
  if (!capturedBy) {
    return { kind: "unknown" };
  }
  const separator = capturedBy.indexOf(":");
  const source = separator > 0 ? capturedBy.slice(0, separator) : capturedBy;
  const rest = separator > 0 ? capturedBy.slice(separator + 1) : "";
  if (source === "human") {
    const userId = rest || undefined;
    return {
      kind: "human",
      self: Boolean(viewerUserId) && userId === viewerUserId,
      userId,
    };
  }
  if (source === "connector") {
    return { kind: "connector", connector: connectorLabel(rest) || source };
  }
  if (source === "agent") {
    // A passport call stamps `agent:<passport_id>`, and a passport id is a uuid
    // with no name behind it on this side: the design system holds no record
    // lookups, so there is nothing here to resolve it to. The tag then says only
    // what the wire says — an agent produced this — instead of printing an
    // identifier at a reader. A named tool (`agent:enrich`) keeps its name.
    const named = rest === "" || OPAQUE_ID.test(rest) ? undefined : rest;
    return { kind: "agent", agent: named };
  }
  if (source === "system") {
    // The installation's own processing: a scheduled sweep, a backfill, an
    // anonymous public endpoint. Its id is a job name, and a bare `system` with
    // no id at all is a job that never said which one it was.
    return { kind: "system", job: rest || undefined };
  }
  return { kind: "unknown" };
}

/** A uuid: an identifier with no name in it, so no tag may print it. */
const OPAQUE_ID =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** The marker that says the rest of a connector principal names an extension. */
const EXTENSION_MARKER = "ext";

/**
 * Which connector a `connector:` principal names, from what follows the kind.
 *
 * Two grammars end up in the same column: `<system>:<user-uuid>` for a built-in
 * connector and `ext:<unit>[:<user-uuid>]` for an extension unit. Either way the
 * connector is ONE segment and the rest is the member whose grant it ran under,
 * so the label is taken a segment at a time — a uuid tail in a provenance tag
 * names nothing a reader can act on, and the unit is what tells
 * `dispact-connector` from `zalo-oa`.
 *
 * The marker on its own names no unit, so it labels nothing: the empty answer
 * sends the caller back to the kind, and the tag reads "connector" rather than
 * the word "ext" — which is a segment of the grammar, not a thing that ran.
 *
 * The unit is the raw directory name: the transport directory resolves the
 * provider-id grammar (`ext:<unit>:<system>`), which is a different namespace
 * from this one, so there is no nicer label to look up here yet.
 */
function connectorLabel(rest: string): string {
  const [head, ...tail] = rest.split(":");
  return head === EXTENSION_MARKER ? (tail[0] ?? "") : head;
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
//
// A refusal is the OPPOSITE case: `permission_denied` is one code over two
// authorities — an object-RBAC denial (this role does not admit the action on
// this kind of record) and a row-authority denial (the record is on screen
// through a read share) — and nothing on the wire says which of the two
// happened. So the catalog copy replaces the server's detail and deliberately
// does not guess: copy that named the wrong authority would be worse than the
// bare sentinel.
//
// Replacing it loses nothing a reader wanted, and that is the part worth
// knowing before anyone tries to "keep the more specific answer". `httperr`
// builds a refusal's detail from `err.Error()`, and every producer of this
// sentinel wraps it with INTERNALS: `auth.Require` sends the RBAC object and
// verb ("person.update: permission denied"), the admission gate sends its own
// spec name and resolver state. None of that is copy, and showing it would
// leak the shape of the authority model to a client. There is no path on which
// the server sends a sentence written for a reader here.
//
// `seat_tier_insufficient` is the LICENSING ceiling, and it is a different
// refusal from the two above: it is decided before any role is consulted, so a
// reader whose role admits the action is refused anyway. Its server detail is
// the bare sentinel ("seat tier insufficient"), which names a concept no
// reader has met and offers nothing to do about it, so the catalog copy
// replaces it and points at the one person who can lift the ceiling. It names
// the SEAT rather than "your seat": the same code answers a read seat's own
// mutation, an agent passport acting for one, and a grant that would give a
// read seat write access. A surface that knows WHOSE seat it is (share.tsx
// knows it is the recipient's) says so in its own words before it gets here.
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
  if (t && code === "permission_denied") {
    return t("common.permissionDenied");
  }
  if (t && code === "seat_tier_insufficient") {
    return t("common.seatReadOnly");
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

// The same question, read off a query/mutation FAILURE rather than a raw body,
// on the same terms as problemCodeOf: only a ProblemError carries a server
// problem, so a dropped connection never reads as a concurrent edit. Two call
// sites had spelled the instanceof-then-unwrap step by hand, which is one
// forgotten guard away from telling a reader somebody else changed the record
// when the request never reached the server at all.
export function isVersionSkewOf(error: unknown): boolean {
  return error instanceof ProblemError && isVersionSkew(error.problem);
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

// The account's finance summary. It lives here rather than beside the finance
// card because the KPI row reads the SAME figure: one query key, so the two
// readings on a page agree and the second costs no request.
export function useFinanceSummary(orgId: string) {
  return useQuery<components["schemas"]["OrganizationFinanceSummary"]>({
    queryKey: ["finance-summary", orgId],
    queryFn: async () => {
      const { data, error } = await api.GET(
        "/organizations/{id}/finance-summary",
        { params: { path: { id: orgId } } },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });
}
