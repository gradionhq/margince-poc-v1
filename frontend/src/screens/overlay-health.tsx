// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { RefreshCw } from "lucide-react";
import type { components } from "../api/schema";
import { Badge, Button, SectionHeader } from "../design-system/atoms";
import { formatDateTime } from "../format/format";
import { type Locale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import type { QueryLike } from "./common";

// The overlay card's read-only health surface (Settings → Integrations):
// per-object mirror sync freshness and the incumbent API budget window.
// Split out of overlay.tsx solely to keep that file under the 500-line cap
// and its functions under the cognitive-complexity gate — unlike
// connector-status.tsx (genuinely reused by both connectors.tsx and
// home.tsx), this module has exactly one caller (overlay.tsx); the split
// is a size/complexity boundary, not a reuse seam. Every field here is a
// server fact, never a claim: `headroom` prints verbatim because the
// server may answer the `~unknown` sentinel, and a computed substitute
// would be a fabricated number.

export type SyncStatus = components["schemas"]["OverlaySyncStatus"];
export type SyncObject = NonNullable<SyncStatus["objects"]>[number];
export type Budget = components["schemas"]["OverlayBudget"];
export type BudgetBand = components["schemas"]["OverlayBudgetBand"];

const SYNC_STATE_TONE: Record<string, "success" | "warn" | "danger"> = {
  fresh: "success",
  pending_sync: "warn",
  stale: "danger",
};
const SYNC_STATE_LABEL: Record<string, MessageKey> = {
  fresh: "overlay.syncStateFresh",
  pending_sync: "overlay.syncStatePending",
  stale: "overlay.syncStateStale",
};

const BAND_TONE: Record<BudgetBand, "success" | "warn" | "danger"> = {
  ok: "success",
  warn: "warn",
  shed: "danger",
};
const BAND_LABEL: Record<BudgetBand, MessageKey> = {
  ok: "overlay.bandOk",
  warn: "overlay.bandWarn",
  shed: "overlay.bandShed",
};

// converged is true once every reported object class has both landed its
// backfill and settled at "fresh" — absent `objects` means nothing has
// synced yet, so it never reads as converged. Drives the sync-status
// query's own poll (overlay.tsx): while anything is still catching up,
// re-check every 5s; once the mirror is caught up, stop polling rather
// than hammering the server for a state that will not change until the
// next connect/reconcile.
export function converged(data: SyncStatus | undefined): boolean {
  const objects = data?.objects;
  if (!objects || objects.length === 0) {
    return false;
  }
  return objects.every(
    (o) => o.state === "fresh" && o.backfillComplete === true,
  );
}

function SyncStatusPanel({
  query,
  locale,
}: Readonly<{
  query: QueryLike<SyncStatus>;
  locale: Locale;
}>) {
  const t = useT();
  if (query.isPending) {
    return <p className="t-small">{t("overlay.syncLoading")}</p>;
  }
  if (query.isError) {
    return (
      <p className="t-small" style={{ color: "var(--danger)" }}>
        {query.error instanceof Error
          ? query.error.message
          : t("overlay.syncLoadFailed")}
      </p>
    );
  }
  const objects: SyncObject[] = query.data?.objects ?? [];
  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <SectionHeader title={t("overlay.syncTitle")} />
      {objects.length === 0 && (
        <p className="t-small">{t("overlay.syncEmpty")}</p>
      )}
      {objects.length > 0 && (
        <ul
          style={{
            listStyle: "none",
            display: "flex",
            flexDirection: "column",
            gap: "var(--space-2)",
          }}
        >
          {objects.map((o, i) => (
            <li
              key={o.object ?? i}
              style={{
                display: "flex",
                gap: "var(--space-2)",
                alignItems: "center",
                flexWrap: "wrap",
              }}
            >
              <span className="t-mono">{o.object ?? "—"}</span>
              <Badge tone={o.state ? SYNC_STATE_TONE[o.state] : undefined}>
                {o.state ? t(SYNC_STATE_LABEL[o.state]) : "—"}
              </Badge>
              <span className="t-small">
                {o.backfillComplete
                  ? t("overlay.backfillDone")
                  : t("overlay.backfillPending")}
              </span>
              <span className="t-small">
                {o.lastSyncedAt
                  ? t("overlay.lastSynced", {
                      at: formatDateTime(
                        o.lastSyncedAt,
                        locale,
                        "Europe/Berlin",
                      ),
                    })
                  : t("overlay.neverSynced")}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

// The per-source REST breakdown line — split out of BudgetPanel purely to
// keep that function's branch count under the cognitive-complexity gate.
function BudgetSourcesLine({
  sources,
}: Readonly<{ sources: NonNullable<Budget["sources"]> }>) {
  const t = useT();
  return (
    <p className="t-small" style={{ marginTop: "var(--space-1)" }}>
      {t("overlay.budgetSources", {
        forceFresh: sources.force_fresh ?? 0,
        poller: sources.poller ?? 0,
        capture: sources.capture ?? 0,
      })}
    </p>
  );
}

// The per-second Search-API sub-window — split out of BudgetPanel for the
// same reason BudgetSourcesLine is.
function BudgetSearchRow({
  search,
}: Readonly<{ search: NonNullable<Budget["search"]> }>) {
  const t = useT();
  return (
    <div
      style={{
        display: "flex",
        gap: "var(--space-2)",
        alignItems: "center",
        marginTop: "var(--space-1)",
      }}
    >
      <span className="t-small">
        {t("overlay.budgetSearch", {
          consumed: search.consumed ?? 0,
          limit: search.limit ?? "—",
        })}
      </span>
      {search.band && (
        <Badge tone={BAND_TONE[search.band]}>
          {t(BAND_LABEL[search.band])}
        </Badge>
      )}
    </div>
  );
}

function BudgetPanel({ query }: Readonly<{ query: QueryLike<Budget> }>) {
  const t = useT();
  if (query.isPending) {
    return <p className="t-small">{t("overlay.budgetLoading")}</p>;
  }
  if (query.isError) {
    return (
      <p className="t-small" style={{ color: "var(--danger)" }}>
        {query.error instanceof Error
          ? query.error.message
          : t("overlay.budgetLoadFailed")}
      </p>
    );
  }
  const budget = query.data;
  if (!budget) {
    return null;
  }
  return (
    <div style={{ marginTop: "var(--space-3)" }}>
      <SectionHeader title={t("overlay.budgetTitle")} />
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        {budget.band && (
          <Badge tone={BAND_TONE[budget.band]}>
            {t(BAND_LABEL[budget.band])}
          </Badge>
        )}
        <span className="t-mono t-small">
          {budget.consumed ?? 0} / {budget.limit ?? "—"}
        </span>
        {/* headroom is either a real free-capacity count or the server's own
            `~unknown` sentinel — printed verbatim either way, never
            recomputed from consumed/limit (which would fabricate a number
            the server explicitly declined to attribute). */}
        <span className="t-small">
          {t("overlay.budgetHeadroom", { headroom: budget.headroom ?? "—" })}
        </span>
      </div>
      {budget.sources && <BudgetSourcesLine sources={budget.sources} />}
      {budget.search && <BudgetSearchRow search={budget.search} />}
    </div>
  );
}

// The live-mode section (sync + budget + the reconcile/disconnect actions) —
// shown from overlay.tsx whenever the connection is `active` or `error`
// (see OverlayCard's own `live` doc), never gated further here.
export function OverlayLiveSection({
  sync,
  budget,
  locale,
  canManage,
  onReconcile,
  reconcilePending,
  reconcileQueued,
  reconcileError,
  onDisconnect,
}: Readonly<{
  sync: QueryLike<SyncStatus>;
  budget: QueryLike<Budget>;
  locale: Locale;
  canManage: boolean;
  onReconcile: () => void;
  reconcilePending: boolean;
  reconcileQueued: boolean;
  reconcileError: string | null;
  onDisconnect: () => void;
}>) {
  const t = useT();
  return (
    <>
      <SyncStatusPanel query={sync} locale={locale} />
      <BudgetPanel query={budget} />
      {canManage && (
        <div
          style={{
            display: "flex",
            gap: "var(--space-2)",
            marginTop: "var(--space-3)",
          }}
        >
          <Button small onClick={onReconcile} disabled={reconcilePending}>
            <RefreshCw aria-hidden /> {t("overlay.reconcile")}
          </Button>
          <Button small variant="danger" onClick={onDisconnect}>
            {t("overlay.disconnect")}
          </Button>
        </div>
      )}
      {reconcileQueued && (
        <p className="t-small" style={{ marginTop: "var(--space-2)" }}>
          {t("overlay.reconcileQueued")}
        </p>
      )}
      {reconcileError && (
        <p className="t-small" style={{ color: "var(--danger)" }}>
          {reconcileError}
        </p>
      )}
    </>
  );
}
