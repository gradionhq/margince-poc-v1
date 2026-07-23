// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, SectionHeader } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { formatDuration, formatMoney, formatNumber } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import { bandTone } from "./aiusage";
import { canConfigureAutomations, problemMessage, useMe } from "./common";

// The v6 B2 embedding-reindex surface (ADR-0068 design §5.6-swap). The
// status read is admin/ops-only server-side now (migration 0114:
// manager/rep/read_only hold no grant at all on embedding_reindex), so
// this card's status query is itself gated on canConfigureAutomations —
// a non-ops role would otherwise get a 403 rendered as "status
// unavailable" for a card it can never act on anyway. The card returns
// null outright for a non-ops viewer, the same predicate
// EmbedReindexBanner (app/embedreindexbanner.tsx) already gates its own
// query on. The two WRITE actions — confirming a reindex and the
// always-available force rebuild — are admin/ops-only server-side too
// (embedding_reindex object's update grant).

type ReindexStatus = components["schemas"]["EmbedReindexStatus"];
type ReindexPreview = components["schemas"]["EmbedReindexPreview"];
type UtilizationImpact =
  ReindexPreview["per_workspace"][number]["utilization_impact"];

// Shared by the settings card and the app-shell banner so a successful
// confirm's setQueryData (below) updates both surfaces from the one write.
export const embedReindexStatusQueryKey = ["embed-reindex-status"];
const embedReindexPreviewQueryKey = ["embed-reindex-preview"];

// impactLabel names the HYPOTHETICAL post-reindex band the estimator
// disclosed (utilization_impact) — distinct copy from aiusage's bandLabel,
// which names the workspace's CURRENT band ("economy mode" reads wrong for
// a state nothing has entered yet). bandTone is reused verbatim: same
// three-value enum, same colour semantics.
function impactLabel(
  impact: UtilizationImpact,
  t: ReturnType<typeof useT>,
): string {
  if (impact === "degraded") return t("embedreindex.impact.degraded");
  if (impact === "queued") return t("embedreindex.impact.queued");
  return t("embedreindex.impact.normal");
}

// dialogTitle/dialogConfirmLabel factor the mode-dependent copy out of the
// render body below — the ONLY difference between the reindex and rebuild
// flows is which strings the shared ConfirmModal shows.
function dialogTitle(
  mode: "reindex" | "rebuild",
  t: ReturnType<typeof useT>,
): string {
  return mode === "rebuild"
    ? t("embedreindex.rebuildTitle")
    : t("embedreindex.confirmTitle");
}

function dialogConfirmLabel(
  mode: "reindex" | "rebuild",
  pending: boolean,
  t: ReturnType<typeof useT>,
): string {
  if (pending) {
    return t("embedreindex.starting");
  }
  return mode === "rebuild"
    ? t("embedreindex.rebuildConfirmCta")
    : t("embedreindex.confirmCta");
}

function StatusHeader({
  data,
  isRunning,
  locale,
  t,
}: Readonly<{
  data: ReindexStatus;
  isRunning: boolean;
  locale: Locale;
  t: ReturnType<typeof useT>;
}>) {
  const tone = isRunning ? "accent" : data.reindex_needed ? "warn" : "success";
  const label = isRunning
    ? t("embedreindex.statusReembedding")
    : data.reindex_needed
      ? t("embedreindex.statusNeeded")
      : t("embedreindex.statusIdle");
  return (
    <div
      style={{
        display: "flex",
        gap: "var(--space-3)",
        alignItems: "center",
        flexWrap: "wrap",
      }}
    >
      <Badge tone={tone}>{label}</Badge>
      {data.reindex_needed && !isRunning && (
        <span className="t-small">
          {t("embedreindex.entitiesPending", {
            count: formatNumber(data.entities_pending, locale),
          })}
        </span>
      )}
      {isRunning && (
        // F2 recovery: a drift-cancelled/retry-discarded job can leave the
        // marker stuck at reembedding with no live worker behind it — this
        // is the affordance that lets an operator judge "stuck" from "still
        // going" without curl, and Rebuild (below) stays enabled so they
        // can act on that judgment (deals.tsx's own age-since-instant idiom:
        // Date.now() minus the stored UTC instant, formatDuration'd).
        <span className="t-small">
          {t("embedreindex.reembeddingSince", {
            duration: formatDuration(
              Math.max(0, Date.now() - new Date(data.updated_at).getTime()),
              locale,
            ),
          })}
        </span>
      )}
    </div>
  );
}

