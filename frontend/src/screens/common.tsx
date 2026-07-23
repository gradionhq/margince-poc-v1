import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api } from "../api/client";
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
      if (error) throw new Error(problemMessage(error));
    },
    onSuccess: async () => {
      // The next 401 at the boundary is this deliberate exit, not an
      // expired session — the login screen greets it accordingly.
      authExitNotice = "signed-out";
      queryClient.removeQueries({
        predicate: (query) => query.queryKey[0] !== "me",
      });
      await queryClient.resetQueries({ queryKey: ["me"] });
    },
  });
}

// Automation (and pipeline) config is admin/ops-owned in the seeded role
// policies — manager and rep hold read-only grants. This
// mirror gates AFFORDANCES only (UX honesty: no buttons that can only 403);
// the server's auth.Require gate stays the authority on every mutation.
export function canConfigureAutomations(
  roles: readonly string[] | undefined,
): boolean {
  return (roles ?? []).some((role) => role === "admin" || role === "ops");
}

// custom_field CRUD is admin/ops-owned in the seeded role matrix
// (identity/internal/policy/policy.go: custom_field grant is crud for
// admin/ops, read-only for manager/rep/read_only). The server enforces it;
// this predicate keeps the builder and lifecycle controls honestly disabled
// for a role whose call could only 403.
export function canManageCustomFields(
  roles: readonly string[] | undefined,
): boolean {
  return (roles ?? []).some((role) => role === "admin" || role === "ops");
}

// fx_rate + ai_model_rate are admin/ops-owned config surfaces (the seeded
// role matrix grants CRUD to admin/ops, nothing to other roles). The server
// enforces it; this predicate keeps the rate-editor write controls honestly
// disabled for a role whose call could only 403.
export function canManageRates(roles: readonly string[] | undefined): boolean {
  return (roles ?? []).some((role) => role === "admin" || role === "ops");
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
          {query.error instanceof Error ? query.error.message : null}
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
export function provenanceOf(capturedBy: string | undefined): Provenance {
  if (!capturedBy || capturedBy.startsWith("human:")) {
    return { kind: "human" };
  }
  const [source, name] = capturedBy.split(":", 2);
  const label = name ?? source;
  if (source === "connector") {
    return { kind: "connector", connector: label };
  }
  return { kind: "agent", agent: label };
}

// RFC 7807 bodies carry the honest detail; surface it instead of a generic
// failure so the error state names its cause.
export function problemMessage(problem: unknown): string {
  if (problem && typeof problem === "object") {
    const record = problem as Record<string, unknown>;
    if (typeof record.detail === "string") {
      return record.detail;
    }
    if (typeof record.title === "string") {
      return record.title;
    }
  }
  return "request failed";
}

// A create/update whose server error we want to keep STRUCTURED (not just its
// message) throws this — the raw RFC-7807 body rides along so the form can read
// details.existing_id for the dedupe "view existing" link.
export class ProblemError extends Error {
  readonly problem: unknown;
  constructor(problem: unknown) {
    super(problemMessage(problem));
    this.name = "ProblemError";
    this.problem = problem;
  }
}

export function throwProblem(problem: unknown): never {
  throw new ProblemError(problem);
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
