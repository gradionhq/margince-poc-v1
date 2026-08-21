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
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDateTime } from "../format/format";
import { viewerZone } from "../format/timezone";
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
// number. The sync-status/budget rendering lives in the companion
// overlay-health.tsx, split out purely to keep this file under the length
// cap and its own functions under the cognitive-complexity gate — that file
// has exactly one caller (this one), unlike connector-status.tsx's genuine
// two-caller reuse (connectors.tsx and home.tsx).
//
// The card is ONE settings row: what the installation is bound to, and the
// verb that binds it. Region + private-app token are two inputs submitted
// together, which is never an answer that fits in a row's right column — so
// they live behind that verb in a dialog, and the row shows what is set now.
//
// Connect/reconnect stays confirm-first, the same posture as Disconnect: both
// flip `workspace.x_sor_mode` for the whole installation (every seat's reads
// switch source, and writes the mirror can't serve become read-only). Putting
// the two fields inside the CONFIRM rather than behind a second dialog is what
// preserves that: the sentence naming the blast radius is on screen while the
// token is being pasted, and the only press that POSTs is the one beneath it.

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

// What the connection IS — status, when it was bound, which region — as the
// row's answer rather than as a block of its own. A span, not a div: it sits
// inside the row's `value`, which is phrasing content.
function ConnectionSummary({
  connection,
  locale,
}: Readonly<{ connection: Connection; locale: Locale }>) {
  const t = useT();
  return (
    <span className="overlay-facts">
      <Badge tone={STATUS_TONE[connection.status]}>
        {t(STATUS_LABEL[connection.status])}
      </Badge>
      <span className="t-small">
        {t("overlay.connectedAt", {
          at: formatDateTime(connection.connectedAt, locale, viewerZone()),
        })}
      </span>
      <span className="t-small">
        {t("overlay.region")}: {connection.region}
      </span>
    </span>
  );
}

// Everything the card says INSTEAD of a connection row: the read is still in
// flight, this deployment never wired overlay mode, or the read failed. None of
// the three is an ANSWER to a question, so none of them is a row — they sit
// above the list, where the sentence introducing the card already is.
function ConnectionNotice({
  query,
}: Readonly<{ query: QueryLike<Connection | null> }>) {
  const t = useT();
  if (query.isPending) {
    return <p className="t-small">{t("overlay.loading")}</p>;
  }
  if (query.isError) {
    // A deployment with no overlay adapter is a documented configuration, not a
    // failure, and must not read as one.
    return problemCodeOf(query.error) === "not_implemented" ? (
      <EmptyState>
        <p>{t("overlay.notConfigured")}</p>
      </EmptyState>
    ) : (
      <Callout tone="danger" live="alert">
        {problemMessageOf(query.error, t, t("overlay.loadFailed"))}
      </Callout>
    );
  }
  return null;
}

// The verb that binds or re-binds the incumbent, in the row's right column.
//
// A seat without the create grant keeps the control and is TOLD why rather than
// losing the row: `Button`'s `reason` refuses the press, renders the sentence
// and wires `aria-describedby` from the button itself. An absent verb on a card
// that reports a live mirror would read as "there is nothing to connect", which
// is a claim about the installation standing in for one about authority.
//
// Nothing at all until the /me probe has ANSWERED. Every grant reads false
// while it is in flight, so branching on the answer alone flashed "this is
// admin-only" at an admin on every load of this tab.
function ConnectVerb({
  canConnect,
  rolesKnown,
  reconnect,
  onOpen,
}: Readonly<{
  canConnect: boolean;
  rolesKnown: boolean;
  reconnect: boolean;
  onOpen: () => void;
}>) {
  const t = useT();
  if (!rolesKnown) {
    return null;
  }
  return (
    <Button
      small
      variant="primary"
      reason={canConnect ? undefined : t("overlay.adminOnly")}
      onClick={onOpen}
    >
      <Plug aria-hidden />{" "}
      {reconnect ? t("overlay.reconnect") : t("overlay.connect")}
    </Button>
  );
}

// Region + token, and the confirm they are submitted through. Mounted only
// while it is open, which is how the pasted token dies with a cancelled
// attempt instead of sitting in component state until the next one.
function ConnectSetupModal({
  reconnect,
  pending,
  error,
  onClose,
  onConnect,
}: Readonly<{
  reconnect: boolean;
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onConnect: (region: Region, token: string) => void;
}>) {
  const t = useT();
  const [region, setRegion] = useState<Region>("eu1");
  const [token, setToken] = useState("");
  return (
    <ConfirmModal
      open
      onClose={onClose}
      title={
        reconnect
          ? t("overlay.reconnectConfirmTitle")
          : t("overlay.connectConfirmTitle")
      }
      confirmLabel={reconnect ? t("overlay.reconnect") : t("overlay.connect")}
      confirmDisabled={token.trim() === ""}
      pending={pending}
      error={error}
      onConfirm={() => onConnect(region, token.trim())}
    >
      <p className="t-small">{t("overlay.connectConfirmBody")}</p>
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
    </ConfirmModal>
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
  // Which dialog is open, and whether it re-binds an existing connection.
  const [connecting, setConnecting] = useState<{ reconnect: boolean } | null>(
    null,
  );

  // A confirmation must not outlive the grant that opened it. Hiding the dialog
  // is not enough: the state behind it would still be set, so a grant that came
  // back — /me refetches on focus and after any 403 — would resurrect a
  // destructive confirmation nobody re-requested. Clear the state instead.
  useEffect(() => {
    if (!canConnect) {
      setConnecting(null);
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
      setConnecting(null);
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
  const revoked = connected?.status === "revoked";
  // There is something to bind only when nothing is bound yet, or when what is
  // bound has been revoked. An active or errored mirror is steered from the
  // panel's action band instead: Sync now and Disconnect act on the whole
  // connection and carry their own notes, which is what that band is for.
  const offersConnect = connection.isSuccess && (connected === null || revoked);

  return (
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
      <PanelBody>
        <p className="settings-panel-sub">{t("overlay.sub")}</p>
        <ConnectionNotice query={connection} />
        {connection.isSuccess && (
          <SettingList>
            <SettingRow
              label={t("overlay.connectionLabel")}
              description={connected === null ? t("overlay.empty") : undefined}
              value={
                connected ? (
                  <ConnectionSummary connection={connected} locale={locale} />
                ) : (
                  t("overlay.notConnectedYet")
                )
              }
              control={
                offersConnect ? (
                  <ConnectVerb
                    canConnect={canConnect}
                    rolesKnown={rolesKnown}
                    reconnect={revoked}
                    onOpen={() => setConnecting({ reconnect: revoked })}
                  />
                ) : null
              }
            />
          </SettingList>
        )}
      </PanelBody>
      {live && (
        <OverlayLiveSection sync={sync} budget={budget} locale={locale} />
      )}
      {connecting && (
        <ConnectSetupModal
          reconnect={connecting.reconnect}
          pending={connect.isPending}
          error={connect.isError ? problemMessageOf(connect.error, t) : null}
          onClose={() => setConnecting(null)}
          onConnect={(region, token) => {
            // Re-read at the moment of the write. /me refetches on focus and
            // after any 403, so a grant held when this dialog opened can be
            // gone by the time it is confirmed.
            if (!canConnect) {
              return;
            }
            connect.mutate({ region, privateAppToken: token });
          }}
        />
      )}
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
