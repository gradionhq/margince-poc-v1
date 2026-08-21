// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

import "./integrations-provider.css";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plug, Trash2 } from "lucide-react";
import { useState } from "react";
import { api } from "../api/client";
import type { components } from "../api/schema";
import { useCanWrite } from "../app/capability";
import {
  Badge,
  Button,
  EmptyState,
  Field,
  OverflowMenu,
  TableScroll,
  TextInput,
} from "../design-system/atoms";
import { Callout } from "../design-system/callout";
import { ConfirmModal } from "../design-system/confirmmodal";
import { type Fact, FactList } from "../design-system/factlist";
import { Panel, PanelBody, PanelRow } from "../design-system/panel";
import { ProviderMark } from "../design-system/provider-mark";
import { Meter } from "../design-system/readings";
import { SettingList, SettingRow } from "../design-system/settingrow";
import { Switch } from "../design-system/switch";
import { useT } from "../i18n";
import {
  problemCode,
  problemMessageOf,
  QueryGate,
  throwProblem,
  useMe,
} from "./common";
import { connectionLabel, connectionTone } from "./provider-status";

// The licensed-data-provider card (ADR-0101, PI-WIRE-1..5): connect a key,
// decide whether new contacts are enriched automatically, and read what the
// provider says is left against what this installation has spent.
//
// Four settings ROWS, in the order a reader needs them: the key that makes any
// of this possible, the one judgement that spends it, then the two readings
// those two produce. The readings take the full width because a table IS the
// subject rather than an answer to a question; the key and the switch are
// answers, so they sit in the right column at the same x as every other card on
// the page.
//
// Disconnect and delete-data are two verbs because they are two decisions.
// Disconnecting stops new lookups and destroys the key; the data already paid
// for stays on the records. A customer may want either without the other, and a
// single button would make one of them a surprise. Neither is the same WEIGHT as
// connecting, which is why both live behind the overflow beside it: three
// buttons eight pixels apart, one of which irreversibly destroys purchased
// contact data, made a misclick the same size as a decision.

type ProviderConnection = components["schemas"]["ProviderConnection"];

type ConnectionsResult = {
  /** True when this build carries no adapter at all. Not an error: it is the
   *  supported "no provider" configuration, and the card says so plainly
   *  rather than showing a broken control (PI-AC-9). */
  notConfigured: boolean;
  connections: ProviderConnection[];
};

function useProviderConnections() {
  return useQuery({
    queryKey: ["provider-connections"],
    queryFn: async (): Promise<ConnectionsResult> => {
      const { data, error, response } = await api.GET("/provider-connections");
      // 501 is a deployment fact, not a failure — the same shape connectors.tsx
      // uses for a connector nobody configured.
      if (response.status === 501 && problemCode(error) === "not_implemented") {
        return { notConfigured: true, connections: [] };
      }
      if (error || !response.ok) {
        throwProblem(error);
      }
      return { notConfigured: false, connections: data?.data ?? [] };
    },
  });
}

export function ProviderCard() {
  const t = useT();
  const query = useProviderConnections();
  // Every seat reads this card — the balances and the spend are a rep's
  // explanation for a dated value on a person record — while connecting and
  // destroying are admin/ops. The two answers are computed HERE, once, so the
  // posture line below and the affordances inside cannot disagree about who
  // may do what.
  //
  // Connect asks for `create` alone, not the create-or-update an upsert would:
  // the PUT replaces an existing credential, but the server admits it on
  // `create` whichever it turns out to be, so a reader holding only `update`
  // would be shown a button that can only 403.
  const me = useMe();
  const canConnect = useCanWrite("integrations", "create");
  const canDestroy = useCanWrite("integrations", "delete");
  // The configuration PATCH is its own verb (integrations/update.go), so the
  // auto-enrich switch asks for that rather than borrowing either answer above.
  const canEdit = useCanWrite("integrations", "update");
  // Gated on the probe having ANSWERED, so a reader who does hold the grants
  // never sees this flash while /me is in flight.
  const readOnly = me.isSuccess && !canConnect && !canDestroy && !canEdit;
  return (
    <Panel title={t("provider.title")}>
      {/* The intro pays no bottom padding: whatever the gate renders under it —
          a skeleton, a refusal, the no-provider state — brings the body's own
          top padding with it, and two stacked bodies would space them twice. */}
      <PanelBody className="provider-intro">
        <p className="settings-panel-sub">{t("provider.sub")}</p>
        {/* Hoisted OUT of QueryGate, the way the neighbouring webhooks card
            already does it: the gate's empty and error branches replace their
            children wholesale, so a posture line nested inside would go quiet
            in exactly the states — loading, failed, not configured — where a
            reader is most likely to read the missing controls as a bug. The
            card keeps its place and says ONCE what a reader without any of the
            three writes is looking at; the controls below are then simply
            absent (design-system README, "Absent, disabled, or withheld"). */}
        {readOnly && (
          <p className="settings-panel-sub">{t("provider.readOnly")}</p>
        )}
      </PanelBody>
      <QueryGate query={query}>
        {(result) =>
          // An empty list means the same thing a 501 does: no adapter is
          // compiled in. The server returns a row for every REGISTERED
          // provider — including one nobody has connected yet, which is how
          // the key field appears at all — so "no rows" cannot mean "not
          // connected", and both cases read as the honest no-provider state.
          result.notConfigured || result.connections.length === 0 ? (
            <PanelBody>
              <EmptyState>{t("provider.notConfigured")}</EmptyState>
            </PanelBody>
          ) : (
            result.connections.map((connection) => (
              <ProviderConnectionRow
                key={connection.provider}
                connection={connection}
                canConnect={canConnect}
                canDestroy={canDestroy}
                canEdit={canEdit}
              />
            ))
          )
        }
      </QueryGate>
    </Panel>
  );
}

