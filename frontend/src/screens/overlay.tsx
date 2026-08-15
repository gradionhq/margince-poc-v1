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
  EmptyState,
  Field,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { Select } from "../design-system/select";
import { formatDateTime } from "../format/format";
import { type Locale, useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import type { QueryLike } from "./common";
import { problemCodeOf, problemMessageOf, throwProblem, useMe } from "./common";
import {
  converged,
  OverlayLiveActions,
  OverlayLiveSection,
} from "./overlay-health";
import "./overlay.css";

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
  rolesKnown,
  reconnect,
  onRequestConfirm,
}: Readonly<{
  canConnect: boolean;
  // Whether the /me probe has ANSWERED. `canConnect` fails closed while the
  // probe is in flight, so branching on it alone flashed "this is admin-only"
  // at an admin on every single load of this tab — the neighbouring webhooks
  // card has gated on the probe since it was written, and these two cards sat
  // on the same page making opposite decisions about the same question.
  rolesKnown: boolean;
  reconnect: boolean;
  onRequestConfirm: (region: Region, token: string) => void;
}>) {
  const t = useT();
  const [region, setRegion] = useState<Region>("eu1");
  const [token, setToken] = useState("");
  if (!canConnect) {
    // Nothing at all until the probe answers: an unknown grant is not a denial,
    // and saying so before we know is a false statement the reader then has to
    // watch retract itself.
    return rolesKnown ? (
      <p className="t-small">{t("overlay.adminOnly")}</p>
    ) : null;
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
            onChange={(value) => {
              if (isOption(value, REGIONS)) setRegion(value);
            }}
            options={REGIONS.map((r) => ({
              value: r,
              label: t(regionLabel[r]),
            }))}
          />
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
      <div className="overlay-facts">
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
    <div className="overlay-facts">
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

// The padded half of a card whose connection exists: what the connection IS,
// and the reconnect form a revoked one offers. The live health readings are the
// panel's plate and the verbs are its action band, both handed to Panel by
// OverlayCard — split out here purely to keep that function's branch count
// under the cognitive-complexity gate.
function ConnectedBody({
  connection,
  locale,
  canConnect,
  rolesKnown,
  sub,
  onReconnectRequest,
}: Readonly<{
  connection: Connection;
  locale: Locale;
  canConnect: boolean;
  rolesKnown: boolean;
  // The card's own sentence, rendered HERE rather than in a body of its own:
  // two stacked PanelBodies pay the panel's inner interval twice, and the gap
  // read as a missing element between the sentence and the connection it
  // describes.
  sub: string;
  onReconnectRequest: (region: Region, token: string) => void;
}>) {
  return (
    <PanelBody>
      <p className="t-caption">{sub}</p>
      <ConnectionSummary connection={connection} locale={locale} />
      {connection.status === "revoked" && (
        <div className="overlay-connect">
          <OverlayConnectForm
            canConnect={canConnect}
            rolesKnown={rolesKnown}
            reconnect
            onRequestConfirm={onReconnectRequest}
          />
        </div>
      )}
    </PanelBody>
  );
}

// Everything the card shows BEFORE a connection exists: the three states a
// first read can land in, and — when it lands on "never connected" — the form
// that makes one.
//
// The form is not inside an EmptyState any more. `.empty` is `text-align:
// center; font-size: 13px; color: --textMeta`, which is the right chrome for a
// sentence and the wrong chrome for a dropdown, a password field, two field
// labels and a submit: the one form on this tab that binds an entire incumbent
// CRM was rendering centred, small and grey, reading as a caption about a form
// rather than as the form.
function UnconnectedBody({
  query,
  canConnect,
  rolesKnown,
  sub,
  onConnectRequest,
}: Readonly<{
  query: QueryLike<Connection | null>;
  canConnect: boolean;
  rolesKnown: boolean;
  sub: string;
  onConnectRequest: (region: Region, token: string) => void;
}>) {
  const t = useT();
  const notConfigured =
    query.isError && problemCodeOf(query.error) === "not_implemented";
  return (
    <PanelBody>
      <p className="t-caption">{sub}</p>
      {query.isPending && <p className="t-small">{t("overlay.loading")}</p>}
      {notConfigured && (
        <EmptyState>
          <p>{t("overlay.notConfigured")}</p>
        </EmptyState>
      )}
      {query.isError && !notConfigured && (
        <Callout tone="danger" live="alert">
          {problemMessageOf(query.error, t, t("overlay.loadFailed"))}
        </Callout>
      )}
      {!query.isPending && !query.isError && (
        <>
          <p className="t-small">{t("overlay.empty")}</p>
          <div className="overlay-connect">
            <OverlayConnectForm
              canConnect={canConnect}
              rolesKnown={rolesKnown}
              reconnect={false}
              onRequestConfirm={onConnectRequest}
            />
          </div>
        </>
      )}
    </PanelBody>
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
  // The probe itself, not just its answer: every grant above reads false while
  // /me is in flight, so a branch on their absence alone flashes "admin only"
  // at an admin on every load.
  const me = useMe();
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

  const connected = connection.data ?? null;
  const rolesKnown = !me.isPending;

  return (
    // Connect and Disconnect are the same decision in two directions, so they
    // are drawn in the same place: the panel's own action band. Before, Connect
    // sat centred inside an empty state at the TOP of the card and Disconnect
    // hung off the BOTTOM behind two read-only panels, in different chrome —
    // and the one at the bottom is the one that re-points every read in the
    // installation.
    <Panel
      title={t("overlay.title")}
      actions={
        live ? (
          <OverlayLiveActions
            canReconcile={canReconcile}
            canDisconnect={canDisconnect}
            rolesKnown={rolesKnown}
            onReconcile={() => reconcile.mutate()}
            reconcilePending={reconcile.isPending}
            reconcileQueued={reconcile.isSuccess}
            reconcileError={
              reconcile.isError ? problemMessageOf(reconcile.error, t) : null
            }
            onDisconnect={() => setConfirmingDisconnect(true)}
          />
        ) : undefined
      }
    >
      {connected ? (
        <ConnectedBody
          connection={connected}
          locale={locale}
          canConnect={canConnect}
          rolesKnown={rolesKnown}
          sub={t("overlay.sub")}
          onReconnectRequest={(region, token) =>
            setPendingConnect({ reconnect: true, region, token })
          }
        />
      ) : (
        <UnconnectedBody
          query={connection}
          canConnect={canConnect}
          rolesKnown={rolesKnown}
          sub={t("overlay.sub")}
          onConnectRequest={(region, token) =>
            setPendingConnect({ reconnect: false, region, token })
          }
        />
      )}
      {live && (
        <OverlayLiveSection sync={sync} budget={budget} locale={locale} />
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
        error={connect.isError ? problemMessageOf(connect.error, t) : null}
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
        error={
          disconnect.isError ? problemMessageOf(disconnect.error, t) : null
        }
        onConfirm={() => {
          if (!canDisconnect) {
            return;
          }
          disconnect.mutate();
        }}
      >
        <p className="t-small">{t("overlay.disconnectBody")}</p>
      </ConfirmModal>
    </Panel>
  );
}
