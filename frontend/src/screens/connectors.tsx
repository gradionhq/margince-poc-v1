import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Plug, RefreshCw, Send, X } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useRoute } from "../app/router";
import {
  Badge,
  Button,
  Card,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { BackfillPanel } from "./backfill";
import { problemCode, problemMessage } from "./common";
import {
  errorClassKey,
  missingSendGrant,
  statusLabel,
  statusTone,
} from "./connector-status";
import { ImapConnectForm } from "./imap-connect-form";
import { TelegramConnectForm } from "./telegram-connect-form";

// The connected-inboxes card (RC-8): the Settings surface the onboarding copy
// has always promised ("disconnect in one click", "manage in Settings"). It
// lists the live capture connections, lets a stale one reconnect (re-mint the
// same consent URL), and disconnects one in a single confirmed click.
// Every field shown is a server fact from GET /connectors — never a claim.

type CaptureConnection = components["schemas"]["CaptureConnection"];
type Provider = CaptureConnection["provider"];

const providerLabel: Record<Provider, MessageKey> = {
  gmail: "connectors.provGmail",
  gcal: "connectors.provGcal",
  graph: "connectors.provGraph",
  imap: "connectors.provImap",
};

// The OAuth providers whose reconnect re-mints a consent URL; imap reconnects
// (and first-connects) through the inline ImapConnectForm below instead, since
// a credential provider never redirects.
const OAUTH_PROVIDERS = new Set<Provider>(["gmail", "gcal", "graph"]);

// The full connector roster the "Add a connection" affordance offers from —
// the empty state shows all four, the footer shows whichever aren't already
// present in GET /connectors.
const ALL_PROVIDERS: Provider[] = ["gmail", "gcal", "graph", "imap"];

// Disconnecting an OAuth connection deletes OUR stored credential; it does
// not reach out to the vendor to revoke the grant on their side (there is no
// such API call here), so the confirm names the vendor-specific place a
// careful user can go finish that themselves. IMAP has no upstream grant —
// omitted entirely rather than shown as a no-op.
const OAUTH_DISCONNECT_NOTE: Partial<Record<Provider, MessageKey>> = {
  gmail: "connectors.disconnectBodyGoogleNote",
  gcal: "connectors.disconnectBodyGoogleNote",
  graph: "connectors.disconnectBodyMicrosoftNote",
};

// The OAuth callback lands back on #/settings/integrations/{outcome} — the
// route parses to id2 = "ok" | "denied" | "rejected" | "misconfigured" |
// "error". Only these are server-defined (contract-first); any other value is
// silently ignored rather than rendering a raw route segment. "rejected" and
// "misconfigured" exist so a failure nobody can fix by retrying doesn't tell
// the reader to retry: the provider refused the grant, or its API was never
// enabled for this deployment.
const OAUTH_OUTCOME_NOTE: Record<
  string,
  { key: MessageKey; tone: "success" | "danger" }
> = {
  ok: { key: "connectors.oauthOk", tone: "success" },
  denied: { key: "connectors.oauthDenied", tone: "danger" },
  rejected: { key: "connectors.oauthRejected", tone: "danger" },
  misconfigured: { key: "connectors.oauthMisconfigured", tone: "danger" },
  error: { key: "connectors.oauthError", tone: "danger" },
};

type ConnectorsResult = {
  // GET /connectors answers 501 code:not_implemented when this deployment
  // never wired mail capture (httperr.NotImplemented) — a calm, documented
  // feature-off state, never an error card (mirrors webhooks.tsx's
  // webhooks_not_configured treatment).
  notConfigured: boolean;
  data: CaptureConnection[];
};

// The OAuth return outcome (Task 2): the callback lands back on
// #/settings/integrations/{outcome} — id2 on that route only, never parsed
// from location.hash directly (the router already owns that). Split out of
// ConnectorsCard so its dismissal state and branching stay off that
// function's complexity budget. Dismissing (or navigating away, which
// unmounts this card) clears it; the list itself already refetches on
// mount, so "ok" needs no extra invalidation here.
function OAuthOutcomeNote() {
  const t = useT();
  const route = useRoute();
  const oauthOutcome =
    route.screen === "settings" && route.id === "integrations"
      ? route.id2
      : undefined;
  const [dismissedOutcome, setDismissedOutcome] = useState<string | null>(null);
  // Object.hasOwn, not a bare index: a route segment like "constructor" would
  // otherwise resolve to an inherited member and render an empty note.
  const note =
    oauthOutcome &&
    oauthOutcome !== dismissedOutcome &&
    Object.hasOwn(OAUTH_OUTCOME_NOTE, oauthOutcome)
      ? OAUTH_OUTCOME_NOTE[oauthOutcome]
      : undefined;
  if (!note) {
    return null;
  }
  return (
    <p
      role="status"
      className="t-small connector-oauth-note"
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "space-between",
        gap: "var(--space-2)",
        color: note.tone === "success" ? "var(--success)" : "var(--danger)",
      }}
    >
      <span>{t(note.key)}</span>
      <Button
        small
        variant="ghost"
        aria-label={t("connectors.dismissOutcome")}
        onClick={() => setDismissedOutcome(oauthOutcome ?? null)}
      >
        <X aria-hidden />
      </Button>
    </p>
  );
}

