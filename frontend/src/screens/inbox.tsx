import {
  type UseQueryResult,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Modal,
  SectionHeader,
  SegmentedControl,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import {
  AutonomyDot,
  type ConfidenceLevel,
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { formatDateTime } from "../format/format";
import { formatCountdown, useNow } from "../format/now";
import { useLocale, useT } from "../i18n";
import {
  isAlreadyDecided,
  isVersionSkew,
  ProblemError,
  problemMessage,
  provenanceOf,
  QueryGate,
  throwProblem,
} from "./common";

// The approval inbox (B-EP09.12a) — the CANONICAL 🟡 surface. Per-row
// approve/reject plus the inline staged-draft editor: string fields of the
// proposed_change are editable and go up as edited_payload, which the server
// RE-ADMITS from scratch (re-tiered, re-RBAC'd, new diff_hash — ADR-0036);
// an edit can never silently escalate the effect. A 409 version-skew comes
// back as an honest "the world changed, re-stage" row error; a 409
// already-decided drops the stale row instead of offering a re-stage retry
// (Task 10, AC-1..7).

type Approval = components["schemas"]["Approval"];
// The listApprovals contract only accepts these three (crm.yaml: enum
// [pending, approved, rejected]); "expired" is NOT a queryable filter. The
// server computes expiry LAZILY at read time (approvals/inbox.go
// effectiveStatus) — a pending row past its expiry is stored status=pending
// but WIRED back as status="expired". So there is no status=expired query to
// issue; expired items ride in on the status=pending response and we
// re-partition them client-side (Option-1 correction, see the report).
type ApprovalStatus = "pending" | "approved" | "rejected";
type ApprovalPage = { data: Approval[] };

export function confidenceLevel(
  confidence: number | null | undefined,
): ConfidenceLevel | null {
  if (confidence == null) {
    return null;
  }
  if (confidence >= 0.8) {
    return "high";
  }
  if (confidence >= 0.5) {
    return "med";
  }
  return "low";
}

export function useApprovals(status: ApprovalStatus) {
  return useQuery({
    queryKey: ["approvals", status],
    queryFn: async () => {
      const { data, error } = await api.GET("/approvals", {
        params: { query: { status, limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

// The Pending tab: status=pending, but a lazily-expired row (wire
// status="expired") is DROPPED — it is not actionable, it belongs in
// Decided (AC-7: expired never listed as pending). Shaped to satisfy
// QueryGate's UseQueryResult surface without re-rolling its chrome.
export function usePendingApprovals(): UseQueryResult<ApprovalPage> {
  const pending = useApprovals("pending");
  const data: ApprovalPage | undefined = pending.data
    ? { data: pending.data.data.filter((a) => a.status !== "expired") }
    : undefined;
  return { ...pending, data } as unknown as UseQueryResult<ApprovalPage>;
}

// decided_at is the honest sort key (when the human actually acted); an
// expired item auto-transitioned with no decided_at, so it falls back to
// expires_at — never created_at, which would misrepresent an old-but-just-
// expired item as freshly decided.
function decidedRank(approval: Approval): number {
  const at = approval.decided_at ?? approval.expires_at ?? approval.created_at;
  return at ? new Date(at).getTime() : 0;
}

// The Decided tab (Option-1 correction): approved + rejected come from their
// own typed status queries; EXPIRED items cannot be queried (no such filter)
// so they are salvaged from the status=pending response — the lazily-expired
// rows the server wires back as status="expired". Merge, newest decision
// first. Shaped to satisfy QueryGate's UseQueryResult surface without
// re-rolling the loading/error/empty chrome for a merge of three queries.
export function useDecidedApprovals(): UseQueryResult<ApprovalPage> {
  const approved = useApprovals("approved");
  const rejected = useApprovals("rejected");
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
    return Promise.resolve();
  };
  return {
    isPending,
    isError,
    error: firstError,
    data,
    refetch,
  } as unknown as UseQueryResult<ApprovalPage>;
}

function editableStrings(change: Record<string, unknown>): [string, string][] {
  return Object.entries(change).filter((entry): entry is [string, string] => {
    return typeof entry[1] === "string";
  });
}

const STATUS_BADGE_KEY: Record<
  string,
  "inbox.status.approved" | "inbox.status.rejected" | "inbox.status.expired"
> = {
  approved: "inbox.status.approved",
  rejected: "inbox.status.rejected",
  expired: "inbox.status.expired",
};

const STATUS_BADGE_TONE: Record<string, "success" | "danger" | "warn"> = {
  approved: "success",
  rejected: "danger",
  expired: "warn",
};

// AC-2: the row's "view everything" affordance — the full proposed_change
// (key→value), evidence, target_version, proposed_by/on_behalf_of and
// timestamps the summary/evidence-chip row necessarily elides.
function ApprovalDetailModal({
  approvalId,
  open,
  onClose,
}: Readonly<{ approvalId: string; open: boolean; onClose: () => void }>) {
  const t = useT();
  const { locale } = useLocale();
  const headingId = useId();
  const detail = useQuery({
    queryKey: ["approval", approvalId],
    enabled: open,
    queryFn: async () => {
      const { data, error } = await api.GET("/approvals/{id}", {
        params: { path: { id: approvalId } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <h2 id={headingId} className="t-h2" style={{ marginBottom: 12 }}>
        {t("inbox.detail")}
      </h2>
      {open && (
        <QueryGate query={detail}>
          {(approval) => {
            const change = (approval.proposed_change ?? {}) as Record<
              string,
              unknown
            >;
            // Wire field identifiers (contract shape), not translatable prose
            // — rendered raw, exactly like the proposed_change keys below.
            const meta: [string, string][] = [
              ["target_version", String(approval.target_version ?? "—")],
              ["proposed_by", approval.proposed_by],
            ];
            if (approval.on_behalf_of) {
              meta.push(["on_behalf_of", approval.on_behalf_of]);
            }
            meta.push([
              "created_at",
              formatDateTime(approval.created_at, locale, "Europe/Berlin"),
            ]);
            if (approval.decided_at) {
              meta.push([
                "decided_at",
                formatDateTime(approval.decided_at, locale, "Europe/Berlin"),
              ]);
            }
            return (
              <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
                {Object.entries(change).map(([key, value]) => (
                  <div className="field" key={key}>
                    <span className="t-label">{key}</span>
                    <p className="t-mono">
                      {typeof value === "string"
                        ? value
                        : JSON.stringify(value)}
                    </p>
                  </div>
                ))}
                {approval.evidence?.map((item) =>
                  item.evidence_snippet ? (
                    <EvidenceChip
                      key={`${item.source_id}-${item.evidence_snippet.slice(0, 12)}`}
                      evidence={{
                        snippet: item.evidence_snippet,
                        source: item.source_type ?? "",
                      }}
                    />
                  ) : null,
                )}
                {meta.map(([key, value]) => (
                  <div className="field" key={key}>
                    <span className="t-label">{key}</span>
                    <p className="t-mono">{value}</p>
                  </div>
                ))}
              </div>
            );
          }}
        </QueryGate>
      )}
    </Modal>
  );
}

// The header chip: a status badge in the read-only Decided view, else the
// live countdown that flips to the Expired badge at/after expires_at.
function RowStatusChip({
  decided,
  status,
  expiresAtMs,
  isExpired,
  now,
}: Readonly<{
  decided: boolean;
  status: string;
  expiresAtMs: number | null;
  isExpired: boolean;
  now: number;
}>) {
  const t = useT();
  if (decided) {
    return (
      <Badge tone={STATUS_BADGE_TONE[status]}>
        {t(STATUS_BADGE_KEY[status] ?? "inbox.status.expired")}
      </Badge>
    );
  }
  if (expiresAtMs == null) {
    return null;
  }
  if (isExpired) {
    return <Badge tone="danger">{t("inbox.expired")}</Badge>;
  }
  return (
    <span className="t-small">
      {t("inbox.expiresIn", {
        countdown: formatCountdown(expiresAtMs - now, t),
      })}
    </span>
  );
}

// The token / error / skew / already-decided outcomes of a decide mutation —
// each an honest, distinct state (AC-4/5/6).
function DecideOutcome({
  token,
  decide,
  skew,
  alreadyDecided,
  onReRead,
}: Readonly<{
  token: string | null;
  decide: { isError: boolean; error: unknown };
  skew: boolean;
  alreadyDecided: boolean;
  onReRead: () => void;
}>) {
  const t = useT();
  const generic = decide.isError && !skew && !alreadyDecided;
  return (
    <>
      {token && (
        <div className="card card-inset" style={{ marginTop: 8 }}>
          <p className="t-label">{t("inbox.tokenOnce")}</p>
          <p className="t-mono" style={{ wordBreak: "break-all" }}>
            {token}
          </p>
          <Button small onClick={() => navigator.clipboard?.writeText(token)}>
            {t("inbox.copy")}
          </Button>
        </div>
      )}
      {generic && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 8 }}
        >
          {decide.error instanceof Error ? decide.error.message : null}
        </p>
      )}
      {skew && (
        <div style={{ marginTop: 8 }}>
          <p className="t-caption" style={{ color: "var(--danger)" }}>
            {t("inbox.versionSkew")}
          </p>
          <Button small onClick={onReRead}>
            {t("inbox.reRead")}
          </Button>
        </div>
      )}
      {alreadyDecided && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 8 }}
        >
          {t("inbox.alreadyDecided")}
        </p>
      )}
    </>
  );
}

export function ApprovalRow({
  approval,
  decided,
}: Readonly<{ approval: Approval; decided?: boolean }>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState("");
  const [detailOpen, setDetailOpen] = useState(false);
  const [token, setToken] = useState<string | null>(null);
  const now = useNow(1000);

  const decide = useMutation({
    mutationFn: async (input: {
      verdict: "approve" | "reject";
      editedPayload?: Record<string, unknown>;
      reason?: string;
    }) => {
      const path =
        input.verdict === "approve"
          ? "/approvals/{id}/approve"
          : "/approvals/{id}/reject";
      const { data, error } = await api.POST(path, {
        params: { path: { id: approval.id } },
        ...(input.verdict === "approve" && input.editedPayload
          ? { body: { edited_payload: input.editedPayload } }
          : {}),
        ...(input.verdict === "reject"
          ? { body: { reason: input.reason ?? "" } }
          : {}),
      });
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (data) => {
      queryClient.invalidateQueries({ queryKey: ["approvals"] });
      if (data?.approval_token) {
        setToken(data.approval_token);
      }
    },
    onError: (error) => {
      const problem = error instanceof ProblemError ? error.problem : null;
      if (problem && isAlreadyDecided(problem)) {
        queryClient.invalidateQueries({ queryKey: ["approvals", "pending"] });
      }
    },
  });

  const change = (approval.proposed_change ?? {}) as Record<string, unknown>;
  const strings = editableStrings(change);
  const level = confidenceLevel(approval.confidence);

  const problem =
    decide.error instanceof ProblemError ? decide.error.problem : null;
  const skew = problem ? isVersionSkew(problem) : false;
  const alreadyDecided = problem ? isAlreadyDecided(problem) : false;

  const startEdit = () => {
    setDraft(Object.fromEntries(strings));
    setEditing(true);
  };

  const approveEdited = () => {
    decide.mutate({
      verdict: "approve",
      editedPayload: { ...change, ...draft },
    });
    setEditing(false);
  };

  const confirmReject = () => {
    decide.mutate({ verdict: "reject", reason });
    setRejecting(false);
    setReason("");
  };

  const reRead = () => {
    queryClient.invalidateQueries({ queryKey: ["approvals", "pending"] });
    queryClient.invalidateQueries({ queryKey: ["approval", approval.id] });
    decide.reset();
  };

  const expiresAtMs = approval.expires_at
    ? new Date(approval.expires_at).getTime()
    : null;
  // Expired either because the wire already stamped it (lazy server expiry)
  // or the live clock crossed expires_at since this row was fetched.
  const isExpired =
    approval.status === "expired" ||
    (expiresAtMs != null && now >= expiresAtMs);

  return (
    <article
      className="staging-card"
      style={{ marginBottom: 10 }}
      data-approval={approval.id}
    >
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: 8,
          flexWrap: "wrap",
        }}
      >
        {!decided && <AutonomyDot tier="confirm" />}
        <span className="t-label">{approval.kind}</span>
        <ProvenanceTag provenance={provenanceOf(approval.proposed_by)} />
        {level && <ConfidenceMeter level={level} />}
        <RowStatusChip
          decided={!!decided}
          status={approval.status}
          expiresAtMs={expiresAtMs}
          isExpired={isExpired}
          now={now}
        />
        <Button small onClick={() => setDetailOpen(true)}>
          {t("inbox.detail")}
        </Button>
      </div>
      {approval.summary && <p style={{ marginTop: 8 }}>{approval.summary}</p>}
      {approval.evidence?.map((item) =>
        item.evidence_snippet ? (
          <EvidenceChip
            key={`${item.source_id}-${item.evidence_snippet.slice(0, 12)}`}
            evidence={{
              snippet: item.evidence_snippet,
              source: item.source_type ?? "",
            }}
          />
        ) : null,
      )}
      {!decided &&
        (editing ? (
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              gap: 8,
              marginTop: 10,
            }}
          >
            {strings.map(([key]) => (
              <div className="field" key={key}>
                <span className="t-label" id={`edit-${approval.id}-${key}`}>
                  {key}
                </span>
                <TextInput
                  aria-labelledby={`edit-${approval.id}-${key}`}
                  value={draft[key] ?? ""}
                  onChange={(event) =>
                    setDraft((current) => ({
                      ...current,
                      [key]: event.target.value,
                    }))
                  }
                />
              </div>
            ))}
            <div className="approval-gate">
              <Button variant="primary" small onClick={approveEdited}>
                {t("inbox.approveEdited")}
              </Button>
              <Button small onClick={() => setEditing(false)}>
                {t("deals.cancel")}
              </Button>
            </div>
          </div>
        ) : (
          <div className="approval-gate">
            <Button
              variant="primary"
              small
              disabled={decide.isPending}
              onClick={() => decide.mutate({ verdict: "approve" })}
            >
              {t("trust.accept")}
            </Button>
            {strings.length > 0 && (
              <Button small onClick={startEdit}>
                {t("trust.edit")}
              </Button>
            )}
            <Button
              small
              disabled={decide.isPending}
              onClick={() => setRejecting(true)}
            >
              {t("inbox.reject")}
            </Button>
          </div>
        ))}
      <DecideOutcome
        token={token}
        decide={decide}
        skew={skew}
        alreadyDecided={alreadyDecided}
        onReRead={reRead}
      />
      <ConfirmModal
        open={rejecting}
        onClose={() => setRejecting(false)}
        title={t("inbox.reject")}
        confirmLabel={t("inbox.reject")}
        pending={decide.isPending}
        onConfirm={confirmReject}
      >
        <div className="field">
          <label className="t-label" htmlFor={`reject-reason-${approval.id}`}>
            {t("inbox.rejectReason")}
          </label>
          <textarea
            id={`reject-reason-${approval.id}`}
            className="input"
            value={reason}
            onChange={(event) => setReason(event.target.value)}
          />
          <p className="t-caption">{t("inbox.rejectReasonHint")}</p>
        </div>
      </ConfirmModal>
      <ApprovalDetailModal
        approvalId={approval.id}
        open={detailOpen}
        onClose={() => setDetailOpen(false)}
      />
    </article>
  );
}

export function InboxScreen() {
  const t = useT();
  const [tab, setTab] = useState<"pending" | "decided">("pending");
  const pendingQuery = usePendingApprovals();
  const decidedQuery = useDecidedApprovals();
  const query = tab === "pending" ? pendingQuery : decidedQuery;
  return (
    <div className="wrap narrow">
      <SectionHeader title={t("nav.inbox")} sub={t("inbox.sub")} />
      <SegmentedControl
        options={["pending", "decided"] as const}
        value={tab}
        onChange={setTab}
        labels={{
          pending: t("inbox.tab.pending"),
          decided: t("inbox.tab.decided"),
        }}
      />
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <div style={{ marginTop: 12 }}>
            {page.data.map((approval) => (
              <ApprovalRow
                key={approval.id}
                approval={approval}
                decided={tab === "decided"}
              />
            ))}
          </div>
        )}
      </QueryGate>
    </div>
  );
}
