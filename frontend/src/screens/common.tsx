import {
  type UseQueryResult,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api, workspaceSlug } from "../api/client";
import { Button, EmptyState, Skeleton } from "../design-system/atoms";
import type { Provenance } from "../design-system/trust";
import { useT } from "../i18n";
import type { MessageKey } from "../i18n/en";

// Shared screen plumbing: honest loading / error / empty states (§3a screen-
// state matrix), the captured_by → provenance mapping every list reuses, and
// the ONE /me query the auth gate and every role-aware surface share.

// The session principal (GET /v1/me): identity + effective role keys. One
// spelling, one ["me"] cache entry — the App auth gate, the settings identity
// card, and role-aware affordances all read the same probe. Without a
// workspace slug there is no tenant to ask, so the hook fails fast instead of
// guaranteeing a 401 round-trip.
export function useMe() {
  return useQuery({
    queryKey: ["me"],
    retry: false,
    queryFn: async () => {
      if (!workspaceSlug()) {
        throw new Error("no workspace");
      }
      const { data, error } = await api.GET("/me");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

// AS-1: sign out. Clears ALL cached tenant data on success, then the ["me"]
// probe re-runs → 401 → AuthGate renders the login screen. No manual
// redirect — the gate owns it.
export function useLogout() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/auth/logout");
      if (error) throw new Error(problemMessage(error));
    },
    onSuccess: () => queryClient.clear(),
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

export function QueryGate<Data>({
  query,
  empty,
  children,
}: Readonly<{
  query: UseQueryResult<Data>;
  empty?: (data: Data) => boolean;
  children: (data: Data) => ReactNode;
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
  if (empty?.(query.data)) {
    return <EmptyState>{t("common.empty")}</EmptyState>;
  }
  return <>{children(query.data)}</>;
}

// captured_by is server-stamped "human:<uuid> | agent:<id> | connector:<name>".
export function provenanceOf(capturedBy: string | undefined): Provenance {
  if (!capturedBy || capturedBy.startsWith("human:")) {
    return { kind: "human" };
  }
  const [source, name] = capturedBy.split(":", 2);
  return { kind: "agent", agent: name ? `${source}:${name}` : source };
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

// The cold-start / enrichment field vocabulary (compose/enrichextract.go)
// rendered as human labels; an unmapped field falls back to its key with the
// underscores spaced out — readable, never raw snake_case.
const COLD_FIELD_LABELS: Record<string, MessageKey> = {
  icp: "ob.field.icp",
  buying_center: "ob.field.buying_center",
  value_proposition: "ob.field.value_proposition",
  usp: "ob.field.usp",
  buying_intents: "ob.field.buying_intents",
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
