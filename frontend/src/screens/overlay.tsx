// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plug } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  SectionHeader,
  TextInput,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { formatDateTime } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import {
  canManageOverlay,
  ProblemError,
  problemCode,
  throwProblem,
  useMe,
} from "./common";
import { converged, OverlayLiveSection } from "./overlay-health";

// The overlay card (Settings → Integrations): the incumbent connection
// lifecycle plus the two health reads the backend already serves. Every
// field shown is a server fact, never a claim — `headroom` (rendered in
// overlay-health.tsx) prints verbatim because the server may answer the
// `~unknown` sentinel, and a computed substitute would be a fabricated
// number. Mirrors connectors.tsx's shape (structure, ConfirmModal usage,
// Badge tones, error handling) against a different set of endpoints — read
// that file first if this one is confusing on its own. The sync-status/
// budget rendering lives in the companion overlay-health.tsx purely to
// keep this file under the length cap, the same split connectors.tsx
// itself uses for connector-status.tsx.

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

// A query queued via throwProblem carries the RFC-7807 code on `.problem`;
// anything else (a network exception, say) never claims a server code.
function overlayProblemCode(error: unknown): string | null {
  return error instanceof ProblemError ? problemCode(error.problem) : null;
}

// The connect/reconnect form: region + private-app token, shared by the
// not-yet-connected empty state and a revoked connection's Reconnect
// affordance. A non-admin/ops seat sees an honest note instead of a form it
// could only submit into a 403 (connect/disconnect are admin/ops-only,
// identity/internal/policy's overlay_connection posture) — the server stays
// the RBAC authority regardless.
function OverlayConnectForm({
  canManage,
  reconnect,
  pending,
  error,
  onSubmit,
}: Readonly<{
  canManage: boolean;
  reconnect: boolean;
  pending: boolean;
  error: string | null;
  onSubmit: (region: Region, token: string) => void;
}>) {
  const t = useT();
  const [region, setRegion] = useState<Region>("eu1");
  const [token, setToken] = useState("");
  if (!canManage) {
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
        onSubmit(region, token);
      }}
    >
      <div className="field">
        <label className="t-label" htmlFor="overlay-region">
          {t("overlay.region")}
        </label>
        <select
          id="overlay-region"
          className="input"
          value={region}
          onChange={(event) => setRegion(event.target.value as Region)}
        >
          {REGIONS.map((r) => (
            <option key={r} value={r}>
              {t(regionLabel[r])}
            </option>
          ))}
        </select>
      </div>
      <div className="field">
        <label className="t-label" htmlFor="overlay-token">
          {t("overlay.token")}
        </label>
        <TextInput
          id="overlay-token"
          type="password"
          autoComplete="off"
          value={token}
          required
          onChange={(event) => setToken(event.target.value)}
        />
        <p className="t-caption">{t("overlay.tokenHint")}</p>
      </div>
      {error && (
        <p
          role="alert"
          className="t-caption"
          style={{ color: "var(--danger)" }}
        >
          {error}
        </p>
      )}
      <div style={{ display: "flex", gap: "var(--space-2)" }}>
        <Button
          small
          variant="primary"
          type="submit"
          disabled={!ready || pending}
        >
          <Plug aria-hidden />{" "}
          {pending
            ? t("overlay.connecting")
            : reconnect
              ? t("overlay.reconnect")
              : t("overlay.connect")}
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

export function OverlayCard() {
  const t = useT();
  const { locale } = useLocale();
  const me = useMe();
  const canManage = canManageOverlay(me.data?.roles);
  const queryClient = useQueryClient();
  const [confirmingDisconnect, setConfirmingDisconnect] = useState(false);

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
    onSuccess: () => queryClient.invalidateQueries(),
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
    connection.isError &&
    overlayProblemCode(connection.error) === "not_implemented";
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
            canManage={canManage}
            reconnect={false}
            pending={connect.isPending}
            error={connect.isError ? connect.error.message : null}
            onSubmit={(region, token) =>
              connect.mutate({ region, privateAppToken: token })
            }
          />
        </EmptyState>
      )}
      {connection.isSuccess && connection.data && (
        <>
          <ConnectionSummary connection={connection.data} locale={locale} />
          {connection.data.status === "revoked" && (
            <div style={{ marginTop: "var(--space-3)" }}>
              <OverlayConnectForm
                canManage={canManage}
                reconnect
                pending={connect.isPending}
                error={connect.isError ? connect.error.message : null}
                onSubmit={(region, token) =>
                  connect.mutate({ region, privateAppToken: token })
                }
              />
            </div>
          )}
          {live && (
            <OverlayLiveSection
              sync={sync}
              budget={budget}
              locale={locale}
              canManage={canManage}
              onReconcile={() => reconcile.mutate()}
              reconcilePending={reconcile.isPending}
              reconcileQueued={reconcile.isSuccess}
              reconcileError={
                reconcile.isError ? reconcile.error.message : null
              }
              onDisconnect={() => setConfirmingDisconnect(true)}
            />
          )}
        </>
      )}
      <ConfirmModal
        open={confirmingDisconnect}
        onClose={() => setConfirmingDisconnect(false)}
        title={t("overlay.disconnectTitle")}
        confirmLabel={t("overlay.disconnect")}
        confirmVariant="danger"
        pending={disconnect.isPending}
        error={disconnect.isError ? disconnect.error.message : null}
        onConfirm={() => disconnect.mutate()}
      >
        <p className="t-small">{t("overlay.disconnectBody")}</p>
      </ConfirmModal>
    </Card>
  );
}
