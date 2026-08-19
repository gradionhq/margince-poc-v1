import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Plug, RefreshCw, Send, X } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useRoute } from "../app/router";
import {
  Badge,
  Button,
  Checkbox,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { formatDateTime } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { BackfillPanel } from "./backfill";
import { problemCode, problemMessageOf, throwProblem } from "./common";
import {
  errorClassKey,
  missingSendGrant,
  statusLabel,
  statusTone,
} from "./connector-status";
import { ImapConnectForm } from "./imap-connect-form";
import { TelegramConnectForm } from "./telegram-connect-form";
import "./connectors.css";

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

// The OAuth callback lands back on #/settings/connections/{outcome} — the
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

export type ConnectorsResult = {
  // GET /connectors answers 501 code:not_implemented when this deployment
  // never wired mail capture (httperr.NotImplemented) — a calm, documented
  // feature-off state, never an error card (mirrors webhooks.tsx's
  // webhooks_not_configured treatment).
  notConfigured: boolean;
  data: CaptureConnection[];
};

// The OAuth return outcome (Task 2): the callback lands back on
// #/settings/connections/{outcome} — id2 on that route only, never parsed
// from location.hash directly (the router already owns that). Split out of
// ConnectorsCard so its dismissal state and branching stay off that
// function's complexity budget. Dismissing (or navigating away, which
// unmounts this card) clears it; the list itself already refetches on
// mount, so "ok" needs no extra invalidation here.
function OAuthOutcomeNote() {
  const t = useT();
  const route = useRoute();
  const oauthOutcome =
    route.screen === "settings" && route.id === "connections"
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
  // A Callout, not a hand-tinted paragraph: this is the surface reporting on
  // what the reader just did, which is exactly the closed tone set Callout
  // owns. `.connector-oauth-note` was a class no stylesheet ever declared, so
  // every rule its name implied did nothing at all.
  return (
    <Callout
      tone={note.tone}
      live="status"
      actions={
        <Button
          small
          variant="ghost"
          aria-label={t("connectors.dismissOutcome")}
          onClick={() => setDismissedOutcome(oauthOutcome ?? null)}
        >
          <X aria-hidden /> {t("connectors.dismissOutcome")}
        </Button>
      }
    >
      {t(note.key)}
    </Callout>
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
  connectError,
  shareAck,
  onShareAck,
  onConnect,
  onImap,
}: Readonly<{
  addable: Provider[];
  pending: boolean;
  // The one-time mail-sharing acknowledgment (one sentence, one checkbox):
  // captured correspondence is workspace-readable by default, and the server
  // refuses the connect (422 sharing_not_acknowledged) until it is given.
  // Held by the parent so the OAuth buttons and the IMAP form opener gate on
  // the same tick.
  shareAck: boolean;
  onShareAck: (v: boolean) => void;
  notConfigured501: Provider | null;
  // Why the last connect started from THESE buttons failed, or null. It renders
  // inside this block, under the strip that produced it: a reason reported in a
  // section of its own — below a rule, above an unrelated heading — names no
  // button at all, and the reader has to guess which press it answers.
  connectError: string | null;
  onConnect: (provider: Provider) => void;
  onImap: () => void;
}>) {
  const t = useT();
  return (
    <>
      {(addable.includes("gcal") || addable.includes("gmail")) && (
        <p className="t-small">{t("connectors.googleSeparateNote")}</p>
      )}
      <Checkbox
        checked={shareAck}
        onChange={(e) => onShareAck(e.currentTarget.checked)}
        label={t("connectors.shareAck")}
      />
      <div className="connector-add-actions">
        {addable.map((p) =>
          p === "imap" ? (
            <Button key={p} small disabled={!shareAck} onClick={onImap}>
              <Mail aria-hidden /> {t(providerLabel[p])}
            </Button>
          ) : (
            <Button
              key={p}
              small
              variant={p === "gmail" ? "primary" : undefined}
              disabled={pending || !shareAck}
              onClick={() => onConnect(p)}
            >
              <Plug aria-hidden /> {t(providerLabel[p])}
            </Button>
          ),
        )}
      </div>
      {notConfigured501 && (
        <Callout tone="danger" live="alert" className="connector-add-note">
          {t("connectors.providerNotConfigured", {
            provider: t(providerLabel[notConfigured501]),
          })}
        </Callout>
      )}
      {connectError && (
        <Callout tone="danger" live="alert" className="connector-add-note">
          {connectError}
        </Callout>
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
        throwProblem(error);
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
    <li className="connector-row connector-card">
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
        throwProblem(error);
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
      <Callout tone="danger" live="alert">
        {problemMessageOf(query.error, t, t("connectors.loadFailed"))}
      </Callout>
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
        error={
          disconnect.isError ? problemMessageOf(disconnect.error, t) : null
        }
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

type ConnectFailure = {
  provider: Provider | undefined;
  message: string;
} | null;

// Which strip of buttons owes the reader the reason a connect failed. One
// mutation drives two of them — the add block's provider picks and a roster
// row's Reconnect — so a single shared error region could only ever sit under
// one, which is how the reason ended up in a band of its own naming no button
// at all. A provider is either already on the roster or still addable, never
// both, so the mutation's own variable answers it: the strip offering that
// provider carries the message and every other strip stays silent.
function failureOwnedBy(
  failure: ConnectFailure,
  owner: readonly Provider[],
): string | null {
  if (!failure?.provider || !owner.includes(failure.provider)) {
    return null;
  }
  return failure.message;
}

// One mail connection, as a full-bleed hairline row on the panel's own ground
// — not a second bordered card nested inside the first one. Split out of
// ConnectorsCard for the same reason ConnectorAddPanel and OAuthOutcomeNote
// were: the row is where most of this card's branching lives, and it does not
// belong on the card function's complexity budget.
function ConnectorRow({
  conn,
  connectPending,
  connectError,
  onReconnect,
  onImapReconnect,
  onDisconnect,
}: Readonly<{
  conn: CaptureConnection;
  connectPending: boolean;
  // Why the reconnect pressed on THIS row failed, or null — reported here,
  // under the button that produced it, rather than in a band of its own.
  connectError: string | null;
  onReconnect: () => void;
  onImapReconnect: () => void;
  onDisconnect: () => void;
}>) {
  const t = useT();
  const { locale } = useLocale();
  const at = (iso: string) => formatDateTime(iso, locale, "Europe/Berlin");
  const needsReconnect =
    conn.status === "reauth_required" || missingSendGrant(conn);
  return (
    <PanelRow className="connector-row">
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
              ? t("connectors.lastSynced", { at: at(conn.last_synced_at) })
              : t("connectors.neverSynced")}
          </span>
          {conn.next_sync_due_at && (
            <span className="t-small connector-synced">
              {t("connectors.nextCheck", { at: at(conn.next_sync_due_at) })}
            </span>
          )}
          <span className="t-small connector-synced">
            {conn.watch_expires_at
              ? t("connectors.pushRenewal", { at: at(conn.watch_expires_at) })
              : t("connectors.polled")}
          </span>
          {(conn.status === "error" || conn.status === "reauth_required") && (
            <span className="t-small connector-error">
              {t(errorClassKey(conn.last_sync_error_class))}
            </span>
          )}
          {/* Named here rather than at send time: the composer's 422 arrives
              after the rep has written the mail, and it can only be cleared
              from this card. */}
          {missingSendGrant(conn) && (
            <span className="t-small">{t("connectors.reconnectToSend")}</span>
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
        {needsReconnect &&
          (OAUTH_PROVIDERS.has(conn.provider) ? (
            <Button small disabled={connectPending} onClick={onReconnect}>
              <RefreshCw aria-hidden /> {t("connectors.reconnect")}
            </Button>
          ) : (
            <Button small onClick={onImapReconnect}>
              <RefreshCw aria-hidden /> {t("connectors.reconnect")}
            </Button>
          ))}
        <Button small variant="ghost" onClick={onDisconnect}>
          {t("connectors.disconnect")}
        </Button>
      </span>
      {connectError && (
        <div className="connector-row-note">
          <Callout tone="danger" live="alert">
            {connectError}
          </Callout>
        </div>
      )}
      {conn.status === "connected" && (
        <div className="connector-backfill">
          <BackfillPanel provider={conn.provider} initial={conn.backfill} />
        </div>
      )}
    </PanelRow>
  );
}

/**
 * The installation's capture connections, in one spelling.
 *
 * Exported because the card is no longer the only reader: chrome that reports
 * whether the agent can reach its sources needs the same list, and two queries
 * against one path are two answers that can disagree on screen.
 */
export function useConnectors() {
  return useQuery({
    queryKey: ["connectors"],
    queryFn: async (): Promise<ConnectorsResult> => {
      const { data, error, response } = await api.GET("/connectors");
      if (response.status === 501 && problemCode(error) === "not_implemented") {
        return { notConfigured: true, data: [] };
      }
      if (error) {
        throwProblem(error);
      }
      return { notConfigured: false, data: data.data };
    },
  });
}

export function ConnectorsCard() {
  const t = useT();
  const qc = useQueryClient();
  const [pendingDisconnect, setPendingDisconnect] = useState<Provider | null>(
    null,
  );
  const [imapConnectOpen, setImapConnectOpen] = useState(false);
  const [shareAck, setShareAck] = useState(false);
  const [notConfigured501, setNotConfigured501] = useState<Provider | null>(
    null,
  );

  const connectors = useConnectors();

  const connect = useMutation({
    mutationFn: async (provider: Provider) => {
      setNotConfigured501(null);
      const { data, error, response } = await api.POST(
        "/connectors/{provider}/connect",
        {
          params: { path: { provider } },
          // Lands the post-consent redirect back on Settings (Task 2's
          // contract field) rather than the default onboarding landing.
          // share_acknowledged is honest here: the add buttons stay disabled
          // until the checkbox is ticked, and a RECONNECT re-asserts the
          // acknowledgment the connection's first connect already recorded.
          body: { return_to: "settings", share_acknowledged: true },
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
        throwProblem(error);
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
        throwProblem(error);
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

  const connectFailure = connect.isError
    ? {
        provider: connect.variables,
        message: problemMessageOf(connect.error, t),
      }
    : null;

  const addPanel = (
    <ConnectorAddPanel
      addable={addable}
      pending={connect.isPending}
      shareAck={shareAck}
      onShareAck={setShareAck}
      notConfigured501={notConfigured501}
      connectError={failureOwnedBy(connectFailure, addable)}
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
    <Panel title={t("connectors.title")}>
      <PanelBody>
        <p className="t-caption">{t("connectors.sub")}</p>
        <OAuthOutcomeNote />
        {connectors.isPending && (
          <p className="t-small">{t("connectors.loading")}</p>
        )}
        {connectors.isError && (
          <Callout tone="danger" live="alert">
            {problemMessageOf(connectors.error, t, t("connectors.loadFailed"))}
          </Callout>
        )}
        {connectors.isSuccess && notConfigured && (
          <EmptyState>
            <p>{t("connectors.notConfigured")}</p>
          </EmptyState>
        )}
        {/* The roster is empty, so this is the ONE place to add the first
            connection — the "Add a connection" block below only renders beside
            an existing roster. It sits under the sentence rather than centred
            inside it. */}
        {connectors.isSuccess && !notConfigured && rows.length === 0 && (
          <>
            <p className="t-small">{t("connectors.empty")}</p>
            {addable.length > 0 && addPanel}
          </>
        )}
      </PanelBody>
      {!notConfigured &&
        rows.map((conn) => (
          <ConnectorRow
            key={conn.id}
            conn={conn}
            connectPending={connect.isPending}
            connectError={failureOwnedBy(connectFailure, [conn.provider])}
            onReconnect={() => connect.mutate(conn.provider)}
            onImapReconnect={() => setImapConnectOpen(true)}
            onDisconnect={() => setPendingDisconnect(conn.provider)}
          />
        ))}
      {!notConfigured &&
        rows.length > 0 &&
        (addable.length > 0 || notConfigured501) && (
          <PanelBody>
            <SectionHeader title={t("connectors.addConnection")} level={3} />
            {addPanel}
          </PanelBody>
        )}
      <ConfirmModal
        open={pendingDisconnect !== null}
        onClose={() => setPendingDisconnect(null)}
        title={t("connectors.disconnectTitle")}
        confirmLabel={t("connectors.disconnect")}
        confirmVariant="danger"
        pending={disconnect.isPending}
        error={
          disconnect.isError ? problemMessageOf(disconnect.error, t) : null
        }
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
      <PanelBody>
        <SectionHeader
          title={t("connectors.telegramTitle")}
          sub={t("connectors.telegramSub")}
          level={3}
        />
        <TelegramConnectorPanel />
      </PanelBody>
    </Panel>
  );
}