// The "Add a connection" affordance (Task 1): shared between the empty
// state and the roster footer — an OAuth pick connects+redirects, IMAP
// opens the inline form, and a provider-specific 501 renders a provider-
// named note. Split out of ConnectorsCard so this branching stays off that
// function's complexity budget (same reasoning as OAuthOutcomeNote above).
function ConnectorAddPanel({
  addable,
  pending,
  notConfigured501,
  onConnect,
  onImap,
}: Readonly<{
  addable: Provider[];
  pending: boolean;
  notConfigured501: Provider | null;
  onConnect: (provider: Provider) => void;
  onImap: () => void;
}>) {
  const t = useT();
  return (
    <>
      {(addable.includes("gcal") || addable.includes("gmail")) && (
        <p className="t-small">{t("connectors.googleSeparateNote")}</p>
      )}
      <div className="connector-add-actions">
        {addable.map((p) =>
          p === "imap" ? (
            <Button key={p} small onClick={onImap}>
              <Mail aria-hidden /> {t(providerLabel[p])}
            </Button>
          ) : (
            <Button
              key={p}
              small
              variant={p === "gmail" ? "primary" : undefined}
              disabled={pending}
              onClick={() => onConnect(p)}
            >
              <Plug aria-hidden /> {t(providerLabel[p])}
            </Button>
          ),
        )}
      </div>
      {notConfigured501 && (
        <p className="t-small" style={{ color: "var(--danger)" }}>
          {t("connectors.providerNotConfigured", {
            provider: t(providerLabel[notConfigured501]),
          })}
        </p>
      )}
    </>
  );
}

type ChannelConnection = components["schemas"]["ChannelConnection"];

type ChannelConnectionsResult = {
  // GET /channel-connections answers 503 when this deployment serves no
  // messaging channels, or has no credential store to seal a bot token in — a
  // calm, documented feature-off state, mirroring the mail card's 501
  // not_implemented treatment above rather than an error card.
  notConfigured: boolean;
  data: ChannelConnection[];
};

function useChannelConnections() {
  return useQuery({
    queryKey: ["channel-connections"],
    queryFn: async (): Promise<ChannelConnectionsResult> => {
      const { data, error, response } = await api.GET("/channel-connections");
      if (
        response.status === 503 &&
        (problemCode(error) === "channel_connections_not_configured" ||
          problemCode(error) === "channel_credentials_not_configured")
      ) {
        return { notConfigured: true, data: [] };
      }
      if (error) {
        throw new Error(problemMessage(error));
      }
      return { notConfigured: false, data: data.data };
    },
  });
}

