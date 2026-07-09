import { type UseQueryResult, useQuery } from "@tanstack/react-query";
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

// Automation (and pipeline) config is admin/ops-owned in the seeded role
// policies (decisions/0006) — manager and rep hold read-only grants. This
// mirror gates AFFORDANCES only (UX honesty: no buttons that can only 403);
// the server's auth.Require gate stays the authority on every mutation.
export function canConfigureAutomations(
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
