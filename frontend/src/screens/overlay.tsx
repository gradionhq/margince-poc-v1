// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plug } from "lucide-react";
import { useEffect, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import { isOption } from "../app/options";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  Field,
  SectionHeader,
  Select,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { formatDateTime } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import type { QueryLike } from "./common";
import { problemCodeOf, throwProblem } from "./common";
import type { Budget, SyncStatus } from "./overlay-health";
import { converged, OverlayLiveSection } from "./overlay-health";

// The overlay card (Settings → Integrations): the incumbent connection
// lifecycle plus the two health reads the backend already serves. Every
// field shown is a server fact, never a claim — `headroom` (rendered in
// overlay-health.tsx) prints verbatim because the server may answer the
// `~unknown` sentinel, and a computed substitute would be a fabricated
// number. Mirrors connectors.tsx's shape (structure, ConfirmModal usage,
// Badge tones, error handling) against a different set of endpoints — read
// that file first if this one is confusing on its own. The sync-status/
// budget rendering lives in the companion overlay-health.tsx, split out
// purely to keep this file under the length cap and its own functions
// under the cognitive-complexity gate — that file has exactly one caller
// (this one), unlike connector-status.tsx's genuine two-caller reuse
// (connectors.tsx and home.tsx).
//
// Connect/reconnect is confirm-first, the same posture as Disconnect: both
// flip `workspace.x_sor_mode` for the whole installation (every seat's
// reads switch source, and writes the mirror can't serve become
// read-only), so filling in a token must never fire the mutation by
// itself — OverlayConnectForm only ever hands the typed
// (region, token) up to a request-confirm callback; OverlayCard owns the
// ConfirmModal that actually calls `connect.mutate`.

type Connection = components["schemas"]["OverlayConnection"];
type ConnectionStatus = Connection["status"];

type Region = "eu1" | "us";
const REGIONS: Region[] = ["eu1", "us"];
const regionLabel: Record<Region, MessageKey> = {
  eu1: "overlay.regionEu1",
  us: "overlay.regionUs",
};

const STATUS_TONE: Record<ConnectionStatus, "success" | "warn" | "danger"> = {
  active: "success",
  revoked: "warn",
  error: "danger",
};
const STATUS_LABEL: Record<ConnectionStatus, MessageKey> = {
  active: "overlay.statusActive",
  revoked: "overlay.statusRevoked",
  error: "overlay.statusError",
};

// The connect/reconnect form: region + private-app token, shared by the
// not-yet-connected empty state and a revoked connection's Reconnect
// affordance. A non-admin/ops seat sees an honest note instead of a form it
// could only submit into a 403 (connect/disconnect are admin/ops-only,
// identity/internal/policy's overlay_connection posture) — the server stays
// the RBAC authority regardless. Submitting never mutates directly: it only
// hands the typed values to `onRequestConfirm`, which OverlayCard uses to
// open the shared connect ConfirmModal — the actual `POST
// /overlay/connection` fires only from that modal's confirm, never from
// this form's own submit.
function OverlayConnectForm({
  canConnect,
  reconnect,
  onRequestConfirm,
}: Readonly<{
  canConnect: boolean;
  reconnect: boolean;
  onRequestConfirm: (region: Region, token: string) => void;
}>) {
  const t = useT();
  const [region, setRegion] = useState<Region>("eu1");
  const [token, setToken] = useState("");
  if (!canConnect) {
    return <p className="t-small">{t("overlay.adminOnly")}</p>;
  }
  const ready = token.trim() !== "";
  return (
    <form
      className="form-stack"
      onSubmit={(event) => {
        event.preventDefault();
        if (!ready) {
          return;
        }
        onRequestConfirm(region, token);
      }}
    >
      <Field label={t("overlay.region")}>
        {(control) => (
          <Select
            {...control}
            value={region}
            onChange={(event) => {
              const value = event.target.value;
              if (isOption(value, REGIONS)) setRegion(value);
            }}
          >
            {REGIONS.map((r) => (
              <option key={r} value={r}>
                {t(regionLabel[r])}
              </option>
            ))}
          </Select>
        )}
      </Field>
      <Field label={t("overlay.token")} hint={t("overlay.tokenHint")}>
        {(control) => (
          <TextInput
            {...control}
            type="password"
            autoComplete="off"
            value={token}
            required
            onChange={(event) => setToken(event.target.value)}
          />
        )}
      </Field>
      <div style={{ display: "flex", gap: "var(--space-2)" }}>
        <Button small variant="primary" type="submit" disabled={!ready}>
          <Plug aria-hidden />{" "}
          {reconnect ? t("overlay.reconnect") : t("overlay.connect")}
        </Button>
      </div>
    </form>
  );
}

// The connected header row (status badge, connected-at, region) — split out
// of OverlayCard purely to keep that function's branch count under the
// cognitive-complexity gate.
function ConnectionSummary({
  connection,
  locale,
}: Readonly<{ connection: Connection; locale: Locale }>) {
  const t = useT();
  return (
    <div
      style={{
        display: "flex",
        gap: "var(--space-2)",
        alignItems: "center",
        flexWrap: "wrap",
      }}
    >
      <Badge tone={STATUS_TONE[connection.status]}>
        {t(STATUS_LABEL[connection.status])}
      </Badge>
      <span className="t-small">
        {t("overlay.connectedAt", {
          at: formatDateTime(connection.connectedAt, locale, "Europe/Berlin"),
        })}
      </span>
      <span className="t-small">
        {t("overlay.region")}: {connection.region}
      </span>
    </div>
  );
}

// The full "a connection exists" body (summary + revoked reconnect + live
// health/actions) — split out of OverlayCard purely to keep that function's
// branch count under the cognitive-complexity gate.
function ConnectedBody({
  connection,
  locale,
  canConnect,
  canReconcile,
  canDisconnect,
  live,
  sync,
  budget,
  onReconnectRequest,
  onReconcile,
  reconcilePending,
  reconcileQueued,
  reconcileError,
  onDisconnect,
}: Readonly<{
  connection: Connection;
  locale: Locale;
  canConnect: boolean;
  canReconcile: boolean;
  canDisconnect: boolean;
  live: boolean;
  sync: QueryLike<SyncStatus>;
  budget: QueryLike<Budget>;
  onReconnectRequest: (region: Region, token: string) => void;
  onReconcile: () => void;
  reconcilePending: boolean;
  reconcileQueued: boolean;
  reconcileError: string | null;
  onDisconnect: () => void;
}>) {
  return (
    <>
      <ConnectionSummary connection={connection} locale={locale} />
      {connection.status === "revoked" && (
        <div style={{ marginTop: "var(--space-3)" }}>
          <OverlayConnectForm
            canConnect={canConnect}
            reconnect
            onRequestConfirm={onReconnectRequest}
          />
        </div>
      )}
      {live && (
        <OverlayLiveSection
          sync={sync}
          budget={budget}
          locale={locale}
          canReconcile={canReconcile}
          canDisconnect={canDisconnect}
          onReconcile={onReconcile}
          reconcilePending={reconcilePending}
          reconcileQueued={reconcileQueued}
          reconcileError={reconcileError}
          onDisconnect={onDisconnect}
        />
      )}
    </>
  );
}

export function OverlayCard() {
  const t = useT();
  const { locale } = useLocale();
  // Connecting binds an incumbent CRM, reconciling re-syncs the mirror, and
  // disconnecting purges it and flips the workspace back to native — create,
  // update and delete on the same object, and three different amounts of
  // damage.
  const canConnect = useCanWrite("overlay_connection", "create");
  const canReconcile = useCanWrite("overlay_connection", "update");
  const canDisconnect = useCanWrite("overlay_connection", "delete");
  const queryClient = useQueryClient();
  const [confirmingDisconnect, setConfirmingDisconnect] = useState(false);
  // Connect/reconnect is confirm-first (same posture as Disconnect below):
  // the form only stages the typed values here, never mutates directly —
  // the ConfirmModal rendered at the bottom of this component is the one
  // place `connect.mutate` is ever called.
  const [pendingConnect, setPendingConnect] = useState<{
    reconnect: boolean;
    region: Region;
    token: string;
  } | null>(null);

  // A confirmation must not outlive the grant that opened it. Hiding the modal
  // is not enough: the state behind it would still be set, so a grant that came
  // back — /me refetches on focus and after any 403 — would resurrect a
  // destructive confirmation nobody re-requested. Clear the state instead.
  useEffect(() => {
    if (!canConnect) {
      setPendingConnect(null);
    }
  }, [canConnect]);
  useEffect(() => {
    if (!canDisconnect) {
      setConfirmingDisconnect(false);
    }
  }, [canDisconnect]);

  const connection = useQuery({
    queryKey: ["overlay", "connection"],
    queryFn: async () => {
      const { data, error, response } = await api.GET(
        "/overlay/connection",
        {},
      );
      // 404 is "never connected" — the honest empty state, not an error.
      if (response.status === 404) {
        return null;
      }
      if (error) {
        throwProblem(error);
      }
      return data ?? null;
    },
  });

  // Sync and budget are readable whenever the workspace is in overlay mode —
  // an errored connection still has a mirror and a spent budget window to
  // report, so gating them on `active` alone would blank the very screen an
  // operator opens when something is wrong.
  const status = connection.data?.status;
  const live = status === "active" || status === "error";

  const sync = useQuery({
    queryKey: ["overlay", "sync-status"],
    enabled: live,
    queryFn: async () => {
      const { data, error } = await api.GET("/overlay/sync-status", {});
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    refetchInterval: (query) => (converged(query.state.data) ? false : 5000),
  });

  const budget = useQuery({
    queryKey: ["overlay", "budget"],
    enabled: live,
    queryFn: async () => {
      const { data, error } = await api.GET("/overlay/budget", {});
      if (error) {
        throwProblem(error);
      }
      return data;
    },
  });

  // The whole app's data source just changed — /me included. Invalidate
  // everything rather than clear(): clear() destroys the mounted ["me"]
  // observer without refetching it (see useLogout's note in common.tsx).
  const connect = useMutation({
    mutationFn: async (input: { region: string; privateAppToken: string }) => {
      const { error } = await api.POST("/overlay/connection", {
        body: { incumbent: "hubspot", ...input },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setPendingConnect(null);
      queryClient.invalidateQueries();
    },
  });

  // Disconnect flips the workspace back to native, same blast radius as
  // Connect flipping it to overlay — every cached read may now answer
  // differently, so this also invalidates everything rather than just the
  // overlay keys.
  const disconnect = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/overlay/connection", {});
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setConfirmingDisconnect(false);
      queryClient.invalidateQueries();
    },
  });

  // Reconcile only ever queues a sweep the worker runs later (202) — it
  // never touches anything the sync/budget reads wouldn't reflect once that
  // sweep lands, so only those two keys need invalidating.
  const reconcile = useMutation({
    mutationFn: async () => {
      const { error } = await api.POST("/overlay/reconcile", {});
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["overlay", "sync-status"] });
      queryClient.invalidateQueries({ queryKey: ["overlay", "budget"] });
    },
  });

  const notConfigured =
    connection.isError && problemCodeOf(connection.error) === "not_implemented";
  const loadFailed = connection.isError && !notConfigured;

  return (
    <Card>
      <SectionHeader title={t("overlay.title")} sub={t("overlay.sub")} />
      {connection.isPending && (
        <p className="t-small">{t("overlay.loading")}</p>
      )}
      {notConfigured && (
        <EmptyState>
          <p>{t("overlay.notConfigured")}</p>
        </EmptyState>
      )}
      {loadFailed && (
        <p className="t-small" style={{ color: "var(--danger)" }}>
          {connection.error instanceof Error
            ? connection.error.message
            : t("overlay.loadFailed")}
        </p>
      )}
      {connection.isSuccess && connection.data === null && (
        <EmptyState>
          <p>{t("overlay.empty")}</p>
          <OverlayConnectForm
            canConnect={canConnect}
            reconnect={false}
            onRequestConfirm={(region, token) =>
              setPendingConnect({ reconnect: false, region, token })
            }
          />
        </EmptyState>
      )}
      {connection.isSuccess && connection.data && (
        <ConnectedBody
          connection={connection.data}
          locale={locale}
          canConnect={canConnect}
          canReconcile={canReconcile}
          canDisconnect={canDisconnect}
          live={live}
          sync={sync}
          budget={budget}
          onReconnectRequest={(region, token) =>
            setPendingConnect({ reconnect: true, region, token })
          }
          onReconcile={() => reconcile.mutate()}
          reconcilePending={reconcile.isPending}
          reconcileQueued={reconcile.isSuccess}
          reconcileError={reconcile.isError ? reconcile.error.message : null}
          onDisconnect={() => setConfirmingDisconnect(true)}
        />
      )}
      <ConfirmModal
        open={pendingConnect !== null}
        onClose={() => setPendingConnect(null)}
        title={
          pendingConnect?.reconnect
            ? t("overlay.reconnectConfirmTitle")
            : t("overlay.connectConfirmTitle")
        }
        confirmLabel={
          pendingConnect?.reconnect
            ? t("overlay.reconnect")
            : t("overlay.connect")
        }
        pending={connect.isPending}
        error={connect.isError ? connect.error.message : null}
        onConfirm={() => {
          // Re-read at the moment of the write. /me refetches on focus and
          // after any 403, so a grant held when this dialog opened can be gone
          // by the time it is confirmed.
          if (!pendingConnect || !canConnect) {
            return;
          }
          connect.mutate({
            region: pendingConnect.region,
            privateAppToken: pendingConnect.token,
          });
        }}
      >
        <p className="t-small">{t("overlay.connectConfirmBody")}</p>
      </ConfirmModal>
      <ConfirmModal
        open={confirmingDisconnect}
        onClose={() => setConfirmingDisconnect(false)}
        title={t("overlay.disconnectTitle")}
        confirmLabel={t("overlay.disconnect")}
        confirmVariant="danger"
        pending={disconnect.isPending}
        error={disconnect.isError ? disconnect.error.message : null}
        onConfirm={() => {
          if (!canDisconnect) {
            return;
          }
          disconnect.mutate();
        }}
      >
        <p className="t-small">{t("overlay.disconnectBody")}</p>
      </ConfirmModal>
    </Card>
  );
}
