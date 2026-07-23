import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail, Plug, RefreshCw } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { navigate } from "../app/router";
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
import { problemMessage } from "./common";

// The connected-inboxes card (RC-8): the Settings surface the onboarding copy
// has always promised ("disconnect in one click", "manage in Settings"). It
// lists the live capture connections, lets a stale one reconnect (re-mint the
// same consent URL), and disconnects one in a single confirmed click.
// Every field shown is a server fact from GET /connectors — never a claim.

type CaptureConnection = components["schemas"]["CaptureConnection"];
type Provider = CaptureConnection["provider"];
type Status = CaptureConnection["status"];

const providerLabel: Record<Provider, MessageKey> = {
  gmail: "connectors.provGmail",
  gcal: "connectors.provGcal",
  graph: "connectors.provGraph",
  imap: "connectors.provImap",
};

const statusTone: Record<Status, "success" | "warn" | "danger" | undefined> = {
  connected: "success",
  reauth_required: "warn",
  error: "danger",
  disconnected: undefined,
};

const statusLabel: Record<Status, MessageKey> = {
  connected: "connectors.statusConnected",
  reauth_required: "connectors.statusReauth",
  error: "connectors.statusError",
  disconnected: "connectors.statusDisconnected",
};

// The OAuth providers whose reconnect re-mints a consent URL; imap reconnects
// through the onboarding form, not a redirect.
const OAUTH_PROVIDERS = new Set<Provider>(["gmail", "gcal", "graph"]);

export function ConnectorsCard() {
  const t = useT();
  const { locale } = useLocale();
  const qc = useQueryClient();
  const [pendingDisconnect, setPendingDisconnect] = useState<Provider | null>(
    null,
  );

  const connectors = useQuery({
    queryKey: ["connectors"],
    queryFn: async () => {
      const { data, error } = await api.GET("/connectors");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const reconnect = useMutation({
    mutationFn: async (provider: Provider) => {
      const { data, error } = await api.POST("/connectors/{provider}/connect", {
        params: { path: { provider } },
        body: {},
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

  const rows = (connectors.data?.data ?? []).filter(
    (c) => c.status !== "disconnected",
  );

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
      {connectors.isSuccess && rows.length === 0 && (
        <EmptyState>
          <p>{t("connectors.empty")}</p>
          <Button
            small
            variant="primary"
            onClick={() => navigate({ screen: "onboarding", id: "connect" })}
          >
            <Plug aria-hidden /> {t("connectors.connectCta")}
          </Button>
        </EmptyState>
      )}
      {rows.length > 0 && (
        <ul className="connectors-list">
          {rows.map((conn) => (
            <li key={conn.id} className="connector-row">
              <span className="connector-id">
                <Mail aria-hidden />
                <span>
                  <strong>{t(providerLabel[conn.provider])}</strong>
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
                </span>
              </span>
              <span className="connector-actions">
                <Badge tone={statusTone[conn.status]}>
                  {t(statusLabel[conn.status])}
                </Badge>
                {conn.status === "reauth_required" &&
                  OAUTH_PROVIDERS.has(conn.provider) && (
                    <Button
                      small
                      disabled={reconnect.isPending}
                      onClick={() => reconnect.mutate(conn.provider)}
                    >
                      <RefreshCw aria-hidden /> {t("connectors.reconnect")}
                    </Button>
                  )}
                <Button
                  small
                  variant="ghost"
                  onClick={() => setPendingDisconnect(conn.provider)}
                >
                  {t("connectors.disconnect")}
                </Button>
              </span>
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
      </ConfirmModal>
    </Card>
  );
}
