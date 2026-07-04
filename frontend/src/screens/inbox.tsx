import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Button, SectionHeader, TextInput } from "../design-system/atoms";
import {
  AutonomyDot,
  type ConfidenceLevel,
  ConfidenceMeter,
  EvidenceChip,
  ProvenanceTag,
} from "../design-system/trust";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, provenanceOf, QueryGate } from "./common";

// The approval inbox (B-EP09.12a) — the CANONICAL 🟡 surface. Per-row
// approve/reject plus the inline staged-draft editor: string fields of the
// proposed_change are editable and go up as edited_payload, which the server
// RE-ADMITS from scratch (re-tiered, re-RBAC'd, new diff_hash — ADR-0036);
// an edit can never silently escalate the effect. A 409 version-skew comes
// back as an honest "the world changed, re-stage" row error.

type Approval = components["schemas"]["Approval"];

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

export function usePendingApprovals() {
  return useQuery({
    queryKey: ["approvals", "pending"],
    queryFn: async () => {
      const { data, error } = await api.GET("/approvals", {
        params: { query: { status: "pending", limit: 50 } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });
}

function editableStrings(change: Record<string, unknown>): [string, string][] {
  return Object.entries(change).filter((entry): entry is [string, string] => {
    return typeof entry[1] === "string";
  });
}

export function ApprovalRow({ approval }: { approval: Approval }) {
  const t = useT();
  const { locale } = useLocale();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Record<string, string>>({});

  const decide = useMutation({
    mutationFn: async (input: {
      verdict: "approve" | "reject";
      editedPayload?: Record<string, unknown>;
    }) => {
      const path =
        input.verdict === "approve"
          ? "/approvals/{id}/approve"
          : "/approvals/{id}/reject";
      const { error } = await api.POST(path, {
        params: { path: { id: approval.id } },
        ...(input.verdict === "approve" && input.editedPayload
          ? { body: { edited_payload: input.editedPayload } }
          : {}),
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["approvals", "pending"] });
    },
  });

  const change = (approval.proposed_change ?? {}) as Record<string, unknown>;
  const strings = editableStrings(change);
  const level = confidenceLevel(approval.confidence);

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
        <AutonomyDot tier="confirm" />
        <span className="t-label">{approval.kind}</span>
        <ProvenanceTag provenance={provenanceOf(approval.proposed_by)} />
        {level && <ConfidenceMeter level={level} />}
        {approval.expires_at && (
          <span className="t-small">
            {t("inbox.expires", {
              at: formatDateTime(approval.expires_at, locale, "Europe/Berlin"),
            })}
          </span>
        )}
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
      {editing ? (
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
            onClick={() => decide.mutate({ verdict: "reject" })}
          >
            {t("inbox.reject")}
          </Button>
        </div>
      )}
      {decide.isError && (
        <p
          className="t-caption"
          style={{ color: "var(--danger)", marginTop: 8 }}
        >
          {decide.error instanceof Error ? decide.error.message : null}
        </p>
      )}
    </article>
  );
}

export function InboxScreen() {
  const t = useT();
  const query = usePendingApprovals();
  return (
    <div className="wrap narrow">
      <SectionHeader title={t("nav.inbox")} sub={t("inbox.sub")} />
      <QueryGate query={query} empty={(page) => page.data.length === 0}>
        {(page) => (
          <div>
            {page.data.map((approval) => (
              <ApprovalRow key={approval.id} approval={approval} />
            ))}
          </div>
        )}
      </QueryGate>
    </div>
  );
}
