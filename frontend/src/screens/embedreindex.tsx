// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCan, useCanWrite } from "../app/capability";
import { Badge, Button, EmptyState } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { CardBoundary } from "../design-system/cardboundary";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDuration, formatMoney, formatNumber } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import { bandTone } from "./aiusage";
import { problemMessageOf, QueryGate, throwProblem, useMe } from "./common";
import "./embedreindex.css";

// The v6 B2 embedding-reindex surface (ADR-0068 design §5.6-swap). The
// status read is admin/ops-only server-side now (migration 0115:
// manager/rep/read_only hold no grant at all on embedding_reindex), so
// this card's status query is itself gated on embedding_reindex:read —
// a non-ops role would otherwise get a 403 rendered as "status
// unavailable" for a card it can never act on anyway. That is the same
// grant EmbedReindexBanner (app/embedreindexbanner.tsx) gates its own
// query on; the card without it keeps its place and states the denial
// (the withheld branch below). The two WRITE actions — confirming a
// reindex and the always-available force rebuild — are admin/ops-only
// server-side too (embedding_reindex object's update grant).

type ReindexStatus = components["schemas"]["EmbedReindexStatus"];
type ReindexPreview = components["schemas"]["EmbedReindexPreview"];
type UtilizationImpact = NonNullable<ReindexPreview["utilization_impact"]>;

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

