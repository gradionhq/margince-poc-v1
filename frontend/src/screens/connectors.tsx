import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, RefreshCw, Send, X } from "lucide-react";
import { useId, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useRoute } from "../app/router";
import { Badge, Button, EmptyState, Modal } from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDateTime } from "../format/format";
import { viewerZone } from "../format/timezone";
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

// The connected-inboxes surface (RC-8): the Settings cards the onboarding copy
// has always promised ("disconnect in one click", "manage in Settings"). It
// lists the live capture connections, lets a stale one reconnect (re-mint the
// same consent URL), and disconnects one in a single confirmed click.
// Every field shown is a server fact from GET /connectors — never a claim.
//
// TWO panels, not one. Mail capture and the workspace's Telegram bot are two
// subjects that happened to share a card: one is a per-user roster of mailboxes
// read from /connectors, the other is a workspace-wide bot read from
// /channel-connections, and the Telegram half was a level-3 heading buried
// under the mail half's roster. The exported component renders both because
// settings.tsx composes one entry for the pair.
//
// Every decision on both panels is a SettingRow: what the connection is on the
// left, what it is set to on the right, at the one x the whole settings page
// lines up on (design-system/settingrow.tsx). Adding a connection is not one of
// those decisions — it is the card's create verb, so it sits in the header and
// opens a dialog rather than taking a row in the column a reader travels to
// audit the roster.

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
// the empty state shows all four, the row shows whichever aren't already
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
// the panel so its dismissal state and branching stay off that function's
// complexity budget. Dismissing (or navigating away, which unmounts this
// card) clears it; the list itself already refetches on mount, so "ok" needs
// no extra invalidation here.
//
// It sits ABOVE the row list rather than in it: it reports on what the reader
// just did, which is not one of the card's standing decisions.
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

// A connection's identity, as the left half of its row: the provider this
// build's own name for it, and the account it reads. One shape for a mailbox
// and for a bot, because a reader auditing the page reads both the same way.
function ConnectionIdentity({
  icon: Icon,
  name,
  account,
}: Readonly<{
  icon: typeof Mail;
  name: string;
  account?: string | null;
}>) {
  return (
    <span className="connector-id">
      <Icon aria-hidden />
      <span>
        <strong>{name}</strong>
        {account && <span className="connector-account">{account}</span>}
      </span>
    </span>
  );
}

// What each provider actually brings, one sentence each. They exist because
// the choice cannot be made from four names: "Gmail" and "Google Calendar"
// are two halves of one account, only Gmail can ever send, and IMAP is the
// answer for every host with no OAuth at all. A strip of four buttons had
// nowhere to say any of that.
const PROVIDER_BLURB: Record<Provider, MessageKey> = {
  gmail: "connectors.addGmailBrings",
  gcal: "connectors.addGcalBrings",
  graph: "connectors.addGraphBrings",
  imap: "connectors.addImapBrings",
};