function ProviderConnectionRow({
  connection,
  canConnect,
  canDestroy,
  canEdit,
}: Readonly<{
  connection: ProviderConnection;
  canConnect: boolean;
  canDestroy: boolean;
  canEdit: boolean;
}>) {
  const t = useT();
  return (
    <>
      {/* The provider is what this block is ABOUT, so it names it as a
          heading: the panel's own h2 is "Contact data", and heading navigation
          that lands there has to be able to step into one provider at a time.
          The rows under it are named by their own labels rather than by
          headings — a settings row's naming is the label, which is what puts
          every answer on this page at one x. */}
      <PanelRow>
        <div className="provider-head">
          <span className="provider-mark">
            <ProviderMark providerKey={connection.provider} />
          </span>
          <h3 className="provider-name">{connection.provider}</h3>
          <Badge tone={connectionTone(connection.status)}>
            {t(connectionLabel(connection.status))}
          </Badge>
        </div>
      </PanelRow>
      <PanelBody>
        <SettingList>
          <CredentialRow
            connection={connection}
            canConnect={canConnect}
            canDestroy={canDestroy}
          />
          <PolicyRow connection={connection} canEdit={canEdit} />
          <SettingRow
            label={t("provider.credits")}
            layout="stack"
            control={<CreditsReading connection={connection} />}
          />
          <SettingRow
            label={t("provider.spend")}
            description={t("provider.spend.hint")}
            layout="stack"
            control={<SpendReading connection={connection} />}
          />
        </SettingList>
      </PanelBody>
    </>
  );
}

