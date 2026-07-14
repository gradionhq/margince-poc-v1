import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { TriangleAlert } from "lucide-react";
import { type ReactNode, useCallback, useEffect, useId, useState } from "react";
import { api } from "../api/client";
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
import {
  type Approval,
  useDecidedApprovals,
  usePendingApprovals,
} from "./inbox.queries";

// Re-exported so existing consumers (home.tsx) keep importing from "./inbox".
export { useDecidedApprovals, usePendingApprovals } from "./inbox.queries";

// The approval inbox (B-EP09.12a) — the CANONICAL 🟡 surface. Per-row
// approve/reject plus the inline staged-draft editor: string fields of the
// proposed_change are editable and go up as edited_payload, which the server
// RE-ADMITS from scratch (re-tiered, re-RBAC'd, new diff_hash — ADR-0036);
// an edit can never silently escalate the effect. A 409 version-skew comes
// back as an honest "the world changed, re-stage" row error; a 409
// already-decided drops the stale row instead of offering a re-stage retry
// (Task 10, AC-1..7).

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

function editableStrings(change: Record<string, unknown>): [string, string][] {
  return Object.entries(change).filter((entry): entry is [string, string] => {
    return typeof entry[1] === "string";
  });
}

// The per-claim evidence chips, shared by the row and the detail modal (was
// duplicated verbatim in both). A snippet-less evidence item is dropped.
function EvidenceList({
  evidence,
}: Readonly<{ evidence: Approval["evidence"] }>) {
  return (
    <>
      {evidence?.map((item) =>
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
    </>
  );
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
// An unexpected status must never yield tone={undefined} (mirrors the label
// lookup's fallback) — an unknown decided state reads as a neutral warn.
function statusTone(status: string): "success" | "danger" | "warn" {
  return STATUS_BADGE_TONE[status] ?? "warn";
}

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
                <EvidenceList evidence={approval.evidence} />
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
      <Badge tone={statusTone(status)}>
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
  // TTL as a chip that escalates as expiry nears (mockup's amber→red): warn
  // under 6h, danger under 1h, neutral beyond — never inert gray text.
  const remaining = expiresAtMs - now;
  const urgency =
    remaining < 60 * 60 * 1000
      ? "danger"
      : remaining < 6 * 60 * 60 * 1000
        ? "warn"
        : undefined;
  return (
    <Badge tone={urgency}>
      {t("inbox.expiresIn", { countdown: formatCountdown(remaining, t) })}
    </Badge>
  );
}

// The row-local decide outcomes that KEEP the row mounted: a generic error
// and the version-skew re-stage state. The success token (AC-4) and the
// already-decided note (AC-6) are deliberately NOT here — both fire a pending
// invalidation that unmounts this row, so they are surfaced at screen level
// (InboxScreen) where they survive the refetch.
function DecideOutcome({
  decide,
  skew,
  alreadyDecided,
  onReRead,
}: Readonly<{
  decide: { isError: boolean; error: unknown };
  skew: boolean;
  alreadyDecided: boolean;
  onReRead: () => void;
}>) {
  const t = useT();
  const generic = decide.isError && !skew && !alreadyDecided;
  return (
    <>
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
    </>
  );
}

// The screen-level "shown once" approval-token surface (AC-4). Rendered by
// InboxScreen/HomeScreen — NOT the row — so the pending invalidation that
// unmounts the just-approved row cannot take the token with it. This is the
// most consequential irrecoverable state on the surface, so it leads with a
// strong heading + a warn-tinted banner, not a small gray caption.
function TokenOnceModal({
  token,
  onClose,
}: Readonly<{ token: string | null; onClose: () => void }>) {
  const t = useT();
  const headingId = useId();
  const [copied, setCopied] = useState(false);
  // A fresh token clears the previous "copied" acknowledgement (referencing
  // `token` in the body keeps it a genuine effect dependency).
  useEffect(() => {
    if (token != null) {
      setCopied(false);
    }
  }, [token]);
  const handleCopy = () => {
    if (!token) {
      return;
    }
    const clip = navigator.clipboard;
    if (!clip) {
      setCopied(false);
      return;
    }
    clip.writeText(token).then(
      () => setCopied(true),
      () => setCopied(false),
    );
  };
  return (
    <Modal open={token != null} onClose={onClose} labelledBy={headingId}>
      <h2
        id={headingId}
        className="t-h2"
        style={{ color: "var(--textPrimary)", marginBottom: 10 }}
      >
        {t("inbox.tokenTitle")}
      </h2>
      <div
        style={{
          display: "flex",
          gap: 8,
          alignItems: "center",
          background: "var(--warnBg)",
          border: "1px solid var(--warnBorder)",
          borderRadius: "var(--r-sm)",
          padding: "8px 10px",
          marginBottom: 10,
        }}
      >
        <TriangleAlert size={16} color="var(--warn)" aria-hidden />
        <span className="t-caption" style={{ color: "var(--warn)" }}>
          {t("inbox.tokenOnce")}
        </span>
      </div>
      <p className="t-mono" style={{ wordBreak: "break-all" }}>
        {token}
      </p>
      <div className="actions">
        <Button small onClick={handleCopy}>
          {copied ? t("inbox.copied") : t("inbox.copy")}
        </Button>
        <Button small variant="primary" onClick={onClose}>
          {t("inbox.tokenDone")}
        </Button>
      </div>
    </Modal>
  );
}

// Shared decision sink (AC-4/AC-6, cross-surface): owns the screen-level state
// that must OUTLIVE the row that triggered it (a decide invalidates the
// pending list, unmounting the row) — the once-shown approval token and the
// "already decided by someone else" note. BOTH InboxScreen and HomeScreen
// consume it so either surface catches the minted token AND shows the honest
// already-decided note; neither may live in ApprovalRow (it unmounts).
export function useApprovalTokenSink(): {
  onApproved: (approvalId: string, token: string) => void;
  onAlreadyDecided: () => void;
  tokenModal: ReactNode;
  decidedNote: ReactNode;
} {
  const t = useT();
  const [token, setToken] = useState<string | null>(null);
  const [alreadyDecided, setAlreadyDecided] = useState(false);
  const onApproved = useCallback(
    (_approvalId: string, minted: string) => setToken(minted),
    [],
  );
  const onAlreadyDecided = useCallback(() => setAlreadyDecided(true), []);
  const tokenModal = (
    <TokenOnceModal token={token} onClose={() => setToken(null)} />
  );
  const decidedNote = alreadyDecided ? (
    <div
      className="card card-inset"
      style={{
        marginTop: 12,
        display: "flex",
        gap: 8,
        alignItems: "center",
      }}
    >
      <p className="t-caption" style={{ color: "var(--danger)", flex: 1 }}>
        {t("inbox.alreadyDecided")}
      </p>
      <Button small onClick={() => setAlreadyDecided(false)}>
        {t("inbox.dismiss")}
      </Button>
    </div>
  ) : null;
  return { onApproved, onAlreadyDecided, tokenModal, decidedNote };
}

export function ApprovalRow({
  approval,
  decided,
  onApproved,
  onAlreadyDecided,
}: Readonly<{
  approval: Approval;
  decided?: boolean;
  // Lift the just-minted token / the already-decided signal to a surface that
  // survives this row's unmount (the pending invalidation drops it). Optional
  // so HomeScreen can reuse the row without a screen-level surface.
  onApproved?: (approvalId: string, token: string) => void;
  onAlreadyDecided?: () => void;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [rejecting, setRejecting] = useState(false);
  const [reason, setReason] = useState("");
  const [detailOpen, setDetailOpen] = useState(false);
  // Only a live pending row with an expiry needs the per-second clock; a
  // read-only decided row (or one without expires_at) never shows a countdown,
  // so its interval is disabled — no needless per-second re-renders on long
  // Decided lists (interval 0 ⇒ useNow does not tick).
  const needsCountdown = !decided && approval.expires_at != null;
  const now = useNow(needsCountdown ? 1000 : 0);

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
      // Lift the token FIRST — the parent state is set before the invalidation
      // below unmounts this row, so the screen-level surface always receives it.
      if (data?.approval_token) {
        onApproved?.(approval.id, data.approval_token);
      }
      queryClient.invalidateQueries({ queryKey: ["approvals"] });
    },
    onError: (error) => {
      const problem = error instanceof ProblemError ? error.problem : null;
      if (problem && isAlreadyDecided(problem)) {
        onAlreadyDecided?.();
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
        {/* kind is meta, not the headline — the human reads the summary first */}
        <span className="t-small">{approval.kind}</span>
        <ProvenanceTag provenance={provenanceOf(approval.proposed_by)} />
        {level && <ConfidenceMeter level={level} />}
        <RowStatusChip
          decided={!!decided}
          status={approval.status}
          expiresAtMs={expiresAtMs}
          isExpired={isExpired}
          now={now}
        />
        {/* lighter, secondary affordance — must not compete with Accept/Reject */}
        <button
          type="button"
          className="link-button"
          style={{ marginInlineStart: "auto" }}
          onClick={() => setDetailOpen(true)}
        >
          {t("inbox.detail")}
        </button>
      </div>
      {approval.summary && (
        <p className="t-h2" style={{ marginTop: 8 }}>
          {approval.summary}
        </p>
      )}
      <EvidenceList evidence={approval.evidence} />
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
        confirmVariant="danger"
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
  // Screen-level surfaces that must outlive the row that triggered them (a
  // decide invalidates the pending list, unmounting the row): the once-shown
  // approval token (AC-4, via the shared sink) and the "already decided by
  // someone else" note (AC-6).
  const { onApproved, onAlreadyDecided, tokenModal, decidedNote } =
    useApprovalTokenSink();
  const pendingQuery = usePendingApprovals();
  const decidedQuery = useDecidedApprovals(tab === "decided");
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
      {decidedNote}
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <div style={{ marginTop: 12 }}>
            {page.data.map((approval) => (
              <ApprovalRow
                key={approval.id}
                approval={approval}
                decided={tab === "decided"}
                onApproved={onApproved}
                onAlreadyDecided={onAlreadyDecided}
              />
            ))}
          </div>
        )}
      </QueryGate>
      {tokenModal}
    </div>
  );
}
