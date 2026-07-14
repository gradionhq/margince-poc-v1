import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { problemMessage, type QueryLike } from "./common";

// The approvals query-hook family, split out of inbox.tsx (which had grown
// past ~690 lines). The Decided view is an Option-1 client-side partition:
// the listApprovals contract only accepts status ∈ {pending, approved,
// rejected} (crm.yaml enum), and the server computes expiry LAZILY at read
// time (approvals/inbox.go effectiveStatus) — a pending row past its expiry
// is STORED status=pending but WIRED back as status="expired". So there is no
// status=expired query; expired items ride in on the status=pending response
// and are re-partitioned here. The merged hooks return a plain QueryLike (the
// narrow surface QueryGate reads) — no `as unknown as UseQueryResult` lie.

export type Approval = components["schemas"]["Approval"];
export type ApprovalStatus = "pending" | "approved" | "rejected";
export type ApprovalPage = { data: Approval[] };

// A single page tops out at 50 (the server's page cap) — a 51st pending
// approval, or decided history past 50 rows, must still be reachable. The
// pending/decided partition below (and the expired salvage inside it) needs
// the FULL set to sort/filter correctly, not a manually-paged slice, so this
// walks every page via the API's opaque `next_cursor` and merges them into
// one collection before the query resolves.
async function fetchAllApprovals(status: ApprovalStatus): Promise<Approval[]> {
  const all: Approval[] = [];
  let cursor: string | null | undefined;
  do {
    const { data, error } = await api.GET("/approvals", {
      params: { query: { status, limit: 50, cursor: cursor ?? undefined } },
    });
    if (error) {
      throw new Error(problemMessage(error));
    }
    all.push(...data.data);
    cursor = data.page?.has_more ? (data.page.next_cursor ?? null) : null;
  } while (cursor);
  return all;
}

export function useApprovals(status: ApprovalStatus, enabled = true) {
  return useQuery({
    queryKey: ["approvals", status],
    enabled,
    queryFn: async (): Promise<ApprovalPage> => ({
      data: await fetchAllApprovals(status),
    }),
  });
}

// The Pending tab: status=pending, but a lazily-expired row (wire
// status="expired") is DROPPED — it is not actionable, it belongs in
// Decided (AC-7: expired never listed as pending).
export function usePendingApprovals(): QueryLike<ApprovalPage> {
  const pending = useApprovals("pending");
  return {
    isPending: pending.isPending,
    isError: pending.isError,
    error: pending.error,
    data: pending.data
      ? { data: pending.data.data.filter((a) => a.status !== "expired") }
      : undefined,
    refetch: pending.refetch,
  };
}

// decided_at is the honest sort key (when the human actually acted); an
// expired item auto-transitioned with no decided_at, so it falls back to
// expires_at — never created_at, which would misrepresent an old-but-just-
// expired item as freshly decided.
function decidedRank(approval: Approval): number {
  const at = approval.decided_at ?? approval.expires_at ?? approval.created_at;
  return at ? new Date(at).getTime() : 0;
}

// The Decided tab: approved + rejected from their own typed status queries;
// EXPIRED items (unqueryable) salvaged from the status=pending response.
// Merge, newest decision first. The two decided-only fetches are gated so
// they don't fire on the default Pending view; the pending query is always-on
// (shared cache key with usePendingApprovals) and supplies the expired rows.
export function useDecidedApprovals(enabled = true): QueryLike<ApprovalPage> {
  const approved = useApprovals("approved", enabled);
  const rejected = useApprovals("rejected", enabled);
  const pending = useApprovals("pending");
  const all = [approved, rejected, pending];
  const isPending = all.some((query) => query.isPending);
  const isError = all.some((query) => query.isError);
  const firstError = all.find((query) => query.isError)?.error ?? null;
  const expired = (pending.data?.data ?? []).filter(
    (a) => a.status === "expired",
  );
  const data: ApprovalPage | undefined =
    isPending || isError
      ? undefined
      : {
          data: [
            ...(approved.data?.data ?? []),
            ...(rejected.data?.data ?? []),
            ...expired,
          ].sort((a, b) => decidedRank(b) - decidedRank(a)),
        };
  const refetch = () => {
    approved.refetch();
    rejected.refetch();
    pending.refetch();
  };
  return { isPending, isError, error: firstError, data, refetch };
}