function ReindexActions({
  data,
  isRunning,
  onReindex,
  onRebuild,
  t,
}: Readonly<{
  data: ReindexStatus;
  isRunning: boolean;
  onReindex: () => void;
  onRebuild: () => void;
  t: ReturnType<typeof useT>;
}>) {
  return (
    <div className="actions" style={{ marginTop: "var(--space-3)" }}>
      {data.reindex_needed && !isRunning && (
        <Button variant="primary" small onClick={onReindex}>
          {t("embedreindex.reviewCta")}
        </Button>
      )}
      {/* Always available, independent of reindex_needed AND of isRunning —
          the v6 B2 rebuild affordance, and F2's stuck-marker recovery path:
          a drift-cancelled or retry-discarded job leaves the marker stuck
          at reembedding with no live worker behind it, so disabling Rebuild
          while isRunning would make that state unrecoverable without curl.
          Re-confirming with force:true is harmless either way — a genuinely
          live job answers 409 reindex_running (shown as the modal's error),
          a dead one re-enqueues (202). */}
      <Button small onClick={onRebuild}>
        {t("embedreindex.rebuildCta")}
      </Button>
    </div>
  );
}

function EstimateBody({
  preview,
  locale,
  t,
}: Readonly<{
  preview: ReindexPreview | undefined;
  locale: Locale;
  t: ReturnType<typeof useT>;
}>) {
  if (preview === undefined) {
    return null;
  }
  return (
    <div>
      <p>
        {t("embedreindex.estimateEntities")}{" "}
        <strong>{formatNumber(preview.entities_pending, locale)}</strong>
      </p>
      {preview.estimated_ai_tokens !== undefined && (
        <p className="t-small">
          {t("embedreindex.estimateTokens")} ~
          {formatNumber(preview.estimated_ai_tokens, locale)}
        </p>
      )}
      {preview.estimated_cost_minor !== undefined && (
        <p className="t-small">
          {t("embedreindex.estimateCost")} ~
          {formatMoney(
            preview.estimated_cost_minor,
            preview.currency ?? "USD",
            locale,
          )}
        </p>
      )}
      <p className="t-small">{t("embedreindex.estimateQualityHeuristic")}</p>
      {preview.per_workspace.length > 0 && (
        <>
          <p className="t-small" style={{ marginTop: "var(--space-3)" }}>
            {t("embedreindex.utilizationTitle")}
          </p>
          <ul style={{ listStyle: "none", paddingLeft: 0 }}>
            {preview.per_workspace.map((row) => (
              <li key={row.workspace_id}>
                <Badge tone={bandTone(row.utilization_impact)}>
                  {impactLabel(row.utilization_impact, t)}
                </Badge>{" "}
                <span className="t-small">
                  {row.workspace_id} ·{" "}
                  {t("embedreindex.workspacePending", {
                    count: formatNumber(row.entities_pending, locale),
                  })}
                </span>
              </li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}

export function EmbedReindexCard() {
  const t = useT();
  const { locale } = useLocale();
  const me = useMe();
  const qc = useQueryClient();
  const canWrite = canConfigureAutomations(me.data?.roles);
  const [mode, setMode] = useState<"reindex" | "rebuild" | null>(null);
  // The identity the operator is previewing against, snapshotted when the
  // dialog opens — NOT re-read from the live status query at confirm time. A
  // background status refetch (window focus, invalidation) could otherwise
  // swap in a newer configured_identity, silently defeating the server's
  // reindex_identity_drift guard: the whole point is to confirm against the
  // binding that was on screen.
  const [previewedIdentity, setPreviewedIdentity] = useState<string | null>(
    null,
  );
  const openDialog = (next: "reindex" | "rebuild", identity: string) => {
    setPreviewedIdentity(identity);
    setMode(next);
  };
  const closeDialog = () => {
    setMode(null);
    setPreviewedIdentity(null);
  };

  const status = useQuery({
    queryKey: embedReindexStatusQueryKey,
    enabled: canWrite,
    queryFn: async (): Promise<ReindexStatus> => {
      const { data, error } = await api.GET("/embeddings/reindex/status");
      if (error) {
        throw new Error(problemMessage(error));
      }
      if (!data) {
        throw new Error("malformed reindex status response");
      }
      return data;
    },
  });

  // Fetched only once the dialog is open (ADR-0020 preview-before-spend):
  // the estimate is what the operator is about to consent to, not a figure
  // computed ahead of the decision to look.
  const preview = useQuery({
    queryKey: embedReindexPreviewQueryKey,
    enabled: mode !== null,
    queryFn: async (): Promise<ReindexPreview> => {
      const { data, error } = await api.GET("/embeddings/reindex/preview");
      if (error) {
        throw new Error(problemMessage(error));
      }
      if (!data) {
        throw new Error("malformed reindex preview response");
      }
      return data;
    },
  });

  const confirm = useMutation({
    mutationFn: async (force: boolean): Promise<ReindexStatus> => {
      const { data, error } = await api.POST("/embeddings/reindex", {
        body: {
          // The identity this SPA previewed against, snapshotted at dialog
          // open — the server 409s (reindex_identity_drift) if the embed
          // binding changed since, so this must be the on-screen value, not a
          // possibly-refetched live one.
          previewed_identity: previewedIdentity ?? undefined,
          force,
        },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      if (!data) {
        throw new Error("malformed reindex confirm response");
      }
      return data;
    },
    onSuccess: (data) => {
      // The 202 body is the SAME status read the GET returns — seed the
      // shared cache directly so the card and the banner both reflect
      // "reembedding" without an extra round trip.
      qc.setQueryData(embedReindexStatusQueryKey, data);
      closeDialog();
    },
  });

  // Non-ops viewers hold no read grant on embedding_reindex server-side
  // (migration 0115) and have nothing actionable to do with this card
  // regardless — render nothing rather than a "status unavailable" card
  // for a 403 that was always expected. This runs after every hook call
  // above so the hooks-call-order stays unconditional; the query itself
  // is `enabled: canWrite`, so no request even fires for this viewer.
  if (!canWrite) {
    return null;
  }

  if (status.isPending) {
    return (
      <section className="card" style={{ marginBottom: "var(--space-4)" }}>
        <SectionHeader
          title={t("embedreindex.title")}
          sub={t("embedreindex.sub")}
        />
        <p className="t-small">{t("embedreindex.loading")}</p>
      </section>
    );
  }
  if (status.isError || !status.data) {
    return (
      <section className="card" style={{ marginBottom: "var(--space-4)" }}>
        <SectionHeader
          title={t("embedreindex.title")}
          sub={t("embedreindex.sub")}
        />
        <p className="t-small">{t("embedreindex.statusUnavailable")}</p>
      </section>
    );
  }

  const data = status.data;
  const isRunning = data.status === "reembedding";

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader
        title={t("embedreindex.title")}
        sub={t("embedreindex.sub")}
      />
      <StatusHeader data={data} isRunning={isRunning} locale={locale} t={t} />
      {/* canWrite is always true past the !canWrite early return above —
          this card renders ReindexActions unconditionally, not gated
          again, since a non-ops viewer never reaches this far. */}
      <ReindexActions
        data={data}
        isRunning={isRunning}
        onReindex={() => openDialog("reindex", data.configured_identity)}
        onRebuild={() => openDialog("rebuild", data.configured_identity)}
        t={t}
      />
      <ConfirmModal
        open={mode !== null}
        onClose={closeDialog}
        title={dialogTitle(mode ?? "reindex", t)}
        confirmLabel={dialogConfirmLabel(
          mode ?? "reindex",
          confirm.isPending,
          t,
        )}
        // Gate on a fully-loaded, non-errored, non-refetching estimate — a
        // cached preview that is refetching (isFetching) or has errored must
        // not leave Confirm live over stale scope/cost.
        confirmDisabled={
          preview.isPending ||
          preview.isFetching ||
          preview.isError ||
          !preview.data
        }
        pending={confirm.isPending}
        error={confirm.error?.message}
        onConfirm={() => confirm.mutate(mode === "rebuild")}
      >
        {preview.isPending && (
          <p className="t-small">{t("embedreindex.previewLoading")}</p>
        )}
        {preview.isError && (
          <p className="t-small" style={{ color: "var(--danger)" }}>
            {preview.error.message}
          </p>
        )}
        <EstimateBody preview={preview.data} locale={locale} t={t} />
      </ConfirmModal>
    </section>
  );
}