// What THIS installation consumed. The hint on the row above says why it can
// legitimately differ from the provider's own invoice: the same credits are
// spendable through their app, so neither figure is a check on the other.
function SpendReading({
  connection,
}: Readonly<{ connection: ProviderConnection }>) {
  const t = useT();
  const months = connection.spend?.months ?? [];
  if (months.length === 0) {
    return <p className="provider-empty">{t("provider.spend.none")}</p>;
  }
  // The series only carries months that HAD spend, so its newest entry is not
  // necessarily this one — an installation that bought nothing yet this month
  // would otherwise see last month's total labelled as the current bill.
  const now = new Date();
  const current = `${now.getUTCFullYear()}-${String(now.getUTCMonth() + 1).padStart(2, "0")}-01`;
  return (
    // Five columns of billing, all of them reconciled against an invoice, so
    // none can be dropped on a narrow screen. `TableScroll` is the one spelling
    // of the box that scrolls inside the card, the same one DataTable puts
    // every list it draws inside (atoms.tsx).
    <TableScroll className="provider-reading" label={t("provider.spend")}>
      <table className="provider-spend-table">
        <thead>
          <tr>
            <th>{t("provider.spend.month")}</th>
            <th>{t("provider.spend.pool")}</th>
            <th>{t("provider.spend.chargedHead")}</th>
            <th>{t("provider.spend.heldHead")}</th>
            <th>{t("provider.spend.runsHead")}</th>
          </tr>
        </thead>
        <tbody>
          {months.map((month) => (
            <tr key={`${month.month}-${month.pool}`}>
              <td>
                {month.month === current
                  ? t("provider.spend.thisMonth")
                  : month.month}
              </td>
              <td>{month.pool}</td>
              <td>{month.charged_credits}</td>
              {/* Never folded into the charge: the platform does not know
                  whether those credits were spent, and a total that quietly
                  counted them either way would assert something it cannot
                  support. This is the figure a human reconciles against the
                  provider's invoice. */}
              <td className="provider-held">
                {month.held_credits > 0 ? month.held_credits : "—"}
              </td>
              <td>{month.runs}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </TableScroll>
  );
}

// What the PROVIDER says is left — their number, never ours. A customer may
// spend the same credits through the provider's own app, so this is a reading
// of their ledger and the card never presents it as our accounting.
function CreditsReading({
  connection,
}: Readonly<{ connection: ProviderConnection }>) {
  const t = useT();
  // Iterated, never hardcoded to email/mobile: the pool names are the
  // PROVIDER's own vocabulary, and a second provider meters different ones.
  // A pool whose balance is null is a pool we have no reading for — the
  // disconnect clears the number with the credential that fetched it. Rendering
  // it as 0 would assert an empty account, which is a different claim from
  // "we do not know" and the one thing this block must never say by accident.
  const pools = Object.entries(connection.credits?.pools ?? {}).filter(
    ([, balance]) => balance !== null && balance !== undefined,
  );
  if (pools.length === 0) {
    // Two different silences. With no key we never asked, and saying the
    // provider "has not told us" would blame them for our own empty state.
    return (
      <p className="provider-empty">
        {connection.credential_present
          ? t("provider.credits.none")
          : t("provider.credits.notConnected")}
      </p>
    );
  }
  const highest = Math.max(1, ...pools.map(([, balance]) => balance ?? 0));
  // A pool is one reading inside the row, not a section of its own: FactList's
  // `dt` is the row label the grid was built for. The bar rides in `note`,
  // under the figure it qualifies — and it carries the pool's name itself,
  // since a role="meter" takes no accessible name from the text beside it.
  const facts: Fact[] = pools.map(([pool, balance]) => ({
    key: pool,
    term: pool,
    value: balance ?? 0,
    note: <Meter value={balance ?? 0} max={highest} label={pool} flat />,
  }));
  return (
    <div className="provider-reading">
      <FactList className="provider-pools" facts={facts} numeric />
      {(connection.effective_constraints ?? []).length > 0 && (
        <p className="provider-block-hint">
          {t("provider.constraints")}:{" "}
          {(connection.effective_constraints ?? []).join(", ")}
        </p>
      )}
    </div>
  );
}

function usePatchConfiguration(
  provider: ProviderConnection["provider"],
  version: number,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (automaticIndividualCreate: boolean) => {
      const { data, error } = await api.PATCH(
        "/provider-connections/{provider}",
        {
          params: {
            path: { provider },
            // The saved policy carries a version, and a blind write would
            // silently overwrite a colleague's edit. A 409 is version skew,
            // which the refetch below resolves by showing what is actually
            // stored.
            header: { "If-Match": String(version) },
          },
          body: {
            configuration: {
              automatic_individual_create: automaticIndividualCreate,
            },
          },
        },
      );
      if (error) {
        throwProblem(error);
      }
      return data;
    },
    onSuccess: (updated) => {
      queryClient.setQueryData(
        ["provider-connections"],
        (current: ConnectionsResult | undefined) =>
          current
            ? {
                ...current,
                connections: current.connections.map((c) =>
                  c.provider === updated?.provider ? updated : c,
                ),
              }
            : current,
      );
    },
    onError: () => {
      void queryClient.invalidateQueries({
        queryKey: ["provider-connections"],
      });
    },
  });
}

// Auto-enrich is the one control here that a reader who may not change it still
// needs to READ: it is the only place the installation says whether new contacts
// are being enriched at somebody's expense. So it is neither absent (that would
// hide a granted read) nor withheld (there is a fact to show) — it is the shape
// the design system keeps for exactly this: a Switch, because flipping it
// writes, with `reason` carrying the denial to a screen reader through
// aria-describedby rather than leaving it beside the control as decoration.
function PolicyRow({
  connection,
  canEdit,
}: Readonly<{ connection: ProviderConnection; canEdit: boolean }>) {
  const t = useT();
  const patch = usePatchConfiguration(connection.provider, connection.version);
  const configuration = connection.configuration;
  const disconnected = connection.status !== "connected";
  return (
    <>
      <SettingRow
        label={t("provider.autoEnrich")}
        description={t("provider.autoEnrichHint")}
        control={(control) => (
          <Switch
            // The row's description reaches the switch: what auto-enrich DOES
            // is the sentence on the left, and a node-form control cannot see
            // the id the row minted for it.
            describedBy={control["aria-describedby"]}
            checked={configuration.automatic_individual_create ?? false}
            // Three causes, and only one of them is worth words. A permission
            // is permanent and has to be explained; a write in flight explains
            // itself by finishing, and a disconnected provider is already
            // stated by the status beside the provider's name.
            //
            // The shared single-control sentence, not the card's own posture
            // line: that one names why the CARD is read-only and would say the
            // same thing twice here, once as prose and once attached to the
            // control.
            reason={canEdit ? undefined : t("captureSettings.adminOnly")}
            disabled={!canEdit || disconnected}
            pending={patch.isPending}
            onChange={(next) => patch.mutate(next)}
            // The row already draws this name on the left, so the switch keeps
            // its own copy hidden: it owns its accessible name by design (see
            // switch.tsx) and pointing it at the row's span as well would name
            // it twice.
            label={t("provider.autoEnrich")}
            labelHidden
          />
        )}
      />
      {/* Under the row whose flip failed, at the row's full width, rather than
          squeezed into the control column beside the switch: the reason names
          what the reader just tried to change, and a sentence sharing a
          nowrap flex line with a switch is a sentence nobody reads. */}
      {patch.error && (
        <div className="provider-row-note">
          <Callout tone="danger" live="alert">
            {problemMessageOf(patch.error, t)}
          </Callout>
        </div>
      )}
    </>
  );
}

// The two destructive decisions, behind the overflow they share. A component
// rather than duplicated JSX because they render in two places: beside the key
// verb for a reader who may also connect, and alone for one who may not.
//
// The overflow is the point, not the tidiness. Disconnect is recoverable —
// reconnect the key and lookups resume — while delete-data irreversibly
// destroys contact details this installation PAID for. Neither belongs at the
// same weight as Connect, and the confirm ladder behind them (a plain confirm,
// then a typed one) already said so before the button row did.
function DestructiveActions({
  onDisconnect,
  onDeleteData,
}: Readonly<{ onDisconnect: () => void; onDeleteData: () => void }>) {
  const t = useT();
  return (
    <OverflowMenu label={t("record.moreActions")}>
      <Button small type="button" onClick={onDisconnect}>
        {t("provider.disconnect")}
      </Button>
      <Button small variant="danger" type="button" onClick={onDeleteData}>
        <Trash2 aria-hidden /> {t("provider.deleteData")}
      </Button>
    </OverflowMenu>
  );
}

// The key, and the confirm it is submitted through. Mounted only while it is
// open: a key sealed server-side is never returned, so holding a typed one
// after a dialog the reader abandoned would keep a secret in the page for no
// purpose at all.
//
// The field lives INSIDE the confirm rather than behind a second dialog on top
// of it. The posture is the same either way — nothing is sent until the confirm
// is pressed — and this way the sentence about what connecting costs is on
// screen while the key is being pasted.
function CredentialDialog({
  connection,
  pending,
  error,
  onClose,
  onConnect,
}: Readonly<{
  connection: ProviderConnection;
  pending: boolean;
  error: string | null;
  onClose: () => void;
  onConnect: (apiKey: string) => void;
}>) {
  const t = useT();
  const [key, setKey] = useState("");
  const connected = connection.credential_present;
  return (
    <ConfirmModal
      open
      title={t("provider.connectConfirm.title")}
      confirmLabel={connected ? t("provider.reconnect") : t("provider.connect")}
      confirmDisabled={key.trim() === ""}
      onConfirm={() => onConnect(key.trim())}
      onClose={onClose}
      pending={pending}
      error={error}
    >
      <p className="t-small">{t("provider.connectConfirm.body")}</p>
      {/* The field is write-only in both states: a sealed key is never sent
          back to the browser, so the box is empty even when one is in place.
          Left unexplained that reads as "no key connected" while the card
          behind it shows a live balance — so the label and the hint say which
          state this is, and the placeholder does not pretend to hold a
          value. */}
      <Field
        label={connected ? t("provider.apiKeyStored") : t("provider.apiKey")}
        hint={
          connected ? t("provider.apiKeyReplaceHint") : t("provider.apiKeyHint")
        }
      >
        {(control) => (
          <TextInput
            {...control}
            type="password"
            autoComplete="off"
            value={key}
            required
            placeholder={
              connected ? t("provider.apiKeyReplacePlaceholder") : ""
            }
            onChange={(event) => setKey(event.target.value)}
          />
        )}
      </Field>
    </ConfirmModal>
  );
}

function CredentialRow({
  connection,
  canConnect,
  canDestroy,
}: Readonly<{
  connection: ProviderConnection;
  canConnect: boolean;
  canDestroy: boolean;
}>) {
  const t = useT();
  const queryClient = useQueryClient();
  const [connecting, setConnecting] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [typed, setTyped] = useState("");

  const connect = useMutation({
    // The key arrives as a variable rather than through a closure over the
    // dialog's state: the press belongs to the committed render, so what it
    // carries cannot be older than the field the reader typed into.
    mutationFn: async (apiKey: string) => {
      const { error } = await api.PUT("/provider-connections/{provider}", {
        params: { path: { provider: connection.provider } },
        body: { api_key: apiKey },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      // Closing unmounts the dialog, which is what discards the typed key: it
      // is sealed server-side and never returned, so keeping it would hold a
      // secret in the page for no purpose.
      setConnecting(false);
      void queryClient.invalidateQueries({
        queryKey: ["provider-connections"],
      });
    },
  });

  const disconnect = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE("/provider-connections/{provider}", {
        params: { path: { provider: connection.provider } },
      });
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setDisconnecting(false);
      void queryClient.invalidateQueries({
        queryKey: ["provider-connections"],
      });
    },
  });

  const deleteData = useMutation({
    mutationFn: async () => {
      const { error } = await api.DELETE(
        "/provider-connections/{provider}/data",
        {
          params: { path: { provider: connection.provider } },
        },
      );
      if (error) {
        throwProblem(error);
      }
    },
    onSuccess: () => {
      setDeleting(false);
      setTyped("");
      void queryClient.invalidateQueries({
        queryKey: ["provider-connections"],
      });
    },
  });

  const connected = connection.credential_present;
  // A stored key is what makes either destructive action meaningful, and the
  // grant is what makes it permitted.
  const destructive = connected && canDestroy;
  return (
    <>
      <SettingRow
        label={t("provider.apiKey")}
        description={
          connected ? t("provider.apiKeyReplaceHint") : t("provider.apiKeyHint")
        }
        control={
          <>
            {/* The key field goes behind the verb it feeds: two of the three
                things a connect needs — the key and the confirmation that it
                costs money — are not answers that fit in a row's right
                column. A reader who may not connect has nothing to open. */}
            {canConnect && (
              <Button
                small
                variant="primary"
                type="button"
                onClick={() => setConnecting(true)}
              >
                <Plug aria-hidden />{" "}
                {connected ? t("provider.reconnect") : t("provider.connect")}
              </Button>
            )}
            {/* Stopping the flow and destroying what was bought are a
                different authority from connecting, so they stand on their own
                for a reader who holds one grant and not the other. */}
            {destructive && (
              <DestructiveActions
                onDisconnect={() => setDisconnecting(true)}
                onDeleteData={() => setDeleting(true)}
              />
            )}
          </>
        }
      />

      {connecting && (
        <CredentialDialog
          connection={connection}
          pending={connect.isPending}
          error={connect.isError ? problemMessageOf(connect.error, t) : null}
          onClose={() => setConnecting(false)}
          onConnect={(apiKey) => {
            if (!canConnect) {
              return;
            }
            connect.mutate(apiKey);
          }}
        />
      )}

      {/* Both destructive confirms carry their own mutation's failure. Without
          it the dialog simply closed on a refusal and the card looked exactly
          as it had before — a disconnect the server rejected was
          indistinguishable from one it accepted. */}
      <ConfirmModal
        open={disconnecting}
        confirmVariant="danger"
        title={t("provider.disconnectConfirm.title")}
        confirmLabel={t("provider.disconnect")}
        onConfirm={() => disconnect.mutate()}
        onClose={() => setDisconnecting(false)}
        pending={disconnect.isPending}
        error={
          disconnect.isError ? problemMessageOf(disconnect.error, t) : null
        }
      >
        {t("provider.disconnectConfirm.body")}
      </ConfirmModal>

      {/* Typed confirmation, like the data reset: this destroys purchased
          data on every contact, and a misclick must not be able to do it. */}
      <ConfirmModal
        open={deleting}
        confirmVariant="danger"
        title={t("provider.deleteDataConfirm.title")}
        confirmLabel={t("provider.deleteData")}
        confirmDisabled={typed !== connection.provider}
        onConfirm={() => deleteData.mutate()}
        pending={deleteData.isPending}
        error={
          deleteData.isError ? problemMessageOf(deleteData.error, t) : null
        }
        onClose={() => {
          setDeleting(false);
          setTyped("");
        }}
      >
        <p>{t("provider.deleteDataConfirm.body")}</p>
        <Field label={t("provider.deleteDataConfirm.typed")}>
          {(control) => (
            <TextInput
              {...control}
              value={typed}
              onChange={(event) => setTyped(event.target.value)}
            />
          )}
        </Field>
      </ConfirmModal>
    </>
  );
}