// The "Add a connection" affordance (Task 1), as ONE verb and a dialog.
//
// It was a row whose control held a strip of four buttons — the shape the
// spacing contract names outright: three or more verbs in a row's right column
// collapse behind one. Four picks squeezed against a wrapping description also
// left no room for the sentence each provider needs, and made Gmail the
// primary of a card that exists to REPORT the roster rather than to push one
// mailbox.
//
// So the picks are rows of their own in here: the provider names itself on the
// left, its sentence under that, and one verb at the same x as every other
// answer in the product. The reasons a connect failed — a provider this
// deployment never wired, or a refusal from the one it did — land in the dialog
// the press happened in, which is the only place that names the button they
// answer.
function AddConnectionDialog({
  open,
  onClose,
  addable,
  pendingProvider,
  notConfigured501,
  connectError,
  onConnect,
  onImap,
}: Readonly<{
  open: boolean;
  onClose: () => void;
  addable: Provider[];
  /** The provider whose connect is in flight, or null. */
  pendingProvider: Provider | null;
  notConfigured501: Provider | null;
  // Why the last connect started from THESE buttons failed, or null.
  connectError: string | null;
  onConnect: (provider: Provider) => void;
  onImap: () => void;
}>) {
  const t = useT();
  const headingId = useId();
  return (
    <Modal open={open} onClose={onClose} labelledBy={headingId}>
      <div className="form-stack">
        <h2 id={headingId} className="t-h2">
          {t("connectors.addConnection")}
        </h2>
        <SettingList>
          {addable.map((provider) => (
            <SettingRow
              key={provider}
              testId={`connector-add-${provider}`}
              label={t(providerLabel[provider])}
              description={t(PROVIDER_BLURB[provider])}
              // The function form, but for the DESCRIPTION only: the sentence
              // under the provider's name is what the choice turns on, so the
              // button that makes it has to carry it. The row's
              // `aria-labelledby` is deliberately left behind — it would
              // outrank `aria-label` and leave four buttons announcing the word
              // they share instead of the provider they differ by, and the
              // visible "Connect" would no longer be inside the name a reader
              // hears.
              control={({ id, "aria-describedby": describedBy }) => (
                <Button
                  small
                  id={id}
                  aria-describedby={describedBy}
                  variant="ghost"
                  aria-label={t("connectors.connectProvider", {
                    provider: t(providerLabel[provider]),
                  })}
                  pending={pendingProvider === provider}
                  onClick={
                    provider === "imap" ? onImap : () => onConnect(provider)
                  }
                >
                  {t("connectors.connect")}
                </Button>
              )}
            />
          ))}
        </SettingList>
        {notConfigured501 && (
          <Callout tone="danger" live="alert">
            {t("connectors.providerNotConfigured", {
              provider: t(providerLabel[notConfigured501]),
            })}
          </Callout>
        )}
        {connectError && (
          <Callout tone="danger" live="alert">
            {connectError}
          </Callout>
        )}
      </div>
    </Modal>
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

// One live bot as a row: which bot it is on the left, whether it is live on the
// right, and the two verbs that change it beside that.
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
    <SettingRow
      testId="telegram-connection"
      label={
        <ConnectionIdentity
          icon={Send}
          name={t("connectors.provTelegram")}
          account={`@${connection.channelLabel}`}
        />
      }
      value={
        <Badge tone={statusTone(connection.status)}>
          {t(statusLabel(connection.status))}
        </Badge>
      }
      control={
        <div className="connector-actions">
          <Button small onClick={onEdit}>
            <RefreshCw aria-hidden /> {t("connectors.telegramEditToken")}
          </Button>
          <Button small variant="ghost" onClick={onDisconnect}>
            {t("connectors.disconnect")}
          </Button>
        </div>
      }
    />
  );
}

// Everything the Telegram panel shows INSTEAD of its rows. Split out so the
// panel function keeps one return and its hooks stay unconditional.
function TelegramNotice({
  query,
}: Readonly<{ query: ReturnType<typeof useChannelConnections> }>) {
  const t = useT();
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
  return null;
}

// The workspace's messaging bot, as its own panel.
//
// A bot connects for the WHOLE workspace rather than per-user (Task 17,
// design §9.1/§9.2), and a send needs exactly one of them: with a second
// live bot the workspace can send nothing at all until an admin removes it.
// This panel is the only surface that can, so it must show every connection
// the list returns — a bot it hides is a bot nobody can disconnect. Every one
// of them is a row of its own for exactly that reason.
//
// Editing goes through the SAME TelegramConnectForm modal, whose PATCH takes
// the place of a disconnect-reconnect cycle (§9.2). The panel mounts one
// form instance, keyed to whichever row opened it.
function TelegramConnectorsPanel() {
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

  const connections =
    query.isSuccess && !query.data.notConfigured ? query.data.data : [];
  const closeForms = () => {
    setConnectOpen(false);
    setEditingConnection(null);
  };

  return (
    <Panel
      title={t("connectors.telegramTitle")}
      // The connect verb in the header, the same shape the mail card next to it
      // takes: as the zero state's row it was labelled "Telegram" under a card
      // titled "Telegram bot" — the card's own subject, said twice, with the
      // act beside it.
      titleAction={
        query.isSuccess &&
        !query.data.notConfigured &&
        connections.length === 0 && (
          <Button
            small
            data-testid="telegram-connect"
            onClick={() => setConnectOpen(true)}
          >
            <Send aria-hidden /> {t("connectors.telegramConnectCta")}
          </Button>
        )
      }
    >
      <PanelBody>
        {/* In the BODY, not `Panel`'s `sub`. A description in the header band
            raises that band's own height, so this card's title sat lower than
            every sibling's on the tab and the whole page lost its beat over one
            sentence. Read here it is also the first thing under the title
            rather than a second line competing with it. */}
        <p className="settings-panel-sub">{t("connectors.telegramSub")}</p>
        <TelegramNotice query={query} />
        {query.isSuccess && !query.data.notConfigured && (
          <SettingList>
            {connections.length === 0 ? (
              // What the card is FOR, in the roster's own place: which bot is
              // carrying messages. The verb that changes it is in the header.
              <SettingRow
                label={t("connectors.telegramRosterLabel")}
                layout="stack"
                control={
                  <EmptyState>{t("connectors.telegramEmpty")}</EmptyState>
                }
              />
            ) : (
              connections.map((connection) => (
                <TelegramConnectionRow
                  key={connection.id}
                  connection={connection}
                  onEdit={() => setEditingConnection(connection)}
                  onDisconnect={() => setDisconnecting(connection)}
                />
              ))
            )}
          </SettingList>
        )}
      </PanelBody>
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
    </Panel>
  );
}