// The label does not change while the reindex is starting. `ConfirmModal`
// draws the busy mark and sets `aria-busy`, and a renamed confirm on top of
// that says the same thing twice while moving the accessible name of the
// control the reader is focused on.
function dialogConfirmLabel(
  mode: "reindex" | "rebuild",
  t: ReturnType<typeof useT>,
): string {
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
    <div className="embedreindex-status">
      <Badge tone={tone}>{label}</Badge>
      {data.reindex_needed && !isRunning && (
        <span className="t-small">
          {t("embedreindex.entitiesPending", {
            count: formatNumber(data.entities_pending, locale),
          })}
        </span>
      )}
      {isRunning && (
        // F2 recovery: a run whose last worker died leaves the marker stuck at
        // reembedding with nothing behind it — this is the affordance that lets
        // an operator judge "stuck" from "still going" without curl, and Rebuild
        // (below) stays enabled so they can act on that judgment (deals.tsx's
        // own age-since-instant idiom: Date.now() minus the stored UTC instant,
        // formatDuration'd).
        //
        // It reads the age of the last PROGRESS, not of the run: a running pass
        // refreshes updated_at as it embeds, so a long healthy reindex shows a
        // small number here and a stalled one grows without bound. That is what
        // makes the judgment possible, and it is why the label says "last
        // progress" rather than "reindexing since".
        <span className="t-small">
          {t("embedreindex.lastProgress", {
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
    <div className="embedreindex-estimate">
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
      {preview.utilization_impact && (
        <>
          <p className="t-small">{t("embedreindex.utilizationTitle")}</p>
          <p>
            <Badge tone={bandTone(preview.utilization_impact)}>
              {impactLabel(preview.utilization_impact, t)}
            </Badge>{" "}
            <span className="t-small">
              {t("embedreindex.workspacePending", {
                count: formatNumber(preview.entities_pending, locale),
              })}
            </span>
          </p>
        </>
      )}
    </div>
  );
}

export function EmbedReindexCard() {
  const t = useT();
  const { locale } = useLocale();
  const qc = useQueryClient();
  const me = useMe();
  // Reading the status and acting on it are separate grants. They move
  // together in the seeded matrix, but the card gates its query on the read and
  // its actions on the update, so a role granted one without the other gets a
  // card that tells the truth rather than one that 403s on click.
  const canRead = useCan("embedding_reindex", "read");
  const canWrite = useCanWrite("embedding_reindex", "update");
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
  // Clearing `mode`, not just hiding the modal: the preview observer keys off
  // it, so a dialog left half-open would keep asking for an estimate after the
  // grant that justified it was withdrawn, and would reappear if it came back.
  useEffect(() => {
    if (!canWrite) {
      setMode(null);
    }
  }, [canWrite]);

  const closeDialog = () => {
    setMode(null);
    setPreviewedIdentity(null);
  };

  const status = useQuery({
    queryKey: embedReindexStatusQueryKey,
    enabled: canRead,
    queryFn: async (): Promise<ReindexStatus> => {
      const { data, error } = await api.GET("/embeddings/reindex/status");
      if (error) {
        throwProblem(error);
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
        throwProblem(error);
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
        throwProblem(error);
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

  // Withheld, not absent: a permission is what denies this, so the card keeps its
  // place and says why it is empty rather than vanishing. The reader it is written
  // for is narrower than it looks — the Maintenance entry opens on this very read
  // or the admin role, so nobody below ops reaches the page at all. It serves an
  // operator whose embedding_reindex grant an edited role no longer carries, and
  // for them an absent card would read as "this installation has no search index"
  // rather than "this is not yours to see".
  //
  // The query stays `enabled: canRead` and that half of the reasoning stands:
  // the answer is already known, so asking for a 403 in order to render it
  // would turn a settled denial into a "status unavailable" the reader cannot
  // act on. This runs after every hook call above so the hooks-call-order stays
  // unconditional, and it is gated on the /me probe itself so the notice waits
  // for the grants rather than flashing while they are in flight.
  //
  // ONE panel shell for every state this card can be in. It used to be four
  // copies of the same header — withheld, loading, unreadable, ready — and the
  // two middle ones had drifted into a bare paragraph apiece: no live region on
  // the loading line, and no retry at all on the failure. QueryGate is the
  // shared spelling of those three rungs, and it carries the server's own
  // explanation with the retry rather than replacing it with "unavailable".
  let body: ReactNode;
  if (!canRead) {
    body = (
      <QueryGate query={me}>
        {() => (
          <EmptyState>
            <p className="t-small">{t("embedreindex.withheld")}</p>
          </EmptyState>
        )}
      </QueryGate>
    );
  } else {
    body = (
      <QueryGate query={status}>
        {(data) => {
          const isRunning = data.status === "reembedding";
          return (
            <>
              <SettingList>
                <SettingRow
                  label={t("embedreindex.statusLabel")}
                  control={
                    <StatusHeader
                      data={data}
                      isRunning={isRunning}
                      locale={locale}
                      t={t}
                    />
                  }
                />
                {/* Gated on the update grant, not on the read that got us this
                    far: a viewer may be entitled to see the status without
                    being entitled to start a rebuild. */}
                {canWrite && (
                  <>
                    {data.reindex_needed && !isRunning && (
                      <SettingRow
                        label={t("embedreindex.reindexLabel")}
                        description={t("embedreindex.reindexHelp")}
                        control={
                          <Button
                            variant="primary"
                            small
                            onClick={() =>
                              openDialog("reindex", data.configured_identity)
                            }
                          >
                            {t("embedreindex.reviewCta")}
                          </Button>
                        }
                      />
                    )}
                    {/* Always available, independent of reindex_needed AND of
                        isRunning — the v6 B2 rebuild affordance, and F2's
                        stuck-marker recovery path: a drift-cancelled or
                        retry-discarded job leaves the marker stuck at
                        reembedding with no live worker behind it, so disabling
                        Rebuild while isRunning would make that state
                        unrecoverable without curl. Re-confirming with
                        force:true is harmless either way — a genuinely live job
                        answers 409 reindex_running (shown as the modal's
                        error), a dead one re-enqueues (202). */}
                    <SettingRow
                      label={t("embedreindex.rebuildLabel")}
                      description={t("embedreindex.rebuildHelp")}
                      control={
                        <Button
                          small
                          onClick={() =>
                            openDialog("rebuild", data.configured_identity)
                          }
                        >
                          {t("embedreindex.rebuildCta")}
                        </Button>
                      }
                    />
                  </>
                )}
              </SettingList>
              {/* Outside the list: a modal is not one of the card's rows, and a
                  closed one standing in the list would take a hairline of its
                  own. */}
              {canWrite && (
                <ConfirmModal
                  open={mode !== null}
                  onClose={closeDialog}
                  title={dialogTitle(mode ?? "reindex", t)}
                  confirmLabel={dialogConfirmLabel(mode ?? "reindex", t)}
                  // Gate on a fully-loaded, non-errored, non-refetching
                  // estimate — a cached preview that is refetching
                  // (isFetching) or has errored must not leave Confirm live
                  // over stale scope/cost.
                  //
                  // Dropped once the reindex is out, because by then the
                  // estimate has stopped being a precondition: the act is
                  // already gone, and the confirm invalidates this very
                  // preview — so leaving the gate armed flips the button from
                  // "working" to "refused" in the middle of its own write.
                  confirmDisabled={
                    !confirm.isPending &&
                    (preview.isPending ||
                      preview.isFetching ||
                      preview.isError ||
                      !preview.data)
                  }
                  pending={confirm.isPending}
                  error={
                    confirm.error ? problemMessageOf(confirm.error, t) : null
                  }
                  onConfirm={() => {
                    // The grant can be withdrawn while this dialog sits open
                    // — /me refetches on focus and after any 403 — so the
                    // write re-reads it rather than trusting the capability
                    // that opened the dialog.
                    if (!canWrite) {
                      return;
                    }
                    confirm.mutate(mode === "rebuild");
                  }}
                >
                  {preview.isPending && (
                    <p className="t-small">
                      {t("embedreindex.previewLoading")}
                    </p>
                  )}
                  {/* A failed estimate is what this dialog says about ITSELF,
                      and it is the reason Confirm is refused — so it is a
                      `Callout`, not a tinted paragraph: red text alone carries
                      the meaning in colour only. `alert` because it appears in
                      answer to the reader opening this dialog and it names the
                      thing standing between them and the act. */}
                  {preview.isError && (
                    <Callout tone="danger" live="alert">
                      <p>{problemMessageOf(preview.error, t)}</p>
                    </Callout>
                  )}
                  <EstimateBody preview={preview.data} locale={locale} t={t} />
                </ConfirmModal>
              )}
            </>
          );
        }}
      </QueryGate>
    );
  }

  // No bottom margin of its own: `.settings-stack` owns the gap between cards.
  return (
    <Panel title={t("embedreindex.title")}>
      <PanelBody>
        <p className="settings-panel-sub">{t("embedreindex.sub")}</p>
        <CardBoundary>{body}</CardBoundary>
      </PanelBody>
    </Panel>
  );
}
