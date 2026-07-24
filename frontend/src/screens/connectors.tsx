import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Plug, RefreshCw } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
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
import { errorClassKey, statusLabel, statusTone } from "./connector-status";
import { ImapConnectForm } from "./imap-connect-form";

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

type ConnectorsResult = {
  // GET /connectors answers 501 code:not_implemented when this deployment
  // never wired mail capture (httperr.NotImplemented) — a calm, documented
  // feature-off state, never an error card (mirrors webhooks.tsx's
  // webhooks_not_configured treatment).
  notConfigured: boolean;
  data: CaptureConnection[];
};

export function ConnectorsCard() {
  const t = useT();
  const { locale } = useLocale();
  const qc = useQueryClient();
  const [pendingDisconnect, setPendingDisconnect] = useState<Provider | null>(
    null,
  );
  const [imapConnectOpen, setImapConnectOpen] = useState(false);

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

  const reconnect = useMutation({
    mutationFn: async (provider: Provider) => {
      const { data, error } = await api.POST("/connectors/{provider}/connect", {
        params: { path: { provider } },
        // Lands the post-consent redirect back on Settings (Task 2's
        // contract field) rather than the default onboarding landing.
        body: { return_to: "settings" },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
    onSuccess: (data) => {
      if (data.authorize_url) {
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

  return (
    <Card>
      <SectionHeader title={t("connectors.title")} sub={t("connectors.sub")} />
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
          <div
            style={{
              display: "flex",
              gap: "var(--space-2)",
              justifyContent: "center",
              flexWrap: "wrap",
            }}
          >
            <Button
              small
              variant="primary"
              disabled={reconnect.isPending}
              onClick={() => reconnect.mutate("gmail")}
            >
              <Plug aria-hidden /> {t("connectors.connectCta")}
            </Button>
            <Button small onClick={() => setImapConnectOpen(true)}>
              <Mail aria-hidden /> {t("connectors.imapConnectCta")}
            </Button>
          </div>
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
                </span>
              </span>
              <span className="connector-actions">
                <Badge tone={statusTone(conn.status)}>
                  {t(statusLabel(conn.status))}
                </Badge>
                {conn.status === "reauth_required" &&
                  (OAUTH_PROVIDERS.has(conn.provider) ? (
                    <Button
                      small
                      disabled={reconnect.isPending}
                      onClick={() => reconnect.mutate(conn.provider)}
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
      {reconnect.isError && (
        <p className="t-small" style={{ color: "var(--danger)" }}>
          {reconnect.error.message}
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
    </Card>
  );
}
