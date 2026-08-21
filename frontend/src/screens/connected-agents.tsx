// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import { useMutation, useQuery } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { Badge, Button, Disclosure, EmptyState } from "../design-system/atoms";
import { ConfirmModal } from "../design-system/confirmmodal";
import { Panel, PanelBody } from "../design-system/panel";
import { ScopeChips } from "../design-system/passportselect";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { formatDate } from "../format/format";
import { useLocale, useT } from "../i18n";
import type { MessageKey } from "../i18n/en";
import { problemMessageOf, QueryGate, throwProblem } from "./common";
import "./connected-agents.css";

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
// step is the config file its docs name. The OAuth handshake is identical. That
// caveat is a statement about ONE client, so it travels with that client as its
// row's `note` rather than as a loose paragraph under all four, where it read as
// a footnote to the whole guide.
const CONNECT_GUIDES: readonly Readonly<{
  id: string;
  name: string;
  command: (url: string) => string;
  note?: MessageKey;
}>[] = [
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
    note: "agents.connectAntigravityPath",
  },
];

// Four commands nobody runs twice. It is REFERENCE rather than a decision, so
// it reads last and closed — a `Disclosure` in the card's own row list, forced
// open only while nothing is connected, which is the one state where it is the
// point of the card rather than a footnote to it.
//
// The inset Card this replaces was a box inside a box: the disclosure body
// already says the contents belong to the section that was opened.
//
// Inside it, the SAME shape as a card: one line of prose, then a `SettingList`.
// It was a hand-laid `<dl>` before — a bold client name with its command flush
// under it at its own gap — so opening the disclosure left the page's row rhythm
// at the summary and reverted to a third layout underneath, on the one card a
// reader reaches it from. One client is one row: the client is the naming, its
// command is the SUBJECT rather than an answer that fits beside the name, so it
// takes the stacked row's full width (a 60-character command in a right-hand
// column is unreadable at any page measure).
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
          <>
            <p className="settings-panel-sub">{t("agents.connectSteps")}</p>
            <SettingList>
              {CONNECT_GUIDES.map((guide) => (
                <SettingRow
                  key={guide.id}
                  layout="stack"
                  // A product's own name, never translated — the same reason the
                  // command is not: both are typed exactly as they are shown.
                  label={guide.name}
                  description={guide.note && t(guide.note)}
                  control={
                    <code className="t-mono t-small agents-guide-command">
                      {guide.command(connector.url)}
                    </code>
                  }
                />
              ))}
            </SettingList>
          </>
        ) : (
          // Nothing to do and nothing to set: the disclosure's one row states the
          // fact and what still holds despite it. A row rather than two loose
          // paragraphs, so the sentence a reader who opened this arrives at sits
          // on the same beat as the rows they came from.
          <SettingList>
            <SettingRow
              label={t("agents.connectorOff")}
              description={t("agents.connectorOffDetail")}
              control={null}
            />
          </SettingList>
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
// Derived ONCE, here, because three things read it — the badge, the date line
// and the verb — and a row whose badge said "renewing" over a button that ends
// a lapsed grant is exactly the contradiction this card exists to remove.
type ConnectionState = Readonly<{
  revoked: boolean;
  renewing: boolean;
  lapsed: boolean;
  ended: boolean;
}>;

function connectionStateOf(passport: Connection, now: number): ConnectionState {
  const revoked = passport.revoked_at != null;
  const expired =
    !revoked &&
    passport.expires_at != null &&
    Date.parse(passport.expires_at) <= now;
  // Renewing, not ended: the client repairs this itself on its next call.
  const renewing = expired && passport.connection.renewable;
  const lapsed = expired && !passport.connection.renewable;
  return { revoked, renewing, lapsed, ended: revoked || lapsed };
}

type Translate = ReturnType<typeof useT>;

/**
 * The row's ANSWER: what state this connection is in, or nothing when it is
 * simply live.
 *
 * `undefined` rather than an empty node for the live case, because the row
 * draws its value slot whenever one is passed — an empty span there would take
 * the control column's gap and push every live row's verb one step off the x
 * the ended rows keep theirs on.
 */
function stateBadge(state: ConnectionState, t: Translate): ReactNode {
  if (state.renewing) {
    return <Badge>{t("agents.renewing")}</Badge>;
  }
  if (!state.ended) {
    return undefined;
  }
  return (
    <Badge tone="danger">
      {t(state.revoked ? "agents.disconnected" : "agents.lapsed")}
    </Badge>
  );
}

/**
 * The row's CONTROL, and the three states do not share one — they are not the
 * same thing to act on.
 *
 * Live and renewing both offer Disconnect. Lapsed offers to end the GRANT
 * instead: its credential is already gone, but the consent beneath it is not,
 * and `onEnd` reaches the same cascade either way (revokePassportTx kills the
 * grant even when the passport it names is already dead). A revoked row offers
 * nothing, because there is nothing left to end.
 *
 * Each verb names the CLIENT it would end, via `aria-label`, because the
 * confirm inside the dialog it opens is named for the act alone: two buttons
 * reading "Disconnect" one dialog apart are ambiguous for a reader and for a
 * name-based query, and only a distinct name separates them.
 */
function endVerb(
  passport: Connection,
  state: ConnectionState,
  onEnd: () => void,
  t: Translate,
): ReactNode {
  const client = passport.connection.client_name;
  if (!state.ended) {
    return (
      <Button
        small
        variant="danger"
        aria-label={t("agents.disconnectNamed", { client })}
        onClick={onEnd}
      >
        {t("agents.disconnectOpen")}
      </Button>
    );
  }
  if (!state.lapsed) {
    return null;
  }
  return (
    <Button
      small
      aria-label={t("agents.revokeGrantNamed", { client })}
      onClick={onEnd}
    >
      {t("agents.revokeGrantOpen")}
    </Button>
  );
}

/**
 * What is true of this connection, in the row's naming half: when it was made,
 * which passport it came from, when its credential turns over, and what the
 * client may do with it.
 *
 * The scope chips sit UNDER the facts rather than beside the verb. They
 * describe the connection, and at 390px a run sharing the control's line
 * wrapped under the button and read as its options.
 */
function ConnectionFacts({
  passport,
  state,
}: Readonly<{ passport: Connection; state: ConnectionState }>) {
  const t = useT();
  const { locale } = useLocale();
  // A credential's lifetime is a personal deadline, so it reads on the
  // viewer's own calendar (format.ts zone-by-purpose, the same split
  // oauthconsent.tsx makes). connected_at is a record date and keeps the fixed
  // zone: it says when a consent was given, not when the reader must act.
  const connected = formatDate(
    passport.connection.connected_at,
    locale,
    "Europe/Berlin",
  );
  // Two of the three states say something about this date; the third must not.
  // A LIVE row shows when the agent must next hold a fresh credential, and a
  // LAPSED one shows the moment it stopped — but a RENEWING row would read
  // "credential expired <date>" beside its own "renewing" badge, which is the
  // contradiction this whole card exists to remove. The badge carries that
  // state alone. A revoked row omits the date too: its credential never
  // reached its expiry.
  const deadline =
    passport.expires_at != null && !state.revoked && !state.renewing
      ? t(state.lapsed ? "agents.expiredOn" : "agents.renewsBy", {
          date: formatDate(
            passport.expires_at,
            locale,
            Intl.DateTimeFormat().resolvedOptions().timeZone,
          ),
        })
      : null;
  return (
    <>
      {/* The strikethrough wraps the FACTS, never the control beside them: a
          struck-through button reads as disabled, and the one an ended row
          offers is very much live. Struck, not dimmed — the same AA contrast
          floor the passport list keeps (B-EP09.21). */}
      <span
        className={state.ended ? "agents-facts agents-ended" : "agents-facts"}
      >
        <span>{t("agents.connectedOn", { date: connected })}</span>
        {/* Omitted, never guessed: a connection made before the provenance was
            recorded has no answer to give. */}
        {passport.connection.lent_passport_label && (
          <span>
            {t("agents.lentFrom", {
              label: passport.connection.lent_passport_label,
            })}
          </span>
        )}
        {deadline && <span>{deadline}</span>}
      </span>
      <span className="agents-scopes">
        <ScopeChips scopes={passport.scopes} />
      </span>
    </>
  );
}

// One connection as a `SettingRow`: the client's name is the naming, its
// provenance and dates are the description, the state is the answer standing in
// the value slot, and the verb that ends it is the control. Before this the
// whole thing was one hand-rolled flex run of eight inline styles, which is how
// the scope chips came to wrap under the button at 390px and read as belonging
// to it.
function ConnectionRow({
  passport,
  onEnd,
}: Readonly<{ passport: Connection; onEnd: () => void }>) {
  const t = useT();
  const state = connectionStateOf(passport, Date.now());
  return (
    <SettingRow
      testId={`connection-${passport.id}`}
      label={
        <span className={state.ended ? "agents-ended" : undefined}>
          {passport.connection.client_name}
        </span>
      }
      description={<ConnectionFacts passport={passport} state={state} />}
      value={stateBadge(state, t)}
      control={endVerb(passport, state, onEnd, t)}
    />
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
  // Where the disconnect confirm hands focus back. Not the row it was opened
  // from: ending a connection removes that row from the refetched list, so the
  // nearest thing that survives is the region the row was in.
  const listRegion = useRef<HTMLDivElement | null>(null);

  const list = useQuery({
    queryKey: ["passports"],
    queryFn: async () => {
      const { data, error } = await api.GET("/passports");
      if (error) {
        throwProblem(error);
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
        throwProblem(error);
      }
    },
    onSuccess: async () => {
      // The refetched list FIRST, then the dialog: closing it hands focus to the
      // list region, and a region still holding the row for a connection the
      // server has already dropped would announce a client that is gone.
      await list.refetch();
      setConfirmId(null);
    },
  });

  // Read at the top rather than inside the gate's render prop: the expiry
  // timer is a hook, so it cannot live where the rows are built.
  const connections = (list.data?.data ?? []).filter(
    (passport): passport is Connection => Boolean(passport.connection),
  );
  useClockAt(nextExpiry(connections, Date.now()));

  return (
    // No margin of its own: the settings stack owns the gap between two cards,
    // and a card that also pays for one gets double the interval its neighbours
    // get.
    <Panel title={t("agents.connected")}>
      <PanelBody>
        <p className="settings-panel-sub">{t("agents.connectedSub")}</p>
        {/* The wrapper is the disconnect confirm's focus anchor: it holds the
          card's row list — the connections that remain, the "no agent is
          connected" line when the ended one was the last, and the way to
          connect another — and it is the only thing here that survives every
          one of those transitions. tabIndex -1 makes it reachable by focus()
          without joining anybody's Tab order. */}
        <div ref={listRegion} tabIndex={-1}>
          <SettingList>
            {/* The gate renders straight into the list, so its rows are the
              list's own children and the hairline falls between them. Its
              pending and error surfaces stand in the same place a row would. */}
            <QueryGate query={list}>
              {() =>
                connections.length === 0 ? (
                  // Written out here rather than left to QueryGate's generic
                  // one: "nothing here" beside a guide explaining how to
                  // connect reads as a loading failure, and the sentence a
                  // human needs is that no agent has connected YET.
                  <EmptyState>{t("agents.noneConnected")}</EmptyState>
                ) : (
                  connections.map((passport) => (
                    <ConnectionRow
                      key={passport.id}
                      passport={passport}
                      onEnd={() => setConfirmId(passport.id)}
                    />
                  ))
                )
              }
            </QueryGate>
            {/* Forced open only once the read has ANSWERED with nothing: an
              `open` computed while the list is still pending would flash the
              guide open and shut for every reader who does have a connection. */}
            <Disclosure
              summary={t("agents.connectHow")}
              open={
                list.isSuccess && connections.length === 0 ? true : undefined
              }
            >
              <ConnectGuide />
            </Disclosure>
          </SettingList>
        </div>
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
            disconnect.error ? problemMessageOf(disconnect.error, t) : null
          }
          // The list the ended connection was in, since the row that opened this
          // confirm is not in the refetched list at all. Landing there reads back
          // what is still connected, which is the question somebody who just
          // disconnected a client actually has next.
          returnFocusTo={() => listRegion.current}
        >
          <p>{t("agents.disconnectConfirm")}</p>
        </ConfirmModal>
      </PanelBody>
    </Panel>
  );
}