function TelegramConnectionRow({
  connection,
  onEdit,
  onDisconnect,
}: Readonly<{
  connection: ChannelConnection;
  onEdit: () => void;
  onDisconnect: () => void;
}>) {
  const t = useT();
  return (
    <li className="connector-row">
      <span className="connector-id">
        <Send aria-hidden />
        <span>
          <strong>{t("connectors.provTelegram")}</strong>
          <span className="t-small connector-account">
            @{connection.channelLabel}
          </span>
        </span>
      </span>
      <span className="connector-actions">
        <Badge tone={statusTone(connection.status)}>
          {t(statusLabel(connection.status))}
        </Badge>
        <Button small onClick={onEdit}>
          <RefreshCw aria-hidden /> {t("connectors.telegramEditToken")}
        </Button>
        <Button small variant="ghost" onClick={onDisconnect}>
          {t("connectors.disconnect")}
        </Button>
      </span>
    </li>
  );
}

// One row per live bot, rendered from the server's own roster.
//
// A bot connects for the WHOLE workspace rather than per-user (Task 17,
// design §9.1/§9.2), and a send needs exactly one of them: with a second
// live bot the workspace can send nothing at all until an admin removes it.
// This panel is the only surface that can, so it must show every connection
// the list returns — a bot it hides is a bot nobody can disconnect.
//
// Editing goes through the SAME TelegramConnectForm modal, whose PATCH takes
// the place of a disconnect-reconnect cycle (§9.2). The panel mounts one
// form instance, keyed to whichever row opened it.
function TelegramConnectorPanel() {
  const t = useT();
  const qc = useQueryClient();
  const query = useChannelConnections();
  const [connectOpen, setConnectOpen] = useState(false);
  const [editingConnection, setEditingConnection] =
    useState<ChannelConnection | null>(null);
  const [disconnecting, setDisconnecting] = useState<ChannelConnection | null>(
    null,
  );

  const disconnect = useMutation({
    mutationFn: async (connection: ChannelConnection) => {
      const { error } = await api.DELETE("/channel-connections/{id}", {
        params: { path: { id: connection.id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      setDisconnecting(null);
      void qc.invalidateQueries({ queryKey: ["channel-connections"] });
    },
  });

  if (query.isPending) {
    return <p className="t-small">{t("connectors.loading")}</p>;
  }
  if (query.isError) {
    return (
      <p className="t-small" style={{ color: "var(--danger)" }}>
        {query.error instanceof Error
          ? query.error.message
          : t("connectors.loadFailed")}
      </p>
    );
  }
  if (query.data.notConfigured) {
    return (
      <EmptyState>
        <p>{t("connectors.telegramNotConfigured")}</p>
      </EmptyState>
    );
  }

  const connections = query.data.data;
  const closeForms = () => {
    setConnectOpen(false);
    setEditingConnection(null);
  };

  return (
    <>
      {connections.length === 0 && (
        <Button small onClick={() => setConnectOpen(true)}>
          <Send aria-hidden /> {t("connectors.telegramConnectCta")}
        </Button>
      )}
      {connections.length > 0 && (
        <ul className="connectors-list">
          {connections.map((connection) => (
            <TelegramConnectionRow
              key={connection.id}
              connection={connection}
              onEdit={() => setEditingConnection(connection)}
              onDisconnect={() => setDisconnecting(connection)}
            />
          ))}
        </ul>
      )}
      <TelegramConnectForm
        // Keyed to the row that opened it, so the form never carries one
        // connection's in-progress state onto another's rotation.
        key={editingConnection?.id ?? "new"}
        open={connectOpen || editingConnection !== null}
        connection={editingConnection ?? undefined}
        onClose={closeForms}
        onConnected={closeForms}
      />
      <ConfirmModal
        open={disconnecting !== null}
        onClose={() => setDisconnecting(null)}
        title={t("connectors.telegramDisconnectTitle")}
        confirmLabel={t("connectors.disconnect")}
        confirmVariant="danger"
        pending={disconnect.isPending}
        error={disconnect.isError ? disconnect.error.message : null}
        onConfirm={() => {
          if (disconnecting) {
            disconnect.mutate(disconnecting);
          }
        }}
      >
        <p className="t-small">{t("connectors.telegramDisconnectBody")}</p>
      </ConfirmModal>
    </>
  );
}

export function ConnectorsCard() {
  const t = useT();
  const { locale } = useLocale();
  const qc = useQueryClient();
  const [pendingDisconnect, setPendingDisconnect] = useState<Provider | null>(
    null,
  );
  const [imapConnectOpen, setImapConnectOpen] = useState(false);
  const [notConfigured501, setNotConfigured501] = useState<Provider | null>(
    null,
  );

  const connectors = useQuery({
    queryKey: ["connectors"],
    queryFn: async (): Promise<ConnectorsResult> => {
      const { data, error, response } = await api.GET("/connectors");
      if (response.status === 501 && problemCode(error) === "not_implemented") {
        return { notConfigured: true, data: [] };
      }
      if (error) {
        throw new Error(problemMessage(error));
      }
      return { notConfigured: false, data: data.data };
    },
  });

  const connect = useMutation({
    mutationFn: async (provider: Provider) => {
      setNotConfigured501(null);
      const { data, error, response } = await api.POST(
        "/connectors/{provider}/connect",
        {
          params: { path: { provider } },
          // Lands the post-consent redirect back on Settings (Task 2's
          // contract field) rather than the default onboarding landing.
          body: { return_to: "settings" },
        },
      );
      // A deployment that never wired this specific provider answers 501
      // code:not_implemented — a calm, provider-named state, never a claim
      // dressed up as a generic failure.
      if (response.status === 501 && problemCode(error) === "not_implemented") {
        setNotConfigured501(provider);
        return null;
      }
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      if (data?.authorize_url) {
        globalThis.location.assign(data.authorize_url);
      }
    },
  });

  const disconnect = useMutation({
    mutationFn: async (provider: Provider) => {
      const { error } = await api.POST("/connectors/{provider}/disconnect", {
        params: { path: { provider } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      setPendingDisconnect(null);
      void qc.invalidateQueries({ queryKey: ["connectors"] });
    },
  });

  const notConfigured = connectors.data?.notConfigured ?? false;
  const rows = (connectors.data?.data ?? []).filter(
    (c) => c.status !== "disconnected",
  );
  const disconnectNoteKey = pendingDisconnect
    ? OAUTH_DISCONNECT_NOTE[pendingDisconnect]
    : undefined;

  const present = new Set(rows.map((r) => r.provider));
  const addable = ALL_PROVIDERS.filter((p) => !present.has(p));
  const addPanel = (
    <ConnectorAddPanel
      addable={addable}
      pending={connect.isPending}
      notConfigured501={notConfigured501}
      onConnect={(p) => connect.mutate(p)}
      onImap={() => {
        // A stale "X isn't configured" note from an earlier OAuth attempt
        // must not linger once the user pivots to the IMAP form instead.
        setNotConfigured501(null);
        setImapConnectOpen(true);
      }}
    />
  );

  return (
    <Card>
      <SectionHeader title={t("connectors.title")} sub={t("connectors.sub")} />
      <OAuthOutcomeNote />
      {connectors.isPending && (
        <p className="t-small">{t("connectors.loading")}</p>
      )}
      {connectors.isError && (
        <p className="t-small" style={{ color: "var(--danger)" }}>
          {connectors.error instanceof Error
            ? connectors.error.message
            : t("connectors.loadFailed")}
        </p>
      )}
      {connectors.isSuccess && notConfigured && (
        <EmptyState>
          <p>{t("connectors.notConfigured")}</p>
        </EmptyState>
      )}
      {connectors.isSuccess && !notConfigured && rows.length === 0 && (
        <EmptyState>
          <p>{t("connectors.empty")}</p>
          {addable.length > 0 && addPanel}
        </EmptyState>
      )}
      {!notConfigured && rows.length > 0 && (
        <ul className="connectors-list">
          {rows.map((conn) => (
            <li key={conn.id} className="connector-row">
              <span className="connector-id">
                <Mail aria-hidden />
                <span>
                  <strong>{t(providerLabel[conn.provider])}</strong>
                  {conn.account_label && (
                    <span className="t-small connector-account">
                      {conn.account_label}
                    </span>
                  )}
                  <span className="t-small connector-synced">
                    {conn.last_synced_at
                      ? t("connectors.lastSynced", {
                          at: formatDateTime(
                            conn.last_synced_at,
                            locale,
                            "Europe/Berlin",
                          ),
                        })
                      : t("connectors.neverSynced")}
                  </span>
                  {conn.next_sync_due_at && (
                    <span className="t-small connector-synced">
                      {t("connectors.nextCheck", {
                        at: formatDateTime(
                          conn.next_sync_due_at,
                          locale,
                          "Europe/Berlin",
                        ),
                      })}
                    </span>
                  )}
                  <span className="t-small connector-synced">
                    {conn.watch_expires_at
                      ? t("connectors.pushRenewal", {
                          at: formatDateTime(
                            conn.watch_expires_at,
                            locale,
                            "Europe/Berlin",
                          ),
                        })
                      : t("connectors.polled")}
                  </span>
                  {(conn.status === "error" ||
                    conn.status === "reauth_required") && (
                    <span
                      className="t-small"
                      style={{ color: "var(--danger)" }}
                    >
                      {t(errorClassKey(conn.last_sync_error_class))}
                    </span>
                  )}
                  {/* Named here rather than at send time: the composer's 422
                      arrives after the rep has written the mail, and it can
                      only be cleared from this card. */}
                  {missingSendGrant(conn) && (
                    <span className="t-small">
                      {t("connectors.reconnectToSend")}
                    </span>
                  )}
                </span>
              </span>
              <span className="connector-actions">
                <Badge tone={statusTone(conn.status)}>
                  {t(statusLabel(conn.status))}
                </Badge>
                {missingSendGrant(conn) && (
                  <Badge tone="warn">{t("connectors.cannotSend")}</Badge>
                )}
                {(conn.status === "reauth_required" ||
                  missingSendGrant(conn)) &&
                  (OAUTH_PROVIDERS.has(conn.provider) ? (
                    <Button
                      small
                      disabled={connect.isPending}
                      onClick={() => connect.mutate(conn.provider)}
                    >
                      <RefreshCw aria-hidden /> {t("connectors.reconnect")}
                    </Button>
                  ) : (
                    <Button small onClick={() => setImapConnectOpen(true)}>
                      <RefreshCw aria-hidden /> {t("connectors.reconnect")}
                    </Button>
                  ))}
                <Button
                  small
                  variant="ghost"
                  onClick={() => setPendingDisconnect(conn.provider)}
                >
                  {t("connectors.disconnect")}
                </Button>
              </span>
              {conn.status === "connected" && (
                <div className="connector-backfill">
                  <BackfillPanel
                    provider={conn.provider}
                    initial={conn.backfill}
                  />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}
      {!notConfigured &&
        rows.length > 0 &&
        (addable.length > 0 || notConfigured501) && (
          <div className="connector-add">
            <SectionHeader title={t("connectors.addConnection")} />
            {addPanel}
          </div>
        )}
      {connect.isError && (
        <p className="t-small" style={{ color: "var(--danger)" }}>
          {connect.error.message}
        </p>
      )}
      <ConfirmModal
        open={pendingDisconnect !== null}
        onClose={() => setPendingDisconnect(null)}
        title={t("connectors.disconnectTitle")}
        confirmLabel={t("connectors.disconnect")}
        confirmVariant="danger"
        pending={disconnect.isPending}
        error={disconnect.isError ? disconnect.error.message : null}
        onConfirm={() => {
          if (pendingDisconnect !== null) {
            disconnect.mutate(pendingDisconnect);
          }
        }}
      >
        <p className="t-small">{t("connectors.disconnectBody")}</p>
        {disconnectNoteKey && <p className="t-small">{t(disconnectNoteKey)}</p>}
      </ConfirmModal>
      <ImapConnectForm
        open={imapConnectOpen}
        onClose={() => setImapConnectOpen(false)}
        onConnected={() => setImapConnectOpen(false)}
      />
      <div className="connector-add">
        <SectionHeader
          title={t("connectors.telegramTitle")}
          sub={t("connectors.telegramSub")}
        />
        <TelegramConnectorPanel />
      </div>
    </Card>
  );
}