type ConnectFailure = {
  provider: Provider | undefined;
  message: string;
} | null;

// Which surface owes the reader the reason a connect failed. One mutation
// drives two of them — the add dialog's provider picks and a roster row's
// Reconnect — so a single shared error region could only ever sit under one,
// which is how the reason ended up in a band of its own naming no button at
// all. A provider is either already on the roster or still addable, never both,
// so the mutation's own variable answers it: whichever surface offers that
// provider carries the message and the other stays silent.
function failureOwnedBy(
  failure: ConnectFailure,
  owner: readonly Provider[],
): string | null {
  if (!failure?.provider || !owner.includes(failure.provider)) {
    return null;
  }
  return failure.message;
}

// The health facts a mailbox row states under its own name. Split out of
// ConnectorRow so that function stays inside the cognitive-complexity gate.
function ConnectorFacts({ conn }: Readonly<{ conn: CaptureConnection }>) {
  const t = useT();
  const { locale } = useLocale();
  const at = (iso: string) => formatDateTime(iso, locale, viewerZone());
  return (
    <>
      <span className="connector-fact">
        {conn.last_synced_at
          ? t("connectors.lastSynced", { at: at(conn.last_synced_at) })
          : t("connectors.neverSynced")}
      </span>
      {conn.next_sync_due_at && (
        <span className="connector-fact">
          {t("connectors.nextCheck", { at: at(conn.next_sync_due_at) })}
        </span>
      )}
      <span className="connector-fact">
        {conn.watch_expires_at
          ? t("connectors.pushRenewal", { at: at(conn.watch_expires_at) })
          : t("connectors.polled")}
      </span>
      {(conn.status === "error" || conn.status === "reauth_required") && (
        <span className="connector-fact connector-error">
          {t(errorClassKey(conn.last_sync_error_class))}
        </span>
      )}
      {/* Named here rather than at send time: the composer's 422 arrives
          after the rep has written the mail, and it can only be cleared
          from this card. */}
      {missingSendGrant(conn) && (
        <span className="connector-fact">
          {t("connectors.reconnectToSend")}
        </span>
      )}
    </>
  );
}

