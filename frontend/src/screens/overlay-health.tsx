// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { RefreshCw } from "lucide-react";
import type { components } from "../api/schema";
import { Badge, Button, SectionHeader } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { PanelPlate } from "../design-system/panel";
import { Meter } from "../design-system/readings";
import { SurfaceState } from "../design-system/surfacestate";
import { formatDateTime } from "../format/format";
import { viewerZone } from "../format/timezone";
import { type Locale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, type QueryLike } from "./common";
import "./overlay.css";

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
type SyncState = NonNullable<SyncObject["state"]>;

// Keyed on the schema's own enum (not a bare `string`) so adding a state/band
// upstream is a compile error here until this map catches up — but the
// server is a separately-deployed process, so a value it sends can still
// outrun what this build was generated against. `Partial` makes that gap
// visible in the type (`MessageKey | undefined`) rather than let a stale map
// silently promise every key exists; labelOrRaw's fallback is what actually
// closes it at render time — see below.
const SYNC_STATE_TONE: Partial<
  Record<SyncState, "success" | "warn" | "danger">
> = {
  fresh: "success",
  pending_sync: "warn",
  stale: "danger",
};
const SYNC_STATE_LABEL: Partial<Record<SyncState, MessageKey>> = {
  fresh: "overlay.syncStateFresh",
  pending_sync: "overlay.syncStatePending",
  stale: "overlay.syncStateStale",
};

const BAND_TONE: Partial<Record<BudgetBand, "success" | "warn" | "danger">> = {
  ok: "success",
  warn: "warn",
  shed: "danger",
};
const BAND_LABEL: Partial<Record<BudgetBand, MessageKey>> = {
  ok: "overlay.bandOk",
  warn: "overlay.bandWarn",
  shed: "overlay.bandShed",
};

// A state/band this build doesn't recognize must never render as blank or
// as the literal string "undefined" (t(undefined) does exactly that) — the
// honest fallback is the server's own raw value, the same "never fabricate,
// never hide a server fact" rule `headroom` above already follows.
function labelOrRaw<K extends string>(
  t: ReturnType<typeof useT>,
  map: Partial<Record<K, MessageKey>>,
  value: K,
): string {
  const key = map[value];
  return key ? t(key) : value;
}

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

// Which state one of these two health reads is in.
//
// A failure is deliberately NOT one of the answers: both callers render the
// server's own words for that case and skip this surface entirely, because the
// generic "this did not load" sentence would replace a message that says which
// thing to go and fix. `empty` is reserved for a round trip that came back and
// carried nothing, which is the only case that may claim there is none — a
// failed read carries zero rows too, and drawing it the same way states a fact
// about the mirror that nobody managed to ask for.
function readingState(
  pending: boolean,
  count: number,
): "loading" | "empty" | "ready" {
  if (pending) {
    return "loading";
  }
  return count === 0 ? "empty" : "ready";
}

function SyncStatusPanel({
  query,
  locale,
}: Readonly<{
  query: QueryLike<SyncStatus>;
  locale: Locale;
}>) {
  const t = useT();
  const objects: SyncObject[] = query.data?.objects ?? [];
  // The heading stays put through every state. It used to be returned past on
  // the way out of a pending or failed read, so the card lost the name of the
  // thing that was loading at exactly the moment a reader needed it.
  return (
    <section className="overlay-section">
      <SectionHeader title={t("overlay.syncTitle")} level={3} />
      {query.isError ? (
        <Callout tone="danger" live="alert">
          {problemMessageOf(query.error, t, t("overlay.syncLoadFailed"))}
        </Callout>
      ) : (
        <SurfaceState
          state={readingState(query.isPending, objects.length)}
          emptyLabel={t("overlay.syncEmpty")}
        >
          <ul className="overlay-sync-list">
            {objects.map((o, i) => (
              <li key={o.object ?? i} className="overlay-sync-row">
                <span className="t-mono overlay-object">{o.object ?? "—"}</span>
                <Badge tone={o.state ? SYNC_STATE_TONE[o.state] : undefined}>
                  {o.state ? labelOrRaw(t, SYNC_STATE_LABEL, o.state) : "—"}
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
                          viewerZone(),
                        ),
                      })
                    : t("overlay.neverSynced")}
                </span>
              </li>
            ))}
          </ul>
        </SurfaceState>
      )}
    </section>
  );
}

// The per-source REST breakdown line — split out of BudgetPanel purely to
// keep that function's branch count under the cognitive-complexity gate.
function BudgetSourcesLine({
  sources,
}: Readonly<{ sources: NonNullable<Budget["sources"]> }>) {
  const t = useT();
  return (
    <p className="t-small overlay-budget-detail">
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
    <div className="overlay-facts overlay-budget-detail">
      <span className="t-small">
        {t("overlay.budgetSearch", {
          consumed: search.consumed ?? 0,
          limit: search.limit ?? "—",
        })}
      </span>
      {search.band && (
        <Badge tone={BAND_TONE[search.band]}>
          {labelOrRaw(t, BAND_LABEL, search.band)}
        </Badge>
      )}
    </div>
  );
}

