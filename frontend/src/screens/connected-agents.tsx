// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
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

// One connection, and whether it is still one.
//
// A connection ends TWO ways and only one of them writes a column. Revocation
// stamps revoked_at (oauth_grant.go's cascade retires every passport under the
// grant), but a credential can also simply RUN OUT. Reading revoked_at alone
// left an ended connection offering Disconnect on a credential that had already
// stopped working.
//
// An expiry is NOT an ending on its own, though, and that is the trap. A grant
// carrying offline_access mints itself a replacement, so its passport passing
// expires_at means the connection is between credentials — normal, and about to
// be repaired by the client's next call. Only a connection that cannot renew is
// over when its credential is. `renewable` is the grant's own refresh_allowed,
// which is why the server sends it: without it this row reports every live
// connector as dead the moment its access token turns over.
//
// The three states do not share a control, because they are not the same thing
// to act on. Live and renewing both offer Disconnect. Lapsed offers to end the
// GRANT instead — its credential is already gone, but the consent beneath it is
// not — and `onEnd` reaches the same cascade either way (revokePassportTx kills
// the grant even when the passport it names is already dead).
function ConnectionRow({
  passport,
  onEnd,
}: Readonly<{ passport: Connection; onEnd: () => void }>) {
  const t = useT();
  const { locale } = useLocale();
  const revoked = passport.revoked_at != null;
  const expired =
    !revoked &&
    passport.expires_at != null &&
    Date.parse(passport.expires_at) <= Date.now();
  // Renewing, not ended: the client repairs this itself on its next call.
  const renewing = expired && passport.connection.renewable;
  const lapsed = expired && !passport.connection.renewable;
  const ended = revoked || lapsed;
  // A credential's lifetime is a personal deadline, so it reads on the
  // viewer's own calendar (format.ts zone-by-purpose, the same split
  // oauthconsent.tsx makes). connected_at is a record date and keeps the fixed
  // zone: it says when a consent was given, not when the reader must act.
  const recordDay = (iso: string) => formatDate(iso, locale, "Europe/Berlin");
  const deadlineDay = (iso: string) =>
    formatDate(iso, locale, Intl.DateTimeFormat().resolvedOptions().timeZone);
  return (
    <li data-connection={passport.id}>
      <div
        style={{
          display: "flex",
          gap: "var(--space-2)",
          alignItems: "center",
          flexWrap: "wrap",
        }}
      >
        {/* The strikethrough wraps the FACTS, never the actions beside them: a
            struck-through button reads as disabled, and the one an ended row
            offers is very much live. Struck, not dimmed — the same AA contrast
            floor the passport list keeps (B-EP09.21). */}
        <span
          style={{
            display: "flex",
            gap: "var(--space-2)",
            alignItems: "center",
            flexWrap: "wrap",
            textDecoration: ended ? "line-through" : undefined,
          }}
        >
          <strong>{passport.connection.client_name}</strong>
          <span className="t-small">
            {t("agents.connectedOn", {
              date: recordDay(passport.connection.connected_at),
            })}
          </span>
          {/* Omitted, never guessed: a connection made before the provenance
              was recorded has no answer to give. */}
          {passport.connection.lent_passport_label && (
            <span className="t-small">
              {t("agents.lentFrom", {
                label: passport.connection.lent_passport_label,
              })}
            </span>
          )}
          {/* This date moves with each renewal, which is the honest thing to
              show: it is when the agent must next hold a live credential, not
              when the consent lapses. A revoked row omits it — its credential
              did not reach its expiry. */}
          {passport.expires_at && !revoked && (
            <span className="t-small">
              {t(expired ? "agents.expiredOn" : "agents.renewsBy", {
                date: deadlineDay(passport.expires_at),
              })}
            </span>
          )}
        </span>
        {renewing && <Badge>{t("agents.renewing")}</Badge>}
        {ended && (
          <Badge tone="danger">
            {t(revoked ? "agents.disconnected" : "agents.lapsed")}
          </Badge>
        )}
        {!ended && (
          <Button
            small
            variant="danger"
            aria-label={t("agents.disconnectNamed", {
              client: passport.connection.client_name,
            })}
            onClick={onEnd}
          >
            {t("agents.disconnect")}
          </Button>
        )}
        {lapsed && (
          <Button
            small
            aria-label={t("agents.revokeGrantNamed", {
              client: passport.connection.client_name,
            })}
            onClick={onEnd}
          >
            {t("agents.revokeGrant")}
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
}

// The MCP clients holding a credential of their own. Disconnect is a harder
// action than revoking a passport and says so: it goes through the connection's
// grant, so the client's ability to RENEW dies with the credential — a revoke
// that killed only the passport would be undone by the next refresh seconds
// later.
// The soonest moment at which some row's status would change if nothing else
// happened — the earliest still-future expiry among the live connections.
// Null when nothing is pending, which is the ordinary case.
function nextExpiry(
  connections: readonly Connection[],
  now: number,
): number | null {
  const upcoming = connections
    .filter((c) => c.revoked_at == null && c.expires_at != null)
    .map((c) => Date.parse(c.expires_at as string))
    .filter((at) => at > now);
  return upcoming.length > 0 ? Math.min(...upcoming) : null;
}

// Re-render when a credential passes its expiry, because THIS list derives a
// status from the clock and nothing else would notice. The app disables
// refetchOnWindowFocus (main.tsx), so a settings tab left open would otherwise
// keep reporting a connection as live indefinitely — the status is computed at
// render, and without this nothing schedules another one.
//
// One timer at the nearest expiry, not a poll: the boundary is known exactly,
// so waking for it is enough and waking every N seconds to check would be
// waste. Re-running on `until` means each crossing schedules the next.
function useClockAt(until: number | null) {
  const [, setTick] = useState(0);
  useEffect(() => {
    if (until == null) {
      return;
    }
    // setTimeout saturates past ~24.8 days (its delay is a signed 32-bit ms
    // value) and would fire IMMEDIATELY, spinning. A passport lifetime reaches
    // 30 days, so the far ones are simply not scheduled: nobody holds a tab
    // open that long, and the next mount recomputes anyway.
    const delay = until - Date.now();
    if (delay <= 0 || delay > 0x7fffffff) {
      return;
    }
    const timer = globalThis.setTimeout(() => setTick((n) => n + 1), delay);
    return () => globalThis.clearTimeout(timer);
  }, [until]);
}

export function ConnectedAgentsCard() {
  const t = useT();
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

  // Read at the top rather than inside the gate's render prop: the expiry
  // timer is a hook, so it cannot live where the rows are built.
  const connections = (list.data?.data ?? []).filter(
    (passport): passport is Connection => Boolean(passport.connection),
  );
  useClockAt(nextExpiry(connections, Date.now()));

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
        {() => {
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
              {connections.map((passport) => (
                <ConnectionRow
                  key={passport.id}
                  passport={passport}
                  onEnd={() => setConfirmId(passport.id)}
                />
              ))}
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
        // The final click revokes a credential AND the grant beneath it; a
        // primary-styled confirm would understate that at the one moment it
        // matters most.
        confirmVariant="danger"
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