// One mail connection, as a row of the panel's SettingList: which mailbox it is
// and how it is doing on the left, what state it is in and the verbs that
// change it on the right.
//
// The history import rides BELOW the row rather than inside a Disclosure, and
// that is not a style choice: BackfillPanel fires a scope-preview POST from an
// effect the moment its run state is "none" (backfill.tsx), and `<details>`
// renders its children whether or not it is open — so a closed disclosure would
// spend money-adjacent requests for every connected mailbox on page load. It is
// mounted here exactly as often as it was before, which is what keeps this a
// layout change and not a data-flow one.
function ConnectorRow({
  conn,
  connectPending,
  connectError,
  onReconnect,
  onImapReconnect,
  onDisconnect,
}: Readonly<{
  conn: CaptureConnection;
  // Whether the reconnect pressed on THIS row is the one in flight. One
  // mutation serves the whole card, so an unscoped "something is connecting"
  // would draw every row's Reconnect as busy over a press it never received.
  connectPending: boolean;
  // Why the reconnect pressed on THIS row failed, or null — reported here,
  // under the button that produced it, rather than in a band of its own.
  connectError: string | null;
  onReconnect: () => void;
  onImapReconnect: () => void;
  onDisconnect: () => void;
}>) {
  const t = useT();
  const needsReconnect =
    conn.status === "reauth_required" || missingSendGrant(conn);
  return (
    <>
      <SettingRow
        testId={`connector-${conn.provider}`}
        label={
          <ConnectionIdentity
            icon={Mail}
            name={t(providerLabel[conn.provider])}
            account={conn.account_label}
          />
        }
        description={<ConnectorFacts conn={conn} />}
        value={
          <span className="connector-badges">
            <Badge tone={statusTone(conn.status)}>
              {t(statusLabel(conn.status))}
            </Badge>
            {missingSendGrant(conn) && (
              <Badge tone="warn">{t("connectors.cannotSend")}</Badge>
            )}
          </span>
        }
        control={
          <div className="connector-control">
            <div className="connector-actions">
              {needsReconnect &&
                (OAUTH_PROVIDERS.has(conn.provider) ? (
                  // `pending`, never `disabled`: a write already on its way is
                  // a different unavailability from one the reader could fix,
                  // and disabling the button they just pressed drops their
                  // focus to <body> at the moment there is something to say.
                  <Button small pending={connectPending} onClick={onReconnect}>
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
            </div>
            {connectError && (
              <Callout tone="danger" live="alert">
                {connectError}
              </Callout>
            )}
          </div>
        }
      />
      {conn.status === "connected" && (
        <div className="connector-backfill">
          <BackfillPanel provider={conn.provider} initial={conn.backfill} />
        </div>
      )}
    </>
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

// The roster as this card's list of decisions: one row per mailbox, or — when
// there is no mailbox — one row saying so.
//
// "No inbox is connected yet" is the ANSWER to the question the whole card asks,
// which mailboxes are capturing, so it takes a row of its own rather than
// floating as a bare paragraph between the card's description and whatever came
// after it. A stacked row caps `.empty`'s page-furniture slab at a row's own
// interval and left-aligns it (settingrow.css), so the sentence reads as a
// sentence instead of as the loudest thing on the card.
function MailRoster({
  rows,
  pendingProvider,
  connectFailure,
  onReconnect,
  onImapReconnect,
  onDisconnect,
}: Readonly<{
  rows: readonly CaptureConnection[];
  pendingProvider: Provider | null;
  connectFailure: ConnectFailure;
  onReconnect: (provider: Provider) => void;
  onImapReconnect: () => void;
  onDisconnect: (provider: Provider) => void;
}>) {
  const t = useT();
  if (rows.length === 0) {
    return (
      <SettingList>
        <SettingRow
          testId="connector-roster-empty"
          layout="stack"
          label={t("connectors.rosterLabel")}
          control={
            <EmptyState>
              <p className="t-small">{t("connectors.empty")}</p>
            </EmptyState>
          }
        />
      </SettingList>
    );
  }
  return (
    <SettingList>
      {rows.map((conn) => (
        <ConnectorRow
          key={conn.id}
          conn={conn}
          connectPending={pendingProvider === conn.provider}
          connectError={failureOwnedBy(connectFailure, [conn.provider])}
          onReconnect={() => onReconnect(conn.provider)}
          onImapReconnect={onImapReconnect}
          onDisconnect={() => onDisconnect(conn.provider)}
        />
      ))}
    </SettingList>
  );
}

// The mail half: the roster of mailboxes this member captures from, and the
// header verb that adds another.
function MailConnectorsPanel() {
  const t = useT();
  const qc = useQueryClient();
  const [pendingDisconnect, setPendingDisconnect] = useState<Provider | null>(
    null,
  );
  const [imapConnectOpen, setImapConnectOpen] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
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

  // Gated on the list having ANSWERED, not merely on it not having failed:
  // `addable` is derived from what is already connected, so before the read
  // lands it says "all four", and a reader would be offered a mailbox they
  // already have.
  const rosterKnown = connectors.isSuccess && !notConfigured;
  const offerAdd = rosterKnown && addable.length > 0;
  const pendingProvider = connect.isPending ? connect.variables : null;

  return (
    <Panel
      title={t("connectors.title")}
      // The card's create verb, in the header — not one more row in the list.
      // A row named "Add a connection" whose control is a button that says the
      // same thing is a decision-shaped thing that answers nothing, and it sat
      // in the column a reader travels to find what each mailbox is set to.
      titleAction={
        offerAdd ? (
          <Button small onClick={() => setAddOpen(true)}>
            {t("connectors.addOpen")}
          </Button>
        ) : undefined
      }
    >
      <PanelBody>
        <p className="settings-panel-sub">{t("connectors.sub")}</p>
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
        {rosterKnown && (
          <MailRoster
            rows={rows}
            pendingProvider={pendingProvider}
            connectFailure={connectFailure}
            onReconnect={(provider) => connect.mutate(provider)}
            onImapReconnect={() => setImapConnectOpen(true)}
            onDisconnect={setPendingDisconnect}
          />
        )}
      </PanelBody>
      <AddConnectionDialog
        open={addOpen && offerAdd}
        onClose={() => setAddOpen(false)}
        addable={addable}
        pendingProvider={pendingProvider}
        notConfigured501={notConfigured501}
        connectError={failureOwnedBy(connectFailure, addable)}
        onConnect={(p) => connect.mutate(p)}
        onImap={() => {
          // A stale "X isn't configured" note from an earlier OAuth attempt
          // must not linger once the user pivots to the IMAP form instead.
          setNotConfigured501(null);
          // Closed BEFORE the IMAP form opens, never stacked behind it: two
          // overlays deep, Escape and the focus restore both answer to the
          // wrong layer, and the chooser has already done its job.
          setAddOpen(false);
          setImapConnectOpen(true);
        }}
      />
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
    </Panel>
  );
}

export function ConnectorsCard() {
  return (
    <>
      <MailConnectorsPanel />
      <TelegramConnectorsPanel />
    </>
  );
}
