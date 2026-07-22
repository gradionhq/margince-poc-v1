// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, SectionHeader } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { formatMoney, formatNumber } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import { bandTone } from "./aiusage";
import { canConfigureAutomations, problemMessage, useMe } from "./common";

// The v6 B2 embedding-reindex surface (ADR-0068 design §5.6-swap). The
// status read is wide-granted to every role (migration 0114: "every role
// reads it — any user's UI needs to show the reindex-needed banner"), so
// EmbedReindexBanner (app/embedreindexbanner.tsx) carries no role gate of
// its own and shares this module's query key. The two WRITE actions —
// confirming a reindex and the always-available force rebuild — are
// admin/ops-only server-side (embedding_reindex object's update grant), so
// this card hides both behind canConfigureAutomations: the same admin/ops
// predicate the AI-runtime and automation cards already gate on, rather
// than a button that could only ever 403.

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
      {/* Always available, independent of reindex_needed — the v6 B2
          rebuild affordance: an operator may want to re-embed a
          current-identity corpus anyway (e.g. after a data fix). */}
      <Button small disabled={isRunning} onClick={onRebuild}>
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

  const status = useQuery({
    queryKey: embedReindexStatusQueryKey,
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
          // The identity this SPA previewed against — the server 409s
          // (reindex_identity_drift) if the embed binding changed since.
          previewed_identity: status.data?.configured_identity,
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
      setMode(null);
    },
  });

  if (status.isPending) {
    return (
      <section className="card" style={{ marginBottom: 14 }}>
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
      <section className="card" style={{ marginBottom: 14 }}>
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
    <section className="card" style={{ marginBottom: 14 }}>
      <SectionHeader
        title={t("embedreindex.title")}
        sub={t("embedreindex.sub")}
      />
      <StatusHeader data={data} isRunning={isRunning} locale={locale} t={t} />
      {canWrite && (
        <ReindexActions
          data={data}
          isRunning={isRunning}
          onReindex={() => setMode("reindex")}
          onRebuild={() => setMode("rebuild")}
          t={t}
        />
      )}
      <ConfirmModal
        open={mode !== null}
        onClose={() => setMode(null)}
        title={dialogTitle(mode ?? "reindex", t)}
        confirmLabel={dialogConfirmLabel(
          mode ?? "reindex",
          confirm.isPending,
          t,
        )}
        confirmDisabled={preview.isPending || !preview.data}
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
