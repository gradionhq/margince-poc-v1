// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import {
  Badge,
  Button,
  EmptyState,
  SectionHeader,
} from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { ScopeChips } from "../design-system/passportselect";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
import { problemMessage, QueryGate } from "./common";

// The other half of the passport story, and the half nothing on this screen
// used to tell. A human mints a passport; a client that connects over MCP is
// then issued its OWN credential derived from the passport the human lent it.
// Both are rows in GET /passports, and listing them together left a connection
// showing under the raw DCR client id its label carries — a name nobody chose,
// next to passports they did.
//
// So the split is by `connection`, the server's own statement of which kind a
// row is, never the `oauth:` label prefix: a label is display text, and a human
// naming a passport "oauth:whatever" must not be able to move it into this card.

type PassportSummary = components["schemas"]["PassportSummary"];
type Connection = PassportSummary & {
  connection: NonNullable<PassportSummary["connection"]>;
};

// The MCP URL as the SERVER states it, read from the RFC 9728 document the
// connector serves. It is `--public-base-url` + /mcp, the value clients are
// required to match exactly, so a guide built from it is the command that
// actually works — the SPA's own origin would only coincide with it.
//
// A 404 is not a failure here: the connector is off for this installation
// (`mcp.connector_enabled`), and the whole route group is absent. That is worth
// saying plainly instead of printing four commands that cannot connect.
type ConnectorState =
  | { readonly enabled: true; readonly url: string }
  | { readonly enabled: false };

async function fetchConnectorState(): Promise<ConnectorState> {
  const response = await fetch("/.well-known/oauth-protected-resource", {
    headers: { accept: "application/json" },
  });
  if (response.status === 404) {
    return { enabled: false };
  }
  if (!response.ok) {
    throw new Error(`discovery answered ${response.status}`);
  }
  const document: unknown = await response.json();
  const resource =
    typeof document === "object" &&
    document !== null &&
    "resource" in document &&
    typeof document.resource === "string"
      ? document.resource
      : "";
  if (resource === "") {
    throw new Error("the discovery document names no resource");
  }
  return { enabled: true, url: resource };
}

// One command per client, because "point your agent at the URL" is exactly the
// instruction people cannot act on. All four reach the same place: the client
// registers itself, and the consent screen asks which passport to lend.
//
// Antigravity is the odd one out only in shape — it has no add command, so its
// step is the config file its docs name. The OAuth handshake is identical.
const CONNECT_GUIDES = [
  {
    id: "claude",
    name: "Claude Code",
    command: (url: string) => `claude mcp add --transport http margince ${url}`,
  },
  {
    id: "codex",
    name: "Codex",
    command: (url: string) =>
      `codex mcp add margince --url ${url}\ncodex mcp login margince`,
  },
  {
    id: "gemini",
    name: "Gemini CLI",
    command: (url: string) => `gemini mcp add --transport http margince ${url}`,
  },
  {
    id: "antigravity",
    name: "Antigravity",
    // ~/.gemini/config/mcp_config.json — `serverUrl` specifically: Antigravity
    // rejects the `url`/`httpUrl` spellings its siblings accept.
    command: (url: string) =>
      `{ "mcpServers": { "margince": { "serverUrl": "${url}" } } }`,
  },
] as const;

function ConnectGuide() {
  const t = useT();
  const state = useQuery({
    queryKey: ["mcp-connector-state"],
    queryFn: fetchConnectorState,
  });
  return (
    <QueryGate query={state}>
      {(connector) =>
        connector.enabled ? (
          <div
            className="card card-inset"
            style={{ marginTop: "var(--space-3)" }}
          >
            <p className="t-label">{t("agents.connectHow")}</p>
            <p className="t-small" style={{ marginTop: "var(--space-1)" }}>
              {t("agents.connectSteps")}
            </p>
            <dl style={{ marginTop: "var(--space-3)" }}>
              {CONNECT_GUIDES.map((guide) => (
                <div key={guide.id} style={{ marginTop: "var(--space-2)" }}>
                  <dt className="t-label">{guide.name}</dt>
                  <dd
                    className="t-mono t-small"
                    style={{
                      // dd carries a browser indent of its own; the command
                      // has to line up under the client name that labels it.
                      margin: 0,
                      marginTop: "var(--space-1)",
                      whiteSpace: "pre-wrap",
                      wordBreak: "break-all",
                    }}
                  >
                    {guide.command(connector.url)}
                  </dd>
                </div>
              ))}
            </dl>
            <p className="t-small" style={{ marginTop: "var(--space-3)" }}>
              {t("agents.connectAntigravityPath")}
            </p>
          </div>
        ) : (
          <div
            className="card card-inset"
            style={{ marginTop: "var(--space-3)" }}
          >
            <p className="t-label">{t("agents.connectorOff")}</p>
            <p className="t-small" style={{ marginTop: "var(--space-1)" }}>
              {t("agents.connectorOffDetail")}
            </p>
          </div>
        )
      }
    </QueryGate>
  );
}