// The window's own figures. Split out of BudgetPanel so that function is the
// state machine and this is the reading, rather than one function being both.
function BudgetReading({ budget }: Readonly<{ budget: Budget }>) {
  const t = useT();
  const limit = budget.limit;
  const consumed = budget.consumed ?? 0;
  return (
    <>
      <div className="overlay-facts">
        {budget.band && (
          <Badge tone={BAND_TONE[budget.band]}>
            {labelOrRaw(t, BAND_LABEL, budget.band)}
          </Badge>
        )}
        <span className="t-mono t-small">
          {consumed} / {limit ?? "—"}
        </span>
        {/* headroom is either a real free-capacity count or the server's own
            `~unknown` sentinel — printed verbatim either way, never
            recomputed from consumed/limit (which would fabricate a number
            the server explicitly declined to attribute). */}
        <span className="t-small">
          {t("overlay.budgetHeadroom", { headroom: budget.headroom ?? "—" })}
        </span>
      </div>
      {/* consumed-out-of-limit IS a proportion, so it is drawn as one. Only
          when the server actually stated a limit: a bar against an unknown
          maximum would invent the very denominator `headroom` refuses to. */}
      {limit !== undefined && limit > 0 && (
        <div className="overlay-budget-meter">
          <Meter
            value={consumed}
            max={limit}
            label={t("overlay.budgetTitle")}
          />
        </div>
      )}
      {budget.sources && <BudgetSourcesLine sources={budget.sources} />}
      {budget.search && <BudgetSearchRow search={budget.search} />}
    </>
  );
}

// A budget window the server answered with nothing is NOT the same fact as an
// installation that has no budget window at all, and returning null said the
// second when only the first was true: the whole section vanished, so "the read
// came back empty" and "this deployment does not meter the incumbent" drew
// exactly the same thing — nothing. The section keeps its place and names which
// silence it is in.
function BudgetPanel({ query }: Readonly<{ query: QueryLike<Budget> }>) {
  const t = useT();
  return (
    <section className="overlay-section">
      <SectionHeader title={t("overlay.budgetTitle")} level={3} />
      {/* A failure keeps the server's own detail rather than the primitive's
          generic sentence: a budget read the incumbent refused for a bad token
          is a different thing to go and fix from one that timed out, and only
          the server knows which. */}
      {query.isError ? (
        <Callout tone="danger" live="alert">
          {problemMessageOf(query.error, t, t("overlay.budgetLoadFailed"))}
        </Callout>
      ) : (
        <SurfaceState
          state={readingState(query.isPending, query.data ? 1 : 0)}
          emptyLabel={t("overlay.budgetEmpty")}
        >
          {query.data && <BudgetReading budget={query.data} />}
        </SurfaceState>
      )}
    </section>
  );
}

// The live-mode READINGS: mirror freshness and the incumbent's budget window,
// on the panel's recessed plate. Shown from overlay.tsx whenever the connection
// is `active` or `error` (see OverlayCard's own `live` doc), never gated
// further here.
//
// The verbs used to hang off the bottom of this section, which put Disconnect —
// the button that re-points every read in the installation — at the very end of
// two read-only panels, in ordinary body chrome, while Connect sat centred in an
// empty state at the top of the same card. They are OverlayLiveActions now, and
// the card hands them to the panel's own action band, so both halves of the same
// decision are drawn in the same place whichever one is available.
export function OverlayLiveSection({
  sync,
  budget,
  locale,
}: Readonly<{
  sync: QueryLike<SyncStatus>;
  budget: QueryLike<Budget>;
  locale: Locale;
}>) {
  return (
    <PanelPlate>
      <SyncStatusPanel query={sync} locale={locale} />
      <BudgetPanel query={budget} />
    </PanelPlate>
  );
}

// The two verbs a live mirror offers, for the panel's action band.
export function OverlayLiveActions({
  canReconcile,
  canDisconnect,
  rolesKnown,
  onReconcile,
  reconcilePending,
  reconcileQueued,
  reconcileError,
  onDisconnect,
}: Readonly<{
  // Two grants, not one: reconciling re-syncs the mirror
  // (overlay_connection:update) while disconnecting tears it down and flips
  // the workspace back to native (overlay_connection:delete).
  canReconcile: boolean;
  canDisconnect: boolean;
  // Whether the /me probe has ANSWERED. Both grants read false while it is in
  // flight, so the withheld sentence below would otherwise flash at an operator
  // on every load — the same defect the connect form carried.
  rolesKnown: boolean;
  onReconcile: () => void;
  reconcilePending: boolean;
  reconcileQueued: boolean;
  reconcileError: string | null;
  onDisconnect: () => void;
}>) {
  const t = useT();
  return (
    <>
      {canReconcile && (
        <Button small onClick={onReconcile} disabled={reconcilePending}>
          <RefreshCw aria-hidden /> {t("overlay.reconcile")}
        </Button>
      )}
      {canDisconnect && (
        <Button small variant="danger" onClick={onDisconnect}>
          {t("overlay.disconnect")}
        </Button>
      )}
      {/* Neither grant, and this band renders ONLY on an installation that is
          already in overlay mode — so a rep/manager seat is looking at live sync
          freshness and a spending budget with nowhere to act. Dropping the row
          silently makes that read as a mirror nobody can steer; the sentence
          makes it read as a mirror that is not theirs to steer. */}
      {rolesKnown && !canReconcile && !canDisconnect && (
        <p className="t-small overlay-action-note">{t("overlay.adminOnly")}</p>
      )}
      {reconcileQueued && (
        <p className="t-small overlay-action-note">
          {t("overlay.reconcileQueued")}
        </p>
      )}
      {reconcileError && (
        <Callout tone="danger" live="alert" className="overlay-action-note">
          {reconcileError}
        </Callout>
      )}
    </>
  );
}