// The MCP clients holding a credential of their own. Disconnect is a harder
// action than revoking a passport and says so: it goes through the connection's
// grant, so the client's ability to RENEW dies with the credential — a revoke
// that killed only the passport would be undone by the next refresh seconds
// later.
export function ConnectedAgentsCard() {
  const t = useT();
  const { locale } = useLocale();
  const [confirmId, setConfirmId] = useState<string | null>(null);

  const list = useQuery({
    queryKey: ["passports"],
    queryFn: async () => {
      const { data, error } = await api.GET("/passports");
      if (error) {
        throw new Error(problemMessage(error));
      }
      return data;
    },
  });

  const disconnect = useMutation({
    mutationFn: async (id: string) => {
      const { error } = await api.DELETE("/passports/{id}", {
        params: { path: { id } },
      });
      if (error) {
        throw new Error(problemMessage(error));
      }
    },
    onSuccess: () => {
      setConfirmId(null);
      list.refetch();
    },
  });

  return (
    <section className="card" style={{ marginBottom: "var(--space-4)" }}>
      <SectionHeader
        title={t("agents.connected")}
        sub={t("agents.connectedSub")}
      />
      {/* The empty state is written out here rather than left to QueryGate's
          generic one: "nothing here" beside a guide explaining how to connect
          reads as a loading failure, and the sentence a human needs is that no
          agent has connected YET. */}
      <QueryGate query={list}>
        {(page) => {
          const connections = page.data.filter(
            (passport): passport is Connection => Boolean(passport.connection),
          );
          if (connections.length === 0) {
            return <EmptyState>{t("agents.noneConnected")}</EmptyState>;
          }
          return (
            <ul
              style={{
                listStyle: "none",
                display: "flex",
                flexDirection: "column",
                gap: "var(--space-3)",
              }}
            >
              {connections.map((passport) => {
                const revoked = passport.revoked_at != null;
                return (
                  <li key={passport.id} data-connection={passport.id}>
                    <div
                      style={{
                        display: "flex",
                        gap: "var(--space-2)",
                        alignItems: "center",
                        flexWrap: "wrap",
                        // struck, not dimmed — the same AA contrast floor the
                        // passport list keeps (B-EP09.21).
                        textDecoration: revoked ? "line-through" : undefined,
                      }}
                    >
                      <strong>{passport.connection.client_name}</strong>
                      <span className="t-small">
                        {t("agents.connectedOn", {
                          date: formatDate(
                            passport.connection.connected_at,
                            locale,
                            "Europe/Berlin",
                          ),
                        })}
                      </span>
                      {/* Omitted, never guessed: a connection made before the
                          provenance was recorded has no answer to give. */}
                      {passport.connection.lent_passport_label && (
                        <span className="t-small">
                          {t("agents.lentFrom", {
                            label: passport.connection.lent_passport_label,
                          })}
                        </span>
                      )}
                      {revoked && (
                        <Badge tone="danger">{t("agents.disconnected")}</Badge>
                      )}
                      {!revoked && (
                        <Button
                          small
                          variant="danger"
                          onClick={() => setConfirmId(passport.id)}
                        >
                          {t("agents.disconnect")}
                        </Button>
                      )}
                    </div>
                    <div
                      style={{
                        display: "flex",
                        gap: "var(--space-1)",
                        flexWrap: "wrap",
                        marginTop: "var(--space-1)",
                      }}
                    >
                      <ScopeChips scopes={passport.scopes} />
                    </div>
                  </li>
                );
              })}
            </ul>
          );
        }}
      </QueryGate>
      <ConnectGuide />
      <ConfirmModal
        open={confirmId != null}
        onClose={() => {
          setConfirmId(null);
          disconnect.reset();
        }}
        title={t("agents.disconnect")}
        confirmLabel={t("agents.disconnect")}
        onConfirm={() => confirmId && disconnect.mutate(confirmId)}
        pending={disconnect.isPending}
        error={
          disconnect.error instanceof Error ? disconnect.error.message : null
        }
      >
        <p>{t("agents.disconnectConfirm")}</p>
      </ConfirmModal>
    </section>
  );
}
